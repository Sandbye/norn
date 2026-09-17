package headless

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/config"
)

func argsFor(t *testing.T, agent config.AgentConfig, brief string) (string, []string) {
	t.Helper()
	cmd, err := Command(context.Background(), agent, "/w/logic", brief)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Dir != "/w/logic" {
		t.Fatalf("cmd.Dir = %q, want the role worktree", cmd.Dir)
	}
	return cmd.Path, cmd.Args[1:]
}

func has(args []string, want ...string) bool {
	joined := " " + strings.Join(args, " ") + " "
	for _, w := range want {
		if !strings.Contains(joined, " "+w+" ") {
			return false
		}
	}
	return true
}

// The claude run must stay on the normal login and carry enough authority to
// commit. --bare would force ANTHROPIC_API_KEY, which is API billing for a
// subscription user; a missing permission mode would deny every edit and leave
// a role that exits 0 having written nothing.
func TestClaudeRunArgs(t *testing.T) {
	_, args := argsFor(t, config.AgentConfig{}, "brief text")
	if has(args, "--bare") {
		t.Fatalf("claude run passed --bare: %v", args)
	}
	if !has(args, "-p", "--permission-mode", "auto", "--append-system-prompt") {
		t.Fatalf("claude run missing its flags: %v", args)
	}
	if args[1] != Instruction {
		t.Fatalf("prompt = %q, want the unattended instruction", args[1])
	}
	if _, args := argsFor(t, config.AgentConfig{Model: "opus"}, ""); !has(args, "--model", "opus") {
		t.Fatalf("model not passed: %v", args)
	}
}

// Codex takes its context in the prompt, since it has no system-prompt flag,
// and needs the writable sandbox for the same reason claude needs its
// permission mode.
func TestCodexRunArgs(t *testing.T) {
	path, args := argsFor(t, config.AgentConfig{Command: "codex"}, "brief text")
	if !strings.HasSuffix(path, "codex") {
		t.Fatalf("ran %q, want codex", path)
	}
	if args[0] != "exec" || !has(args, "--json", "--sandbox", "workspace-write", "--approve-for-me") {
		t.Fatalf("codex run missing its flags: %v", args)
	}
	prompt := args[len(args)-1]
	if !strings.HasPrefix(prompt, Instruction) || !strings.Contains(prompt, "brief text") {
		t.Fatalf("codex prompt lost the instruction or the brief: %q", prompt)
	}
}

// An agent with no runner fails the start by name. Launching it with another
// agent's flags would have it exit 0 having done nothing, which reads as a role
// that finished and merges an empty branch.
func TestUnsupportedAgent(t *testing.T) {
	agent := config.AgentConfig{Command: "opencode"}
	if err := Supported(agent); !errors.Is(err, ErrUnsupportedAgent) {
		t.Fatalf("Supported(opencode) = %v, want ErrUnsupportedAgent", err)
	}
	if !strings.Contains(Supported(agent).Error(), "opencode") {
		t.Fatalf("error does not name the agent: %v", Supported(agent))
	}
	if err := Supported(config.AgentConfig{}); err != nil {
		t.Fatalf("the default agent must be supported: %v", err)
	}
}
