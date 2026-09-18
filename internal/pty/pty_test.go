package pty

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// waitFor polls until cond holds, so a test never depends on how fast a shell
// starts. A terminal is asynchronous by nature: the program writes when it
// writes, and the emulator sees it a moment later.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The screen is what the program drew, not the bytes it wrote: this is the
// surface a pane renders, so it is the one worth testing.
func TestScreenRendersOutput(t *testing.T) {
	term, err := Start(exec.Command("sh", "-c", "printf 'hello strand'; sleep 5"), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	waitFor(t, "the program's text on screen", func() bool {
		return strings.Contains(term.Screen(), "hello strand")
	})
}

// Input goes through untouched, which is what lets an agent's own prompts,
// slash commands and ctrl-c work without norn knowing about any of them.
func TestWriteReachesTheProgram(t *testing.T) {
	term, err := Start(exec.Command("sh", "-c", "read line; printf 'got:%s' \"$line\""), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	if err := term.Write([]byte("weave\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the program to echo what it read", func() bool {
		return strings.Contains(term.Screen(), "got:weave")
	})
}

// The bell is the attention signal the rail reads. Counted rather than
// flagged, because two questions asked while you were elsewhere are two.
func TestBellIsCounted(t *testing.T) {
	term, err := Start(exec.Command("sh", "-c", "printf 'a\\ab\\a'; sleep 5"), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	waitFor(t, "two bells", func() bool { return term.ReadBell() == 2 })
	if n := term.ReadBell(); n != 0 {
		t.Fatalf("ReadBell did not reset: %d", n)
	}
}

// Exit is the strand's terminal event: it is what turns a running strand into
// one that is ready to land, so the code has to be exact.
func TestExitCode(t *testing.T) {
	term, err := Start(exec.Command("sh", "-c", "exit 3"), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	code, err := term.Exit()
	if err != nil {
		t.Fatalf("Exit reported an error for a clean non-zero exit: %v", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	select {
	case <-term.Done():
	default:
		t.Fatal("Done was still open after Exit returned")
	}
}

// A resized pane has to tell the program, or the agent keeps wrapping to the
// width it started with.
func TestResizeReachesTheProgram(t *testing.T) {
	// stty rather than tput: tput needs a terminfo entry for $TERM, and a CI
	// runner often has no TERM at all.
	term, err := Start(exec.Command("sh", "-c", "trap 'printf \"cols:$(stty size | cut -d\" \" -f2)\"' WINCH; sleep 5"), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	// The trap has to be installed before the signal, and there is no way to
	// observe that from here other than giving the shell a moment.
	time.Sleep(200 * time.Millisecond)
	if err := term.Resize(72, 20); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the program to see its new width", func() bool {
		return strings.Contains(term.Screen(), "cols:72")
	})
}

// Writing to a closed terminal is an error rather than a panic: a pane can be
// closed while a keystroke is still in flight.
func TestWriteAfterClose(t *testing.T) {
	term, err := Start(exec.Command("sh", "-c", "sleep 5"), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if err := term.Write([]byte("x")); err == nil {
		t.Fatal("write to a closed terminal reported success")
	}
	if err := term.Close(); err != nil {
		t.Fatalf("second Close returned an error: %v", err)
	}
}
