package tui

import (
	"fmt"
	"strings"

	"github.com/sandbye/norn/internal/state"
)

// The task board: what is done, what is outstanding, and who is waiting for
// you, for one task, on one screen.
//
// It exists because a split task's truth is spread across three agent
// transcripts, and reading three transcripts to answer "can I ship this yet" is
// the work norn is supposed to remove. Everything here is already known: git
// says what has reached the trunk, and each strand's own `.state.md` says what
// it thinks it is doing next.

// renderBoard draws the task the cursor is on.
func (d Dashboard) renderBoard(vis []dashRow) string {
	rows := d.boardRows(vis)
	if len(rows) == 0 {
		return popover(dimStyle.Render("no task here: the cursor is not on a strand"), 46, d.width, d.height)
	}

	width := min(max(d.width-8, 40), 110)
	var b strings.Builder
	b.WriteString(titleStyle.Render("◈ "+truncate(taskLabel(rows[0]), width-4)) + "\n")
	b.WriteString(dimStyle.Render("  trunk: "+rows[0].TaskTrunk) + "\n")
	if rows[0].TaskBlocked != "" {
		b.WriteString(dirtyStyle.Render("  blocked: "+truncate(rows[0].TaskBlocked, width-12)) + "\n")
	}
	b.WriteString("\n")

	for i, r := range rows {
		b.WriteString(boardStrand(r, width, i == d.boardCursor) + "\n")
	}

	b.WriteString("\n" + dimStyle.Render(boardSummary(rows)))
	b.WriteString("\n" + dimStyle.Render("↑/↓ pick · → enter · d review · S plan · L land · esc close"))
	return popover(b.String(), width, d.width, d.height)
}

// boardRows are the strands of the task under the cursor, trunk first, since
// the trunk is what the others land on and what opens the PR.
func (d Dashboard) boardRows(vis []dashRow) []dashRow {
	// The pinned task wins over the rail's cursor: the board survives a reload
	// that reorders the rail, and it is how norn returns here after a review.
	id := d.boardTask
	if id == "" {
		if d.cursor >= len(vis) {
			return nil
		}
		id = vis[d.cursor].TaskID
	}
	if id == "" {
		return nil
	}
	var trunk, rest []dashRow
	for _, r := range d.rows {
		if r.TaskID != id || r.Role == "" {
			continue
		}
		if r.Branch == r.TaskTrunk {
			trunk = append(trunk, r)
			continue
		}
		rest = append(rest, r)
	}
	return append(trunk, rest...)
}

// boardStrand is one strand's block: what it is, where its work stands, and the
// one line it says about itself.
func boardStrand(r dashRow, width int, selected bool) string {
	name := r.Role
	if r.Branch == r.TaskTrunk {
		name += dimStyle.Render(" (integrates)")
	}

	marker, style := "  ", branchStyle
	if selected {
		marker, style = cursorStyle.Render("▸ "), cursorStyle
	}

	var b strings.Builder
	b.WriteString(marker + style.Render(fmt.Sprintf("%-14s", name)) + boardState(r) + "\n")
	if r.Next != "" {
		b.WriteString(dimStyle.Render("    next: ") + truncate(r.Next, width-12) + "\n")
	}
	if r.Blocked != "" {
		b.WriteString(dirtyStyle.Render("    blocked: "+truncate(r.Blocked, width-14)) + "\n")
	}
	return b.String()
}

// boardState is the one phrase that says whether this strand needs anything.
func boardState(r dashRow) string {
	switch {
	case r.Bell:
		return dirtyStyle.Render("waiting for you")
	case r.WaitsFor != "":
		return dimStyle.Render("waits for " + r.WaitsFor)
	case r.Run == "" && r.Branch != r.TaskTrunk:
		return activeStyle.Render("ready to start · R")
	case r.Run == state.RunFailed:
		return dirtyStyle.Render("failed")
	case r.Ahead > 0 && r.Run == state.RunRunning:
		return activeStyle.Render(fmt.Sprintf("working · %d commit(s) not on trunk", r.Ahead))
	case r.Ahead > 0:
		return activeStyle.Render(fmt.Sprintf("%d commit(s) to land", r.Ahead))
	case r.Run == state.RunRunning:
		return dimStyle.Render("working")
	case r.Run == state.RunMerged:
		return dimStyle.Render("landed")
	case r.Branch == r.TaskTrunk:
		return dimStyle.Render("trunk")
	default:
		return dimStyle.Render("nothing outstanding")
	}
}

// boardSummary is the line that answers "can I ship this yet".
func boardSummary(rows []dashRow) string {
	var waiting, outstanding, failed, ready int
	for _, r := range rows {
		switch {
		case r.Run == state.RunFailed:
			failed++
		case r.Bell:
			waiting++
		}
		if r.Branch != r.TaskTrunk {
			outstanding += r.Ahead
			if r.Run == "" && r.WaitsFor == "" {
				ready++
			}
		}
	}
	switch {
	case failed > 0:
		return fmt.Sprintf("%d strand(s) failed", failed)
	case waiting > 0:
		return fmt.Sprintf("%d strand(s) waiting on you", waiting)
	case outstanding > 0:
		return fmt.Sprintf("%d commit(s) still to reach the trunk", outstanding)
	case ready > 0:
		return fmt.Sprintf("%d strand(s) can start now: R", ready)
	default:
		return "everything written is on the trunk: the integrating strand can verify and open the PR"
	}
}

// boardSelected is the strand the board's cursor is on.
func boardSelected(rows []dashRow, cursor int) (dashRow, bool) {
	if cursor < 0 || cursor >= len(rows) {
		return dashRow{}, false
	}
	return rows[cursor], true
}
