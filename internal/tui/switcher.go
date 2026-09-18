package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/state"
)

// Jumping between strands without going back to the rail and finding the row.
// A centered picker with a query, the way telescope or harpoon works: type a
// few letters of the role, the branch or the task, press enter, you are in it.
//
// It exists because the rail is ordered by what needs you, which is the right
// order for deciding where to go and the wrong one for going somewhere you
// already have in mind.

// switcherRows is how many matches the box shows at once.
const switcherRows = 12

type switcherState struct {
	active bool
	query  string
	cursor int
	// matched is the last rendered result set, so enter acts on what you see
	// rather than recomputing and picking something else.
	matched []dashRow
}

// switcherMatches filters rows to the strands worth jumping to, in rail order.
// Everything is matched against one string per row, since "logic", "880" and
// "tsdown" are all reasonable things to type for the same strand.
func switcherMatches(rows []dashRow, query string) []dashRow {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []dashRow
	for _, r := range rows {
		if r.Role == "" || r.TaskID == "" {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(switcherHaystack(r)), q) {
			out = append(out, r)
		}
	}
	return out
}

func switcherHaystack(r dashRow) string {
	return r.Role + " " + r.Branch + " " + r.TaskGoal + " " + string(r.Run)
}

// handleKey edits the query and moves the cursor, reporting whether the key was
// consumed. Enter and esc are left to the caller, which needs the model.
func (s *switcherState) handleKey(key string) bool {
	switch key {
	case "backspace", "ctrl+h":
		if r := []rune(s.query); len(r) > 0 {
			s.query = string(r[:len(r)-1])
			s.cursor = 0
		}
		return true
	case "ctrl+u":
		s.query, s.cursor = "", 0
		return true
	case "down", "ctrl+n":
		s.cursor++
		return true
	case "up", "ctrl+p":
		if s.cursor > 0 {
			s.cursor--
		}
		return true
	}
	if len(key) == 1 && key[0] >= 0x20 && key[0] < 0x7f {
		s.query += key
		s.cursor = 0
		return true
	}
	return false
}

// selected is the row enter acts on, or false when nothing matched.
func (s switcherState) selected() (dashRow, bool) {
	if len(s.matched) == 0 {
		return dashRow{}, false
	}
	i := s.cursor
	if i >= len(s.matched) {
		i = len(s.matched) - 1
	}
	return s.matched[i], true
}

// renderSwitcher draws the picker over the current view.
func (d Dashboard) renderSwitcher() string {
	width := min(max(d.width-12, 40), 88)
	var b strings.Builder
	b.WriteString(titleStyle.Render("go to strand") + "\n\n")
	b.WriteString(cursorStyle.Render(" ▸ ") + d.switcher.query + cursorStyle.Render("▏") + "\n\n")

	if len(d.switcher.matched) == 0 {
		b.WriteString(dimStyle.Render("  no strand matches"))
		return popover(b.String(), width, d.width, d.height)
	}
	for i, r := range d.switcher.matched {
		if i >= switcherRows {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  +%d more", len(d.switcher.matched)-switcherRows)))
			break
		}
		line := fmt.Sprintf("%-12s %s", r.Role+strings.TrimSpace(runMark(r)), truncate(taskLabel(r), width-18))
		if i == d.switcher.cursor || (d.switcher.cursor >= len(d.switcher.matched) && i == len(d.switcher.matched)-1) {
			b.WriteString(cursorStyle.Render("▸ " + line))
		} else {
			b.WriteString(dimStyle.Render("  " + line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + dimStyle.Render("⏎ enter it · ↑/↓ move · esc close"))
	return popover(b.String(), width, d.width, d.height)
}

// openSwitcher arms the picker with every strand norn knows about.
func (d *Dashboard) openSwitcher() {
	d.switcher = switcherState{active: true}
	d.switcher.matched = switcherMatches(d.rows, "")
}

// enterSelected leaves the picker and attaches to the chosen strand. A strand
// with no session cannot be entered, and says so rather than failing silently.
func (d Dashboard) enterSelected() (tea.Model, tea.Cmd) {
	row, ok := d.switcher.selected()
	d.switcher = switcherState{}
	if !ok {
		return d, nil
	}
	if row.Run == state.RunMerged || row.Run == "" {
		// Nothing to attach to for a strand that was never spawned or is
		// already landed; the log is what is left of it.
		d.notice = row.Role + " has no live session"
		return d, nil
	}
	cols, rows := d.paneSize()
	d.pane.close()
	return d, tea.Batch(openPaneCmd(row.TaskID, row.Role, row.Branch, cols, rows), paneTick())
}
