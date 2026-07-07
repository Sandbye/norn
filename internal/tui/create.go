package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/task"
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
	// focused gates text input: the New tab shows the form but doesn't capture
	// keys until enter, so Tab/1-4 still switch tabs. Enter focuses; esc unfocuses.
	focused bool

	// template is the prompt template this worktree renders; `t` cycles through
	// the available ones (set by the App, which has cfg).
	template  string
	templates []string

	// model is the per-session model for this worktree; `M` cycles the choices.
	// "" means the agent's own default. Empty `models` (non-claude) hides it.
	model  string
	models []string

	// Task picker (`T`): seed the hint from a real GitHub/ClickUp task.
	taskProvider task.Provider
	repoRoot     string
	pickingTask  bool
	taskLoading  bool
	tasks        []task.Task
	taskCursor   int
	taskErr      error
	taskFilter   filterState // `/` fuzzy over group (list/folder) + title
	selectedTask *task.Task  // the picked task, baked into the brief

	width, height int
}

// visibleTasks narrows + ranks tasks by the picker filter (matches the
// list/folder group and the title), so typing a list name scopes the list.
func (m createModel) visibleTasks() []task.Task {
	if m.taskFilter.query == "" {
		return m.tasks
	}
	type scored struct {
		t     task.Task
		score int
	}
	var hits []scored
	for _, t := range m.tasks {
		if s, ok := fuzzyScore(m.taskFilter.query, t.Group+" "+t.Title); ok {
			hits = append(hits, scored{t, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]task.Task, len(hits))
	for i, h := range hits {
		out[i] = h.t
	}
	return out
}

type taskLoadedMsg struct {
	tasks []task.Task
	err   error
}

func listTasksCmd(p task.Provider, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		tasks, err := p.List(context.Background(), repoRoot)
		return taskLoadedMsg{tasks: tasks, err: err}
	}
}

// cycleTemplate advances to the next available template.
func (m *createModel) cycleTemplate() {
	if len(m.templates) == 0 {
		return
	}
	i := 0
	for j, t := range m.templates {
		if t == m.template {
			i = j
			break
		}
	}
	m.template = m.templates[(i+1)%len(m.templates)]
}

// cycleModel advances to the next model choice.
func (m *createModel) cycleModel() {
	if len(m.models) == 0 {
		return
	}
	i := 0
	for j, mo := range m.models {
		if mo == m.model {
			i = j
			break
		}
	}
	m.model = m.models[(i+1)%len(m.models)]
}

// modelLabel renders the model for display ("default" when unset).
func modelLabel(model string) string {
	if model == "" {
		return "default"
	}
	return model
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
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case taskLoadedMsg:
		m.taskLoading = false
		m.tasks = msg.tasks
		m.taskErr = msg.err
		m.taskCursor = 0
		return m, nil
	case tea.KeyMsg:
		if m.pickingTask {
			return m.updateTaskPicker(msg.String())
		}
		switch m.step {
		case stepHint:
			if !m.focused {
				// Idle: the tab is navigable until the user commits to typing.
				switch msg.String() {
				case "enter":
					m.focused = true
				case "t":
					m.cycleTemplate()
				case "M":
					m.cycleModel()
				case "T":
					if m.taskProvider != nil {
						m.pickingTask = true
						m.taskLoading = true
						return m, listTasksCmd(m.taskProvider, m.repoRoot)
					}
				}
				return m, nil
			}
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
				m.focused = false // back to idle; esc again leaves the tab
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

func (m createModel) updateTaskPicker(s string) (createModel, tea.Cmd) {
	// Filter input: type to narrow by list/folder/title; arrows still navigate.
	if m.taskFilter.active {
		before := m.taskFilter.query
		if m.taskFilter.handleKey(s) {
			if m.taskFilter.query != before {
				m.taskCursor = 0
			}
			return m, nil
		}
	} else if s == "/" {
		m.taskFilter.handleKey(s)
		return m, nil
	}

	vis := m.visibleTasks()
	switch s {
	case "esc":
		m.pickingTask = false
	case "up", "k", "ctrl+p":
		if len(vis) > 0 {
			m.taskCursor--
			if m.taskCursor < 0 {
				m.taskCursor = len(vis) - 1
			}
		}
	case "down", "j", "ctrl+n":
		if len(vis) > 0 {
			m.taskCursor++
			if m.taskCursor >= len(vis) {
				m.taskCursor = 0
			}
		}
	case "enter":
		if m.taskCursor < len(vis) {
			t := vis[m.taskCursor]
			// Seed the hint with #id + title so MakeBranch/EnrichBranchName
			// produce <prefix>/#<id>/<slug> and the id flows into the session.
			m.hint = fmt.Sprintf("#%s %s", t.ID, t.Title)
			m.selectedTask = &t // baked into the worktree brief
			m.pickingTask = false
			if len(m.baseBranches) == 1 {
				m.baseBranch = m.baseBranches[0]
				m.confirmed = true
			} else {
				m.step = stepBase
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

	if m.pickingTask {
		b.WriteString(subtitleStyle.Render("Pick a task:"))
		b.WriteString("\n")
		if m.taskFilter.active || m.taskFilter.query != "" {
			b.WriteString("  " + cursorStyle.Render("/") + m.taskFilter.query)
			if m.taskFilter.active {
				b.WriteString(cursorStyle.Render("▏"))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		vis := m.visibleTasks()
		switch {
		case m.taskLoading:
			b.WriteString("   " + dimStyle.Render("loading tasks…"))
		case m.taskErr != nil:
			b.WriteString("   " + errorStyle.Render(m.taskErr.Error()))
		case len(vis) == 0:
			b.WriteString("   " + dimStyle.Render("no tasks match"))
		default:
			// Scroll the list inside the fixed box; fit title to width, show the
			// list/folder group so scope is visible.
			rows := m.height - 14
			if rows < 3 {
				rows = 3
			}
			titleW := m.width - 40
			if titleW < 18 {
				titleW = 18
			}
			start, end := scrollWindow(m.taskCursor, len(vis), rows)
			for i := start; i < end; i++ {
				t := vis[i]
				cursor := "  "
				label := branchStyle.Render(truncate(t.Title, titleW))
				if i == m.taskCursor {
					cursor = cursorStyle.Render("> ")
					label = selectedStyle.Render(truncate(t.Title, titleW))
				}
				row := "  " + cursor + dimStyle.Render("#"+t.ID+" ") + label
				if t.Group != "" {
					row += dimStyle.Render("  " + t.Group)
				}
				b.WriteString(row + "\n")
			}
			if len(vis) > rows {
				b.WriteString(dimStyle.Render(fmt.Sprintf("  %d/%d", m.taskCursor+1, len(vis))))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("j/k move · / filter · ⏎ select · esc back"))
		return b.String()
	}

	switch m.step {
	case stepHint:
		if !m.focused {
			b.WriteString(subtitleStyle.Render("Describe the task, then it becomes a worktree."))
			b.WriteString("\n\n")
			b.WriteString("   " + dimStyle.Render(m.hintInput))
			if m.hintInput == "" {
				b.WriteString(dimStyle.Render("press ⏎ to start typing"))
			}
			b.WriteString("\n\n")
			if m.template != "" {
				b.WriteString("   " + dimStyle.Render("template: ") + branchStyle.Render(m.template) + "\n")
			}
			if len(m.models) > 0 {
				b.WriteString("   " + dimStyle.Render("model: ") + branchStyle.Render(modelLabel(m.model)) + "\n")
			}
			parts := []string{"⏎ type"}
			if m.taskProvider != nil {
				parts = append(parts, "T pick task")
			}
			parts = append(parts, "t template")
			if len(m.models) > 0 {
				parts = append(parts, "M model")
			}
			parts = append(parts, "Tab/1-4 switch tab")
			b.WriteString(helpStyle.Render(strings.Join(parts, " · ")))
			break
		}
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
