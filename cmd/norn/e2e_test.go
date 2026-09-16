package main

// End-to-end tests that run the built binary, because the unit tests cover
// packages and the TUI models but nothing exercises the CLI surface a release
// promises: path resolution, the legacy-namespace fallback, worktree creation,
// and the headless `brief` contract.
//
// Every case gets its own $HOME and its own throwaway repos, so nothing here
// touches the developer's real config, worktrees or session store.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The binary is built once, lazily, on the first case that needs it: building
// in TestMain would run before the flags are parsed, so -short could not skip it.
var (
	buildOnce sync.Once
	nornBin   string
	buildErr  error
)

func binary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e builds the binary; skipped with -short")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "norn-e2e-")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "norn")
		if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("building norn: %w\n%s", err, out)
			return
		}
		nornBin = bin
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return nornBin
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) out() string { return r.stdout + r.stderr }

// runNorn runs the built binary in dir with home as $HOME. A fixed identity and
// an empty XDG_STATE_HOME keep a run independent of the developer's machine.
func runNorn(t *testing.T, home, dir string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binary(t), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_STATE_HOME=",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	_ = cmd.Run()
	return result{stdout.String(), stderr.String(), cmd.ProcessState.ExitCode()}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo builds a clone with a real `origin`, because creating a worktree
// fetches the base from origin and pushes the new branch to it. A bare repo on
// disk gives that without a network.
func newRepo(t *testing.T) (repo string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	gitRun(t, root, "init", "-q", "--bare", "-b", "main", bare)

	repo = filepath.Join(root, "repo")
	gitRun(t, root, "clone", "-q", bare, repo)
	write(t, filepath.Join(repo, "README.md"), "# fixture\n")
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-q", "-m", "init")
	gitRun(t, repo, "push", "-q", "-u", "origin", "main")
	return repo
}

// testConfig keeps a run deterministic and offline: `true` as the agent is a
// no-op that exits 0 (LaunchAgent only Runs the command), and AI naming would
// otherwise shell out to claude.
func testConfig(t *testing.T, home, worktreeDir string) {
	t.Helper()
	write(t, filepath.Join(home, ".config", "norn", "config.yaml"),
		"worktree_dir: "+worktreeDir+"\nbase_branches: [main]\nai_naming: false\nagent:\n  command: \"true\"\n")
}

func TestFreshInstallResolvesTheNornPaths(t *testing.T) {
	home := t.TempDir()
	r := runNorn(t, home, home, "doctor")

	if !strings.Contains(r.out(), "✓ config namespace is norn") {
		t.Errorf("doctor did not report a clean namespace:\n%s", r.out())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "work")); err == nil {
		t.Error("a fresh install created the legacy ~/.config/work")
	}
}

// The upgrade path: an install that only has the old dirs must keep working,
// and must be told how to retire them.
func TestLegacyConfigIsStillReadAndReported(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".config", "work", "config.yaml"), "theme: frog\n")

	r := runNorn(t, home, home, "doctor")
	if !strings.Contains(r.out(), "still reading the old \"work\" paths") {
		t.Errorf("doctor did not flag the legacy namespace:\n%s", r.out())
	}
	if !strings.Contains(r.out(), "mv ~/.config/work ~/.config/norn") {
		t.Errorf("doctor did not name the move:\n%s", r.out())
	}

	// Proof the legacy file is genuinely parsed rather than skipped: a broken
	// one has to fail the run. Passing on unreadable config is the silent
	// failure this whole fallback exists to avoid.
	broken := t.TempDir()
	write(t, filepath.Join(broken, ".config", "work", "config.yaml"), "theme: [unterminated\n")
	r = runNorn(t, broken, broken, "doctor")
	if r.code == 0 || !strings.Contains(r.out(), "yaml:") {
		t.Errorf("a broken legacy config did not fail the run (code %d):\n%s", r.code, r.out())
	}
}

func TestCreateProducesWorktreeBranchBriefAndSessionRow(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t)
	worktrees := filepath.Join(home, "worktrees")
	testConfig(t, home, worktrees)

	r := runNorn(t, home, repo, "create", "fix payout rounding")
	if r.code != 0 {
		t.Fatalf("create exited %d:\n%s", r.code, r.out())
	}

	// Ask git rather than guessing the layout: worktrees nest under the branch
	// type, so the path is <worktree_dir>/<type>/<slug>. git reports the real
	// path, and on macOS the temp dir reaches it through /var -> /private/var.
	realWorktrees, err := filepath.EvalSymlinks(worktrees)
	if err != nil {
		t.Fatalf("no worktree dir created: %v", err)
	}
	var wt, branch string
	for _, line := range strings.Split(gitOut(t, repo, "worktree", "list", "--porcelain"), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && strings.HasPrefix(p, realWorktrees) {
			wt = p
		}
		if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok && wt != "" && branch == "" {
			branch = b
		}
	}
	if wt == "" {
		t.Fatalf("no worktree created under %s", realWorktrees)
	}

	if _, err := os.Stat(filepath.Join(wt, ".worktree.md")); err != nil {
		t.Errorf("no .worktree.md brief in %s", wt)
	}
	if !strings.HasPrefix(branch, "fix/") {
		t.Errorf("branch = %q, want a fix/ branch from a %q hint", branch, "fix payout rounding")
	}

	// The session row is what puts the thread on the dashboard.
	var store struct {
		Sessions []struct{ Path, Branch string } `json:"sessions"`
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "state", "norn", "sessions.json"))
	if err != nil {
		t.Fatalf("no session store: %v", err)
	}
	if err := json.Unmarshal(data, &store); err != nil {
		t.Fatalf("session store is not valid JSON: %v", err)
	}
	found := false
	for _, s := range store.Sessions {
		if s.Branch == branch {
			found = true
		}
	}
	if !found {
		t.Errorf("no session row for %s: %s", branch, data)
	}
}

// brief is the headless contract other tools build on, so its promise that it
// creates nothing is part of the API, not an implementation detail.
func TestBriefReportsAndCreatesNothing(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t)
	worktrees := filepath.Join(home, "worktrees")
	testConfig(t, home, worktrees)

	r := runNorn(t, home, repo, "brief", "--repo", repo, "--hint", "fix payout rounding")
	if r.code != 0 {
		t.Fatalf("brief exited %d:\n%s", r.code, r.out())
	}

	var out struct {
		Branch string `json:"branch"`
		Brief  string `json:"brief"`
		Config struct {
			Sources []string `json:"sources"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("brief output is not JSON: %v\n%s", err, r.stdout)
	}
	if !strings.HasPrefix(out.Branch, "fix/") {
		t.Errorf("branch = %q, want a fix/ branch", out.Branch)
	}
	if out.Brief == "" {
		t.Error("brief text is empty")
	}
	if len(out.Config.Sources) == 0 {
		t.Error("config.sources is empty, so a caller can't tell 'no project config' from 'no verify commands'")
	}

	if _, err := os.Stat(worktrees); err == nil {
		t.Error("brief created a worktree dir")
	}
	if branches := gitOut(t, repo, "branch", "--list", "fix/*"); branches != "" {
		t.Errorf("brief created branches: %q", branches)
	}
}

// The wrapper and writeCdTarget have to agree on one directory, or pressing
// enter silently fails to cd.
func TestShellInitPointsAtTheResolvedCacheDir(t *testing.T) {
	home := t.TempDir()
	r := runNorn(t, home, home, "shell-init", "zsh")

	want := filepath.Join(home, ".cache", "norn", "cd-target-")
	if !strings.Contains(r.stdout, want) {
		t.Errorf("wrapper does not point at %s:\n%s", want, r.stdout)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// diff's whole job is answering "what am I about to ship". A base that does not
// resolve used to answer "nothing", with exit 0, because the underlying git
// calls failed quietly and an empty diff prints as a clean branch.
func TestDiffRejectsAnUnresolvableBase(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t)
	testConfig(t, home, filepath.Join(home, "worktrees"))

	gitRun(t, repo, "checkout", "-q", "-b", "fix/rounding")
	write(t, filepath.Join(repo, "ledger.go"), "package ledger\n")
	gitRun(t, repo, "add", "ledger.go")
	gitRun(t, repo, "commit", "-q", "-m", "add ledger")

	// Sanity: against a real base the commit shows up. Without this the test
	// would pass on a repo that simply has nothing to diff.
	if r := runNorn(t, home, repo, "diff", "--plain", "--base", "main"); r.code != 0 || !strings.Contains(r.stdout, "ledger.go") {
		t.Fatalf("known base: exit %d, want the commit in the diff:\n%s", r.code, r.out())
	}

	r := runNorn(t, home, repo, "diff", "--plain", "--base", "does-not-exist")
	if r.code == 0 {
		t.Errorf("unknown base exited 0:\n%s", r.out())
	}
	if strings.Contains(r.out(), "No committed changes") {
		t.Errorf("unknown base reported a clean branch:\n%s", r.out())
	}
	if !strings.Contains(r.stderr, "does-not-exist") {
		t.Errorf("error does not name the bad ref:\n%s", r.stderr)
	}
}

// The worst newcomer outcome norn had: with no agent installed, create cleared
// the screen and scrollback, exited 0, said nothing, and left a worktree the
// user never heard about. The README calls the agent optional, so this path has
// to end with the user knowing where the work is.
func TestCreateWithoutAnAgentSaysWhereTheWorktreeIs(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t)
	worktrees := filepath.Join(home, "worktrees")
	write(t, filepath.Join(home, ".config", "norn", "config.yaml"),
		"worktree_dir: "+worktrees+"\nbase_branches: [main]\nai_naming: false\nagent:\n  command: norn-no-such-agent\n")

	r := runNorn(t, home, repo, "create", "fix payout rounding")

	if r.code != 0 {
		t.Fatalf("create exited %d, want 0 (the worktree is still real):\n%s", r.code, r.out())
	}
	if !strings.Contains(r.stdout, worktrees) {
		t.Errorf("output never names the worktree path:\n%s", r.out())
	}
	if !strings.Contains(r.stderr, "norn-no-such-agent") {
		t.Errorf("stderr does not say the agent is missing:\n%s", r.stderr)
	}
	// ESC[2J clears the screen and ESC[3J the scrollback: doing either here
	// erases the only line that named the worktree.
	for _, seq := range []string{"\x1b[2J", "\x1b[3J"} {
		if strings.Contains(r.stdout, seq) {
			t.Errorf("cleared the screen with %q despite having no agent to hand off to", seq)
		}
	}
}
