// Package strand runs one agent of a task in its own detached tmux session.
//
// tmux owns the process, not norn. That is the whole point: quitting the TUI,
// closing the lid or dropping an SSH connection leaves the agent working, and
// norn reattaches to what it finds. norn's own pane is a client of that
// session, never its parent.
//
// This package knows tmux and nothing else. It takes an argv and a directory,
// and it reports whether a session is alive and how its program ended. Which
// agent that argv names, and what happens to the branch afterwards, belong to
// the caller.
package strand

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// prefix namespaces norn's sessions so a session list stays readable and List
// can find them again after a restart.
const prefix = "norn"

// socket is norn's own tmux server. A private server rather than the user's:
// norn sets `remain-on-exit` globally, which would change how their own
// sessions behave, and their `tmux ls` should not fill with strands.
const socket = "norn"

// ErrNoTmux is tmux missing from PATH. Named so the caller can say what to
// install rather than reporting an exec error nobody can act on.
var ErrNoTmux = errors.New("tmux is not installed, and a strand runs inside one")

// ErrNoSession is a session that does not exist, which after a spawn means the
// agent already exited and tmux reaped it.
var ErrNoSession = errors.New("no tmux session")

// Name is the session for one strand of one task. Derived rather than stored:
// a restart has to find these again knowing only the task and the role.
func Name(taskID, role string) string {
	return prefix + "-" + taskID + "-" + role
}

// Available reports whether tmux can be used at all.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// Spawn starts argv in a detached session of the given size, in dir. The
// session keeps its pane after the program exits (`remain-on-exit`), which is
// what lets Status report an exit code instead of the session simply vanishing.
func Spawn(taskID, role, dir string, argv []string, cols, rows int) error {
	if !Available() {
		return ErrNoTmux
	}
	if len(argv) == 0 {
		return errors.New("strand: no command to run")
	}
	name := Name(taskID, role)
	// Three steps, and the order is the point. The session is created running
	// the default shell, which holds the server open; `remain-on-exit` is set
	// while nothing can exit; only then does the agent start. Starting the
	// agent first races it: a command that exits immediately takes its session,
	// its last screen and its exit code with it before the option lands.
	out, err := run("new-session", "-d", "-s", name, "-c", dir,
		"-x", strconv.Itoa(max(cols, 20)), "-y", strconv.Itoa(max(rows, 5)))
	if err != nil {
		return fmt.Errorf("strand: spawn %s: %w: %s", name, err, out)
	}
	if out, err := run("set-option", "-g", "remain-on-exit", "on"); err != nil {
		_ = Kill(taskID, role)
		return fmt.Errorf("strand: remain-on-exit: %w: %s", err, out)
	}
	// monitor-bell makes tmux remember a bell rung while nobody was attached,
	// which is the only attention signal that works for an agent norn knows
	// nothing about.
	if out, err := run("set-option", "-g", "monitor-bell", "on"); err != nil {
		_ = Kill(taskID, role)
		return fmt.Errorf("strand: monitor-bell: %w: %s", err, out)
	}
	// norn draws its own frame, so tmux's status bar is a stolen row that also
	// puts the agent's cursor one line off from where norn renders it. And
	// `window-size latest` makes the session follow the pane it is being viewed
	// in: without it the session keeps the size it was spawned at, and the
	// agent lays out for a terminal nobody is looking at.
	for _, opt := range [][2]string{{"status", "off"}, {"window-size", "latest"}} {
		if out, err := run("set-option", "-g", opt[0], opt[1]); err != nil {
			_ = Kill(taskID, role)
			return fmt.Errorf("strand: %s: %w: %s", opt[0], err, out)
		}
	}
	respawn := append([]string{"respawn-pane", "-k", "-t", name, "-c", dir, "--"}, argv...)
	if out, err := run(respawn...); err != nil {
		_ = Kill(taskID, role)
		return fmt.Errorf("strand: start agent in %s: %w: %s", name, err, out)
	}
	return nil
}

// AttachCmd is the command that joins a strand's session. It is meant to run
// inside a pseudo-terminal: tmux draws the agent's screen into it, and the
// caller renders that.
//
// `-u` forces UTF-8 for a client whose environment does not say so, which is
// how box drawing in an agent's UI turns into question marks. It is a global
// tmux flag, so it goes before the subcommand: after it, tmux 3.7 rejects the
// whole command with `unknown flag -u`.
func AttachCmd(taskID, role string) *exec.Cmd {
	return exec.Command("tmux", "-L", socket, "-u", "attach-session", "-t", Name(taskID, role))
}

// Alive reports whether the strand's session still exists. A session exists
// while the agent runs and, thanks to remain-on-exit, after it has finished.
func Alive(taskID, role string) bool {
	_, err := run("has-session", "-t", Name(taskID, role))
	return err == nil
}

// Status is how a strand's program is doing.
type Status struct {
	Running  bool // the agent is still running
	Exited   bool // it has exited and tmux is holding the dead pane
	ExitCode int  // meaningful only when Exited
}

// Read reports the strand's status from tmux itself, which is what makes this
// survive a norn restart: the answer comes from the session, not from a
// process handle norn happens to hold.
func Read(taskID, role string) (Status, error) {
	if !Available() {
		return Status{}, ErrNoTmux
	}
	name := Name(taskID, role)
	// has-session first, because display-message falls back to another session
	// when its target is missing: without this a strand reports a sibling's
	// status, which is worse than reporting nothing.
	if !Alive(taskID, role) {
		return Status{}, fmt.Errorf("%w: %s", ErrNoSession, name)
	}
	out, err := run("display-message", "-p", "-t", name, "#{pane_dead},#{pane_dead_status}")
	if err != nil {
		return Status{}, fmt.Errorf("%w: %s", ErrNoSession, name)
	}
	dead, code, _ := strings.Cut(strings.TrimSpace(out), ",")
	if dead != "1" {
		return Status{Running: true}, nil
	}
	// A pane killed by a signal reports no status; treat that as a failure
	// rather than as a clean exit, since a strand that was killed did not
	// finish its work.
	n, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil {
		return Status{Exited: true, ExitCode: -1}, nil
	}
	return Status{Exited: true, ExitCode: n}, nil
}

// Kill ends a strand's session, whether its agent is running or its pane is
// dead. Missing is success: the caller wanted it gone.
func Kill(taskID, role string) error {
	if !Alive(taskID, role) {
		return nil
	}
	out, err := run("kill-session", "-t", Name(taskID, role))
	if err != nil {
		return fmt.Errorf("strand: kill %s: %w: %s", Name(taskID, role), err, out)
	}
	return nil
}

// List returns norn's own session names, so a restart can find the strands it
// left running and a clean-up can spot the ones nothing points at any more.
func List() ([]string, error) {
	if !Available() {
		return nil, ErrNoTmux
	}
	out, err := run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		// No server running means no sessions, which is not a failure.
		return nil, nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, prefix+"-") {
			names = append(names, line)
		}
	}
	return names, nil
}

func run(args ...string) (string, error) {
	out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
	return string(out), err
}

// Send delivers a line to a strand as if it had been typed into its pane,
// followed by Enter.
//
// This is the whole messaging channel: one way, no inbox, no reply. It works
// for any agent because it is keystrokes into a terminal, which is also why it
// interrupts: a note arrives in the middle of whatever that strand is doing,
// so it is for things the other strand must know, not for chat.
func Send(taskID, role, text string) error {
	if !Available() {
		return ErrNoTmux
	}
	if !Alive(taskID, role) {
		return fmt.Errorf("%w: %s", ErrNoSession, Name(taskID, role))
	}
	name := Name(taskID, role)
	// -l sends the text literally, so a note containing a semicolon or a brace
	// is not read as tmux syntax.
	if out, err := run("send-keys", "-t", name, "-l", text); err != nil {
		return fmt.Errorf("strand: send to %s: %w: %s", name, err, out)
	}
	if out, err := run("send-keys", "-t", name, "Enter"); err != nil {
		return fmt.Errorf("strand: send to %s: %w: %s", name, err, out)
	}
	return nil
}

// Rang reports whether the strand rang the bell since anyone last looked at it.
// tmux holds the flag while the session is detached, so a question asked at
// 02:00 is still flagged in the morning.
func Rang(taskID, role string) bool {
	if !Alive(taskID, role) {
		return false
	}
	out, err := run("display-message", "-p", "-t", Name(taskID, role), "#{window_bell_flag}")
	return err == nil && strings.TrimSpace(out) == "1"
}
