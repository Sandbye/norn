package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

func RepoRoot() (string, error) {
	out, err := cmdOutput(".", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository")
	}
	return out, nil
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

	for _, kind := range []string{"task", "review"} {
		kindDir := filepath.Join(worktreeDir, kind)
		entries, err := os.ReadDir(kindDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wtPath := filepath.Join(kindDir, e.Name())

			if filterCommon != "" {
				wc, err := CommonDir(wtPath)
				if err != nil || wc != filterCommon {
					continue
				}
			}

			branch := branchAt(wtPath)
			lastCommit, commitMsg := lastCommitInfo(wtPath)

			rel := filepath.Join(kind, e.Name())

			results = append(results, Worktree{
				Path:       wtPath,
				Branch:     branch,
				RelPath:    rel,
				Kind:       kind,
				LastCommit: lastCommit,
				CommitMsg:  commitMsg,
			})
		}
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

func CleanEmptyDirs(worktreeDir string) {
	for _, kind := range []string{"task", "review"} {
		kindDir := filepath.Join(worktreeDir, kind)
		entries, err := os.ReadDir(kindDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(kindDir, e.Name())
			// Remove if empty
			os.Remove(p)
		}
		os.Remove(kindDir)
	}
	os.Remove(worktreeDir)
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

func MakeBranch(kind, hint string) string {
	ts := time.Now().Format("150405")
	if hint == "" {
		return fmt.Sprintf("%s/%s-%s", kind, time.Now().Format("20060102"), ts)
	}
	slug := slugify(hint)
	return fmt.Sprintf("%s/%s-%s", kind, slug, ts)
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
