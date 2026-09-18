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

// A role's own args reach codex, which is the only way to set what norn has no
// opinion on: reasoning effort is the largest lever on what an unattended role
// costs. They sit last, so a repeated flag resolves in the role's favour, and
// the prompt stays the final argument.
func TestCodexPassesRoleArgs(t *testing.T) {
	agent := config.AgentConfig{Command: "codex", Model: "gpt-5", Args: []string{"-c", `model_reasoning_effort="low"`}}
	_, args := argsFor(t, agent, "")
	if !has(args, "-c", `model_reasoning_effort="low"`) {
		t.Fatalf("role args did not reach codex: %v", args)
	}
	if args[len(args)-1] != Instruction {
		t.Fatalf("args displaced the prompt: %v", args)
	}
	if i, j := index(args, "--model"), index(args, "-c"); i > j {
		t.Fatalf("role args did not land after norn's own flags: %v", args)
	}

	// A claude role's args are honored too: a field that is authoritative
	// enough to fail validation but not to do anything is a flag that neither
	// errors nor applies, which costs an hour to notice.
	_, claudeArgs := argsFor(t, config.AgentConfig{Args: []string{"--add-dir", "/w/shared"}}, "")
	if !has(claudeArgs, "--add-dir", "/w/shared") {
		t.Fatalf("claude run dropped the role's args: %v", claudeArgs)
	}
}

// Every flag the runner sets must be in config.ReservedArgs, which is what
// stops a role's `args:` from repeating one. The list lives in config because
// config cannot import this package, so this test is the only thing keeping the
// two in step.
func TestReservedArgsCoversEveryFlagSet(t *testing.T) {
	for _, agent := range []config.AgentConfig{{}, {Command: "codex"}} {
		_, args := argsFor(t, agent, "brief")
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				continue // a value or the prompt, not a flag
			}
			if index(config.ReservedArgs, a) < 0 {
				t.Fatalf("%s sets %q but config.ReservedArgs does not list it, so a role could repeat it", agent.Command, a)
			}
		}
	}
}

func index(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
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
