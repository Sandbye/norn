// Package pty runs a program in a pseudo-terminal and keeps a rendered picture
// of its screen.
//
// It exists so norn can show a live agent without knowing anything about that
// agent: the program believes it owns a terminal, writes whatever escape
// sequences it likes, and this package turns those into a string and a few
// signals. No agent knowledge, no protocol, no git. That is what makes a pane
// work for an agent nobody has written support for.
package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Term is a program running in a pseudo-terminal, with its screen emulated.
//
// Every method is safe to call while the program is running: output arrives on
// its own goroutine, and the emulator is guarded because the render loop reads
// the screen while that goroutine writes to it.
type Term struct {
	mu   sync.Mutex
	vt   *vt.Emulator
	file *os.File // the pty master
	cmd  *exec.Cmd

	// bells is atomic rather than under mu: the emulator calls the bell
	// callback from inside its own Write, which pump calls while holding mu,
	// and a second Lock there would deadlock the reader on the first `\a`.
	bells atomic.Int64
	// closed is atomic for the same reason as bells: the reply goroutine reads
	// it while pump holds mu inside vt.Write, and taking mu there deadlocks the
	// pair.
	closed atomic.Bool

	done chan struct{}
	exit int
	err  error
}

// Start runs cmd in a pseudo-terminal of the given size and begins reading its
// output. cmd must not have Stdin, Stdout or Stderr set: the pty is all three.
func Start(cmd *exec.Cmd, cols, rows int) (*Term, error) {
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("pty: size must be positive, got %dx%d", cols, rows)
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", cmd.Path, err)
	}

	t := &Term{
		vt:   vt.NewEmulator(cols, rows),
		file: f,
		cmd:  cmd,
		done: make(chan struct{}),
	}
	// The bell is the one signal norn reads out of the stream: it is how a CLI
	// asks for attention, and every terminal program has it, which is why it
	// beats knowing any particular agent's idea of "waiting".
	t.vt.SetCallbacks(vt.Callbacks{Bell: func() { t.bells.Add(1) }})

	go t.pump()
	go t.reply()
	return t, nil
}

// pump feeds the program's output into the emulator until the pty closes, then
// reaps the process. A closed pty is the normal end of a run, not a failure:
// the read fails because the child exited.
func (t *Term) pump() {
	defer close(t.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := t.file.Read(buf)
		if n > 0 {
			t.mu.Lock()
			_, _ = t.vt.Write(buf[:n])
			t.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	err := t.cmd.Wait()
	t.exit = t.cmd.ProcessState.ExitCode()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.err = err
	}
}

// reply carries the emulator's answers back to the program. A terminal is not
// write-only: a client asks it for its device attributes, its cursor position,
// its colours, and waits for the answer. Without this the emulator's reply
// buffer fills, its Write blocks while pump holds the lock, and the whole pane
// freezes with nothing drawn, which is exactly how tmux attach appeared to
// hang.
func (t *Term) reply() {
	buf := make([]byte, 4096)
	for {
		n, err := t.vt.Read(buf)
		if n > 0 {
			if t.closed.Load() {
				return
			}
			if _, werr := t.file.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Screen renders the program's current screen.
func (t *Term) Screen() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.vt.Render()
}

// Cursor is where the program's cursor sits, in cells from the top left of the
// screen. The renderer needs it to put the real terminal's cursor in the same
// place: a rendered screen with no cursor is a text editor you cannot see
// yourself typing in.
func (t *Term) Cursor() (col, row int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pos := t.vt.CursorPosition()
	return pos.X, pos.Y
}

// Write sends input to the program, exactly as a terminal would. No
// interpretation on the way: whatever norn encodes is what the agent receives,
// which is why slash commands, ctrl-c and permission prompts need no support.
func (t *Term) Write(p []byte) error {
	if t.closed.Load() {
		return errors.New("pty: write to a closed terminal")
	}
	_, err := t.file.Write(p)
	return err
}

// Resize tells the program its terminal changed size. Without it a pane that
// changes width leaves the agent wrapping to the old one.
func (t *Term) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("pty: size must be positive, got %dx%d", cols, rows)
	}
	t.mu.Lock()
	t.vt.Resize(cols, rows)
	t.mu.Unlock()
	return pty.Setsize(t.file, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// ReadBell returns how many times the program rang the bell since the last
// call, and resets the count. A count rather than a flag: two questions asked
// while you were looking elsewhere are two, and the caller decides whether that
// matters.
func (t *Term) ReadBell() int {
	return int(t.bells.Swap(0))
}

// Done is closed when the program has exited and its output is fully drained.
func (t *Term) Done() <-chan struct{} { return t.done }

// Exit reports how the program ended. It blocks until it has.
func (t *Term) Exit() (code int, err error) {
	<-t.done
	return t.exit, t.err
}

// Close ends the program's terminal. The program sees its input close, which
// is how a shell or an agent is asked to leave.
func (t *Term) Close() error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	return t.file.Close()
}
