package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The Threads tab, task-first.
//
// A task is the unit of work; a strand is one agent's share of it. The old rail
// listed every strand of every task in one flat column and kept the shape of
// the task in a popover, so the two questions a person actually asks, "which
// task needs me" and "which of its strands", were answered on different
// screens. Here the left column is tasks, the right is the strands of the one
// under the cursor, and the popover is gone.

// taskCard is one task's rows, trunk first, with the line that says what the
// task needs next.
type taskCard struct {
	id        string
	label     string
	rows      []dashRow
	hasCursor bool
}

// taskCards groups the visible rows into tasks, keeping rail order, and returns
// the worktrees that belong to no task separately: they are threads, not
// strands, and they get their own short section rather than a fake task.
func taskCards(vis []dashRow, cursor int) (cards []taskCard, loose []dashRow, looseCursor int) {
	looseCursor = -1
	index := map[string]int{}
	for i, r := range vis {
		if r.TaskID == "" {
			loose = append(loose, r)
			if i == cursor {
				looseCursor = len(loose) - 1
			}
			continue
		}
		at, seen := index[r.TaskID]
		if !seen {
			index[r.TaskID] = len(cards)
			cards = append(cards, taskCard{id: r.TaskID, label: taskLabel(r)})
			at = len(cards) - 1
		}
		cards[at].rows = append(cards[at].rows, r)
		if i == cursor {
			cards[at].hasCursor = true
		}
	}
	// The trunk first in each card: it is what the others land on, and it is
	// where the PR comes from.
	for i := range cards {
		var trunk, rest []dashRow
		for _, r := range cards[i].rows {
			if r.Branch == r.TaskTrunk {
				trunk = append(trunk, r)
				continue
			}
			rest = append(rest, r)
		}
		cards[i].rows = append(trunk, rest...)
	}
	return cards, loose, looseCursor
}

// renderTaskList is the left column: one task per entry, with the next thing
// that task needs from a person underneath it.
func (d Dashboard) renderTaskList(cards []taskCard, loose []dashRow, looseCursor, w, h int) string {
	var lines []string
	for _, c := range cards {
		marker, style := "  ", branchStyle
		if c.hasCursor {
			marker, style = cursorStyle.Render("▸ "), cursorStyle
		}
		lines = append(lines, marker+style.Render(fitCell(truncate(c.label, w-3), w-3)))
		lines = append(lines, "    "+cardHeadline(c.rows, w-5))
	}
	if len(loose) > 0 {
		// A machine collects worktrees that belong to no task, and after a few
		// weeks there are more of them than tasks. They are a list to clean,
		// not a list to work from, so only the recent ones and the one under
		// the cursor are worth the rows.
		shown, extra := trimLoose(loose, looseCursor, looseShown)
		lines = append(lines, "", dimStyle.Render(fitCell(fmt.Sprintf("LOOSE · %d", len(loose)), w)))
		for _, r := range shown {
			marker, style := "  ", dimStyle
			if looseCursor >= 0 && looseCursor < len(loose) && loose[looseCursor].Path == r.Path {
				marker, style = cursorStyle.Render("▸ "), cursorStyle
			}
			name := truncate(r.Branch, w-12)
			lines = append(lines, marker+style.Render(fitCell(name, w-11))+dimStyle.Render(compactAge(r.LastActivityAt)))
		}
		if extra > 0 {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("    +%d older · c to clean", extra)))
		}
	}
	return strings.Join(window(lines, h), "\n")
}

// looseShown is how many task-less worktrees the column lists before it stops.
const looseShown = 5

// trimLoose keeps the newest few, plus whichever one the cursor is on, since a
// row you cannot see is a row you cannot act on. Returns the rows to draw and
// how many were left out. The cursor index is remapped by the caller's marker
// comparison, so the row itself carries the highlight.
func trimLoose(loose []dashRow, cursor, keep int) ([]dashRow, int) {
	if len(loose) <= keep {
		return loose, 0
	}
	shown := append([]dashRow{}, loose[:keep]...)
	if cursor >= keep {
		shown[keep-1] = loose[cursor]
	}
	return shown, len(loose) - keep
}

// cardHeadline is the one thing this task needs, in the shared vocabulary.
func cardHeadline(rows []dashRow, w int) string {
	var needs, land, start, landed, strands int
	for _, r := range rows {
		if r.Role == "" || r.Branch == r.TaskTrunk {
			continue
		}
		strands++
		switch status, _ := strandStatus(r); {
		case status == "needs you" || status == "uncommitted" || status == "failed":
			needs++
		case r.Ahead > 0:
			land++
		case status == "landed":
			landed++
		case status == "can start":
			start++
		}
	}
	switch {
	case needs > 0:
		return dirtyStyle.Render(truncate(fmt.Sprintf("%d need you", needs), w))
	case land > 0:
		return activeStyle.Render(truncate(fmt.Sprintf("%d to land · L", land), w))
	case start > 0:
		return activeStyle.Render(truncate(fmt.Sprintf("%d can start · R", start), w))
	case strands > 0 && landed == strands:
		return activeStyle.Render(truncate("all landed · ready for review", w))
	default:
		return dimStyle.Render(truncate("working", w))
	}
}

// renderStrands is the right column: the strands of the task under the cursor,
// and what the one you are on is doing.
func (d Dashboard) renderStrands(vis []dashRow, cards []taskCard, w, h int) string {
	var current *taskCard
	for i := range cards {
		if cards[i].hasCursor {
			current = &cards[i]
		}
	}
	if current == nil {
		if d.cursor < len(vis) {
			return d.renderDetail(vis[d.cursor], w)
		}
		return ""
	}

	var lines []string
	lines = append(lines, taskHeaderStyle.Render(truncate(current.label, w)))
	if blocked := current.rows[0].TaskBlocked; blocked != "" {
		lines = append(lines, dirtyStyle.Render(truncate("blocked: "+blocked, w)))
	}
	lines = append(lines, "")

	nameW := 4
	for _, r := range current.rows {
		nameW = max(nameW, len(r.Role))
	}
	nameW = min(nameW, max(w-statusWidth-6, 8))

	var selected dashRow
	for _, r := range current.rows {
		status, statusStyle := strandStatus(r)
		name := fitCell(truncate(r.Role, nameW), nameW)
		here := d.cursor < len(vis) && vis[d.cursor].Path == r.Path
		if here {
			selected = r
			name = cursorStyle.Render(name)
		} else {
			name = branchStyle.Render(name)
		}
		marker := "  "
		if here {
			marker = cursorStyle.Render("▸ ")
		}
		line := marker + statusGlyph(r) + " " + name + "  " + statusStyle.Render(status)
		if r.Branch == r.TaskTrunk {
			line += dimStyle.Render("  trunk")
		}
		lines = append(lines, line)
	}

	if selected.Path != "" {
		lines = append(lines, "", strings.Join(strandDetailLines(selected, w), "\n"))
	}
	return strings.Join(window(lines, h), "\n")
}

// strandDetailLines is what the selected strand says about itself: the thing it
// asked, what it plans to do next, and anything blocking it.
func strandDetailLines(r dashRow, w int) []string {
	var out []string
	if r.Question != "" {
		out = append(out, dirtyStyle.Render("asked ")+truncate(quoteOneLine(r.Question), max(w-8, 20)))
	}
	if r.Next != "" {
		out = append(out, dimStyle.Render("next  ")+truncate(r.Next, max(w-8, 20)))
	}
	if r.Blocked != "" {
		out = append(out, dirtyStyle.Render("stuck ")+truncate(r.Blocked, max(w-8, 20)))
	}
	if pr := prDetail(r); pr != "" && pr != "—" {
		out = append(out, dimStyle.Render("pr    ")+pr)
	}
	return out
}

// window clips a column to the height it has, keeping the top: a task list
// longer than the pane scrolls with the cursor elsewhere, and clipping the
// bottom is the honest half to lose.
func window(lines []string, h int) []string {
	if h <= 0 || len(lines) <= h {
		return lines
	}
	return append(lines[:h-1], dimStyle.Render(fmt.Sprintf("  +%d more", len(lines)-h+1)))
}

// joinColumns puts the two columns side by side with a rule between them.
func joinColumns(left, right string, leftW, h int) string {
	l := lipgloss.NewStyle().
		Width(leftW).
		Height(h).
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(colorSurface).
		Render(left)
	return lipgloss.JoinHorizontal(lipgloss.Top, l, "  ", right)
}
