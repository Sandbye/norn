package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Lines taken from a real `claude -p --output-format stream-json` run and a
// real `codex exec --json` run. Fixtures rather than invented shapes: the whole
// value of this package is that it matches what the agents actually emit.
const claudeLines = `{"type":"system","subtype":"init","cwd":"/w/logic","session_id":"5372c70d"}
{"type":"system","subtype":"thinking_tokens","estimated_tokens":50}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"cat README.md"}}]}}
{"type":"user","message":{"content":[{"tool_use_id":"toolu_01","type":"tool_result","content":"# norn"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Added the line.\nSecond paragraph."}]}}
{"type":"system","subtype":"vcs_state_changed","kind":"commit","branch":"feature/x/logic"}
{"type":"result","subtype":"success","stop_reason":"end_turn","total_cost_usd":1.1438935}`

const codexLines = `{"type":"thread.started","thread_id":"01a0ae48"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"ok"}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":["go","test","./..."]}}
{"type":"turn.completed","usage":{"input_tokens":13837,"output_tokens":5}}`

func parseAll(t *testing.T, body string) []Event {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "logic.log")
	if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events, err := Tail(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// A claude run reduces to what it said, what it ran, and how it ended. The
// counters and rate-limit envelopes are dropped: they are the bulk of the file
// and none of the story.
func TestClaudeRun(t *testing.T) {
	events := parseAll(t, claudeLines)
	var got []string
	for _, ev := range events {
		got = append(got, string(ev.Kind)+": "+ev.Text)
	}
	want := []string{
		"note: session started in /w/logic",
		"tool: Bash cat README.md",
		"text: Added the line. …",
		"note: commit on feature/x/logic",
		"result: done, end_turn, $1.14",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Codex reports a thread of items instead, and the protocol frames around them
// carry nothing to read.
func TestCodexRun(t *testing.T) {
	events := parseAll(t, codexLines)
	var got []string
	for _, ev := range events {
		got = append(got, string(ev.Kind)+": "+ev.Text)
	}
	want := []string{
		"text: ok",
		"tool: bash go test ./...",
		"result: done, 13837 in / 5 out tokens",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A log that does not exist yet is an empty view, not an error: the viewer
// opens on a role before its first line lands.
func TestMissingLog(t *testing.T) {
	events, err := Tail(filepath.Join(t.TempDir(), "nope.log"), 10)
	if err != nil || events != nil {
		t.Fatalf("Tail on a missing file = %v, %v; want nil, nil", events, err)
	}
}

// Tail keeps the newest events, since the viewer is a rail of recent activity.
func TestTailKeepsTheEnd(t *testing.T) {
	events := parseAll(t, claudeLines)
	dir := t.TempDir()
	path := filepath.Join(dir, "logic.log")
	if err := os.WriteFile(path, []byte(claudeLines+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	last, err := Tail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 2 || last[1].Kind != KindResult {
		t.Fatalf("Tail(2) = %+v, want the last two events", last)
	}
	if last[1] != events[len(events)-1] {
		t.Fatalf("Tail(2) dropped the newest event: %+v", last)
	}
}

// An unparseable line is skipped, and a plain-text line (the supervisor's own
// progress) is shown as it is.
func TestNonJSONLines(t *testing.T) {
	events := parseAll(t, "logic: started (pid 24036)\nnot json at all {")
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	if events[0].Kind != KindNote || !strings.Contains(events[0].Text, "pid 24036") {
		t.Fatalf("supervisor line not shown: %+v", events[0])
	}
}
