package tui

import (
	"os"
	"os/exec"

	"github.com/sandbye/norn/internal/config"
)

// envWithPWD returns the parent environment with PWD overridden to wtPath.
// Claude (and many tools) read PWD from env rather than calling getcwd, so we
// must override it explicitly — cmd.Dir alone is not enough to make the
// `@` file picker browse the worktree.
func envWithPWD(wtPath string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	pwdFound := false
	for _, e := range env {
		if len(e) >= 4 && e[:4] == "PWD=" {
			out = append(out, "PWD="+wtPath)
			pwdFound = true
			continue
		}
		out = append(out, e)
	}
	if !pwdFound {
		out = append(out, "PWD="+wtPath)
	}
	return out
}

// wireStdio points a command at the terminal and the worktree directory.
func wireStdio(cmd *exec.Cmd, wtPath string) *exec.Cmd {
	cmd.Dir = wtPath
	cmd.Env = envWithPWD(wtPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// makeAgentCmd builds the command that launches the configured coding agent in
// a worktree. Claude gets the rich integration (task brief injected via
// --append-system-prompt, `-c` to resume). Any other agent is launched plainly
// in the worktree directory, where `.worktree.md` carries the brief.
func makeAgentCmd(agent config.AgentConfig, wtPath string, resume bool) *exec.Cmd {
	command := agent.Command
	if command == "" {
		command = "claude"
	}

	if command != "claude" {
		return wireStdio(exec.Command(command, agent.Args...), wtPath)
	}

	if resume {
		return wireStdio(exec.Command("claude", "-c"), wtPath)
	}

	prompt := ""
	if data, err := os.ReadFile(wtPath + "/.worktree.md"); err == nil {
		prompt = string(data)
	}
	return wireStdio(exec.Command("claude",
		"--append-system-prompt", prompt,
		"Start worktree session. Follow the startup procedure in .worktree.md.",
	), wtPath)
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// LaunchAgent runs the configured agent in the worktree and blocks until it exits.
func LaunchAgent(agent config.AgentConfig, wtPath string, resume bool) {
	makeAgentCmd(agent, wtPath, resume).Run()
}
