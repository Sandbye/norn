package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// splitTask builds the shape a split create produces: a trunk worktree and one
// role worktree branched off it, both as sibling leaves under one branch name.
func splitTask(t *testing.T) (trunkPath, trunkBranch, rolePath, roleBranch string) {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", main).CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	run(t, main, "commit", "-q", "--allow-empty", "-m", "init")

	trunkBranch, roleBranch = "feature/x/trunk", "feature/x/logic"
	trunkPath = filepath.Join(dir, "trunk")
	rolePath = filepath.Join(dir, "logic")
	run(t, main, "worktree", "add", "-q", trunkPath, "-b", trunkBranch)
	run(t, main, "worktree", "add", "-q", rolePath, "-b", roleBranch, trunkBranch)
	return trunkPath, trunkBranch, rolePath, roleBranch
}

func writeCommit(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", name)
	run(t, dir, "commit", "-q", "-m", msg)
}

// A role that committed lands on trunk as a merge commit, and one that
// committed nothing counts zero rather than erroring.
func TestMergeNoFF(t *testing.T) {
	trunkPath, trunkBranch, rolePath, roleBranch := splitTask(t)

	if n, err := CommitsAhead(trunkPath, trunkBranch, roleBranch); err != nil || n != 0 {
		t.Fatalf("CommitsAhead on an untouched role = %d, %v; want 0, nil", n, err)
	}

	writeCommit(t, rolePath, "logic.txt", "v1\n", "add logic")
	n, err := CommitsAhead(trunkPath, trunkBranch, roleBranch)
	if err != nil || n != 1 {
		t.Fatalf("CommitsAhead = %d, %v; want 1, nil", n, err)
	}

	if err := MergeNoFF(trunkPath, roleBranch, "merge logic"); err != nil {
		t.Fatalf("MergeNoFF: %v", err)
	}
	if InMerge(trunkPath) {
		t.Fatal("a clean merge left the worktree mid-merge")
	}
	if _, err := os.Stat(filepath.Join(trunkPath, "logic.txt")); err != nil {
		t.Fatalf("role's file did not reach trunk: %v", err)
	}
	// --no-ff, so the merge is a commit with two parents even though trunk did
	// not move: the role stays a visible unit in trunk's history.
	if out, _ := execGit(trunkPath, "rev-list", "--count", "--merges", "HEAD~1..HEAD"); out != "1\n" {
		t.Fatalf("merge count = %q, want a merge commit", out)
	}
}

// A conflict is reported as one and the worktree is left mid-merge for a
// person, not aborted behind their back.
func TestMergeNoFFConflictLeavesWorktree(t *testing.T) {
	trunkPath, _, rolePath, roleBranch := splitTask(t)
	writeCommit(t, rolePath, "shared.txt", "role\n", "role edit")
	writeCommit(t, trunkPath, "shared.txt", "trunk\n", "trunk edit")

	err := MergeNoFF(trunkPath, roleBranch, "merge logic")
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("MergeNoFF on a conflict = %v, want ErrMergeConflict", err)
	}
	if !InMerge(trunkPath) {
		t.Fatal("conflict was cleaned up instead of left for a person")
	}
}

// Uncommitted work in trunk stops the merge before it starts, so nothing of
// the person's gets folded into a merge commit they did not make.
func TestMergeNoFFRefusesDirtyTrunk(t *testing.T) {
	trunkPath, _, rolePath, roleBranch := splitTask(t)
	writeCommit(t, rolePath, "logic.txt", "v1\n", "add logic")
	if err := os.WriteFile(filepath.Join(trunkPath, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, trunkPath, "add", "wip.txt")

	if err := MergeNoFF(trunkPath, roleBranch, "merge logic"); !errors.Is(err, ErrTrunkDirty) {
		t.Fatalf("MergeNoFF into a dirty trunk = %v, want ErrTrunkDirty", err)
	}
	if InMerge(trunkPath) {
		t.Fatal("refused merge still started one")
	}
}
