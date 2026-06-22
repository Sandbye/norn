package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/work/internal/git"
)

type menuAction int

const (
	actionResume menuAction = iota
	actionNewTask
	actionNewReview
	actionClean
	actionQuit
	actionCd // cd into the worktree dir (no Claude launch)
)

type menuChoice struct {
	action   menuAction
	worktree *git.Worktree
}

type menuItem struct {
	label    string
	action   menuAction
	worktree *git.Worktree
}

type menuModel struct {
	worktrees []git.Worktree
	items     []menuItem
	cursor    int
	chosen    *menuChoice
	// cdMode flips the default action on a worktree row: enter = cd into its
	// dir, `l` = launch/resume Claude. Set by `work -d`. Off = normal: enter
	// launches/resumes.
	cdMode bool
}

func newMenuModel() menuModel {
	return menuModel{}
}

func (m menuModel) buildItems() []menuItem {
	var items []menuItem

	// Group existing worktrees
	var tasks, reviews []git.Worktree
	for i := range m.worktrees {
		switch m.worktrees[i].Kind {
		case "review":
			reviews = append(reviews, m.worktrees[i])
		default:
			tasks = append(tasks, m.worktrees[i])
		}
	}

	if len(tasks) > 0 {
		for i := range tasks {
			wt := tasks[i]
			label := fmt.Sprintf("%s  %s  %s",
				branchStyle.Render(wt.Branch),
				ageStyle.Render(git.Age(wt.LastCommit)),
				commitMsgStyle.Render(truncate(wt.CommitMsg, 40)),
			)
			items = append(items, menuItem{
				label:    label,
				action:   actionResume,
				worktree: &wt,
			})
		}
	}

	if len(reviews) > 0 {
		for i := range reviews {
			wt := reviews[i]
			label := fmt.Sprintf("%s  %s  %s",
				branchStyle.Render(wt.Branch),
				ageStyle.Render(git.Age(wt.LastCommit)),
				commitMsgStyle.Render(truncate(wt.CommitMsg, 40)),
			)
			items = append(items, menuItem{
				label:    label,
				action:   actionResume,
				worktree: &wt,
			})
		}
	}

	// Separator
	if len(m.worktrees) > 0 {
		items = append(items, menuItem{label: "---"})
	}

	items = append(items,
		menuItem{label: kindTaskStyle.Render("New task"), action: actionNewTask},
		menuItem{label: kindReviewStyle.Render("New review"), action: actionNewReview},
		menuItem{label: dimStyle.Render("Clean worktrees"), action: actionClean},
		menuItem{label: dimStyle.Render("Quit"), action: actionQuit},
	)

	return items
}

func (m menuModel) Update(msg tea.Msg) (menuModel, tea.Cmd) {
	m.items = m.buildItems()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.cursor--
			if m.cursor < 0 {
				m.cursor = len(m.items) - 1
			}
			// Skip separators
			if m.items[m.cursor].label == "---" {
				m.cursor--
				if m.cursor < 0 {
					m.cursor = len(m.items) - 1
				}
			}
		case "down", "j":
			m.cursor++
			if m.cursor >= len(m.items) {
				m.cursor = 0
			}
			if m.items[m.cursor].label == "---" {
				m.cursor++
				if m.cursor >= len(m.items) {
					m.cursor = 0
				}
			}
		case "enter":
			if m.cursor < len(m.items) {
				item := m.items[m.cursor]
				if item.label == "---" {
					break
				}
				action := item.action
				// In cd-mode, enter on a worktree row jumps to its dir instead
				// of launching Claude. Non-worktree rows keep their action.
				if m.cdMode && item.worktree != nil && action == actionResume {
					action = actionCd
				}
				m.chosen = &menuChoice{
					action:   action,
					worktree: item.worktree,
				}
			}
		case "l":
			// Launch/resume Claude for the row under the cursor. Primary use is
			// cd-mode (where enter cd's), but harmless in normal mode too.
			if m.cursor < len(m.items) {
				item := m.items[m.cursor]
				if item.worktree != nil {
					m.chosen = &menuChoice{action: actionResume, worktree: item.worktree}
				}
			}
		case "q", "esc":
			m.chosen = &menuChoice{action: actionQuit}
		}
	}

	return m, nil
}

func (m menuModel) View() string {
	m.items = m.buildItems()
	var b strings.Builder

	if len(m.worktrees) > 0 {
		title := "Active worktrees"
		if m.cdMode {
			title = "Jump to worktree"
		}
		b.WriteString(headerStyle.Render(title))
		b.WriteString("\n")
	}

	for i, item := range m.items {
		if item.label == "---" {
			b.WriteString(dimStyle.Render("  ─────────────────────"))
			b.WriteString("\n")
			continue
		}

		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
		}

		if i == m.cursor {
			b.WriteString(cursor + selectedStyle.Render(stripAnsi(item.label)))
		} else {
			b.WriteString(cursor + item.label)
		}
		b.WriteString("\n")
	}

	help := "j/k navigate  enter select  q quit"
	if m.cdMode {
		help = "j/k navigate  enter cd  l launch claude  q quit"
	}
	b.WriteString(helpStyle.Render(help))

	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// stripAnsi is a simple strip for re-rendering selected items.
// We just return the plain text approximation.
func stripAnsi(s string) string {
	// Simple approach: strip escape sequences
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
