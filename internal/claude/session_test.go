package claude

import (
	"testing"
	"time"
)

// parseTail maps the last message-bearing record to a base state, skipping
// trailing non-message records and a leading partial line.
func TestParseTail(t *testing.T) {
	ts := "2026-07-29T07:23:11.883Z"
	line := func(typ, stop string) string {
		s := `{"type":"` + typ + `","timestamp":"` + ts + `","message":{`
		if stop != "" {
			s += `"stop_reason":"` + stop + `"`
		}
		return s + `}}`
	}
	// non-message trailer Claude appends after a turn (no timestamp, no role).
	trailer := `{"type":"ai-title"}` + "\n" + `{"type":"agent-name"}`

	cases := []struct {
		name string
		data string
		want AgentState
	}{
		{"assistant end_turn → waiting", "partial\n" + line("assistant", "end_turn"), StateWaiting},
		{"assistant tool_use → working", "partial\n" + line("assistant", "tool_use"), StateWorking},
		{"user tool_result → working", "partial\n" + line("user", ""), StateWorking},
		{"skips ai-title/agent-name trailer", "partial\n" + line("assistant", "end_turn") + "\n" + trailer, StateWaiting},
		{"skips unparsable last line", "partial\n" + line("assistant", "tool_use") + "\n{garbage", StateWorking},
		{"empty → unknown", "", StateUnknown},
		{"only non-message records → unknown", "partial\n" + trailer, StateUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := parseTail([]byte(c.data))
			if got != c.want {
				t.Errorf("parseTail = %q, want %q", got, c.want)
			}
		})
	}
}

// resolve decays stale working/waiting to idle, but keeps fresh states.
func TestResolveIdleOverlay(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-1 * time.Minute)
	stale := now.Add(-IdleAfter - time.Minute)

	cases := []struct {
		name  string
		state AgentState
		ts    time.Time
		want  AgentState
	}{
		{"fresh working stays working", StateWorking, fresh, StateWorking},
		{"fresh waiting stays waiting", StateWaiting, fresh, StateWaiting},
		{"stale working → idle", StateWorking, stale, StateIdle},
		{"stale waiting → idle", StateWaiting, stale, StateIdle},
		{"unknown stays unknown", StateUnknown, stale, StateUnknown},
		{"zero timestamp never idles", StateWorking, time.Time{}, StateWorking},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolve(c.state, c.ts, now); got != c.want {
				t.Errorf("resolve = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSlugFor(t *testing.T) {
	if got := slugFor("/Users/sandbye/Documents/GitHub/work"); got != "-Users-sandbye-Documents-GitHub-work" {
		t.Errorf("slugFor = %q", got)
	}
	if got := slugFor("/Users/x/worktrees/feature/foo.bar"); got != "-Users-x-worktrees-feature-foo-bar" {
		t.Errorf("slugFor with dot = %q", got)
	}
}
