package tui

import (
	"os"
	"os/exec"
)

func makeClaudeCmd(wtPath string, resume bool) *exec.Cmd {
	if resume {
		cmd := exec.Command("claude", "-c")
		cmd.Dir = wtPath
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd
	}

	promptPath := wtPath + "/.worktree.md"
	promptData, err := os.ReadFile(promptPath)
	prompt := ""
	if err == nil {
		prompt = string(promptData)
	}

	cmd := exec.Command("claude",
		"--append-system-prompt", prompt,
		"Start worktree session. Follow the startup procedure in .worktree.md.",
	)
	cmd.Dir = wtPath
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}


func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func LaunchClaude(wtPath string, resume bool) {
	cmd := makeClaudeCmd(wtPath, resume)
	cmd.Run()
}
