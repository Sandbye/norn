package strand

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sandbye/norn/internal/pty"
)

// Attaching has to render the session and carry input, or a pane shows nothing
// and eats keys. Tested here rather than through the TUI, because a frozen
// pane looks identical whether tmux, the pseudo-terminal or Bubble Tea is the
// one at fault.
func TestAttachRendersAndAcceptsInput(t *testing.T) {
	if !Available() {
		t.Skip("tmux not installed")
	}
	id := "test" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := Spawn(id, "logic", t.TempDir(), []string{"sh"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Kill(id, "logic") })

	term, err := pty.Start(AttachCmd(id, "logic"), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	// The prompt is not a signal worth waiting for: a shell's PS1 differs by
	// distro and can be empty. Keep typing until the output shows up instead.
	deadline := time.Now().Add(15 * time.Second)
	next := time.Now()
	for time.Now().Before(deadline) {
		if strings.Contains(term.Screen(), "marker-ok") {
			return
		}
		if time.Now().After(next) {
			if err := term.Write([]byte("printf marker-ok\r")); err != nil {
				t.Fatal(err)
			}
			next = time.Now().Add(time.Second)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("attach never rendered the typed output; last screen:\n%s", term.Screen())
}
