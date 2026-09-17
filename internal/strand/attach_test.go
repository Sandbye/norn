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

	deadline := time.Now().Add(10 * time.Second)
	wrote := false
	for time.Now().Before(deadline) {
		screen := term.Screen()
		if !wrote && strings.Contains(screen, "$") {
			// The shell prompt is up, so the client is drawing.
			if err := term.Write([]byte("printf marker-ok\r")); err != nil {
				t.Fatal(err)
			}
			wrote = true
		}
		if strings.Contains(screen, "marker-ok") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("attach never rendered the typed output; last screen:\n%s", term.Screen())
}
