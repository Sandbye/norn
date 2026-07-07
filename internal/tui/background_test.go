package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSealBackgroundStampsResets(t *testing.T) {
	// A foreground-only span followed by a reset — the reset is where the gap
	// comes from and must be sealed with the panel background.
	in := "\x1b[38;2;255;0;0mhello\x1b[0m"
	out := sealBackground(in, lipgloss.Color("#2e3440")) // 46,52,64

	if !strings.Contains(out, "\x1b[0;48;2;46;52;64m") {
		t.Errorf("reset didn't get bg sealed:\n%q", out)
	}
	if !strings.Contains(out, "38;2;255;0;0") {
		t.Errorf("foreground not preserved:\n%q", out)
	}
}

func TestSealBackgroundPreservesIntentionalBg(t *testing.T) {
	// The selected-row highlight: an explicit background must survive untouched,
	// otherwise the row goes black-on-black.
	in := "\x1b[38;2;46;52;64;48;2;136;192;208mSELECTED\x1b[0m"
	out := sealBackground(in, lipgloss.Color("#2e3440"))

	if !strings.Contains(out, "48;2;136;192;208") {
		t.Errorf("intentional highlight bg was clobbered:\n%q", out)
	}
	// The trailing reset still gets sealed back to base.
	if !strings.Contains(out, "\x1b[0;48;2;46;52;64m") {
		t.Errorf("reset after highlight not sealed:\n%q", out)
	}
}

func TestSealBackgroundLeavesBasicBg(t *testing.T) {
	in := "\x1b[41mx\x1b[0m" // basic red background — intentional, keep it
	out := sealBackground(in, lipgloss.Color("#2e3440"))
	if !strings.Contains(out, "\x1b[41m") {
		t.Errorf("basic bg should be preserved:\n%q", out)
	}
}

func TestHexRGB(t *testing.T) {
	r, g, b := hexRGB(lipgloss.Color("#2e3440"))
	if r != 46 || g != 52 || b != 64 {
		t.Errorf("hexRGB = %d,%d,%d want 46,52,64", r, g, b)
	}
	if r, g, b := hexRGB(lipgloss.Color("bad")); r != 0 || g != 0 || b != 0 {
		t.Errorf("hexRGB(bad) = %d,%d,%d want 0,0,0", r, g, b)
	}
}
