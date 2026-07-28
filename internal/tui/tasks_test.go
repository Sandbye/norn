package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/task"
)

// The Tasks tab renders a two-pane layout into the fixed-width panel frame()
// draws. Guard that neither pane overflows (which would wrap and garble it) at
// any terminal size, with long titles/bodies.
func TestTasksTabFitsPanel(t *testing.T) {
	tasks := []task.Task{
		{ID: "14", Title: strings.Repeat("very-long-title-word ", 8), Kind: "feature",
			Labels: []string{"documentation", "good-first-issue"}, Description: strings.Repeat("long body sentence that must wrap and clip. ", 30)},
		{ID: "13", Title: "Short one", Kind: "fix"},
	}
	for _, w := range []int{200, 140, 120, 100, 80} {
		m := tasksModel{provider: task.GitHub{}, scope: "owner/repo", width: w, height: 40, tasks: tasks}
		out := m.View()

		innerText := frameWidth - 6
		if inner := w - 8; inner < frameWidth {
			innerText = inner - 6
		}
		for i, line := range strings.Split(out, "\n") {
			if gw := lipgloss.Width(line); gw > innerText {
				t.Errorf("width=%d: line %d is %d wide, exceeds panel inner %d: %q", w, i, gw, innerText, line)
			}
		}
	}
}

func TestTasksTabNoProvider(t *testing.T) {
	m := tasksModel{provider: nil, width: 120, height: 40}
	out := m.View()
	if !strings.Contains(out, "No task provider") {
		t.Errorf("expected no-provider hint, got:\n%s", out)
	}
}
