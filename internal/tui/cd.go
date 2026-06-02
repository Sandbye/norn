package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/work/internal/git"
)

type cdModel struct {
	worktrees []git.Worktree
	cursor    int
	chosen    *git.Worktree
	cancelled bool
}

func newCdModel() cdModel {
	return cdModel{}
}

func (m cdModel) Update(msg tea.Msg) (cdModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.cursor--
			if m.cursor < 0 {
				m.cursor = len(m.worktrees) - 1
			}
		case "down", "j":
			m.cursor++
			if m.cursor >= len(m.worktrees) {
				m.cursor = 0
			}
		case "enter":
			if len(m.worktrees) > 0 {
				wt := m.worktrees[m.cursor]
				m.chosen = &wt
			}
		case "esc", "q":
			m.cancelled = true
		}
	}
	return m, nil
}

func (m cdModel) View() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Jump to worktree"))
	b.WriteString("\n\n")

	if len(m.worktrees) == 0 {
		b.WriteString(dimStyle.Render("  No worktrees found."))
		return b.String()
	}

	for i, wt := range m.worktrees {
		cursor := "  "
		label := branchStyle.Render(wt.Branch)
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
			label = selectedStyle.Render(wt.Branch)
		}
		age := ageStyle.Render(git.Age(wt.LastCommit))
		b.WriteString("  " + cursor + label + " " + age + "\n")
	}

	b.WriteString(helpStyle.Render("j/k navigate  enter select  esc quit"))
	return b.String()
}

