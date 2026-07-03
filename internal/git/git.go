package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
}

func RepoRoot() (string, error) {
	out, err := cmdOutput(".", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository")
	}
	return out, nil
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
		return filepath.Base(dir)
	}
	// CommonDir is the path to the main repo's .git dir (or .git/worktrees/...
	// inside a worktree). Strip the trailing .git component to get the repo dir.
	parent := filepath.Dir(common)
	if filepath.Base(common) != ".git" {
		// Worktree case: common ends in something like .git, but parent is repo.
		// Walk up until we find a dir whose .git matches.
		parent = filepath.Dir(common)
	}
	return filepath.Base(parent)
}

func CommonDir(dir string) (string, error) {
	common, err := cmdOutput(dir, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(common) {
		top, err := cmdOutput(dir, "git", "rev-parse", "--show-toplevel")
		if err != nil {
			return "", err
		}
		common = filepath.Join(top, common)
	}
	resolved, err := filepath.EvalSymlinks(common)
	if err != nil {
		return common, nil
	}
	return resolved, nil
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

// FetchPrune runs `git fetch --prune` so remote-tracking refs reflect server state.
func FetchPrune(repoRoot string) error {
	return cmdRun(repoRoot, "git", "fetch", "--prune", "--quiet")
}

// CheckRemoteGone marks worktrees whose `origin/<branch>` ref is missing.
// Read-only rev-parse calls are fanned out across goroutines (bounded to 8).
// Caller is responsible for running FetchPrune first if fresh data is needed.
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
				if err := cmdRun(repoRoot, "git", "merge-base", "--is-ancestor", worktrees[idx].Branch, ref); err == nil {
					worktrees[idx].Merged = true
					worktrees[idx].MergedInto = base
					return
				}
			}
		}(i)
	}
	wg.Wait()
	return worktrees
}

func CreateWorktree(repoRoot, worktreeDir, branch, base string) (string, error) {
	wtPath := filepath.Join(worktreeDir, branch)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", err
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
	if err := cmdRun(wtPath, "git", "push", "-u", "origin", branch, "--quiet"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: initial push of %s failed: %v\n", branch, err)
	}

	return wtPath, nil
}

func RemoveWorktree(repoRoot, wtPath, branch string) error {
	var errs []string

	if err := cmdRun(repoRoot, "git", "worktree", "remove", "--force", wtPath); err != nil {
		errs = append(errs, "worktree remove: "+err.Error())
	}
	// Remove leftover dir
	if _, err := os.Stat(wtPath); err == nil {
		os.RemoveAll(wtPath)
	}
	// Prune ghost entries in .git/worktrees/ so branch is fully released
	_ = cmdRun(repoRoot, "git", "worktree", "prune")

	if err := cmdRun(repoRoot, "git", "branch", "-D", branch); err != nil {
		errs = append(errs, "branch -D: "+err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
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

// MakeBranch produces a branch name from a hint, following the team SOP at
// how-we-build/coding-guidelines/git-strategy.md (Conventional Branch).
//
//	Format: <prefix>/#<taskId>/<desc>  or  <prefix>/<desc>  (no id case)
//	Prefixes:
//	  feature — new feature (default)
//	  fix     — production bug fix
//	  hotfix  — urgent prod fix
//	  epic    — multi-task umbrella
//	  chore   — everything else (refactor, docs, deps, tooling, internal)
//	  review  — work-only kind for review worktrees; not a real PR prefix
//
// CU id is parsed from `CU-<id>` literals or `clickup.com/t/<id>` URLs and
// emitted as `#<id>` (no `CU-` prefix, per SOP). Both the type keyword and the
// id are stripped from the hint before slugging.
func MakeBranch(kind, hint string) string {
	cuID, hintRest := extractCUID(hint)

	var prefix string
	if kind == "review" {
		prefix = "review"
	} else {
		prefix, hintRest = inferType(hintRest)
	}

	desc := slugify(hintRest)

	parts := []string{prefix}
	if cuID != "" {
		parts = append(parts, "#"+cuID)
	}
	if desc != "" {
		parts = append(parts, desc)
	}
	// Fallback if nothing distinguishing — use timestamp as desc.
	if len(parts) == 1 {
		parts = append(parts, time.Now().Format("20060102-150405"))
	}
	return strings.Join(parts, "/")
}

var cuRegex = regexp.MustCompile(`(?i)(?:\bCU-|\S*\bclickup\.com/t/)([a-z0-9]+)\S*`)

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
// Prefixes (per how-we-build/coding-guidelines/git-strategy.md):
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
