package claude

// Live agent state per worktree, derived by reading the LOCAL Claude Code
// session transcript that Claude writes for that worktree. This is log
// inspection of the user's own files — no API calls, no network, no use of
// Claude's generated content. It powers the dashboard's mission-control STATE
// column.
//
// Source: ~/.claude/projects/<slug>/<session-uuid>.jsonl, where <slug> is the
// worktree's absolute path with `/` and `.` replaced by `-` (Claude Code's
// convention). One file per session; the newest by mtime is the live one.
// Files can be large (tens of MB), so we only ever tail the end.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AgentState is what a worktree's Claude session is doing right now.
type AgentState string

const (
	StateWorking AgentState = "working" // mid tool loop / turn in progress
	StateWaiting AgentState = "waiting" // turn ended, expects the user
	StateIdle    AgentState = "idle"    // no transcript activity for a while
	StateStuck   AgentState = "stuck"   // reserved; error detection is future work
	StateUnknown AgentState = ""        // no transcript / non-claude / unparsable
)

const (
	tailBytesN = 64 * 1024        // how much of the transcript tail to read
	IdleAfter  = 30 * time.Minute // no new record for this long → idle
)

// tailRecord is the minimal shape we need from a transcript line. Everything
// else in the (undocumented) envelope is ignored on purpose, so a format change
// degrades to StateUnknown rather than breaking.
type tailRecord struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		StopReason string `json:"stop_reason"`
	} `json:"message"`
}

// Probe returns the live agent state for a worktree and the timestamp of its
// last transcript activity. Returns StateUnknown (and zero time) when there's no
// readable transcript. Never errors — a missing/odd signal is just "unknown".
func Probe(worktreePath string) (AgentState, time.Time) {
	f := newestTranscript(filepath.Join(projectsDir(), slugFor(worktreePath)))
	if f == "" {
		return StateUnknown, time.Time{}
	}
	data, err := tailBytes(f, tailBytesN)
	if err != nil {
		return StateUnknown, time.Time{}
	}
	state, ts := parseTail(data)
	return resolve(state, ts, time.Now()), ts
}

// HasSession reports whether a worktree has a transcript Claude can continue,
// i.e. whether `claude -c` in that directory would resume something.
func HasSession(worktreePath string) bool {
	return newestTranscript(filepath.Join(projectsDir(), slugFor(worktreePath))) != ""
}

// resolve applies the idle overlay: stale activity (working or waiting) decays
// to idle. A fresh end_turn stays "waiting" so it reads as "needs you now"; an
// old one becomes a dormant "idle". Split out so it's unit-testable.
func resolve(state AgentState, ts, now time.Time) AgentState {
	if state == StateUnknown {
		return StateUnknown
	}
	if !ts.IsZero() && now.Sub(ts) > IdleAfter {
		return StateIdle
	}
	return state
}

// parseTail walks transcript-tail bytes from the end and maps the last
// message-bearing record to a base state (before the idle overlay). Pure and
// deterministic, so it's unit-testable without touching ~/.claude.
func parseTail(data []byte) (AgentState, time.Time) {
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 1 {
		lines = lines[1:] // drop the first, likely-partial line
	}
	for i := len(lines) - 1; i >= 0; i-- {
		ln := bytes.TrimSpace(lines[i])
		if len(ln) == 0 {
			continue
		}
		var r tailRecord
		if json.Unmarshal(ln, &r) != nil {
			continue
		}
		switch r.Type {
		case "assistant":
			return assistantState(r.Message.StopReason), r.Timestamp
		case "user":
			// A user record is either a tool_result being fed back (mid loop) or
			// a fresh human turn — either way the agent is about to act.
			return StateWorking, r.Timestamp
		default:
			continue // attachment / ai-title / agent-name / system, etc.
		}
	}
	return StateUnknown, time.Time{}
}

func assistantState(stop string) AgentState {
	switch stop {
	case "end_turn", "stop_sequence":
		return StateWaiting
	default:
		// tool_use / max_tokens / "" (still streaming) → working.
		return StateWorking
	}
}

// slugFor maps a worktree path to Claude Code's project-dir slug: the absolute
// path with `/` and `.` replaced by `-`.
func slugFor(worktreePath string) string {
	abs := worktreePath
	if a, err := filepath.Abs(worktreePath); err == nil {
		abs = a
	}
	return strings.NewReplacer("/", "-", ".", "-").Replace(abs)
}

// projectsDir is Claude Code's transcript root. Honors CLAUDE_CONFIG_DIR.
func projectsDir() string {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "projects")
}

// newestTranscript returns the newest-mtime *.jsonl in dir, or "".
func newestTranscript(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, cand{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	return cands[0].path
}

// tailBytes reads the last n bytes of a file (or the whole file if smaller).
func tailBytes(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(info.Size()-n, 0)
	if _, err := f.Seek(off, 0); err != nil {
		return nil, err
	}
	buf := make([]byte, info.Size()-off)
	if _, err := f.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}
