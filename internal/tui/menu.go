package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/git"
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
	filter filterState
}

func newMenuModel() menuModel {
	return menuModel{}
}

func (m menuModel) buildItems() []menuItem {
	var items []menuItem

	row := func(wt git.Worktree) menuItem {
		label := fmt.Sprintf("%s  %s  %s",
			branchStyle.Render(wt.Branch),
			ageStyle.Render(git.Age(wt.LastCommit)),
			commitMsgStyle.Render(truncate(wt.CommitMsg, 40)),
		)
		w := wt
		return menuItem{label: label, action: actionResume, worktree: &w}
	}

	// When a filter query is active, show only matching worktrees, ranked —
	// no kind grouping, no action rows (you're searching, not navigating menus).
	if m.filter.query != "" {
		for _, wt := range rankWorktrees(m.worktrees, m.filter.query) {
			items = append(items, row(wt))
		}
		return items
	}

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

	for _, wt := range tasks {
		items = append(items, row(wt))
	}
	for _, wt := range reviews {
		items = append(items, row(wt))
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

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	s := key.String()

	// Filter input mode: printable chars + backspace + esc edit the query;
	// navigation/selection fall through below. j/k type into the query here,
	// so navigation while filtering uses arrows or ctrl+n/ctrl+p.
	if m.filter.active {
		before := m.filter.query
		if m.filter.handleKey(s) {
			if m.filter.query != before {
				m.items = m.buildItems()
				m.cursor = 0
			}
			return m, nil
		}
	} else if s == "/" {
		m.filter.handleKey(s)
		return m, nil
	}

	switch s {
	case "up", "k", "ctrl+p":
		m.moveCursor(-1)
	case "down", "j", "ctrl+n":
		m.moveCursor(+1)
	case "enter":
		if m.cursor < len(m.items) {
			item := m.items[m.cursor]
			if item.label == "---" {
				break
			}
			action := item.action
			// In cd-mode, enter on a worktree row jumps to its dir instead of
			// launching Claude. Non-worktree rows keep their action.
			if m.cdMode && item.worktree != nil && action == actionResume {
				action = actionCd
			}
			m.chosen = &menuChoice{action: action, worktree: item.worktree}
		}
	case "l":
		// Launch/resume Claude for the row under the cursor (primary use is
		// cd-mode, where enter cd's). Skipped while filtering — 'l' types there.
		if !m.filter.active && m.cursor < len(m.items) {
			if wt := m.items[m.cursor].worktree; wt != nil {
				m.chosen = &menuChoice{action: actionResume, worktree: wt}
			}
		}
	case "q", "esc":
		// Only reached when not filtering — an active filter consumes esc/q.
		m.chosen = &menuChoice{action: actionQuit}
	}

	return m, nil
}

// moveCursor steps the selection by dir, wrapping and skipping separators.
func (m *menuModel) moveCursor(dir int) {
	n := len(m.items)
	if n == 0 {
		return
	}
	for i := 0; i < n; i++ {
		m.cursor += dir
		if m.cursor < 0 {
			m.cursor = n - 1
		} else if m.cursor >= n {
			m.cursor = 0
		}
		if m.items[m.cursor].label != "---" {
			return
		}
	}
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

	// Filter line.
	if m.filter.active || m.filter.query != "" {
		b.WriteString(cursorStyle.Render("/") + m.filter.query)
		if m.filter.active {
			b.WriteString(cursorStyle.Render("▏"))
		}
		if len(m.items) == 0 {
			b.WriteString(dimStyle.Render("  no matches"))
		}
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

	var help string
	switch {
	case m.filter.active:
		help = "type to filter  ↑/↓ or ctrl+n/p move  enter select  esc clear"
	case m.cdMode:
		help = "j/k navigate  / filter  enter cd  l launch claude  q quit"
	default:
		help = "j/k navigate  / filter  enter select  q quit"
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
