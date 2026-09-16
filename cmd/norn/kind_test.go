package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The session store keeps resolved paths, so a worktree_dir that goes through a
// symlink must still classify: otherwise a review thread flips to task the first
// time it's resumed from the dashboard.
func TestWorktreeKindThroughSymlink(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktrees := filepath.Join(real, "worktrees")
	wt := filepath.Join(worktrees, "review", "pr-64")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(real, "link")
	if err := os.Symlink(worktrees, link); err != nil {
		t.Fatal(err)
	}

	if got := worktreeKind(worktrees, wt); got != "review" {
		t.Errorf("same spelling: kind = %q, want review", got)
	}
	// worktree_dir spelled through the symlink, path as the store returns it.
	if got := worktreeKind(link, wt); got != "review" {
		t.Errorf("symlinked worktree_dir: kind = %q, want review", got)
	}
}
