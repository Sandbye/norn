package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/git"
)

type cleanModel struct {
	worktrees     []git.Worktree
	remoteChecked bool
	cursor        int
	selected      map[string]bool // keyed by worktree Path so filtering is safe
	confirming    bool
	dirtyCount    int // selected worktrees that will be SKIPPED (dirty)
	unmergedCount int // selected, will-remove worktrees whose branch will be kept (unmerged)
	done          bool
	removing      bool // removal fired, running off the UI thread
	toRemove      []git.Worktree
	filter        filterState

	// After removal: show a summary of what happened (removed / skipped / branch
	// kept) in-TUI instead of leaking git output. Any key sets dismissed.
	showResults bool
	results     []git.RemoveOutcome
	dismissed   bool
}

func newCleanModel() cleanModel {
	return cleanModel{
		selected: make(map[string]bool),
	}
}

// autoSelectDone pre-selects rows whose work is done — either remote-gone
// (PR merged + branch deleted) or merged into a base branch (catches
// squash-merges where the remote branch survives) — and moves the cursor to
// the first such row. Opening Clean becomes a one-keystroke flow (d → y).
func (m *cleanModel) autoSelectDone() {
	sorted := m.sorted()
	first := -1
	for i, wt := range sorted {
		if wt.RemoteGone || wt.Merged {
			m.selected[wt.Path] = true
			if first < 0 {
				first = i
			}
		}
	}
	if first >= 0 {
		m.cursor = first
	}
}

func (m cleanModel) sorted() []git.Worktree {
	wts := make([]git.Worktree, len(m.worktrees))
	copy(wts, m.worktrees)
	sort.Slice(wts, func(i, j int) bool {
		return wts[i].LastCommit.Before(wts[j].LastCommit)
	})
	return wts
}

// visible is the sorted list narrowed by the active filter query.
func (m cleanModel) visible() []git.Worktree {
	s := m.sorted()
	if m.filter.query == "" {
		return s
	}
	return rankWorktrees(s, m.filter.query)
}

func (m cleanModel) Update(msg tea.Msg) (cleanModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	s := key.String()

	if m.showResults {
		m.dismissed = true // any key returns to Threads
		return m, nil
	}
	if m.removing {
		return m, nil // removal in flight: ignore keys until it finishes
	}

	if m.confirming {
		switch s {
		case "y", "Y", "enter":
			m.done = true
			m.toRemove = nil
			for _, wt := range m.worktrees {
				if m.selected[wt.Path] {
					m.toRemove = append(m.toRemove, wt)
				}
			}
		case "n", "N", "esc", "q":
			m.confirming = false
		}
		return m, nil
	}

	// Filter input mode (printable/backspace/esc edit query; nav falls through).
	if m.filter.active {
		before := m.filter.query
		if m.filter.handleKey(s) {
			if m.filter.query != before {
				m.cursor = 0
			}
			return m, nil
		}
	} else if s == "/" {
		m.filter.handleKey(s)
		return m, nil
	}

	vis := m.visible()
	switch s {
	case "up", "k", "ctrl+p":
		if len(vis) > 0 {
			m.cursor--
			if m.cursor < 0 {
				m.cursor = len(vis) - 1
			}
		}
	case "down", "j", "ctrl+n":
		if len(vis) > 0 {
			m.cursor++
			if m.cursor >= len(vis) {
				m.cursor = 0
			}
		}
	case " ", "x":
		if m.cursor < len(vis) {
			p := vis[m.cursor].Path
			if m.selected[p] {
				delete(m.selected, p)
			} else {
				m.selected[p] = true
			}
		}
	case "a":
		// Toggle all *visible* rows.
		allSel := len(vis) > 0
		for _, wt := range vis {
			if !m.selected[wt.Path] {
				allSel = false
				break
			}
		}
		for _, wt := range vis {
			if allSel {
				delete(m.selected, wt.Path)
			} else {
				m.selected[wt.Path] = true
			}
		}
	case "g":
		// Select all done (gone-from-remote OR merged), regardless of filter.
		for _, wt := range m.worktrees {
			if wt.RemoteGone || wt.Merged {
				m.selected[wt.Path] = true
			}
		}
	case "d", "enter":
		if len(m.selected) > 0 {
			m.confirming = true
			// Predict the real outcome from cached state so the confirm isn't
			// blind: dirty rows get SKIPPED (git worktree remove refuses), and
			// unmerged clean rows are removed but keep their branch.
			m.dirtyCount, m.unmergedCount = 0, 0
			for _, wt := range m.worktrees {
				if !m.selected[wt.Path] {
					continue
				}
				if wt.Dirty {
					m.dirtyCount++ // skipped; branch + files left intact
					continue
				}
				if !wt.Merged && !wt.RemoteGone {
					m.unmergedCount++
				}
			}
		}
	}
	// esc/q aren't handled here: the App handles them globally (esc → Threads,
	// q → quit) whenever Clean isn't filtering or confirming.

	return m, nil
}

func (m cleanModel) View() string {
	if m.showResults {
		return m.resultsView()
	}

	sorted := m.visible()
	var b strings.Builder

	b.WriteString(headerStyle.Render("Clean worktrees"))
	b.WriteString("\n")

	if len(m.worktrees) == 0 {
		b.WriteString(subtitleStyle.Render("No worktrees to clean — nothing merged or gone."))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("esc back"))
		return b.String()
	}

	if !m.remoteChecked {
		b.WriteString(subtitleStyle.Render("Checking remote branches..."))
		b.WriteString("\n\n")
	}

	// Filter line.
	if m.filter.active || m.filter.query != "" {
		b.WriteString(cursorStyle.Render("/") + m.filter.query)
		if m.filter.active {
			b.WriteString(cursorStyle.Render("▏"))
		}
		if len(sorted) == 0 {
			b.WriteString(dimStyle.Render("  no matches"))
		}
		b.WriteString("\n")
	}

	// Column widths (visible). Gutter = cursor(2)+check(2)+kind(1)+space(1) = 6.
	const (
		branchW = 34
		ageW    = 5
		remoteW = 7
		commitW = 30
	)
	total := 6 + branchW + 1 + ageW + 1 + remoteW + 1 + commitW

	// Header (fit plain text, then dim).
	hdr := strings.Repeat(" ", 6) +
		dimStyle.Render(fitCell("BRANCH", branchW)) + " " +
		dimStyle.Render(fitCell("AGE", ageW)) + " " +
		dimStyle.Render(fitCell("REMOTE", remoteW)) + " " +
		dimStyle.Render(fitCell("LAST", commitW))
	b.WriteString(hdr)
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  " + strings.Repeat("─", total-2)))
	b.WriteString("\n")

	for i, wt := range sorted {
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
		}
		check := "  "
		if m.selected[wt.Path] {
			check = selectedStyle.Render("● ")
		}
		kind := kindTaskStyle.Render("T")
		if wt.Kind == "review" {
			kind = kindReviewStyle.Render("R")
		}

		var remoteText string
		remoteStyle := activeStyle
		switch {
		case !m.remoteChecked:
			remoteText, remoteStyle = "...", dimStyle
		case wt.RemoteGone:
			remoteText, remoteStyle = "gone", goneStyle
		case wt.Merged:
			remoteText, remoteStyle = "merged", goneStyle
		default:
			remoteText = "active"
		}

		// Fit plain text to each column, THEN style, so alignment holds.
		line := cursor + check + kind + " " +
			branchStyle.Render(fitCell(wt.Branch, branchW)) + " " +
			ageStyle.Render(fitCell(git.Age(wt.LastCommit), ageW)) + " " +
			remoteStyle.Render(fitCell(remoteText, remoteW)) + " " +
			commitMsgStyle.Render(fitCell(wt.CommitMsg, commitW))

		if wt.Dirty {
			line += " " + dirtyStyle.Render("dirty") // will be skipped on remove
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")

	switch {
	case m.removing:
		b.WriteString(confirmStyle.Render("Removing…"))
	case m.confirming:
		// Say exactly what will happen: dirty rows are skipped, so they don't
		// count toward "remove"; call out kept branches. No post-hoc surprise.
		remove := len(m.selected) - m.dirtyCount
		var notes []string
		if m.dirtyCount > 0 {
			notes = append(notes, fmt.Sprintf("%d dirty skipped", m.dirtyCount))
		}
		if m.unmergedCount > 0 {
			notes = append(notes, fmt.Sprintf("%d keep branch (unmerged)", m.unmergedCount))
		}
		msg := fmt.Sprintf("Remove %d worktree(s)", remove)
		if len(notes) > 0 {
			msg += ", " + strings.Join(notes, ", ")
		}
		b.WriteString(confirmStyle.Render(msg + "? (y/n)"))
	case m.filter.active:
		b.WriteString(helpStyle.Render("type to filter  ↑/↓ move  space select  esc clear"))
	default:
		parts := []string{"j/k navigate", "/ filter", "space select", "a all", "g select done"}
		if len(m.selected) > 0 {
			parts = append(parts, "d delete")
		}
		parts = append(parts, "esc back")
		b.WriteString(helpStyle.Render(strings.Join(parts, "  ")))
	}

	return b.String()
}

// resultsView summarizes what removal actually did, so skipped/kept items and
// the reasons are shown clearly in the TUI (no leaked git output).
func (m cleanModel) resultsView() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Clean — done"))
	b.WriteString("\n\n")

	removed := 0
	var skipped, kept []git.RemoveOutcome
	for _, r := range m.results {
		if r.Removed {
			removed++
		}
		if r.Skipped {
			skipped = append(skipped, r)
		}
		if r.BranchKept {
			kept = append(kept, r)
		}
	}

	b.WriteString(activeStyle.Render(fmt.Sprintf("Removed %d worktree(s).", removed)))
	b.WriteString("\n")

	if len(skipped) > 0 {
		b.WriteString("\n")
		b.WriteString(goneStyle.Render(fmt.Sprintf("Skipped %d (left intact):", len(skipped))))
		b.WriteString("\n")
		for _, r := range skipped {
			b.WriteString("  " + branchStyle.Render(r.Branch) + dimStyle.Render(" — "+r.Reason) + "\n")
		}
	}

	if len(kept) > 0 {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("Kept %d branch(es) (unmerged — delete manually if you're sure):", len(kept))))
		b.WriteString("\n")
		for _, r := range kept {
			b.WriteString("  " + branchStyle.Render(r.Branch) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("press any key to return"))
	return b.String()
}
