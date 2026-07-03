package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/work/internal/git"
)

type cleanModel struct {
	worktrees     []git.Worktree
	remoteChecked bool
	cursor        int
	selected      map[string]bool // keyed by worktree Path so filtering is safe
	confirming    bool
	done          bool
	cancelled     bool
	toRemove      []git.Worktree
	filter        filterState
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
		}
	case "esc", "q":
		// Only reached when not filtering — active filter consumes esc/q.
		m.cancelled = true
	}

	return m, nil
}

func (m cleanModel) View() string {
	sorted := m.visible()
	var b strings.Builder

	b.WriteString(headerStyle.Render("Clean worktrees"))
	b.WriteString("\n")

	if len(m.worktrees) == 0 {
		b.WriteString(subtitleStyle.Render("No worktrees found."))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("q back"))
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

	// Table header
	hdr := fmt.Sprintf("  %-3s %-40s %-6s %-8s %s",
		"",
		dimStyle.Render("Branch"),
		dimStyle.Render("Age"),
		dimStyle.Render("Remote"),
		dimStyle.Render("Last commit"),
	)
	b.WriteString(hdr)
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  "+strings.Repeat("─", 80)))
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

		// Kind indicator
		var kind string
		if wt.Kind == "review" {
			kind = kindReviewStyle.Render("R")
		} else {
			kind = kindTaskStyle.Render("T")
		}

		branch := branchStyle.Render(truncate(wt.Branch, 36))
		age := ageStyle.Render(fmt.Sprintf("%-5s", git.Age(wt.LastCommit)))

		var remote string
		switch {
		case !m.remoteChecked:
			remote = dimStyle.Render("...")
		case wt.RemoteGone:
			remote = goneStyle.Render("gone")
		case wt.Merged:
			remote = goneStyle.Render("merged")
		default:
			remote = activeStyle.Render("active")
		}
		remote = fmt.Sprintf("%-8s", remote)

		commit := commitMsgStyle.Render(truncate(wt.CommitMsg, 30))

		line := fmt.Sprintf("%s%s%s %-38s %s %s %s",
			cursor, check, kind, branch, age, remote, commit,
		)

		if i == m.cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")

	if m.confirming {
		count := len(m.selected)
		b.WriteString(confirmStyle.Render(fmt.Sprintf("Remove %d worktree(s)? (y/n)", count)))
	} else if m.filter.active {
		b.WriteString(helpStyle.Render("type to filter  ↑/↓ move  space select  esc clear"))
	} else {
		var parts []string
		parts = append(parts, "j/k navigate", "/ filter", "space select", "a all", "g select done")
		if len(m.selected) > 0 {
			parts = append(parts, "d delete")
		}
		parts = append(parts, "q back")
		b.WriteString(helpStyle.Render(strings.Join(parts, "  ")))
	}

	return b.String()
}
