package claude

import (
	"strings"
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
			got, _, _ := parseTail([]byte(c.data))
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

// The dashboard's reason for reading the transcript at all is to save a trip
// into the session, so it needs what the agent actually asked, not just that it
// stopped.
func TestParseTailReturnsTheLastAssistantText(t *testing.T) {
	const rec = `{"type":"assistant","timestamp":"2026-09-16T09:00:00Z","message":{"stop_reason":"end_turn","content":[` +
		`{"type":"thinking","text":"internal reasoning nobody asked for"},` +
		`{"type":"text","text":"Should I drop the column or keep it nullable?"}]}}`

	state, _, text := parseTail([]byte("partial\n" + rec))
	if state != StateWaiting {
		t.Errorf("state = %q, want waiting", state)
	}
	if text != "Should I drop the column or keep it nullable?" {
		t.Errorf("text = %q", text)
	}
	if strings.Contains(text, "internal reasoning") {
		t.Errorf("thinking block leaked into the question: %q", text)
	}
}

// Mid-turn text is a progress note, not something to answer. Offering it as a
// question would invite a reply to a thread that is still working.
func TestProbeOnlyReportsAQuestionWhenWaiting(t *testing.T) {
	cases := []struct {
		name, stop string
		want       bool
	}{
		{"end_turn carries the question", "end_turn", true},
		{"tool_use does not", "tool_use", false},
	}
	for _, c := range cases {
		rec := `{"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339) +
			`","message":{"stop_reason":"` + c.stop + `","content":[{"type":"text","text":"mid sentence"}]}}`
		state, ts, text := parseTail([]byte("partial\n" + rec))
		st := Status{State: resolve(state, ts, time.Now()), Last: ts}
		if st.State == StateWaiting {
			st.Question = text
		}
		if got := st.Question != ""; got != c.want {
			t.Errorf("%s: question present = %v, want %v", c.name, got, c.want)
		}
	}
}

// A long message is capped from the end, since that is where a question lands.
func TestAssistantTextKeepsTheTail(t *testing.T) {
	var r tailRecord
	r.Message.Content = []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: strings.Repeat("filler ", 1000) + "FINAL QUESTION?"}}

	got := assistantText(r)
	if len([]rune(got)) > questionMax+1 {
		t.Errorf("text is %d runes, over the cap", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "FINAL QUESTION?") {
		t.Errorf("tail was dropped, ends with: %q", got[len(got)-min(40, len(got)):])
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("truncation is not marked: %q", got[:20])
	}
}
