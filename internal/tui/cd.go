package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/git"
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
		b.WriteString(dimStyle.Render("  no worktrees yet, create one with `norn \"hint\"`"))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("esc back"))
		return b.String()
	}

	strands := labelStrands()
	lastTask := ""
	for i, wt := range m.worktrees {
		// A strand jumps by its role under one task header: three entries whose
		// branches differ in the last segment are indistinguishable at a glance,
		// and the role is what you are choosing between.
		name := wt.Branch
		if st, ok := strands[wt.Path]; ok {
			if st.taskID != lastTask {
				label := st.goal
				if label == "" {
					label = st.taskID
				}
				b.WriteString("  " + taskHeaderStyle.Render("◈ "+label) + "\n")
			}
			lastTask = st.taskID
			name = "  " + st.role
			if st.integrate {
				name += " (trunk)"
			}
		} else {
			lastTask = ""
		}

		cursor := "  "
		label := branchStyle.Render(name)
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
			label = selectedStyle.Render(name)
		}
		age := ageStyle.Render(git.Age(wt.LastCommit))
		b.WriteString("  " + cursor + label + " " + age + "\n")
	}

	b.WriteString(helpStyle.Render("j/k navigate  enter select  esc quit"))
	return b.String()
}
