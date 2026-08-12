package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Worktree struct {
	Path       string
	Branch     string
	RelPath    string
	Kind       string // "task" or "review"
	LastCommit time.Time
	CommitMsg  string
	RemoteGone bool
	Merged     bool   // branch is an ancestor of a base branch (work is done)
	MergedInto string // which base it merged into (for display)
	Dirty      bool   // has uncommitted/untracked changes (cached; drives clean's preview)
}

func RepoRoot() (string, error) {
	out, err := cmdOutput(".", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository")
	}
	return out, nil
}

// RepoRootAt returns the repo root for an arbitrary directory — it need not be
// the cwd, and need not have a checkout. A working tree resolves to its
// toplevel; a bare repo (a mirror) resolves to the git dir itself. This is what
// lets a headless caller point norn at a mirror it already holds.
func RepoRootAt(dir string) (string, error) {
	if top, err := cmdOutput(dir, "git", "rev-parse", "--show-toplevel"); err == nil && top != "" {
		return top, nil
	}
	gitDir, err := cmdOutput(dir, "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	return gitDir, nil
}

// OriginRepoName returns the basename of the *main* repo for a given directory.
// Inside a worktree, this resolves to the upstream repo's name rather than the
// worktree's own dir name — so per-project config (~/.config/work/projects/<name>.yaml)
// keeps matching no matter which worktree you're in.
func OriginRepoName(dir string) string {
	common, err := CommonDir(dir)
	if err != nil || common == "" {
		// Fallback: basename of show-toplevel.
		if top, err2 := cmdOutput(dir, "git", "rev-parse", "--show-toplevel"); err2 == nil {
			return filepath.Base(top)
		}
		return RepoNameFromGitDir(dir)
	}
	return RepoNameFromGitDir(common)
}

// RepoNameFromGitDir maps a git dir to the repo name: ".../<repo>/.git" → repo
// (a normal checkout or any worktree of it), and ".../<repo>.git" → repo (a
// bare mirror, which has no parent checkout to take the name from).
func RepoNameFromGitDir(gitDir string) string {
	if filepath.Base(gitDir) == ".git" {
		return filepath.Base(filepath.Dir(gitDir))
	}
	return strings.TrimSuffix(filepath.Base(gitDir), ".git")
}

func CommonDir(dir string) (string, error) {
	common, err := cmdOutput(dir, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(common) {
		// git prints it relative to its own cwd, which is dir. A bare repo
		// reports "." and has no toplevel to resolve against.
		abs, err := filepath.Abs(filepath.Join(dir, common))
		if err != nil {
			return "", err
		}
		common = abs
	}
	resolved, err := filepath.EvalSymlinks(common)
	if err != nil {
		return common, nil
	}
	return resolved, nil
}

// MainCheckout returns the primary worktree (the dir that holds the real .git
// directory), derived from any checkout under the same repo. "" on error.
func MainCheckout(dir string) string {
	common, err := CommonDir(dir)
	if err != nil {
		return ""
	}
	return filepath.Dir(common) // ".../<main>/.git" → ".../<main>"
}

func ListWorktrees(worktreeDir string, filterCommon string) ([]Worktree, error) {
	var results []Worktree

	kinds, err := os.ReadDir(worktreeDir)
	if err != nil {
		return results, nil
	}

	for _, kindEntry := range kinds {
		if !kindEntry.IsDir() {
			continue
		}
		kind := kindEntry.Name()
		kindDir := filepath.Join(worktreeDir, kind)

		// Walk inside kind dir. Branch may be `feat/CU-xxx/desc` so the worktree
		// dir is nested two levels deep; also keep flat layout for legacy
		// `task/foo-123` worktrees. Stop descending once a `.git` marker is hit.
		_ = filepath.Walk(kindDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() || path == kindDir {
				return nil
			}
			if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
				return nil
			}

			if filterCommon != "" {
				wc, err := CommonDir(path)
				if err != nil || wc != filterCommon {
					return filepath.SkipDir
				}
			}

			branch := branchAt(path)
			lastCommit, commitMsg := lastCommitInfo(path)

			rel, _ := filepath.Rel(worktreeDir, path)

			results = append(results, Worktree{
				Path:       path,
				Branch:     branch,
				RelPath:    rel,
				Kind:       kind,
				LastCommit: lastCommit,
				CommitMsg:  commitMsg,
			})
			return filepath.SkipDir
		})
	}

	return results, nil
}

// CheckoutClass classifies a filesystem path by its git checkout kind:
//
//	"worktree" — a linked worktree (`.git` is a file pointing at the admin dir)
//	"main"     — the primary checkout (`.git` is a real directory)
//	"dead"     — path is gone or not a git checkout
//
// This is the authoritative test the dashboard uses to decide what's a live
// thread: only linked worktrees count, so deleted paths and the main checkout
// are reaped automatically.
func CheckoutClass(path string) string {
	if path == "" {
		return "dead"
	}
	fi, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		return "dead"
	}
	if fi.IsDir() {
		return "main"
	}
	return "worktree"
}

// CurrentBranch returns the checked-out branch at path, or "" when detached or
// on error. Used to reconcile a session row's branch against the live checkout.
func CurrentBranch(path string) string {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" { // detached
		return ""
	}
	return b
}

// FetchPrune runs `git fetch --prune` so remote-tracking refs reflect server state.
func FetchPrune(repoRoot string) error {
	return cmdRun(repoRoot, "git", "fetch", "--prune", "--quiet")
}

// CheckRemoteGone marks worktrees whose `origin/<branch>` ref is missing.
// Read-only rev-parse calls are fanned out across goroutines (bounded to 8).
// Caller is responsible for running FetchPrune first if fresh data is needed.
// CheckDirty marks worktrees with uncommitted/untracked changes, in parallel,
// so the clean view can preview per-row what a removal will actually do (dirty
// worktrees get skipped) without a git call per render.
func CheckDirty(worktrees []Worktree) []Worktree {
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i := range worktrees {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			worktrees[idx].Dirty = IsDirty(worktrees[idx].Path)
		}(i)
	}
	wg.Wait()
	return worktrees
}

func CheckRemoteGone(repoRoot string, worktrees []Worktree) []Worktree {
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i := range worktrees {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			remoteBranch := "origin/" + worktrees[idx].Branch
			_, err := cmdOutput(repoRoot, "git", "rev-parse", "--verify", "refs/remotes/"+remoteBranch)
			worktrees[idx].RemoteGone = err != nil
		}(i)
	}
	wg.Wait()
	return worktrees
}

// CheckMerged marks worktrees whose branch is already an ancestor of one of the
// base branches (i.e. the work is merged) — a stronger "done" signal than
// remote-gone, since it also catches squash-merges where the remote branch was
// never deleted. Checks against origin/<base> for each configured base. Run
// FetchPrune first for fresh results. Fanned out, bounded to 8.
func CheckMerged(repoRoot string, worktrees []Worktree, bases []string) []Worktree {
	if len(bases) == 0 {
		return worktrees
	}
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i := range worktrees {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, base := range bases {
				if worktrees[idx].Branch == base {
					continue // a base branch isn't "merged into itself"
				}
				ref := "refs/remotes/origin/" + base
				// --is-ancestor exits 0 when the branch is fully contained in base.
				if err := cmdRun(repoRoot, "git", "merge-base", "--is-ancestor", worktrees[idx].Branch, ref); err != nil {
					continue
				}
				// An untouched branch (forked, never committed) sits exactly at
				// origin/base — an ancestor, but nothing was merged. Only call it
				// merged once base has advanced past the branch tip.
				if revCount(repoRoot, worktrees[idx].Branch+".."+ref) == 0 {
					continue
				}
				worktrees[idx].Merged = true
				worktrees[idx].MergedInto = base
				return
			}
		}(i)
	}
	wg.Wait()
	return worktrees
}

// revCount returns the number of commits in the given range (e.g. "a..b"), or 0
// on error. Cheap ancestry/divergence probe.
func revCount(repoRoot, rangeExpr string) int {
	out, err := exec.Command("git", "-C", repoRoot, "rev-list", "--count", rangeExpr).Output()
	if err != nil {
		return 0
	}
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n); err != nil {
		return 0
	}
	return n
}

// BranchExists reports whether a local branch of that name exists.
func BranchExists(repoRoot, branch string) bool {
	return cmdRun(repoRoot, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil
}

// WorktreePathForBranch returns the path of the linked worktree that has branch
// checked out, or "" if none. Lets creation reuse a dropped-but-not-deleted
// thread instead of colliding on the branch name.
func WorktreePathForBranch(repoRoot, branch string) string {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return ""
	}
	want := "branch refs/heads/" + branch
	path := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == want:
			return path
		}
	}
	return ""
}

// ExcludeLocalMeta hides norn's per-worktree metadata (`.worktree.md`,
// `.state.md`, `.norn/`) from git without touching the tracked `.gitignore`, so
// a fresh worktree isn't dirty and these local artifacts can never reach a
// commit or diff. Writes to the exclude file git actually consults for this
// worktree (`git rev-parse --git-path info/exclude`, the shared common-dir
// file), idempotently, so calling it on an existing worktree fixes the whole
// repo. Best-effort: never fails worktree creation.
func ExcludeLocalMeta(wtPath string) {
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	excl := strings.TrimSpace(string(out))
	if excl == "" {
		return
	}
	if !filepath.IsAbs(excl) {
		excl = filepath.Join(wtPath, excl)
	}

	data, _ := os.ReadFile(excl)
	have := map[string]bool{}
	for _, ln := range strings.Split(string(data), "\n") {
		have[strings.TrimSpace(ln)] = true
	}
	var add []string
	for _, p := range []string{".worktree.md", ".state.md", ".norn/"} {
		if !have[p] {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return
	}

	if err := os.MkdirAll(filepath.Dir(excl), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(excl, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(data) > 0 && data[len(data)-1] != '\n' {
		f.WriteString("\n")
	}
	for _, p := range add {
		fmt.Fprintln(f, p)
	}
}

func CreateWorktree(repoRoot, worktreeDir, branch, base string) (string, error) {
	wtPath := filepath.Join(worktreeDir, branch)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", err
	}

	// Reuse a dropped-but-not-deleted thread: dropping only delists a session,
	// so the branch + its worktree survive on disk. Re-create then means reuse.
	if existing := WorktreePathForBranch(repoRoot, branch); existing != "" {
		ExcludeLocalMeta(existing)
		return existing, nil
	}
	// Branch exists but isn't checked out anywhere (worktree removed, branch
	// kept) → attach a fresh worktree to it rather than colliding on `-b`.
	if BranchExists(repoRoot, branch) {
		if err := cmdRun(repoRoot, "git", "worktree", "add", wtPath, branch); err != nil {
			return "", fmt.Errorf("worktree add (existing branch) failed: %w", err)
		}
		ExcludeLocalMeta(wtPath)
		return wtPath, nil
	}

	if err := cmdRun(repoRoot, "git", "fetch", "origin", base, "--quiet"); err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}

	if err := cmdRun(repoRoot, "git", "worktree", "add", "-b", branch, wtPath, "origin/"+base); err != nil {
		return "", fmt.Errorf("worktree add failed: %w", err)
	}

	// Push the empty branch immediately so origin tracks it from day one.
	// Without this, the dashboard's PR lookup and `work diff` against origin/<branch>
	// produce false negatives until the user pushes manually. Best-effort:
	// failures don't block worktree creation (offline / auth issues happen).
	// Silent on failure (offline / auth): printing here would leak under the
	// TUI, and the branch works locally until the next push.
	_ = cmdRun(wtPath, "git", "push", "-u", "origin", branch, "--quiet")

	ExcludeLocalMeta(wtPath)
	return wtPath, nil
}

// FetchPRHead fetches a pull request's head commit into a local branch,
// fork-safe: the `pull/<n>/head` ref resolves for same-repo AND fork PRs on
// GitHub, and the leading `+` force-updates the branch if it already exists (so
// re-reviewing an updated PR just refreshes). Assumes `origin` is the GitHub remote.
func FetchPRHead(repoRoot, prNum, localBranch string) error {
	refspec := fmt.Sprintf("+pull/%s/head:%s", prNum, localBranch)
	if err := cmdRun(repoRoot, "git", "fetch", "origin", refspec, "--quiet"); err != nil {
		return fmt.Errorf("fetch PR #%s failed (is origin a GitHub remote?): %w", prNum, err)
	}
	return nil
}

// AddWorktreeFromRef adds a worktree checked out to an existing local branch.
// Unlike CreateWorktree it does NOT create a new branch (`-b`) or push — used
// for review worktrees, where the branch already exists (a fetched PR head), the
// checkout is read-only, and pushing someone else's PR would be wrong.
func AddWorktreeFromRef(repoRoot, worktreeDir, branch string) (string, error) {
	wtPath := filepath.Join(worktreeDir, branch)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", err
	}
	if err := cmdRun(repoRoot, "git", "worktree", "add", wtPath, branch); err != nil {
		return "", fmt.Errorf("worktree add failed: %w", err)
	}
	ExcludeLocalMeta(wtPath)
	return wtPath, nil
}

// AddWorktreeTracking adds a worktree for a branch that only exists on the
// remote: it creates the local branch at the remote ref's tip and sets it to
// track it. Not a divergent copy — the new local ref points at exactly
// `remoteRef`, which is what checking out someone else's branch has to mean.
func AddWorktreeTracking(repoRoot, worktreeDir, branch, remoteRef string) (string, error) {
	wtPath := filepath.Join(worktreeDir, branch)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", err
	}
	if err := cmdRun(repoRoot, "git", "worktree", "add", "--track", "-b", branch, wtPath, remoteRef); err != nil {
		return "", fmt.Errorf("worktree add (tracking %s) failed: %w", remoteRef, err)
	}
	ExcludeLocalMeta(wtPath)
	return wtPath, nil
}

// RemoteBranchExists reports whether a remote-tracking branch of that name
// exists (`refs/remotes/<remote>/<branch>`).
func RemoteBranchExists(repoRoot, remote, branch string) bool {
	return cmdRun(repoRoot, "git", "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch) == nil
}

// IsDirty reports whether the worktree has uncommitted changes.
func IsDirty(wtPath string) bool {
	out, err := exec.Command("git", "-C", wtPath, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// ChangedFiles returns the sorted set of repo-relative paths this worktree
// touches: files that differ from base (committed since the fork point via
// `base...HEAD`) plus anything uncommitted or untracked. Best-effort — a
// failing git call contributes nothing rather than erroring the caller.
func ChangedFiles(wtPath, base string) []string {
	set := map[string]struct{}{}
	if base != "" {
		if out, err := exec.Command("git", "-C", wtPath, "diff", "--name-only", base+"...HEAD").Output(); err == nil {
			for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if l != "" {
					set[l] = struct{}{}
				}
			}
		}
	}
	if out, err := exec.Command("git", "-C", wtPath, "status", "--porcelain").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if len(l) < 4 {
				continue // "XY path" — need at least a status pair + a path
			}
			p := strings.TrimSpace(l[3:])
			if i := strings.Index(p, " -> "); i >= 0 {
				p = p[i+4:] // rename "old -> new": the new path is what's touched
			}
			if p != "" {
				set[p] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// RemoveOutcome reports what RemoveWorktree actually did for one worktree, so
// the caller can surface a clear summary instead of leaking raw git output.
type RemoveOutcome struct {
	Branch     string
	Removed    bool   // worktree checkout removed from disk
	BranchKept bool   // worktree removed, but the branch was left (unmerged)
	Skipped    bool   // nothing touched — worktree left fully intact
	Stashed    bool   // uncommitted work was parked in the stash before removal
	StashRef   string // stash commit sha (stash@{N} indices shift; the sha doesn't)
	Reason     string // short human reason for Skipped / BranchKept
}

// RemoveRequest is one worktree to remove plus what norn already knows about it,
// so RemoveWorktree can pick the right escalation without re-deriving state.
type RemoveRequest struct {
	Path           string
	Branch         string
	MergedUpstream bool // merged into a base branch, or its remote branch is gone
	Force          bool // remove even when dirty; uncommitted work is stashed first
}

// StashWorktree parks everything uncommitted (untracked included) in the
// repo-shared stash and returns the stash commit sha, short. The stash lives in
// the common dir, so it outlives the worktree checkout. ("", nil) means there
// was nothing to stash.
func StashWorktree(wtPath, branch string) (string, error) {
	out, err := captureRun(wtPath, "git", "stash", "push", "-u", "-m", "norn: "+branch)
	if err != nil {
		return "", fmt.Errorf("%s", removeFirstLine(out))
	}
	if strings.Contains(out, "No local changes to save") {
		return "", nil
	}
	sha, err := cmdOutput(wtPath, "git", "rev-parse", "--short=12", "refs/stash")
	if err != nil {
		return "", nil // stashed, but the ref didn't resolve — don't block removal
	}
	return sha, nil
}

// RemoveWorktree removes a worktree and reports the outcome. Default is safe;
// force is recoverable, never destructive:
//   - `git worktree remove` (no --force). If it refuses (uncommitted / locked),
//     the worktree is left FULLY intact and reported as Skipped — no dir nuke,
//     no branch touch. This avoids orphaned folders + disk bloat.
//   - req.Force stashes the dirty state first (repo-shared stash, recoverable by
//     sha) and only then removes with --force. A failed stash aborts the removal:
//     forcing past it would be the one destructive path here. Locked worktrees
//     are still skipped — a lock is a deliberate mark, dirt isn't.
//   - Branch delete only after the worktree is gone: `git branch -d` (safe). If
//     that refuses, escalate to -D ONLY when MergedUpstream is true (norn
//     already confirmed the branch merged / its remote is gone — git's -d
//     HEAD-check is a false alarm there). Otherwise the branch is KEPT (a
//     dangling ref is harmless; force-losing unmerged work is not).
//
// All git output is captured, never printed to the terminal.
func RemoveWorktree(repoRoot string, req RemoveRequest) RemoveOutcome {
	wtPath, branch := req.Path, req.Branch
	res := RemoveOutcome{Branch: branch}

	args := []string{"worktree", "remove", wtPath}
	if req.Force {
		if IsDirty(wtPath) {
			sha, err := StashWorktree(wtPath, branch)
			if err != nil {
				res.Skipped = true
				res.Reason = "stash failed: " + err.Error()
				return res
			}
			if sha != "" {
				res.Stashed, res.StashRef = true, sha
			}
		}
		// --force also clears ignored leftovers (node_modules, .env symlinks)
		// that the stash deliberately doesn't carry.
		args = append(args, "--force")
	}

	if out, err := captureRun(repoRoot, "git", args...); err != nil {
		res.Skipped = true
		res.Reason = removeReason(out)
		return res
	}
	res.Removed = true
	// Release the ghost entry in .git/worktrees/ so the branch is deletable.
	_, _ = captureRun(repoRoot, "git", "worktree", "prune")

	if _, err := captureRun(repoRoot, "git", "branch", "-d", branch); err != nil {
		if req.MergedUpstream {
			_, _ = captureRun(repoRoot, "git", "branch", "-D", branch) // remote-confirmed merged: safe
		} else {
			res.BranchKept = true
			res.Reason = "branch not merged"
		}
	}
	return res
}

// removeFirstLine reduces captured git output to its first non-empty line, so a
// reason stays one line in the TUI.
func removeFirstLine(out string) string {
	for _, ln := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			return s
		}
	}
	return "unknown error"
}

// removeReason turns a failed `git worktree remove` into a short reason.
func removeReason(out string) string {
	o := strings.ToLower(out)
	switch {
	case strings.Contains(o, "modified or untracked") || strings.Contains(o, "use --force"):
		return "uncommitted changes"
	case strings.Contains(o, "locked"):
		return "locked"
	default:
		return "in use"
	}
}

// PruneWorktrees removes stale entries in .git/worktrees/ whose directories
// no longer exist. Safe to call at any time.
func PruneWorktrees(repoRoot string) error {
	return cmdRun(repoRoot, "git", "worktree", "prune")
}

func SymlinkEnvFiles(repoRoot, wtPath string) error {
	return filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == "node_modules" || name == ".git" || name == ".next" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(info.Name(), ".env") {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		dst := filepath.Join(wtPath, rel)
		if _, err := os.Lstat(dst); err == nil {
			return nil // already exists
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		return os.Symlink(path, dst)
	})
}

// CleanEmptyDirs walks worktreeDir bottom-up and removes every empty directory.
// `os.Remove` only succeeds on empty dirs, so non-empty trees stay untouched.
func CleanEmptyDirs(worktreeDir string) {
	var dirs []string
	_ = filepath.Walk(worktreeDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Reverse so children removed before parents.
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i])
	}
}

func Age(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// DefaultBranchFormat is the shape used when a project sets no branch_format:
// task id in the middle segment. Set branch_format per repo where the
// convention differs, e.g. `{prefix}/{title}/CU-{id}` for id-last.
const DefaultBranchFormat = "{prefix}/#{id}/{title}"

// ComposeBranch renders a branch-name template. Tokens: {prefix}, {title},
// {id} (bare id — the template supplies the `#` or `CU-` decoration). A
// segment whose token is empty is dropped whole, so its decoration goes with
// it rather than leaving `chore/#/foo`.
func ComposeBranch(format, prefix, id, title string) string {
	if format == "" {
		format = DefaultBranchFormat
	}
	var parts []string
	for _, seg := range strings.Split(format, "/") {
		if id == "" && strings.Contains(seg, "{id}") {
			continue
		}
		if title == "" && strings.Contains(seg, "{title}") {
			continue
		}
		seg = strings.ReplaceAll(seg, "{prefix}", prefix)
		seg = strings.ReplaceAll(seg, "{id}", id)
		seg = strings.ReplaceAll(seg, "{title}", title)
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	return strings.Join(parts, "/")
}

// MakeBranch produces a branch name from a hint, in the repo's branch_format
// (see ComposeBranch; empty format → DefaultBranchFormat).
//
//	Prefixes:
//	  feature — new feature (default)
//	  fix     — production bug fix
//	  hotfix  — urgent prod fix
//	  epic    — multi-task umbrella
//	  chore   — everything else (refactor, docs, deps, tooling, internal)
//	  review  — work-only kind for review worktrees; not a real PR prefix
//
// CU id is parsed from `CU-<id>` literals or `clickup.com/t/<id>` URLs. Both
// the type keyword and the id are stripped from the hint before slugging.
func MakeBranch(kind, hint, format string) string {
	cuID, hintRest := extractCUID(hint)

	var prefix string
	if kind == "review" {
		prefix = "review"
	} else {
		prefix, hintRest = inferType(hintRest)
	}

	title := slugify(hintRest)
	// Fallback if nothing distinguishing — use timestamp as the title.
	if cuID == "" && title == "" {
		title = time.Now().Format("20060102-150405")
	}
	return ComposeBranch(format, prefix, cuID, title)
}

var cuRegex = regexp.MustCompile(`(?i)(?:\bCU-|\S*\bclickup\.com/t/)([a-z0-9]+)\S*`)

// clickupIDPatterns matches a ClickUp task id in any of the forms it shows up:
// a clickup.com URL, a CU-<id> literal, a branch's #<id> segment, or a bare
// id (ClickUp ids start `86`). Ordered most-specific first.
var clickupIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)clickup\.com/t/([0-9a-z]+)`),
	regexp.MustCompile(`(?i)\bCU-([0-9a-z]+)`),
	regexp.MustCompile(`#([0-9a-z]{6,})`),
	regexp.MustCompile(`\b(86[0-9a-z]{6,})\b`),
}

// ClickUpID extracts a ClickUp task id from a branch name, hint, or URL, or ""
// if none is present.
func ClickUpID(s string) string {
	for _, re := range clickupIDPatterns {
		if m := re.FindStringSubmatch(s); m != nil {
			return strings.ToLower(m[1])
		}
	}
	return ""
}

// extractCUID returns the CU task id (lowercase, no prefix) and the hint with
// the matched portion removed. Empty id means no match.
func extractCUID(hint string) (string, string) {
	m := cuRegex.FindStringSubmatchIndex(hint)
	if m == nil {
		return "", hint
	}
	id := strings.ToLower(hint[m[2]:m[3]])
	rest := hint[:m[0]] + " " + hint[m[1]:]
	return id, rest
}

// inferType picks a Conventional Branch prefix from the first matching keyword
// in the hint. Returns the prefix and the hint with that keyword stripped so
// it doesn't pollute the slug. Default: feature.
//
// Prefixes:
// feature | fix | hotfix | epic | chore. `chore` is the catch-all for refactor,
// docs, deps, tooling, internal cleanup — those don't get their own branch
// prefix even though they DO have their own commit-type.
func inferType(hint string) (string, string) {
	lower := strings.ToLower(hint)
	keywords := []struct {
		token  string
		prefix string
	}{
		// hotfix before fix so `hotfix` doesn't get eaten by the `fix` rule.
		{"hotfix", "hotfix"},
		{"bugfix", "fix"},
		{"fix", "fix"},
		{"bug", "fix"},
		{"broken", "fix"},
		{"error", "fix"},
		{"epic", "epic"},
		// chore catch-all bucket
		{"chore", "chore"},
		{"refactor", "chore"},
		{"refac", "chore"},
		{"docs", "chore"},
		{"doc", "chore"},
		{"documentation", "chore"},
		{"deps", "chore"},
		{"dependency", "chore"},
		{"dependencies", "chore"},
		{"tooling", "chore"},
		{"internal", "chore"},
		{"cleanup", "chore"},
		{"tidy", "chore"},
		{"test", "chore"},
		{"tests", "chore"},
		// feature triggers
		{"feature", "feature"},
		{"feat", "feature"},
		{"add", "feature"},
		{"new", "feature"},
		{"implement", "feature"},
		{"create", "feature"},
	}
	for _, k := range keywords {
		idx := strings.Index(lower, k.token)
		if idx < 0 {
			continue
		}
		// Word boundary check — avoid matching inside another word.
		if idx > 0 && isWordChar(lower[idx-1]) {
			continue
		}
		end := idx + len(k.token)
		if end < len(lower) && isWordChar(lower[end]) {
			continue
		}
		return k.prefix, hint[:idx] + hint[end:]
	}
	return "feature", hint
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prev := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prev = false
		} else if !prev {
			b.WriteRune('-')
			prev = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

// BranchLacksSlug reports whether a branch name has no human-readable
// description — e.g. `feature/#86c00000`, `feature/CU-86c00000` or a timestamp
// fallback. These are the names worth enriching with an AI-generated slug. Which
// segment holds the id depends on branch_format, so every segment is checked.
func BranchLacksSlug(branch string) bool {
	parts := strings.Split(branch, "/")
	for _, p := range parts[1:] { // parts[0] is the type prefix
		if p == "" || idSegment.MatchString(p) || timestampSlug.MatchString(p) {
			continue
		}
		return false
	}
	return true
}

var timestampSlug = regexp.MustCompile(`^\d{8}-\d{6}$`)

// idSegment matches a branch segment that is only a task id, in any of the
// decorations the platform docs use: `#<id>`, `CU-<id>`, or bare (ClickUp ids
// start `86`).
var idSegment = regexp.MustCompile(`(?i)^(?:#|cu-)[0-9a-z]+$|^86[0-9a-z]{6,}$`)

// validBranchPrefix matches the Conventional Branch prefixes norn allows.
var validBranchPrefix = regexp.MustCompile(`^(feature|fix|hotfix|epic|chore|review)/`)

// NormalizeSuggestedBranch sanitises a branch name suggested by an LLM into a
// safe git ref, or returns "" if it doesn't look like a valid branch. Takes the
// first line, strips quotes/backticks/whitespace, lowercases, keeps only safe
// chars, and requires a known prefix.
func NormalizeSuggestedBranch(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, " \t`'\"")
	s = strings.ToLower(s)
	// Keep only branch-safe characters; collapse the rest.
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '/' || r == '#' || r == '-' || r == '_':
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-/")
	if !validBranchPrefix.MatchString(out) {
		return ""
	}
	return out
}

// SuggestedBranchParts splits an LLM branch suggestion into prefix and title,
// dropping any id segment the model added on its own — the id is norn's to
// place, per the repo's branch_format. Empty prefix means invalid suggestion.
func SuggestedBranchParts(s string) (prefix, title string) {
	nb := NormalizeSuggestedBranch(s)
	if nb == "" {
		return "", ""
	}
	parts := strings.Split(nb, "/")
	var rest []string
	for _, p := range parts[1:] {
		if p == "" || idSegment.MatchString(p) {
			continue
		}
		rest = append(rest, p)
	}
	return parts[0], strings.Join(rest, "-")
}

func branchAt(dir string) string {
	out, err := cmdOutput(dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return filepath.Base(dir)
	}
	return out
}

func lastCommitInfo(dir string) (time.Time, string) {
	out, err := cmdOutput(dir, "git", "log", "-1", "--format=%ct\t%s")
	if err != nil {
		return time.Time{}, ""
	}
	parts := strings.SplitN(out, "\t", 2)
	if len(parts) < 2 {
		return time.Time{}, ""
	}
	var epoch int64
	fmt.Sscanf(parts[0], "%d", &epoch)
	return time.Unix(epoch, 0), parts[1]
}

func CmdOutputPublic(dir string, name string, args ...string) string {
	out, err := cmdOutput(dir, name, args...)
	if err != nil {
		return ""
	}
	return out
}

func cmdOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func cmdRun(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Capture git's chatter rather than letting it bleed onto the terminal /
	// under the TUI (worktree create/remove, fetch, push are all plumbing).
	// Fold any output into the error so failures stay useful.
	if out, err := cmd.CombinedOutput(); err != nil {
		if o := strings.TrimSpace(string(out)); o != "" {
			return fmt.Errorf("%w: %s", err, o)
		}
		return err
	}
	return nil
}

// captureRun runs a command capturing combined stdout+stderr instead of letting
// it hit the terminal. Used where git's chatter would otherwise leak under the
// TUI (e.g. worktree/branch removal).
func captureRun(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
