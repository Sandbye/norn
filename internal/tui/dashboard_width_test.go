package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/state"
)

// The dashboard table renders into the fixed-width panel frame() draws, not the
// full terminal. Sizing columns to the terminal width made every row wrap. Guard
// that no table line exceeds the panel's inner text width at any terminal size.
func TestDashboardTableFitsPanel(t *testing.T) {
	rows := []dashRow{
		{Session: state.Session{
			Branch:    "feature/86c6j6z55_investigate/a-really-long-branch-name-here",
			Title:     "Investigate a really long task title that has to truncate cleanly",
			Kind:      "task",
			ClickUpID: "86c6j6z55",
			PRNumber:  3686,
			Status:    state.StatusActive,
		}, WorktreeAlive: true},
		{Session: state.Session{
			Branch: "review/pr-3991",
			Kind:   "review",
			Status: state.StatusActive,
		}, WorktreeAlive: true},
	}

	for _, w := range []int{200, 140, 120, 100, 80} {
		d := Dashboard{width: w, height: 40, rows: rows}
		out := d.View()

		innerText := frameWidth - 6
		if inner := w - 8; inner < frameWidth {
			innerText = inner - 6
		}

		lines := strings.Split(out, "\n")
		start := 0
		for i, l := range lines {
			if strings.Contains(l, "BRANCH") {
				start = i
				break
			}
		}
		for _, line := range lines[start:] {
			if gw := lipgloss.Width(line); gw > innerText {
				t.Errorf("width=%d: table line %d wide, exceeds panel inner %d: %q", w, gw, innerText, line)
			}
		}
	}
}
