package git

import (
	"os"
	"path/filepath"
	"testing"
)

// worktreeInLayout builds main + a linked worktree at <root>/task/<branch>, the
// layout norn creates, and returns (main, worktreeRoot, worktreePath).
func worktreeInLayout(t *testing.T, branch string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := execGit(main, "init", "-q", "-b", "main", "."); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	run(t, main, "commit", "-q", "--allow-empty", "-m", "init")

	root := filepath.Join(dir, "worktrees")
	wt := filepath.Join(root, "task", branch)
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, main, "worktree", "add", "-q", wt, "-b", branch)
	run(t, wt, "commit", "-q", "--allow-empty", "-m", "work")
	return main, root, wt
}

// detach drops the branch out from under the worktree, the state you land in
// after deleting a branch that a worktree was on.
func detach(t *testing.T, main, wt, branch string) {
	t.Helper()
	run(t, wt, "checkout", "-q", "--detach")
	run(t, main, "branch", "-D", branch)
}

// A worktree whose branch is gone must still be listed, named by the dir it was
// created as rather than git's literal "HEAD".
func TestListWorktreesDetachedKeepsName(t *testing.T) {
	main, root, wt := worktreeInLayout(t, "orphan-thread")
	detach(t, main, wt, "orphan-thread")

	wts, err := ListWorktrees(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 1 {
		t.Fatalf("got %d worktrees, want 1", len(wts))
	}
	got := wts[0]
	if !got.Detached {
		t.Error("Detached not set for a branchless worktree")
	}
	if got.Branch != "orphan-thread" {
		t.Errorf("Branch = %q, want the dir-derived name (never \"HEAD\")", got.Branch)
	}
}

// The remote/merged probes must not run against a detached row: "origin/HEAD"
// usually resolves, which would have shown it as a live branch.
func TestDetachedSkipsRemoteAndMergedProbes(t *testing.T) {
	main, root, wt := worktreeInLayout(t, "orphan-probe")
	detach(t, main, wt, "orphan-probe")

	wts, _ := ListWorktrees(root, "")
	wts = CheckRemoteGone(main, wts)
	wts = CheckMerged(main, wts, []string{"main"})
	if wts[0].RemoteGone || wts[0].Merged {
		t.Errorf("detached row got remote/merged verdicts: %+v", wts[0])
	}
}

// Removing a detached worktree must not try to delete a branch: the name is a
// label, and `branch -d` on it would report a kept branch that never existed.
func TestRemoveWorktreeDetachedNoBranchDelete(t *testing.T) {
	main, root, wt := worktreeInLayout(t, "orphan-remove")
	detach(t, main, wt, "orphan-remove")

	wts, _ := ListWorktrees(root, "")
	res := RemoveWorktree(main, RemoveRequest{Path: wt, Branch: wts[0].Branch, Detached: true})
	if !res.Removed {
		t.Fatalf("outcome = %+v, want Removed", res)
	}
	if res.BranchKept {
		t.Errorf("reported a kept branch for a detached worktree: %+v", res)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present: %v", err)
	}
}

// DetachedHead labels the sha only when HEAD is off-branch.
func TestDetachedHead(t *testing.T) {
	main, _, wt := worktreeInLayout(t, "orphan-head")
	if got := DetachedHead(wt); got != "" {
		t.Errorf("on-branch worktree reported detached at %q", got)
	}
	detach(t, main, wt, "orphan-head")
	if got := DetachedHead(wt); got == "" {
		t.Error("detached worktree reported no sha")
	}
}

// WorktreePaths finds linked worktrees and ignores the main checkout.
func TestWorktreePathsSkipsMainCheckout(t *testing.T) {
	_, root, wt := worktreeInLayout(t, "scan-me")
	paths := WorktreePaths(root)
	if len(paths) != 1 || paths[0] != wt {
		t.Errorf("WorktreePaths = %v, want [%s]", paths, wt)
	}
}
