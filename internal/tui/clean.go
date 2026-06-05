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
	selected      map[int]bool
	confirming    bool
	done          bool
	cancelled     bool
	toRemove      []git.Worktree
}

func newCleanModel() cleanModel {
	return cleanModel{
		selected: make(map[int]bool),
	}
}

// autoSelectGone pre-selects rows whose remote branch is gone (PR merged or
// branch deleted) and moves the cursor to the first such row. Called once
// when the remote check completes so opening Clean is a one-keystroke flow
// (d → y) instead of "press g, then d, then y".
func (m *cleanModel) autoSelectGone() {
	sorted := m.sorted()
	firstGone := -1
	for i, wt := range sorted {
		if wt.RemoteGone {
			m.selected[i] = true
			if firstGone < 0 {
				firstGone = i
			}
		}
	}
	if firstGone >= 0 {
		m.cursor = firstGone
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

func (m cleanModel) Update(msg tea.Msg) (cleanModel, tea.Cmd) {
	sorted := m.sorted()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirming {
			switch msg.String() {
			case "y", "Y", "enter":
				m.done = true
				m.toRemove = nil
				for idx := range m.selected {
					if idx < len(sorted) {
						m.toRemove = append(m.toRemove, sorted[idx])
					}
				}
			case "n", "N", "esc", "q":
				m.confirming = false
			}
			return m, nil
		}

		switch msg.String() {
		case "up", "k":
			m.cursor--
			if m.cursor < 0 {
				m.cursor = len(sorted) - 1
			}
		case "down", "j":
			m.cursor++
			if m.cursor >= len(sorted) {
				m.cursor = 0
			}
		case " ", "x":
			if m.selected[m.cursor] {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = true
			}
		case "a":
			// Select all
			if len(m.selected) == len(sorted) {
				m.selected = make(map[int]bool)
			} else {
				for i := range sorted {
					m.selected[i] = true
				}
			}
		case "g":
			// Select all gone from remote
			for i, wt := range sorted {
				if wt.RemoteGone {
					m.selected[i] = true
				}
			}
		case "d", "enter":
			if len(m.selected) > 0 {
				m.confirming = true
			}
		case "esc", "q":
			m.cancelled = true
		}
	}

	return m, nil
}

func (m cleanModel) View() string {
	sorted := m.sorted()
	var b strings.Builder

	b.WriteString(headerStyle.Render("Clean worktrees"))
	b.WriteString("\n")

	if len(sorted) == 0 {
		b.WriteString(subtitleStyle.Render("No worktrees found."))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("q back"))
		return b.String()
	}

	if !m.remoteChecked {
		b.WriteString(subtitleStyle.Render("Checking remote branches..."))
		b.WriteString("\n\n")
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
		if m.selected[i] {
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
		if !m.remoteChecked {
			remote = dimStyle.Render("...")
		} else if wt.RemoteGone {
			remote = goneStyle.Render("gone")
		} else {
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
	} else {
		var parts []string
		parts = append(parts, "j/k navigate", "space select", "a all", "g select gone")
		if len(m.selected) > 0 {
			parts = append(parts, "d delete")
		}
		parts = append(parts, "q back")
		b.WriteString(helpStyle.Render(strings.Join(parts, "  ")))
	}

	return b.String()
}
