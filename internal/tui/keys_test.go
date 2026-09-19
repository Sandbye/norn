package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/state"
)

func press(d Dashboard, key string) Dashboard {
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return m.(Dashboard)
}

// `d` means diff everywhere else in norn, and it used to mean delete here,
// which is the one place a wrong guess costs a worktree.
func TestDReadsAndDCleans(t *testing.T) {
	ApplyTheme("nord")
	row := dashRow{
		Session:       state.Session{Branch: "f/t/logic", Path: "/w/logic", Role: "logic", TaskID: "t", Status: state.StatusActive},
		TaskTrunk:     "f/t/trunk",
		WorktreeAlive: true,
	}

	after := press(Dashboard{width: 120, height: 40, rows: []dashRow{row}}, "d")
	if after.result.Action != ResultReview {
		t.Fatalf("d gave action %v, want a review", after.result.Action)
	}
	if after.result.Path != "/w/logic" || after.result.Base != "f/t/trunk" || after.result.Role != "logic" {
		t.Errorf("the review does not carry the strand: %+v", after.result)
	}
	if after.cleanPath != "" {
		t.Error("d still armed the clean view")
	}

	after = press(Dashboard{width: 120, height: 40, rows: []dashRow{row}}, "D")
	if after.cleanPath != "/w/logic" {
		t.Errorf("D did not hand this worktree to clean: %q", after.cleanPath)
	}
	if after.result.Action == ResultReview {
		t.Error("D opened a review")
	}
}

// A worktree whose directory is gone has nothing to read, and saying so beats
// dropping into an empty diff.
func TestDOnADeadWorktreeExplains(t *testing.T) {
	ApplyTheme("nord")
	row := dashRow{Session: state.Session{Branch: "f/t/logic", Path: "/gone", Role: "logic", TaskID: "t", Status: state.StatusActive}}
	after := press(Dashboard{width: 120, height: 40, rows: []dashRow{row}}, "d")
	if after.result.Action == ResultReview {
		t.Error("a missing worktree still opened a review")
	}
	if after.notice == "" {
		t.Error("nothing was said about why d did nothing")
	}
}
