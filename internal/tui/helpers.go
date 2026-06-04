package tui

import (
	"os"
	"os/exec"
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

func makeClaudeCmd(wtPath string, resume bool) *exec.Cmd {
	if resume {
		cmd := exec.Command("claude", "-c")
		cmd.Dir = wtPath
		cmd.Env = envWithPWD(wtPath)
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
	cmd.Env = envWithPWD(wtPath)
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
