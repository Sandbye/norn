package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/task"
)

// Padding-free styles for the panes: selectedStyle/subtitleStyle carry a
// PaddingLeft(1) that would push a width-fit line one column over and wrap it.
var taskSelStyle = lipgloss.NewStyle().Foreground(colorLavender).Bold(true)

// tasksModel is the Tasks tab: browse tracker tasks (GitHub issues / ClickUp)
// in a list + preview pane, open one in the browser, or spawn a worktree from
// it. Selection reuses the New-tab creation flow via createModel.withTask.
type tasksModel struct {
	provider task.Provider
	repoRoot string
	scope    string // repo / workspace label for the header

	tasks   []task.Task
	cursor  int
	loading bool
	err     error
	filter  filterState

	chosen     *task.Task // set once confirmed; App turns it into a worktree
	confirming bool       // enter opened the "create worktree?" y/n gate
	pending    *task.Task // the task awaiting confirmation

	width, height int
}

func newTasksModel(cfg config.Config, repoRoot, scope string) tasksModel {
	return tasksModel{
		provider: providerFor(cfg),
		repoRoot: repoRoot,
		scope:    scope,
	}
}

func (m tasksModel) Init() tea.Cmd {
	if m.provider == nil {
		return nil
	}
	return listTasksCmd(m.provider, m.repoRoot)
}

func (m tasksModel) visible() []task.Task { return filterTasks(m.tasks, m.filter.query) }

func (m tasksModel) Update(msg tea.Msg) (tasksModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case taskLoadedMsg:
		m.loading = false
		m.tasks = msg.tasks
		m.err = msg.err
		m.cursor = 0
		return m, nil
	case tea.KeyMsg:
		s := msg.String()
		// Confirmation gate: enter on a task asks before spawning a worktree,
		// since that launches the agent. y/enter proceeds, n/esc cancels.
		if m.confirming {
			switch s {
			case "y", "enter":
				m.chosen, m.confirming, m.pending = m.pending, false, nil
			case "n", "esc":
				m.confirming, m.pending = false, nil
			}
			return m, nil
		}
		// Filter input first: `/` activates, typing narrows. Arrows/enter fall
		// through so you can navigate + select while filtering.
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
				m.cursor = (m.cursor - 1 + len(vis)) % len(vis)
			}
		case "down", "j", "ctrl+n":
			if len(vis) > 0 {
				m.cursor = (m.cursor + 1) % len(vis)
			}
		case "o":
			if m.cursor < len(vis) && vis[m.cursor].URL != "" {
				openURL(vis[m.cursor].URL)
			}
		case "r":
			if m.provider != nil {
				m.loading = true
				return m, m.Init()
			}
		case "enter":
			if m.cursor < len(vis) {
				t := vis[m.cursor]
				m.pending = &t
				m.confirming = true
			}
		}
	}
	return m, nil
}

func (m tasksModel) View() string {
	var b strings.Builder

	scope := m.scope
	if scope == "" {
		scope = "this repo"
	}
	provider := "none"
	if m.provider != nil {
		provider = m.provider.Name()
	}
	b.WriteString(subtitleStyle.Render(scope + "   provider: " + provider))
	b.WriteString("\n\n")

	switch {
	case m.provider == nil:
		b.WriteString("   " + dimStyle.Render("No task provider set. Settings → Tasks → provider (github / clickup)."))
		b.WriteString("\n\n" + helpStyle.Render("esc back · ⇥ tab"))
		return b.String()
	case m.loading:
		b.WriteString("   " + dimStyle.Render("loading tasks…"))
		b.WriteString("\n\n" + helpStyle.Render("esc back"))
		return b.String()
	case m.err != nil:
		b.WriteString("   " + errorStyle.Render(m.err.Error()))
		b.WriteString("\n\n" + helpStyle.Render("r retry · esc back"))
		return b.String()
	}

	if m.filter.active || m.filter.query != "" {
		b.WriteString("  " + cursorStyle.Render("/") + m.filter.query)
		if m.filter.active {
			b.WriteString(cursorStyle.Render("▏"))
		}
		b.WriteString("\n")
	}

	vis := m.visible()
	if len(vis) == 0 {
		b.WriteString("   " + dimStyle.Render("no open tasks"))
		b.WriteString("\n\n" + helpStyle.Render("r refresh · esc back"))
		return b.String()
	}

	// Two panes: list left, focused task's body right. Sized to the panel
	// frame() renders into (min(frameWidth, width-8) minus padding), not the
	// full terminal, or the panes wrap.
	avail := frameWidth - 6
	if m.width > 0 {
		if inner := m.width - 8; inner < frameWidth {
			avail = inner - 6
		}
	}
	listW := 44
	if avail < 90 { // narrow: shrink the list, keep a usable preview
		listW = max(avail/2, 24)
	}
	previewW := max(avail-listW-2, 20)

	// Size the list to the fixed frame body (minus this view's own chrome:
	// scope line, blank, filter, count, help) so the panel never grows/jumps.
	rows := max(frameBodyRows(m.height)-6, 3)
	start, end := scrollWindow(m.cursor, len(vis), rows)

	// Right-align the id in a fixed field so ids of different digit-lengths
	// (#9 vs #3991) don't shove the badge/title column out of alignment.
	idW := 1
	for _, t := range vis {
		idW = max(idW, len(t.ID))
	}

	var list strings.Builder
	for i := start; i < end; i++ {
		t := vis[i]
		cursor := "  "
		titleStyle := branchStyle
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
			titleStyle = taskSelStyle
		}
		head := cursor + dimStyle.Render(fmt.Sprintf("#%*s ", idW, t.ID)) + kindBadge(t.Kind)
		title := titleStyle.Render(truncate(t.Title, max(listW-lipgloss.Width(head)-1, 6)))
		list.WriteString(head + title + "\n")
	}
	if len(vis) > rows {
		list.WriteString(dimStyle.Render(fmt.Sprintf("  %d/%d", m.cursor+1, len(vis))))
	}

	preview := m.previewPane(vis[m.cursor], previewW, rows)

	panes := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(listW).Render(list.String()),
		"  ",
		preview,
	)
	b.WriteString(panes)
	b.WriteString("\n\n")
	if m.confirming && m.pending != nil {
		b.WriteString(confirmStyle.Render(fmt.Sprintf("create worktree from #%s %s? (y/n)", m.pending.ID, truncate(m.pending.Title, 40))))
	} else {
		b.WriteString(helpStyle.Render("⏎ worktree · o open · / filter · r refresh · esc back"))
	}
	return b.String()
}

// previewPane renders the focused task's title, meta + body, wrapped to width
// and clipped to height so it never blows the panel.
func (m tasksModel) previewPane(t task.Task, width, rows int) string {
	var b strings.Builder
	b.WriteString(taskSelStyle.Render(truncate(t.Title, width)) + "\n")
	meta := dimStyle.Render("#" + t.ID)
	if len(t.Labels) > 0 {
		meta += dimStyle.Render("  " + strings.Join(t.Labels, ", "))
	}
	b.WriteString(meta + "\n\n")

	body := strings.TrimSpace(t.Description)
	if body == "" {
		b.WriteString(dimStyle.Render("(no description)"))
	} else {
		wrapped := lipgloss.NewStyle().Width(width).Render(body)
		lines := strings.Split(wrapped, "\n")
		bodyRows := max(rows-3, 1)
		if len(lines) > bodyRows {
			lines = lines[:bodyRows]
			lines[bodyRows-1] = truncate(lines[bodyRows-1], max(width-1, 4)) + "…"
		}
		b.WriteString(dimStyle.Render(strings.Join(lines, "\n")))
	}
	return lipgloss.NewStyle().Width(width).Render(b.String())
}

// kindBadge renders a short colored tag for a task's inferred kind.
func kindBadge(kind string) string {
	switch kind {
	case "feature":
		return lipgloss.NewStyle().Foreground(colorBlue).Render("[feat] ")
	case "fix":
		return lipgloss.NewStyle().Foreground(colorPeach).Render("[fix]  ")
	}
	return "       " // 7 blanks: keep titles aligned when there's no kind
}
