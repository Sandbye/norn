package config

import "testing"

func TestAgentCommand(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty defaults to claude", Config{}, "claude"},
		{"default config is claude", DefaultConfig(), "claude"},
		{"explicit opencode", Config{Agent: AgentConfig{Command: "opencode"}}, "opencode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.AgentCommand(); got != tt.want {
				t.Errorf("AgentCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHeadlessClaude(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"empty is claude", Config{}, true},
		{"explicit claude", Config{Agent: AgentConfig{Command: "claude"}}, true},
		{"opencode is not claude", Config{Agent: AgentConfig{Command: "opencode"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.HeadlessClaude(); got != tt.want {
				t.Errorf("HeadlessClaude() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAgentMergePreservesDefault verifies that a YAML block setting only
// agent.args keeps the default command, and setting command overrides it.
func TestAgentMergePreservesDefault(t *testing.T) {
	cfg := DefaultConfig()
	if err := UnmarshalYAML([]byte("agent:\n  args: [--flag]\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.AgentCommand(); got != "claude" {
		t.Errorf("args-only block should keep default; got %q", got)
	}

	cfg = DefaultConfig()
	if err := UnmarshalYAML([]byte("agent:\n  command: opencode\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.AgentCommand(); got != "opencode" {
		t.Errorf("command should override; got %q", got)
	}
}
