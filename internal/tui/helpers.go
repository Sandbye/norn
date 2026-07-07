package tui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
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

// frameWidth is the panel's fixed inner width (content box, padding included).
// Fixed so the panel never resizes as the focused row's help line changes
// length — otherwise the whole panel would jitter on every cursor move.
const frameWidth = 104

// frameHeight caps the panel's inner rows so it reads as a centered pane, not a
// full-screen fill. A tab with more content than this grows to fit (no clip).
const frameHeight = 24

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
	// Constant inner height, independent of content, so switching tabs doesn't
	// resize/recenter the panel. Capped well below full-screen (full height felt
	// too tall) but grown to fit any tab whose content is taller, so nothing
	// clips. Long lists still want a scroll (see roadmap).
	innerH := height - 6
	if innerH > frameHeight {
		innerH = frameHeight
	}
	if innerH < 6 {
		innerH = 6
	}
	if lines := strings.Count(content, "\n") + 1; lines > innerH {
		innerH = lines
		if innerH > height-2 {
			innerH = height - 2
		}
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
func makeAgentCmd(agent config.AgentConfig, wtPath string, resume bool, model string) *exec.Cmd {
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
		return wireStdio(exec.Command("claude", "-c"), wtPath)
	}

	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
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

// LaunchAgent runs the configured agent in the worktree and blocks until it exits.
// LaunchAgent runs the coding agent in wtPath. model overrides the config
// default for this launch (empty → default); ignored on resume and non-claude.
func LaunchAgent(agent config.AgentConfig, wtPath string, resume bool, model string) {
	makeAgentCmd(agent, wtPath, resume, model).Run()
}
