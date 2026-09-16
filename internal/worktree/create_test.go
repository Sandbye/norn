package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/state"
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

// repoWithOrigin builds a clone of a bare origin holding one commit on main,
// plus an isolated session store. A create pushes its new branches, so the
// remote has to be real for the push (and its unwind) to be exercised.
func repoWithOrigin(t *testing.T) (repoRoot, worktreeDir string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	origin := filepath.Join(dir, "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	seed := filepath.Join(dir, "seed")
	run(t, dir, "clone", "-q", origin, seed)
	run(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	run(t, seed, "push", "-q", "origin", "main")

	repoRoot = filepath.Join(dir, "repo")
	run(t, dir, "clone", "-q", origin, repoRoot)
	return repoRoot, filepath.Join(dir, "worktrees")
}

func splitCfg(worktreeDir string) config.Config {
	cfg := rolesCfg()
	cfg.WorktreeDir = worktreeDir
	return cfg
}

func branchExists(t *testing.T, repoRoot, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

// One create makes the trunk plus one worktree per non-integrating role, all
// under one task id, and each brief names the role that owns it.
func TestCreateSplitsAcrossRoles(t *testing.T) {
	repoRoot, worktreeDir := repoWithOrigin(t)
	cfg := splitCfg(worktreeDir)

	res, err := Create(cfg, repoRoot, Request{
		Kind:   "task",
		Hint:   "multi model",
		Branch: "feature/multi-model",
		Base:   "main",
		Roles:  []string{"logic", "assets"},
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if !res.Split() {
		t.Fatal("Split() = false, want a task id")
	}
	if len(res.Threads) != 3 {
		t.Fatalf("threads = %d, want 3", len(res.Threads))
	}
	if got := res.Trunk().Branch; got != "feature/multi-model/trunk" {
		t.Fatalf("trunk = %q, want feature/multi-model/trunk", got)
	}

	for _, th := range res.Threads {
		if !branchExists(t, repoRoot, th.Branch) {
			t.Fatalf("branch %q not created", th.Branch)
		}
		brief, err := os.ReadFile(filepath.Join(th.Path, ".worktree.md"))
		if err != nil {
			t.Fatalf("brief for %s: %v", th.Role, err)
		}
		if !strings.Contains(string(brief), "role: "+th.Role) {
			t.Fatalf("brief for %s does not name its role:\n%s", th.Role, brief)
		}
		if !strings.Contains(string(brief), "trunk: feature/multi-model/trunk") {
			t.Fatalf("brief for %s does not name the trunk:\n%s", th.Role, brief)
		}
	}

	// A role forks from the trunk, so its diff baseline is the trunk and not
	// the base the trunk itself came from.
	logic := res.Threads[2]
	brief, _ := os.ReadFile(filepath.Join(logic.Path, ".worktree.md"))
	if !strings.Contains(string(brief), "base: feature/multi-model/trunk") {
		t.Fatalf("role brief forks from the wrong base:\n%s", brief)
	}

	store, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Tasks) != 1 || store.Tasks[0].ID != res.TaskID {
		t.Fatalf("tasks = %+v, want one with id %q", store.Tasks, res.TaskID)
	}
	if store.Tasks[0].Trunk != "feature/multi-model/trunk" || store.Tasks[0].Goal != "multi model" {
		t.Fatalf("task = %+v, want the trunk branch and the hint as goal", store.Tasks[0])
	}
	rows := store.SessionsForTask(res.TaskID)
	if len(rows) != 3 {
		t.Fatalf("sessions for task = %d, want 3", len(rows))
	}
	for _, r := range rows {
		if r.Role == "" {
			t.Fatalf("session %q carries no role", r.Branch)
		}
	}
}

// A repo with roles that a create does not pick still produces exactly the one
// worktree it produced before roles existed, on the plain branch name.
func TestCreateWithoutRolesRecordsNoTask(t *testing.T) {
	repoRoot, worktreeDir := repoWithOrigin(t)

	res, err := Create(splitCfg(worktreeDir), repoRoot, Request{
		Kind:   "task",
		Hint:   "one thing",
		Branch: "feature/one-thing",
		Base:   "main",
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if res.Split() {
		t.Fatalf("Split() = true, want a plain worktree (task id %q)", res.TaskID)
	}
	if len(res.Threads) != 1 || res.Threads[0].Branch != "feature/one-thing" {
		t.Fatalf("threads = %+v, want one on the plain branch", res.Threads)
	}
	store, _ := state.Load()
	if len(store.Tasks) != 0 {
		t.Fatalf("tasks = %+v, want none", store.Tasks)
	}
}

// A create that fails on the second worktree leaves nothing behind: no
// worktree, no branch locally or on origin, and no session row.
func TestCreateUnwindsOnFailure(t *testing.T) {
	repoRoot, worktreeDir := repoWithOrigin(t)
	cfg := splitCfg(worktreeDir)

	// `refs/heads/feature/x/assets/sub` makes `feature/x/assets` an impossible
	// ref, so the create fails after the trunk is already standing.
	run(t, repoRoot, "branch", "feature/x/assets/sub")

	_, err := Create(cfg, repoRoot, Request{
		Kind:   "task",
		Hint:   "doomed",
		Branch: "feature/x",
		Base:   "main",
		Roles:  []string{"logic", "assets"},
	})
	if err == nil {
		t.Fatal("Create() = nil, want the ref conflict")
	}
	// Name the branch that failed, so the test cannot pass on an error raised
	// before the trunk was ever built.
	if !strings.Contains(err.Error(), "feature/x/assets") {
		t.Fatalf("Create() = %v, want the failure to be the assets branch", err)
	}
	if branchExists(t, repoRoot, "feature/x/trunk") {
		t.Fatal("trunk branch survived a failed create")
	}
	if _, statErr := os.Stat(filepath.Join(worktreeDir, "feature", "x", "trunk")); statErr == nil {
		t.Fatal("trunk worktree survived a failed create")
	}
	if out := run(t, repoRoot, "ls-remote", "--heads", "origin", "feature/x/trunk"); strings.TrimSpace(out) != "" {
		t.Fatalf("trunk branch survived on origin: %s", out)
	}
	store, _ := state.Load()
	if len(store.Sessions) != 0 || len(store.Tasks) != 0 {
		t.Fatalf("store = %+v, want nothing recorded", store)
	}
}
