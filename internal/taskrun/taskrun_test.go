package taskrun

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/state"
)

// fixture is a split task on disk and in the store: a trunk worktree, one role
// worktree branched off it, and the task record tying them together.
type fixture struct {
	taskID     string
	trunkPath  string
	trunkRef   string
	rolePath   string
	roleBranch string
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", main).CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	// norn merges with its own process, which inherits the environment rather
	// than this file's git helper, and a CI runner has no identity to inherit.
	gitRun(t, main, "config", "user.name", "norn test")
	gitRun(t, main, "config", "user.email", "test@norn.invalid")
	gitRun(t, main, "commit", "-q", "--allow-empty", "-m", "init")

	f := fixture{
		trunkPath:  filepath.Join(dir, "trunk"),
		trunkRef:   "feature/x/trunk",
		rolePath:   filepath.Join(dir, "logic"),
		roleBranch: "feature/x/logic",
	}
	gitRun(t, main, "worktree", "add", "-q", f.trunkPath, "-b", f.trunkRef)
	gitRun(t, main, "worktree", "add", "-q", f.rolePath, "-b", f.roleBranch, f.trunkRef)

	_, err := state.Mutate(func(s *state.Store) bool {
		task := s.UpsertTask(state.Task{Repo: "norn", Goal: "split", Trunk: f.trunkRef})
		f.taskID = task.ID
		s.UpsertByPath(state.Session{ID: state.MakeID("norn", f.trunkRef), Repo: "norn", Branch: f.trunkRef,
			Path: f.trunkPath, TaskID: task.ID, Role: "integration"})
		s.UpsertByPath(state.Session{ID: state.MakeID("norn", f.roleBranch), Repo: "norn", Branch: f.roleBranch,
			Path: f.rolePath, TaskID: task.ID, Role: "logic"})
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func cfgWithRoles() config.Config {
	return config.Config{Roles: config.Roles{
		"logic":       {AgentConfig: config.AgentConfig{Command: "claude"}},
		"integration": {AgentConfig: config.AgentConfig{Command: "claude"}, Integrates: true},
	}}
}

// fakeAgent replaces the real agent with a shell script, so the test exercises
// the store bookkeeping and the merge rather than a coding agent.
func fakeAgent(t *testing.T, script string) {
	t.Helper()
	prev := startRole
	startRole = func(ctx context.Context, _ config.AgentConfig, dir string, out io.Writer) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		cmd.Stdout, cmd.Stderr = out, out
		return cmd, cmd.Start()
	}
	t.Cleanup(func() { startRole = prev })
}

func sessionAt(t *testing.T, path string) state.Session {
	t.Helper()
	s, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	sess := s.FindByPath(path)
	if sess == nil {
		t.Fatalf("no session row at %s", path)
	}
	return *sess
}

// A role that commits and exits 0 lands on trunk without anyone typing a git
// command, and the store says merged.
func TestRunMergesFinishedRole(t *testing.T) {
	f := newFixture(t)
	fakeAgent(t, "echo v1 > logic.txt && git add logic.txt && git commit -q -m 'add logic'")

	var out strings.Builder
	if err := Run(context.Background(), Options{Cfg: cfgWithRoles(), TaskID: f.taskID, Out: &out}); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(f.trunkPath, "logic.txt")); err != nil {
		t.Fatalf("role's work did not reach trunk: %v\n%s", err, out.String())
	}
	if got := sessionAt(t, f.rolePath).Run; got != state.RunMerged {
		t.Fatalf("run state = %q, want %q\n%s", got, state.RunMerged, out.String())
	}
}

// A non-zero exit leaves trunk untouched and the thread failed, whatever the
// role committed before it died.
func TestRunFailedRoleLeavesTrunkUntouched(t *testing.T) {
	f := newFixture(t)
	fakeAgent(t, "echo v1 > logic.txt && git add logic.txt && git commit -q -m 'half done' && exit 3")

	before := head(t, f.trunkPath)
	err := Run(context.Background(), Options{Cfg: cfgWithRoles(), TaskID: f.taskID, Out: io.Discard})
	if err == nil {
		t.Fatal("Run reported success for a role that exited 3")
	}
	if got := head(t, f.trunkPath); got != before {
		t.Fatalf("trunk moved on a failed role: %s → %s", before, got)
	}
	if got := sessionAt(t, f.rolePath).Run; got != state.RunFailed {
		t.Fatalf("run state = %q, want %q", got, state.RunFailed)
	}
}

// A merge that conflicts stops there: the task is blocked, with the conflict
// left in the trunk worktree for a person rather than resolved or aborted.
func TestRunConflictBlocksTask(t *testing.T) {
	f := newFixture(t)
	gitRun(t, f.trunkPath, "commit", "-q", "--allow-empty", "-m", "trunk moves")
	if err := os.WriteFile(filepath.Join(f.trunkPath, "shared.txt"), []byte("trunk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.trunkPath, "add", "shared.txt")
	gitRun(t, f.trunkPath, "commit", "-q", "-m", "trunk edit")
	fakeAgent(t, "echo role > shared.txt && git add shared.txt && git commit -q -m 'role edit'")

	err := Run(context.Background(), Options{Cfg: cfgWithRoles(), TaskID: f.taskID, Out: io.Discard})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("Run on a conflict = %v, want ErrBlocked", err)
	}
	if !git.InMerge(f.trunkPath) {
		t.Fatal("conflict was cleaned up instead of left for a person")
	}
	s, err2 := state.Load()
	if err2 != nil {
		t.Fatal(err2)
	}
	if blocked := s.FindTask(f.taskID).Blocked; !strings.Contains(blocked, "logic") {
		t.Fatalf("task blocked reason = %q, want it to name the role", blocked)
	}
}

// A role that finished while nobody was watching still merges: the store says
// done, so a later run picks it up without re-running the agent.
func TestRunReconcilesUnwatchedRole(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.rolePath, "logic.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.rolePath, "add", "logic.txt")
	gitRun(t, f.rolePath, "commit", "-q", "-m", "add logic")
	if _, err := state.Mutate(func(s *state.Store) bool {
		return s.SetRun(f.rolePath, state.RunDone, 0)
	}); err != nil {
		t.Fatal(err)
	}
	fakeAgent(t, "echo 'the agent must not run again' >&2 && exit 9")

	if err := Run(context.Background(), Options{Cfg: cfgWithRoles(), TaskID: f.taskID, Out: io.Discard}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.trunkPath, "logic.txt")); err != nil {
		t.Fatalf("unwatched role's work did not reach trunk: %v", err)
	}
	if got := sessionAt(t, f.rolePath).Run; got != state.RunMerged {
		t.Fatalf("run state = %q, want %q", got, state.RunMerged)
	}
}

// An already blocked task refuses to start anything: more roles behind a merge
// that cannot finish is work that cannot land.
func TestRunRefusesBlockedTask(t *testing.T) {
	f := newFixture(t)
	if _, err := state.Mutate(func(s *state.Store) bool {
		return s.SetTaskBlocked(f.taskID, "logic: merge conflict")
	}); err != nil {
		t.Fatal(err)
	}
	fakeAgent(t, "echo 'must not start' >&2 && exit 9")

	if err := Run(context.Background(), Options{Cfg: cfgWithRoles(), TaskID: f.taskID, Out: io.Discard}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Run on a blocked task = %v, want ErrBlocked", err)
	}
	if got := sessionAt(t, f.rolePath).Run; got != "" {
		t.Fatalf("a blocked task started its role anyway: run state %q", got)
	}
}

func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := git.Capture(dir, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v: %s", err, out)
	}
	return strings.TrimSpace(out)
}
