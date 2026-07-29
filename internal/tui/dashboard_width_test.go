package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

func ccRows() []dashRow {
	return []dashRow{
		{Session: state.Session{
			Branch:    "feature/86c6j6z55_investigate/a-really-long-branch-name-here",
			Title:     "Investigate a really long task title that has to truncate cleanly",
			Kind:      "task",
			ClickUpID: "86c6j6z55",
			PRNumber:  3686,
			Status:    state.StatusActive,
		}, WorktreeAlive: true, AgentState: claude.StateWorking, Next: "run a genuinely long next action that must be truncated to the pane"},
		{Session: state.Session{
			Branch: "review/pr-3991",
			Title:  "Beta task",
			Kind:   "review",
			Status: state.StatusActive,
		}, WorktreeAlive: true, AgentState: claude.StateWaiting},
	}
}

// The command-center view renders into the fixed-width panel frame() draws, not
// the full terminal. Guard that no line exceeds the panel's inner width at any
// terminal size (a regression here makes every row wrap).
func TestDashboardCommandCenterFitsPanel(t *testing.T) {
	ApplyTheme("nord")
	rows := ccRows()
	for _, w := range []int{200, 140, 120, 100, 80} {
		d := Dashboard{width: w, height: 40, rows: rows, cursor: 0}
		innerText := frameWidth - 6
		if inner := w - 8; inner < frameWidth {
			innerText = inner - 6
		}
		for i, line := range strings.Split(d.View(), "\n") {
			if gw := lipgloss.Width(line); gw > innerText {
				t.Errorf("width=%d: line %d is %d wide, exceeds panel inner %d: %q", w, i, gw, innerText, line)
			}
		}
	}
}

// The detail pane reflects the selected thread.
func TestDashboardDetailFollowsCursor(t *testing.T) {
	ApplyTheme("nord")
	rows := ccRows()
	d0 := Dashboard{width: 120, height: 30, rows: rows, cursor: 0}
	if !strings.Contains(d0.View(), "Investigate a really long") {
		t.Error("detail should show the cursor-0 title")
	}
	d1 := Dashboard{width: 120, height: 30, rows: rows, cursor: 1}
	if !strings.Contains(d1.View(), "Beta task") {
		t.Error("detail should show the cursor-1 title")
	}
}

// Zero threads renders the empty state, not a broken split.
func TestDashboardEmptyState(t *testing.T) {
	ApplyTheme("nord")
	d := Dashboard{width: 120, height: 30}
	if !strings.Contains(d.View(), "spin one up") {
		t.Errorf("empty state missing: %q", d.View())
	}
}
