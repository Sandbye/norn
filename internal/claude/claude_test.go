package claude

import (
	"strings"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	sample := `{"type":"result","subtype":"success","result":"  Did X and Y.\n","session_id":"abc123","total_cost_usd":0.0123,"is_error":false}`
	r, err := parseEnvelope([]byte(sample))
	if err != nil {
		t.Fatalf("parseEnvelope: %v", err)
	}
	if r.Text != "Did X and Y." {
		t.Errorf("Text = %q, want trimmed %q", r.Text, "Did X and Y.")
	}
	if r.SessionID != "abc123" {
		t.Errorf("SessionID = %q", r.SessionID)
	}
	if r.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v", r.CostUSD)
	}
	if r.IsError {
		t.Error("IsError = true, want false")
	}
}

func TestParseEnvelopeBadJSON(t *testing.T) {
	if _, err := parseEnvelope([]byte("not json")); err == nil {
		t.Error("expected error on bad json")
	}
}

// The flags are the whole contract of a headless run and none of them fail
// loudly: a missing --continue answers a new session instead of the one on
// screen, and a missing --permission-mode silently denies every edit.
func TestBuildArgs(t *testing.T) {
	joined := func(opts Options) string {
		return strings.Join(buildArgs("answer text", opts), " ")
	}

	base := joined(Options{})
	if !strings.HasPrefix(base, "-p answer text --output-format json") {
		t.Errorf("base args = %q", base)
	}
	for _, unwanted := range []string{"--continue", "--permission-mode", "--allowedTools", "--append-system-prompt"} {
		if strings.Contains(base, unwanted) {
			t.Errorf("zero Options passed %s: %q", unwanted, base)
		}
	}

	if got := joined(Options{Continue: true}); !strings.Contains(got, "--continue") {
		t.Errorf("Continue not passed: %q", got)
	}
	if got := joined(Options{PermissionMode: "acceptEdits"}); !strings.Contains(got, "--permission-mode acceptEdits") {
		t.Errorf("PermissionMode not passed: %q", got)
	}
	if got := joined(Options{AllowedTools: []string{"Read", "Bash(git diff *)"}}); !strings.Contains(got, "--allowedTools Read,Bash(git diff *)") {
		t.Errorf("AllowedTools not joined: %q", got)
	}
	if got := joined(Options{SystemPrompt: "be terse"}); !strings.Contains(got, "--append-system-prompt be terse") {
		t.Errorf("SystemPrompt not passed: %q", got)
	}
}
