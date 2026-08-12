package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run fails the test on error — setup steps are not the thing under test.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// repoWithDirtyWorktree builds main + one linked worktree on feat/x that has a
// committed change plus uncommitted and untracked files.
func repoWithDirtyWorktree(t *testing.T) (main, wt string) {
	t.Helper()
	dir := t.TempDir()
	main = filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", main).CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	run(t, main, "commit", "-q", "--allow-empty", "-m", "init")

	wt = filepath.Join(dir, "wt")
	run(t, main, "worktree", "add", "-q", wt, "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt, "add", "tracked.txt")
	run(t, wt, "commit", "-q", "-m", "add tracked")
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return main, wt
}

// A dirty worktree must be left completely alone without Force.
func TestRemoveWorktreeDirtyIsSkipped(t *testing.T) {
	main, wt := repoWithDirtyWorktree(t)

	res := RemoveWorktree(main, RemoveRequest{Path: wt, Branch: "feat/x"})
	if !res.Skipped || res.Removed {
		t.Fatalf("outcome = %+v, want Skipped", res)
	}
	if res.Reason != "uncommitted changes" {
		t.Errorf("reason = %q, want %q", res.Reason, "uncommitted changes")
	}
	if _, err := os.Stat(filepath.Join(wt, "untracked.txt")); err != nil {
		t.Errorf("untracked file gone after a skipped removal: %v", err)
	}
}

// Force removes the worktree, and the work survives in the repo-shared stash —
// recoverable by sha even though the checkout is gone.
func TestRemoveWorktreeForceStashesFirst(t *testing.T) {
	main, wt := repoWithDirtyWorktree(t)

	res := RemoveWorktree(main, RemoveRequest{Path: wt, Branch: "feat/x", Force: true})
	if !res.Removed {
		t.Fatalf("outcome = %+v, want Removed", res)
	}
	if !res.Stashed || res.StashRef == "" {
		t.Fatalf("outcome = %+v, want Stashed with a ref", res)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present: %v", err)
	}

	// `stash branch` restores the base commit too, so it works after the branch
	// was deleted with the worktree.
	run(t, main, "stash", "branch", "recovered", res.StashRef)
	if got, err := os.ReadFile(filepath.Join(main, "tracked.txt")); err != nil || string(got) != "v2\n" {
		t.Errorf("recovered tracked.txt = %q (%v), want %q", got, err, "v2\n")
	}
	if _, err := os.Stat(filepath.Join(main, "untracked.txt")); err != nil {
		t.Errorf("untracked file not recovered: %v", err)
	}
}

// Force on a clean worktree is just a removal: nothing to stash.
func TestRemoveWorktreeForceCleanNoStash(t *testing.T) {
	main, wt := repoWithDirtyWorktree(t)
	run(t, wt, "checkout", "-q", "--", ".")
	if err := os.Remove(filepath.Join(wt, "untracked.txt")); err != nil {
		t.Fatal(err)
	}

	res := RemoveWorktree(main, RemoveRequest{Path: wt, Branch: "feat/x", Force: true})
	if !res.Removed || res.Stashed {
		t.Fatalf("outcome = %+v, want Removed without Stashed", res)
	}
	if out := run(t, main, "stash", "list"); out != "" {
		t.Errorf("stash list = %q, want empty", out)
	}
}
