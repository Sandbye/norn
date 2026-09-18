package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// Claude has aliases norn can name; codex is offered the model its own config
// records, because a hardcoded list of another vendor's ids goes stale inside a
// release. An agent norn knows no models for falls through to free text.
func TestAgentModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("model = \"gpt-5.6-luna\"\nmodel_reasoning_effort = \"medium\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := agentModels("claude"); len(got) == 0 || got[0] != "sonnet" {
		t.Fatalf("claude choices = %v, want the aliases", got)
	}
	got := agentModels("codex")
	if len(got) != 1 || got[0] != "gpt-5.6-luna" {
		t.Fatalf("codex choices = %v, want the model from its own config", got)
	}
	if got := agentModels("opencode"); got != nil {
		t.Fatalf("unknown agent got a model list: %v", got)
	}

	// model_reasoning_effort must not be read as the model.
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("model_reasoning_effort = \"low\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := agentModels("codex"); got != nil {
		t.Fatalf("matched a key that only starts with model: %v", got)
	}
}
