package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type createStep int

const (
	stepHint createStep = iota
	stepBase
)

type createModel struct {
	kind         string // "task" or "review"
	baseBranches []string
	step         createStep
	hint         string
	hintInput    string
	baseBranch   string
	baseCursor   int
	confirmed    bool
	cancelled    bool
}

func newCreateModel(baseBranches []string) createModel {
	if len(baseBranches) == 0 {
		baseBranches = []string{"master"}
	}
	return createModel{
		baseBranches: baseBranches,
		step:         stepHint,
	}
}

func (m createModel) Update(msg tea.Msg) (createModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.step {
		case stepHint:
			switch msg.String() {
			case "enter":
				m.hint = m.hintInput
				if len(m.baseBranches) == 1 {
					m.baseBranch = m.baseBranches[0]
					m.confirmed = true
				} else {
					m.step = stepBase
				}
			case "esc":
				m.cancelled = true
			case "backspace":
				if len(m.hintInput) > 0 {
					m.hintInput = m.hintInput[:len(m.hintInput)-1]
				}
			case "space":
				m.hintInput += " "
			default:
				s := msg.String()
				if len(s) > 0 && !strings.HasPrefix(s, "ctrl+") && !strings.HasPrefix(s, "alt+") {
					m.hintInput += s
				}
			}

		case stepBase:
			switch msg.String() {
			case "up", "k":
				m.baseCursor--
				if m.baseCursor < 0 {
					m.baseCursor = len(m.baseBranches) - 1
				}
			case "down", "j":
				m.baseCursor++
				if m.baseCursor >= len(m.baseBranches) {
					m.baseCursor = 0
				}
			case "enter":
				m.baseBranch = m.baseBranches[m.baseCursor]
				m.confirmed = true
			case "esc":
				m.step = stepHint
			}
		}
	}

	return m, nil
}

func (m createModel) View() string {
	var b strings.Builder

	kindLabel := kindTaskStyle.Render("New task")
	if m.kind == "review" {
		kindLabel = kindReviewStyle.Render("New review")
	}
	b.WriteString(headerStyle.Render(kindLabel))
	b.WriteString("\n")

	switch m.step {
	case stepHint:
		b.WriteString(subtitleStyle.Render("Hint (or enter to skip):"))
		b.WriteString("\n\n")
		cursor := cursorStyle.Render("▎")
		b.WriteString("   " + m.hintInput + cursor)
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("enter confirm  esc back"))

	case stepBase:
		b.WriteString(subtitleStyle.Render("Base branch:"))
		b.WriteString("\n\n")
		for i, br := range m.baseBranches {
			cursor := "  "
			if i == m.baseCursor {
				cursor = cursorStyle.Render("> ")
			}
			label := branchStyle.Render(br)
			if i == m.baseCursor {
				label = selectedStyle.Render(br)
			}
			b.WriteString("  " + cursor + label + "\n")
		}
		b.WriteString(helpStyle.Render("j/k navigate  enter select  esc back"))
	}

	return b.String()
}
