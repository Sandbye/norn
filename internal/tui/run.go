package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/paths"
	"github.com/sandbye/norn/internal/runlog"
)

// runStartedMsg reports whether the detached supervisor got off the ground.
// Only the start is reported: what the roles then do reaches the dashboard
// through the session store, which is where the supervisor writes every
// transition anyway.
type runStartedMsg struct {
	taskID string
	err    error
}

// startRoleRunCmd spawns `norn run <task-id>` detached and returns at once, so
// the rail keeps rendering while the roles work. Detached rather than
// tea.ExecProcess: a role is tens of minutes of real work, and handing the
// terminal over for that long would cost every other thread on the dashboard.
//
// os.Executable, not "norn": a norn built from a branch has to run its own
// supervisor, not whichever one is first on PATH.
func startRoleRunCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		bin, err := os.Executable()
		if err != nil {
			return runStartedMsg{taskID: taskID, err: err}
		}
		log, err := runLog(taskID)
		if err != nil {
			return runStartedMsg{taskID: taskID, err: err}
		}
		cmd := exec.Command(bin, "run", taskID)
		cmd.Stdout, cmd.Stderr = log, log
		// Its own session, so quitting the TUI (or Ctrl-C in it) does not take
		// the roles down with it.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			log.Close()
			return runStartedMsg{taskID: taskID, err: fmt.Errorf("start %s run: %w", filepath.Base(bin), err)}
		}
		go func() { _ = cmd.Wait(); log.Close() }() // reap, so it leaves no zombie
		return runStartedMsg{taskID: taskID}
	}
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
