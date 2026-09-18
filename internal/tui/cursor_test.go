package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The cursor must not cost the line its styling. An agent draws its input
// placeholder in faint text, and a cursor that rebuilt the line from stripped
// runes made that hint look like something the user had typed.
func TestDrawCursorKeepsStyling(t *testing.T) {
	// Tests run without a terminal, where lipgloss renders styles as plain
	// text; the cursor is a style, so it needs a profile to be visible at all.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	out := drawCursor("\x1b[2mtry \"fix the bug\"\x1b[m", 0, 0)
	if !strings.Contains(out, "\x1b[2m") {
		t.Fatalf("the faint attribute was lost: %q", out)
	}
	if !strings.Contains(out, "\x1b[7m") {
		t.Fatalf("no cursor was drawn: %q", out)
	}
	// The cursor must not reset the line's own styling: a full reset here made
	// the rest of a faint placeholder render as ordinary typed text.
	if strings.Contains(out, "\x1b[0m") {
		t.Fatalf("the cursor cell reset every attribute on the line: %q", out)
	}
	if !strings.Contains(out, "\x1b[27m") {
		t.Fatalf("the cursor did not end reverse video specifically: %q", out)
	}
	if out = drawCursor("ab", 5, 0); !strings.Contains(out, "\x1b[7m") || !strings.HasPrefix(out, "ab") {
		t.Fatalf("cursor past the end of the text = %q", out)
	}
	if got := drawCursor("ab", 0, 9); got != "ab" {
		t.Fatalf("out-of-range row rewrote the body: %q", got)
	}
}
