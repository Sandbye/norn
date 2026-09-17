// Package headless runs a role's agent unattended in its own worktree.
//
// Process exit is the whole contract: norn reads the exit code and the role's
// branch, never the agent's own state. That is deliberate. "Is this role done"
// is a terminal question, and answering it from a transcript would mean a state
// adapter per agent before a new agent could serve a role at all.
package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
)

// Instruction is what a headless role is told on top of its brief. A role with
// nobody to ask has to finish or fail rather than wait, and its output has to
// be commits on its own branch: norn merges the branch, so anything left
// uncommitted when the process exits is lost.
const Instruction = "Work this task to completion without asking for confirmation, since nobody is watching this session. " +
	"Follow the startup procedure in .worktree.md. Commit your work on this branch as you go, and leave nothing uncommitted. " +
	"Do not push, do not open a PR, and do not touch another worktree: norn merges this branch into the trunk when you exit."

// ErrUnsupportedAgent is an agent norn has no unattended runner for. It fails
// the start by name rather than launching something with flags that mean
// nothing to it, because an agent that quietly does nothing still exits 0 and
// would read as a role that finished.
var ErrUnsupportedAgent = errors.New("no headless runner for this agent")

// Command builds the unattended run for the agent serving a role, in that
// role's worktree. brief is the worktree's `.worktree.md`, passed the way each
// agent takes context: claude gets it as an appended system prompt, the way an
// interactive launch does, and codex gets it in the prompt because it has no
// equivalent flag.
//
// The permission level is `act` for both: a role that cannot write files or run
// its own tests cannot produce the commits this whole contract merges.
func Command(ctx context.Context, agent config.AgentConfig, dir, brief string) (*exec.Cmd, error) {
	command := agent.Command
	if command == "" {
		command = "claude"
	}

	var args []string
	switch command {
	case "claude":
		// No --bare: it skips the OAuth login and requires ANTHROPIC_API_KEY,
		// which would put a subscription user on API billing to run a role.
		args = []string{"-p", Instruction, "--output-format", "stream-json", "--verbose"}
		if mode := claude.PermissionModeFor(string(config.GrantAct)); mode != "" {
			args = append(args, "--permission-mode", mode)
		}
		if agent.Model != "" {
			args = append(args, "--model", agent.Model)
		}
		if brief != "" {
			args = append(args, "--append-system-prompt", brief)
		}
	case "codex":
		// workspace-write plus --approve-for-me is codex's reading of the same
		// grant: commands run, reviewed automatically instead of by a person.
		args = []string{"exec", "--json", "--sandbox", "workspace-write", "--approve-for-me"}
		if agent.Model != "" {
			args = append(args, "--model", agent.Model)
		}
		prompt := Instruction
		if brief != "" {
			prompt = Instruction + "\n\n" + brief
		}
		args = append(args, prompt)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAgent, command)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	return cmd, nil
}

// Supported reports whether a role's agent can run unattended, so a run can
// reject the whole task up front instead of failing one role after the others
// have already started.
func Supported(agent config.AgentConfig) error {
	_, err := Command(context.Background(), agent, "", "")
	return err
}

// Brief reads a worktree's `.worktree.md`. A missing one is not an error: the
// role still has the repo's own AGENTS.md / CLAUDE.md and its branch name.
func Brief(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".worktree.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// Start launches the role's agent and returns without waiting, streaming both
// output streams into out. Both agents emit JSONL, so that log is the only
// record of what an unattended role did.
//
// Start rather than run-to-completion because the caller records the pid: a
// later `norn run` cannot wait on a process it did not spawn, but it can ask
// whether that pid is still alive.
func Start(ctx context.Context, agent config.AgentConfig, dir string, out io.Writer) (*exec.Cmd, error) {
	cmd, err := Command(ctx, agent, dir, Brief(dir))
	if err != nil {
		return nil, err
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s in %s: %w", cmd.Path, dir, err)
	}
	return cmd, nil
}
