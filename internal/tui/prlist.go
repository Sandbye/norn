package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PRListItem is one row in the PR picker.
type PRListItem struct {
	Number    int
	Title     string
	Author    string
	BaseRef   string
	HeadRef   string
	IsDraft   bool
	UpdatedAt time.Time
}

// PRList is a Bubble Tea picker that returns a selected PR number, or 0 if
// cancelled. Caller (main.go) reads PRList.Selected() after the program exits.
type PRList struct {
	prs      []PRListItem
	cursor   int
	selected int // 0 = no selection
	width    int
	height   int
	quit     bool
}

func NewPRList(prs []PRListItem) PRList {
	return PRList{prs: prs}
}

// Selected returns the chosen PR number (0 if cancelled).
func (m PRList) Selected() int { return m.selected }

func (m PRList) Init() tea.Cmd { return nil }

func (m PRList) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quit = true
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.prs)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.prs) - 1
		case "enter":
			if m.cursor < len(m.prs) {
				m.selected = m.prs[m.cursor].Number
				m.quit = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m PRList) View() string {
	if m.quit {
		return ""
	}

	var b strings.Builder
	hdr := titleStyle.Render("work") + " " + subtitleStyle.Render("diff") +
		dimStyle.Render(fmt.Sprintf("   pick a PR · %d open", len(m.prs)))
	b.WriteString("\n" + hdr + "\n\n")

	if len(m.prs) == 0 {
		b.WriteString(dimStyle.Render("  no open PRs found") + "\n\n" + helpStyle.Render("q quit") + "\n")
		return b.String()
	}

	// Columns: cursor · #num · title · author · base←head · age · draft
	const (
		numW    = 7
		authorW = 12
		refsW   = 32
		ageW    = 6
		draftW  = 6
	)
	titleW := m.width - numW - authorW - refsW - ageW - draftW - 5
	if titleW < 20 {
		titleW = 20
	}

	for i, pr := range m.prs {
		marker := "  "
		if i == m.cursor {
			marker = cursorStyle.Render("› ")
		}
		num := fmt.Sprintf("#%-5d", pr.Number)
		title := truncRight(pr.Title, titleW)
		title = padPlain(title, titleW)
		author := truncRight(pr.Author, authorW-1)
		author = padPlain(author, authorW)
		refs := truncRight(pr.BaseRef+" ← "+pr.HeadRef, refsW-1)
		refs = padPlain(refs, refsW)
		age := padPlain(shortAge(pr.UpdatedAt), ageW)
		draft := "      "
		if pr.IsDraft {
			draft = "draft "
		}

		var row string
		if i == m.cursor {
			row = marker +
				lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Render(num) + " " +
				selectedStyle.Render(title) + " " +
				lipgloss.NewStyle().Foreground(colorMauve).Render(author) + " " +
				dimStyle.Render(refs) + " " +
				ageStyle.Render(age) + " " +
				dimStyle.Render(draft)
		} else {
			row = marker +
				lipgloss.NewStyle().Foreground(colorBlue).Render(num) + " " +
				lipgloss.NewStyle().Foreground(colorText).Render(title) + " " +
				lipgloss.NewStyle().Foreground(colorMauve).Render(author) + " " +
				dimStyle.Render(refs) + " " +
				dimStyle.Render(age) + " " +
				dimStyle.Render(draft)
		}
		b.WriteString(row + "\n")
	}

	b.WriteString("\n" + helpStyle.Render("j/k move · enter open · q quit") + "\n")
	return b.String()
}

// truncRight shortens from the right, appending "…" if cut.
func truncRight(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 2 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
