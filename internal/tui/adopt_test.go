package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sandbye/norn/internal/state"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A worktree on disk must end up in the store even when no session row exists:
// dropped row, hand-made worktree, or a branch deleted under it. Without this
// the only view that shows it is Clean, where the only verb is delete.
func TestAdoptWorktrees(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", main).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v: %s", err, out)
	}
	gitRun(t, main, "commit", "-q", "--allow-empty", "-m", "init")

	root := filepath.Join(dir, "worktrees")
	onBranch := filepath.Join(root, "task", "alpha")
	orphan := filepath.Join(root, "review", "beta")
	if err := os.MkdirAll(filepath.Join(root, "task"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, main, "worktree", "add", "-q", onBranch, "-b", "alpha")
	gitRun(t, main, "worktree", "add", "-q", orphan, "-b", "beta")
	// Delete beta's branch under it: the case that started this.
	gitRun(t, orphan, "checkout", "-q", "--detach")
	gitRun(t, main, "branch", "-D", "beta")

	store := &state.Store{}
	if !adoptWorktrees(store, root) {
		t.Fatal("adoptWorktrees reported nothing added")
	}
	if len(store.Sessions) != 2 {
		t.Fatalf("adopted %d sessions, want 2: %+v", len(store.Sessions), store.Sessions)
	}

	byPath := map[string]state.Session{}
	for _, s := range store.Sessions {
		byPath[s.Path] = s
	}
	a, ok := byPath[onBranch]
	if !ok {
		t.Fatalf("on-branch worktree not adopted: %+v", store.Sessions)
	}
	if a.Branch != "alpha" || a.Kind != "task" || a.Repo != "myrepo" {
		t.Errorf("adopted %+v, want branch=alpha kind=task repo=myrepo", a)
	}
	b, ok := byPath[orphan]
	if !ok {
		t.Fatalf("detached worktree not adopted: %+v", store.Sessions)
	}
	if b.Branch != "beta" {
		t.Errorf("detached branch label = %q, want the dir name \"beta\"", b.Branch)
	}
	if b.Kind != "review" {
		t.Errorf("kind = %q, want review (from the layout)", b.Kind)
	}

	// Idempotent: a second pass must not duplicate or re-report.
	if adoptWorktrees(store, root) {
		t.Error("second pass reported additions")
	}
	if len(store.Sessions) != 2 {
		t.Errorf("store grew to %d sessions on the second pass", len(store.Sessions))
	}
}
