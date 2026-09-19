package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

// One vocabulary for what a strand is doing.
//
// The rail used to say it in glyphs (● ◆ ○ · ✓ ✗ ⤒ !), the board in sentences,
// and the detail pane in a third wording, so the same strand read three ways
// and two of the glyphs meant the same thing. A person scanning ten rows needs
// the same word to mean the same thing everywhere, and needs it to answer one
// question: is this mine to act on.

// statusWidth is the column the rail reserves for the words below.
const statusWidth = 18

// strandStatus is what this strand is doing, in the words used everywhere
// else, plus the style that says whether it wants you.
func strandStatus(r dashRow) (string, lipgloss.Style) {
	switch {
	case r.Role == "":
		return "", dimStyle // a plain worktree is not a strand
	case r.Run == state.RunFailed:
		return "failed", goneStyle
	case r.Bell || r.AgentState == claude.StateWaiting:
		return "needs you", dirtyStyle
	case r.Ahead > 0:
		// Commits here, strands in the task header: the same words counting
		// two different things is what made the old rail unreadable.
		return fmt.Sprintf("%d commit(s)", r.Ahead), activeStyle
	case r.Run == state.RunMerged:
		return "landed", dimStyle
	case r.Uncommitted:
		return "uncommitted", dirtyStyle
	case r.WaitsFor == "*":
		// The reviewer waits for every code strand, which no single name says.
		return "after the code", dimStyle
	case r.WaitsFor == r.Role+"-tests":
		// The test-first pair: naming the sibling again says nothing the row
		// does not already say, and it is what overflows the column.
		return "after its tests", dimStyle
	case r.WaitsFor != "":
		return "after " + r.WaitsFor, dimStyle
	case r.Run == state.RunRunning || r.AgentState == claude.StateWorking:
		return "working", activeStyle
	case r.Run == "":
		return "can start", activeStyle
	default:
		return "idle", dimStyle
	}
}

// statusGlyph is the same answer at a glance: three marks, not eight.
//
// A filled mark wants you, a hollow one is busy, a dot is neither. The words
// carry the detail; the glyph only has to survive peripheral vision.
func statusGlyph(r dashRow) string {
	status, style := strandStatus(r)
	switch status {
	case "":
		return stateGlyph(r.AgentState)
	case "failed":
		return style.Render("✗")
	case "needs you", "uncommitted":
		return style.Render("◆")
	case "landed":
		return style.Render("✓")
	case "working":
		return style.Render("●")
	default:
		return style.Render("○")
	}
}

// statusLegend is what `?` shows, so the marks are never folklore.
var statusLegend = []keyHint{
	{"◆", "needs you: it asked something, or it is holding uncommitted work"},
	{"●", "working"},
	{"○", "waiting its turn, or ready for you to start it"},
	{"✓", "landed on the trunk"},
	{"✗", "failed"},
}
