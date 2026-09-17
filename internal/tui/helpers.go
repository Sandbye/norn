package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
)

var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

// sealBackground closes the background "gaps" that appear when nested lipgloss
// renders emit resets inside a background-styled panel: a reset clears the
// panel bg, so the cells after it fall back to the terminal default. This
// re-asserts the panel background on every reset sequence, so the fill stays
// seamless. SGR state is sticky, so this alone keeps the whole line filled.
//
// Crucially it leaves any sequence that *sets* a background untouched, so
// intentional highlights (the selected row) keep their own color.
func sealBackground(input string, bg lipgloss.Color) string {
	r, g, b := hexRGB(bg)
	newBg := fmt.Sprintf("48;2;%d;%d;%d", r, g, b)

	return ansiSGR.ReplaceAllStringFunc(input, func(seq string) string {
		inner := seq[2 : len(seq)-1] // strip "\x1b[" and "m"
		setsBg, resetsBg := false, inner == ""
		for _, t := range strings.Split(inner, ";") {
			switch {
			case t == "0" || t == "49":
				resetsBg = true
			case t == "48":
				setsBg = true
			default:
				if n, err := strconv.Atoi(t); err == nil && ((n >= 40 && n <= 47) || (n >= 100 && n <= 107)) {
					setsBg = true
				}
			}
		}
		if setsBg || !resetsBg { // intentional bg, or nothing to seal
			return seq
		}
		if inner == "" {
			inner = "0"
		}
		return "\x1b[" + inner + ";" + newBg + "m"
	})
}

// hexRGB parses a "#rrggbb" lipgloss color into 8-bit components.
func hexRGB(c lipgloss.Color) (int, int, int) {
	s := strings.TrimPrefix(string(c), "#")
	if len(s) != 6 {
		return 0, 0, 0
	}
	r, _ := strconv.ParseInt(s[0:2], 16, 0)
	g, _ := strconv.ParseInt(s[2:4], 16, 0)
	b, _ := strconv.ParseInt(s[4:6], 16, 0)
	return int(r), int(g), int(b)
}

// centerBlock shifts a whole text block to the horizontal center of termWidth by
// prefixing every line with a uniform left pad. The block stays left-aligned
// internally (columns line up); only the block as a whole is centered, opencode
// style. Vertical position is untouched, so growing content never overflows.
// Returns content unchanged when it's as wide as the terminal or width is unknown.
func centerBlock(content string, termWidth int) string {
	if termWidth <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	max := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > max {
			max = w
		}
	}
	if max >= termWidth {
		return content
	}
	pad := strings.Repeat(" ", (termWidth-max)/2)
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

// centerScreen places a text block in the center of a width×height canvas, both
// axes, opencode style. The block stays internally left-aligned; only the block
// as a whole is centered. Falls back to horizontal-only centering when the
// height is unknown, and to raw content when neither dimension is known.
func centerScreen(content string, width, height int) string {
	if width <= 0 {
		return content
	}
	if height <= 0 {
		return centerBlock(content, width)
	}
	content = strings.Trim(content, "\n")
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
}

// frameWidth caps the panel's inner width (content box, padding included). The
// panel shrinks to fit smaller terminals (min(frameWidth, width-8)); this is the
// ceiling so a big terminal gets a roomy command center without stretching to
// absurd widths. Capped (not full-width) so the panel doesn't jitter as the
// focused row's help line changes length.
const frameWidth = 118

// frameHeight caps the panel's inner rows so it reads as a centered pane, not a
// full-screen fill. A tab with more content than this grows to fit (no clip).
const frameHeight = 32

// frameInnerHeight is the panel's fixed inner content height for a terminal of
// the given height: capped at frameHeight, shrinking only on small terminals.
// It is a pure function of the terminal size, never of content, so the box
// stays a constant height and never jumps as views change.
func frameInnerHeight(termHeight int) int {
	inner := frameHeight
	if termHeight > 0 && termHeight-6 < frameHeight {
		inner = termHeight - 6
	}
	if inner < 6 {
		inner = 6
	}
	return inner
}

// frameBodyRows is the rows a tab's body may use inside the fixed frame, below
// the two-line masthead + its blank. Views size their scroll windows to this so
// content fits the constant-height panel instead of stretching it.
func frameBodyRows(termHeight int) int {
	rows := frameInnerHeight(termHeight) - 3
	if rows < 3 {
		rows = 3
	}
	return rows
}

// frame wraps content in a rounded frost-bordered panel and floats it centered
// on a Nord-filled screen — an arctic pane that gives norn's views identity.
// Panel and screen share the nord0 background so styled spans never reveal a
// seam. Both width AND height are fixed (relative to the terminal), so the box
// stays put and content grows/shrinks *inside* it rather than moving the box.
// Falls back to plain centering on terminals too small to frame.
func frame(content string, width, height int) string {
	inner := frameWidth
	if max := width - 8; inner > max {
		inner = max
	}
	if width < 44 || height < 10 || inner < 30 {
		return centerScreen(content, width, height)
	}
	content = sealBackground(strings.Trim(content, "\n"), colorBase)
	// Fixed inner height: it never grows to fit content, so the panel never jumps
	// as you switch tabs or the selected thread's detail changes size. It only
	// changes when the terminal is resized. Views size their scroll windows to
	// frameBodyRows so their content fits; anything taller is clipped here rather
	// than stretching the box.
	innerH := frameInnerHeight(height)
	lines := strings.Split(content, "\n")
	if len(lines) > innerH {
		content = strings.Join(lines[:innerH], "\n")
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorLavender).
		BorderBackground(colorBase).
		Background(colorBase).
		Foreground(colorText).
		Padding(1, 3).
		Width(inner).
		Height(innerH)
	panel := style.Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, panel,
		lipgloss.WithWhitespaceBackground(colorBase))
}

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
// makeAgentCmd builds the launch command. model overrides agent.Model for this
// session (empty → fall back to the config default). It only applies to claude
// (as --model) and to fresh sessions; resume (-c) continues the prior model.
func makeAgentCmd(cfg config.Config, agent config.AgentConfig, wtPath string, resume bool, model string) *exec.Cmd {
	return agentCmd(cfg, agent, wtPath, resume, model, false)
}

// strandCmd is makeAgentCmd for an agent that runs as a strand, where nobody
// may be watching this particular pane.
func strandCmd(cfg config.Config, agent config.AgentConfig, wtPath, model string) *exec.Cmd {
	return agentCmd(cfg, agent, wtPath, false, model, true)
}

func agentCmd(cfg config.Config, agent config.AgentConfig, wtPath string, resume bool, model string, strandRun bool) *exec.Cmd {
	command := agent.Command
	if command == "" {
		command = "claude"
	}
	if model == "" {
		model = agent.Model
	}

	if command != "claude" {
		return wireStdio(exec.Command(command, agent.Args...), wtPath)
	}

	if resume {
		// A session the daemon holds cannot be continued: `claude -c` refuses
		// it and tells you to attach instead. That is the normal state for a
		// thread norn started and you left, so check before falling back.
		if s, ok := claude.SessionFor(claude.Sessions(context.Background()), wtPath); ok && s.Background() {
			return wireStdio(exec.Command("claude", "attach", s.ID), wtPath)
		}
		return wireStdio(exec.Command("claude", "-c"), wtPath)
	}

	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort := cfg.EffortFor(model); effort != "" {
		args = append(args, "--effort", effort)
	}
	if strandRun {
		// A strand is one of several agents working in parallel, and you are
		// in at most one pane at a time. Manual mode would have the other
		// strands stop at the first prompt and wait for someone who is looking
		// elsewhere. auto has a classifier review each action instead.
		args = append(args, "--permission-mode", "auto")
		// Prompt suggestions are accepted with Tab, and Tab switches tabs
		// everywhere else in norn, so muscle memory inside a pane types a
		// predicted sentence into the agent instead.
		args = append(args, "--prompt-suggestions", "false")
	}
	prompt := ""
	if data, err := os.ReadFile(wtPath + "/.worktree.md"); err == nil {
		prompt = string(data)
	}
	args = append(args,
		"--append-system-prompt", prompt,
		"Start worktree session. Follow the startup procedure in .worktree.md.",
	)
	return wireStdio(exec.Command("claude", args...), wtPath)
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// scrollWindow returns the [start,end) slice bounds of a list of `total` items
// showing at most `height` rows, keeping `cursor` in view (centered-ish). Use
// it so long lists scroll inside the fixed frame instead of overflowing it.
func scrollWindow(cursor, total, height int) (start, end int) {
	if height <= 0 || total <= height {
		return 0, total
	}
	start = cursor - height/2
	if start < 0 {
		start = 0
	}
	if start+height > total {
		start = total - height
	}
	return start, start + height
}

// truncate shortens s to at most max runes, adding an ellipsis when cut.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// fitCell fits plain text to exactly w visible columns (truncate with … or
// right-pad). Fit BEFORE styling — padding a styled string counts the invisible
// ANSI bytes and shreds column alignment.
func fitCell(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > w {
		if w == 1 {
			return "…"
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

// AgentAvailable reports whether the configured agent binary is on PATH.
// Creating a worktree succeeds with or without it, so the caller has to know
// which it is: handing off to a missing binary clears the screen and leaves
// the user with a blank terminal and a worktree they were never told about.
func AgentAvailable(agent config.AgentConfig) bool {
	command := agent.Command
	if command == "" {
		command = "claude"
	}
	_, err := exec.LookPath(command)
	return err == nil
}

// LaunchAgent runs the coding agent in wtPath and blocks until it exits. model
// overrides the config default for this launch (empty → default); ignored on
// resume and non-claude. The error is returned rather than swallowed: a failure
// to start is invisible otherwise, because the screen was just cleared for it.
func LaunchAgent(cfg config.Config, agent config.AgentConfig, wtPath string, resume bool, model string) error {
	return makeAgentCmd(cfg, agent, wtPath, resume, model).Run()
}

// LaunchAgentPrompt runs the agent in wtPath with prompt as its next message,
// continuing the existing session when there is one. Blocks until it exits.
// Reports whether it launched: only claude takes a prompt argument, so other
// agents are left to the caller.
func LaunchAgentPrompt(agent config.AgentConfig, wtPath, model, prompt string) bool {
	command := agent.Command
	if command == "" {
		command = "claude"
	}
	if command != "claude" {
		return false
	}
	if model == "" {
		model = agent.Model
	}
	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	if claude.HasSession(wtPath) {
		args = append(args, "-c")
	}
	wireStdio(exec.Command("claude", append(args, prompt)...), wtPath).Run()
	return true
}

// popover renders a small box over the current view: bordered, padded, sized to
// its content, centered across the terminal and sitting near the top.
//
// Not frame(): that one fills the terminal's height, which for a box of six
// lines means a tall empty rectangle with the content pushed to the bottom.
func popover(content string, boxWidth, termWidth, termHeight int) string {
	// Width() is the content box, so the border and padding sit outside it:
	// asking for more than the terminal can hold is what clips every line.
	inner := max(min(boxWidth, termWidth-4)-6, 20)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorLavender).
		Padding(1, 2).
		Width(inner).
		Render(content)
	// Centered both ways: a box pinned to the top of a tall terminal reads as
	// something that failed to lay out rather than as a deliberate overlay.
	centered := centerBlock(box, termWidth)
	if top := (termHeight - lipgloss.Height(centered)) / 2; top > 0 {
		centered = strings.Repeat("\n", top) + centered
	}
	return centered
}
