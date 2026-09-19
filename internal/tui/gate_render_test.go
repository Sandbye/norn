package tui

import (
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

func TestRailShowsTheRunningGate(t *testing.T) {
	ApplyTheme("nord")
	rows := []dashRow{{
		Session:       state.Session{Branch: "feature/x/logic", Title: "a task", Kind: "task", Role: "logic", TaskID: "t1", Status: state.StatusActive},
		WorktreeAlive: true, AgentState: claude.StateWorking,
	}}
	d := Dashboard{width: 140, height: 40, rows: rows, cursor: 0, landing: "logic", notice: "landing logic…"}

	if v := d.View(); !strings.Contains(v, "landing logic") {
		t.Fatalf("without a gate running the notice should show:\n%s", v)
	}

	setVerifyStep("logic", "pnpm test", 2, 2)
	t.Cleanup(func() { clearVerifyStep("logic") })
	v := d.View()
	if !strings.Contains(v, "pnpm test") || !strings.Contains(v, "2/2") {
		t.Errorf("the rail does not show the running command:\n%s", v)
	}
	if strings.Contains(v, "landing logic…") {
		t.Error("the stale notice is still on screen next to the live gate line")
	}
}
