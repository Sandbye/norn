package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/paths"
	"github.com/sandbye/norn/internal/runlog"
	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/strand"
)

// runStartedMsg reports whether the detached supervisor got off the ground.
// Only the start is reported: what the roles then do reaches the dashboard
// through the session store, which is where the supervisor writes every
// transition anyway.
type runStartedMsg struct {
	taskID string
	err    error
}

// startRoleRunCmd spawns a live strand per role of the task, each in its own
// detached tmux session, and returns at once.
//
// tmux owns the agents, not norn: quitting the TUI leaves every strand working,
// and the pane is a client that comes and goes. That is also why this does not
// wait for anything. A strand's exit is read back from tmux when the rail asks.
func startRoleRunCmd(cfg config.Config, taskID string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		if !strand.Available() {
			return runStartedMsg{taskID: taskID, err: strand.ErrNoTmux}
		}
		store, err := state.Load()
		if err != nil {
			return runStartedMsg{taskID: taskID, err: err}
		}
		if store.FindTask(taskID) == nil {
			return runStartedMsg{taskID: taskID, err: fmt.Errorf("no task %q in the session store", taskID)}
		}

		started := 0
		for _, sess := range store.SessionsForTask(taskID) {
			// The trunk is a strand like any other: it is where a conflict gets
			// resolved and where the PR is opened, and both have to be possible
			// without leaving norn.
			if sess.Role == "" {
				continue
			}
			if strand.Alive(taskID, sess.Role) {
				continue // already running, and re-spawning would lose its work
			}
			if rc, ok := cfg.Role(sess.Role); ok && rc.After != "" {
				// Waiting on another strand: it starts when that one lands, so
				// its branch forks from a trunk that already holds the work it
				// is supposed to build on.
				continue
			}
			agent := cfg.AgentFor(sess.Role)
			argv := strandCmd(cfg, agent, sess.Path, agent.Model).Args
			if err := strand.Spawn(taskID, sess.Role, sess.Path, argv, cols, rows); err != nil {
				return runStartedMsg{taskID: taskID, err: err}
			}
			setStrandRunning(sess.Path)
			started++
		}
		if started == 0 {
			return runStartedMsg{taskID: taskID, err: errors.New("every strand of this task is already running")}
		}
		return runStartedMsg{taskID: taskID}
	}
}

// setStrandRunning records that a strand is live, so the rail says so and a
// restart knows to look for its session.
func setStrandRunning(path string) {
	_, _ = state.Mutate(func(s *state.Store) bool { return s.SetRun(path, state.RunRunning, 0) })
}

// runLogDir is where a task's run logs live: the supervisor's own progress
// next to the per-role output it writes.
func runLogDir(taskID string) string {
	return filepath.Join(paths.State(), "runs", taskID)
}

// runLog opens the supervisor's log. Nobody is watching a detached run, so it
// needs somewhere to say which role merged and which failed.
func runLog(taskID string) (*os.File, error) {
	dir := runLogDir(taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// logLoadedMsg carries a role's rendered log back to the dashboard.
type logLoadedMsg struct {
	role   string
	events []runlog.Event
	err    error
}

// logTailLines is how much of a run the viewer keeps. A rail of recent
// activity, not a transcript: the file stays on disk for the whole story.
const logTailLines = 200

// loadRunLogCmd reads a role's log and renders it. Re-run on every dashboard
// tick while the viewer is open, which is what makes a running role legible:
// the file is being appended to as the agent works.
func loadRunLogCmd(taskID, role string) tea.Cmd {
	return func() tea.Msg {
		events, err := runlog.Tail(roleLogPath(taskID, role), logTailLines)
		return logLoadedMsg{role: role, events: events, err: err}
	}
}

// roleLogPath is where a role's agent writes its JSONL, the same path the
// supervisor opens for it.
func roleLogPath(taskID, role string) string {
	return filepath.Join(runLogDir(taskID), role+".log")
}

// landedMsg reports the outcome of landing one strand on the trunk.
type landedMsg struct {
	taskID  string
	role    string
	commits int
	blocked bool
	err     error
}

// landStrandCmd merges a finished strand into its task's trunk.
//
// A keypress rather than something norn does on exit: with a live pane, exit
// also means "I quit to look at something", and a merge commit is not undone
// casually. The merge itself is #71's, unchanged.
func landStrandCmd(cfg config.Config, row dashRow) tea.Cmd {
	return func() tea.Msg {
		store, err := state.Load()
		if err != nil {
			return landedMsg{role: row.Role, err: err}
		}
		task := store.FindTask(row.TaskID)
		if task == nil {
			return landedMsg{role: row.Role, err: fmt.Errorf("no task %q in the session store", row.TaskID)}
		}
		trunk := store.FindTaskTrunk(row.TaskID)
		if trunk == nil {
			return landedMsg{role: row.Role, err: fmt.Errorf("task %s has no trunk worktree", row.TaskID)}
		}
		if row.Branch == task.Trunk {
			return landedMsg{role: row.Role, err: errors.New("the trunk is what strands land on")}
		}

		n, err := git.CommitsAhead(trunk.Path, task.Trunk, row.Branch)
		if err != nil {
			return landedMsg{role: row.Role, err: err}
		}
		if n == 0 {
			return landedMsg{role: row.Role, err: errors.New("nothing to land: no commits on this strand")}
		}
		if err := checkExpect(cfg, row); err != nil {
			return landedMsg{role: row.Role, err: err}
		}

		msg := fmt.Sprintf("Merge role %s (%s) into %s", row.Role, row.Branch, task.Trunk)
		if err := git.MergeNoFF(trunk.Path, row.Branch, msg); err != nil {
			reason := fmt.Sprintf("%s: %v", row.Role, err)
			_, _ = state.Mutate(func(s *state.Store) bool { return s.SetTaskBlocked(row.TaskID, reason) })
			return landedMsg{role: row.Role, blocked: true, err: err}
		}
		_, _ = state.Mutate(func(s *state.Store) bool { return s.SetRun(row.Path, state.RunMerged, 0) })
		return landedMsg{taskID: row.TaskID, role: row.Role, commits: n}
	}
}

// checkExpect runs the repo's verify in the strand's own worktree and holds the
// merge unless the result is what the role promised.
//
// This is the half of red-first a machine can check: a test strand whose suite
// passes before the implementation exists has written a test that proves
// nothing, and letting it land is how the whole sequence becomes decoration.
func checkExpect(cfg config.Config, row dashRow) error {
	rc, ok := cfg.Role(row.Role)
	if !ok || rc.Expect == "" {
		return nil
	}
	if len(cfg.Verify) == 0 {
		return fmt.Errorf("%s expects %s, but this repo declares no verify commands to check it with", row.Role, rc.Expect)
	}
	passed, failing, out := runVerify(row.Path, cfg.Verify)
	switch {
	case rc.Expect == config.ExpectGreen && !passed:
		return fmt.Errorf("%s must leave the tree green, and `%s` fails:\n%s", row.Role, failing, out)
	case rc.Expect == config.ExpectRed && passed:
		return fmt.Errorf("%s must leave a failing test, and every verify command passes: a test that does not fail proves nothing", row.Role)
	}
	return nil
}

// runVerify runs the configured commands in dir, stopping at the first failure.
// The output of that one is returned, since it is the only one worth reading.
func runVerify(dir string, cmds []string) (passed bool, failing, output string) {
	for _, c := range cmds {
		cmd := exec.Command("sh", "-c", c)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return false, c, lastLines(string(out), 12)
		}
	}
	return true, "", ""
}

// lastLines keeps the tail of a command's output: a failing suite's useful part
// is at the end, and a full log does not belong in a notice.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// spawnWaitingCmd starts the strands that were waiting for the one that just
// landed. This is what makes the sequence real rather than advisory: the next
// role's branch forks from a trunk that already contains its predecessor's
// work, so a test written before the implementation is a fact of the history.
func spawnWaitingCmd(cfg config.Config, taskID, landed string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		store, err := state.Load()
		if err != nil {
			return runStartedMsg{taskID: taskID, err: err}
		}
		started := 0
		for _, sess := range store.SessionsForTask(taskID) {
			rc, ok := cfg.Role(sess.Role)
			if !ok || rc.After != landed || strand.Alive(taskID, sess.Role) {
				continue
			}
			agent := cfg.AgentFor(sess.Role)
			argv := strandCmd(cfg, agent, sess.Path, agent.Model).Args
			if err := strand.Spawn(taskID, sess.Role, sess.Path, argv, cols, rows); err != nil {
				return runStartedMsg{taskID: taskID, err: err}
			}
			setStrandRunning(sess.Path)
			started++
		}
		if started == 0 {
			return nil
		}
		return runStartedMsg{taskID: taskID}
	}
}
