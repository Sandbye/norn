package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Captured from `claude agents --json` on a real machine: background and
// interactive entries carry different fields, and norn depends on telling them
// apart.
const agentsSample = `[
 {"id":"347b1efc","cwd":"/Users/x/worktrees/feature/a","kind":"background","startedAt":1783689700839,
  "sessionId":"347b1efc-2434-48e8-9df8-b9b4e69a20fd","name":"self-supply-knowledge-base","state":"blocked"},
 {"pid":38419,"cwd":"/Users/x/worktrees/feature/b","kind":"interactive","startedAt":1787659469106,
  "sessionId":"d7f20af1-b974-42e9-b4fd-6756e4a7afcb","name":"e2e-fixtures","status":"idle"}
]`

func TestSessionsParsesBothKinds(t *testing.T) {
	var got []Session
	if err := json.Unmarshal([]byte(agentsSample), &got); err != nil {
		t.Fatalf("sample does not parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d sessions, want 2", len(got))
	}

	bg := got[0]
	if !bg.Background() {
		t.Errorf("background session not recognised: %+v", bg)
	}
	if !bg.Blocked() {
		t.Errorf("blocked state not recognised: %+v", bg)
	}
	if bg.ID != "347b1efc" || bg.SessionID == "" {
		t.Errorf("ids missing: %+v", bg)
	}

	in := got[1]
	if in.Background() {
		t.Errorf("interactive session reported as background: %+v", in)
	}
	if in.Blocked() {
		t.Errorf("interactive session has no state field, so it cannot be blocked: %+v", in)
	}
	if in.PID != 38419 {
		t.Errorf("pid missing: %+v", in)
	}
}

// A worktree under /tmp is reached as /private/tmp on macOS, so a string
// compare finds nothing and norn would fall back as if there were no session.
func TestSessionForResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sessions := []Session{{ID: "abc", Cwd: real, Kind: "background", State: "blocked"}}

	got, ok := SessionFor(sessions, link)
	if !ok || got.ID != "abc" {
		t.Errorf("lookup through a symlink failed: %+v ok=%v", got, ok)
	}
	if _, ok := SessionFor(sessions, filepath.Join(real, "elsewhere")); ok {
		t.Error("matched an unrelated path")
	}
}

func TestSessionForEmptyListing(t *testing.T) {
	if _, ok := SessionFor(nil, "/wt/x"); ok {
		t.Error("found a session in an empty listing")
	}
}

// A headless reply ends the run, so the daemon stops holding the session and it
// leaves `claude agents` even though the conversation is still resumable. The
// first reply used to make every later one fail on "no live session".
func TestSessionIDForFallsBackToTheTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)

	wt := t.TempDir()
	dir := filepath.Join(home, "projects", slugFor(wt))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "fc8e5319-fe30-4dad-977b-750e27b74bdc"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := SessionIDFor(wt); got != id {
		t.Errorf("SessionIDFor = %q, want the transcript's id %q", got, id)
	}
	if got := SessionIDFor(t.TempDir()); got != "" {
		t.Errorf("worktree with no transcript returned %q", got)
	}
}
