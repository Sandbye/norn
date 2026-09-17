// Package runlog renders a headless role's JSONL output as something a person
// can read.
//
// A role driven by `norn run` has no session to open: it is a `claude -p` or
// `codex exec` process writing events to a file. Without this the only account
// of what an unattended agent did is 150KB of raw JSON, which is the same as no
// account at all.
//
// Read-only on purpose. This renders a file norn already writes; it is not a
// chat client, and it never speaks back to the agent.
package runlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Kind is how a line is shown, which is all the styling the caller needs.
type Kind string

const (
	KindText   Kind = "text"   // what the agent said
	KindTool   Kind = "tool"   // a tool call it made
	KindResult Kind = "result" // the run's own outcome
	KindNote   Kind = "note"   // norn's or the agent's housekeeping
)

// Event is one readable line of a run.
type Event struct {
	Kind Kind
	Text string
}

// Tail returns the last max events of a role's log, oldest first. A missing
// file is not an error: a role that has not started yet has no log, and the
// caller shows that as an empty view rather than a failure.
func Tail(path string, max int) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []Event
	sc := bufio.NewScanner(f)
	// Tool results and briefs run long, and a line over the default 64KB would
	// otherwise end the scan silently, truncating the log at its most
	// interesting point.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if ev, ok := Parse(sc.Bytes()); ok {
			events = append(events, ev)
			if max > 0 && len(events) > max {
				events = events[1:]
			}
		}
	}
	if err := sc.Err(); err != nil {
		return events, err
	}
	return events, nil
}

// Parse turns one JSONL line into an event, reporting false for the lines not
// worth showing: token counters, rate-limit envelopes, hook chatter. Anything
// unrecognized is dropped rather than guessed at, so a format change costs
// detail instead of producing nonsense.
func Parse(line []byte) (Event, bool) {
	line = trim(line)
	if len(line) == 0 {
		return Event{}, false
	}
	// A run's own progress lines (`norn run` writes these) are already prose.
	if line[0] != '{' {
		return Event{KindNote, string(line)}, true
	}

	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return Event{}, false
	}
	switch str(raw["type"]) {
	case "assistant": // claude
		return claudeAssistant(raw)
	case "result": // claude, end of run
		return Event{KindResult, claudeResult(raw)}, true
	case "system":
		if str(raw["subtype"]) == "init" {
			return Event{KindNote, "session started in " + str(raw["cwd"])}, true
		}
		if str(raw["subtype"]) == "vcs_state_changed" {
			return Event{KindNote, str(raw["kind"]) + " on " + str(raw["branch"])}, true
		}
		return Event{}, false
	case "error", "stream_error":
		return Event{KindResult, "error: " + str(raw["message"])}, true
	}
	return codexEvent(raw)
}

// claudeAssistant flattens an assistant turn into its text and tool calls. A
// turn carries both, and the tool calls are the half that says what the agent
// actually did to the worktree.
func claudeAssistant(raw map[string]any) (Event, bool) {
	msg, _ := raw["message"].(map[string]any)
	content, _ := msg["content"].([]any)
	var parts []string
	for _, c := range content {
		block, _ := c.(map[string]any)
		switch str(block["type"]) {
		case "text":
			if t := oneLine(str(block["text"])); t != "" {
				parts = append(parts, t)
			}
		case "tool_use":
			return Event{KindTool, toolLine(str(block["name"]), block["input"])}, true
		}
	}
	if len(parts) == 0 {
		return Event{}, false
	}
	return Event{KindText, strings.Join(parts, " ")}, true
}

// claudeResult summarizes the envelope that ends a `claude -p` run: what it
// cost and why it stopped, which is what you want when the row went red.
func claudeResult(raw map[string]any) string {
	out := "done"
	if str(raw["subtype"]) != "" && str(raw["subtype"]) != "success" {
		out = str(raw["subtype"])
	}
	if r := str(raw["stop_reason"]); r != "" {
		out += ", " + r
	}
	if cost, ok := raw["total_cost_usd"].(float64); ok {
		out += fmt.Sprintf(", $%.2f", cost)
	}
	return out
}

// codexEvent reads `codex exec --json`, which reports a thread of items rather
// than claude's flat message shape: `{"type":"item.completed","item":{...}}`,
// with the item naming itself. Verified against a real run; `thread.started`
// and `turn.started` carry nothing to show and are dropped.
func codexEvent(raw map[string]any) (Event, bool) {
	switch str(raw["type"]) {
	case "turn.completed":
		return Event{KindResult, "done" + codexUsage(raw["usage"])}, true
	case "turn.failed", "thread.error":
		return Event{KindResult, "error: " + oneLine(str(raw["error"]))}, true
	case "item.completed", "item.started":
		item, ok := raw["item"].(map[string]any)
		if !ok {
			return Event{}, false
		}
		return codexItem(item)
	}
	return Event{}, false
}

// codexItem renders one item of a codex thread. The item types are open-ended,
// so anything carrying text is shown as text rather than dropped: an unknown
// item that says something is still worth reading.
func codexItem(item map[string]any) (Event, bool) {
	switch str(item["type"]) {
	case "command_execution":
		return Event{KindTool, "bash " + oneLine(joinAny(item["command"]))}, true
	case "file_change":
		return Event{KindTool, "edit " + oneLine(joinAny(item["changes"]))}, true
	case "error":
		return Event{KindResult, "error: " + oneLine(str(item["message"]))}, true
	}
	for _, key := range []string{"text", "message"} {
		if t := oneLine(str(item[key])); t != "" {
			return Event{KindText, t}, true
		}
	}
	return Event{}, false
}

// codexUsage adds the token counts a finished turn reports, the closest codex
// has to claude's cost line.
func codexUsage(v any) string {
	u, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	in, _ := u["input_tokens"].(float64)
	out, _ := u["output_tokens"].(float64)
	if in == 0 && out == 0 {
		return ""
	}
	return fmt.Sprintf(", %.0f in / %.0f out tokens", in, out)
}

// toolLine names a tool call by the one argument that says what it touched, so
// a log line reads as an action rather than as a JSON blob.
func toolLine(name string, input any) string {
	args, _ := input.(map[string]any)
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "prompt"} {
		if v := str(args[key]); v != "" {
			return name + " " + oneLine(v)
		}
	}
	return name
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// joinAny renders a codex field that may be a list (a command) or a map (a
// patch's files) as one short string.
func joinAny(v any) string {
	switch t := v.(type) {
	case []any:
		var parts []string
		for _, e := range t {
			parts = append(parts, str(e))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		var keys []string
		for k := range t {
			keys = append(keys, k)
		}
		return strings.Join(keys, " ")
	default:
		return str(v)
	}
}

// oneLine collapses a block to a single line: the viewer is a rail of recent
// activity, and a pasted file would push everything else off it.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	return strings.Join(strings.Fields(s), " ")
}

func trim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
