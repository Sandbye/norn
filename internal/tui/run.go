package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
			agent := cfg.AgentFor(sess.Role)
			argv := strandCmd(agent, sess.Path, agent.Model).Args
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
func landStrandCmd(row dashRow) tea.Cmd {
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

		msg := fmt.Sprintf("Merge role %s (%s) into %s", row.Role, row.Branch, task.Trunk)
		if err := git.MergeNoFF(trunk.Path, row.Branch, msg); err != nil {
			reason := fmt.Sprintf("%s: %v", row.Role, err)
			_, _ = state.Mutate(func(s *state.Store) bool { return s.SetTaskBlocked(row.TaskID, reason) })
			return landedMsg{role: row.Role, blocked: true, err: err}
		}
		_, _ = state.Mutate(func(s *state.Store) bool { return s.SetRun(row.Path, state.RunMerged, 0) })
		return landedMsg{role: row.Role, commits: n}
	}
}
