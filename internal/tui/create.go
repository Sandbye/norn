package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/sandbye/norn/internal/task"
)

// nornHuhTheme recolors huh's base theme to the active norn palette so the New
// tab form matches the rest of the TUI instead of huh's default purple.
func nornHuhTheme() *huh.Theme {
	t := huh.ThemeBase()
	f := &t.Focused
	f.Base = f.Base.BorderForeground(colorLavender)
	f.Title = f.Title.Foreground(colorLavender).Bold(true)
	f.SelectSelector = f.SelectSelector.Foreground(colorLavender)
	f.SelectedOption = f.SelectedOption.Foreground(colorBlue)
	f.Option = f.Option.Foreground(colorText)
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(colorLavender)
	f.TextInput.Prompt = f.TextInput.Prompt.Foreground(colorLavender)
	f.TextInput.Placeholder = f.TextInput.Placeholder.Foreground(colorOverlay)
	f.TextInput.Text = f.TextInput.Text.Foreground(colorText)
	b := &t.Blurred
	b.Title = b.Title.Foreground(colorSubtext)
	b.Base = b.Base.BorderForeground(colorSurface)
	return t
}

type createModel struct {
	kind         string // "task" or "review"
	baseBranches []string
	templates    []string
	template     string
	models       []string
	model        string

	// The New-tab form (hint + base + template + model) is a huh form. It's
	// rebuilt when seeding from a task (the hint field is dropped then).
	form   *huh.Form
	seeded bool // hint came from a task → form omits the hint field
	// focused gates the form: the tab shows the form but doesn't capture keys
	// until enter, so Tab/1-5 still switch tabs. Enter focuses; the form's own
	// esc aborts back to idle.
	focused bool

	hint       string
	baseBranch string
	confirmed  bool
	cancelled  bool

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

// buildForm assembles the huh form from the current choices. Fields with a
// single option are omitted (base/template) or hidden (model when non-claude);
// when seeded from a task the hint field is dropped since the task defines it.
func (m *createModel) buildForm() *huh.Form {
	var fields []huh.Field
	if !m.seeded {
		fields = append(fields, huh.NewInput().Key("hint").
			Title("Task").Placeholder("what are you working on?"))
	}
	if len(m.baseBranches) > 1 {
		fields = append(fields, huh.NewSelect[string]().Key("base").
			Title("Base branch").Options(huh.NewOptions(m.baseBranches...)...))
	}
	if len(m.templates) > 1 {
		fields = append(fields, huh.NewSelect[string]().Key("template").
			Title("Template").Options(huh.NewOptions(m.templates...)...))
	}
	if len(m.models) > 0 {
		fields = append(fields, huh.NewSelect[string]().Key("model").
			Title("Model").Options(modelOptions(m.models)...))
	}
	return huh.NewForm(huh.NewGroup(fields...)).
		WithShowHelp(true).WithWidth(m.formWidth()).WithTheme(nornHuhTheme())
}

func (m createModel) formWidth() int {
	w := 60
	if m.width > 0 && m.width-12 < w {
		w = m.width - 12
	}
	if w < 24 {
		w = 24
	}
	return w
}

// readForm pulls the submitted values into the model fields. Reads a field only
// when its select was present, so single-option defaults survive.
func (m *createModel) readForm() {
	if !m.seeded {
		m.hint = m.form.GetString("hint")
	}
	if v := m.form.GetString("base"); v != "" {
		m.baseBranch = v
	}
	if v := m.form.GetString("template"); v != "" {
		m.template = v
	}
	if len(m.models) > 0 {
		m.model = m.form.GetString("model") // "" (default) is a valid choice
	}
}

func modelOptions(models []string) []huh.Option[string] {
	opts := make([]huh.Option[string], len(models))
	for i, mo := range models {
		opts[i] = huh.NewOption(modelLabel(mo), mo)
	}
	return opts
}

// moveFront reorders xs so x is first (so a huh Select starts on the resolved
// default). No-op if x isn't present.
func moveFront(xs []string, x string) []string {
	found := false
	for _, v := range xs {
		if v == x {
			found = true
			break
		}
	}
	if !found {
		return xs
	}
	out := make([]string, 0, len(xs))
	out = append(out, x)
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// visibleTasks narrows + ranks tasks by the picker filter.
func (m createModel) visibleTasks() []task.Task {
	return filterTasks(m.tasks, m.taskFilter.query)
}

// filterTasks fuzzy-ranks tasks by group + title against query, best first.
// Empty query returns the tasks unchanged. Shared by the New-tab picker and
// the Tasks tab so both filter identically.
func filterTasks(tasks []task.Task, query string) []task.Task {
	if query == "" {
		return tasks
	}
	type scored struct {
		t     task.Task
		score int
	}
	var hits []scored
	for _, t := range tasks {
		if s, ok := fuzzyScore(query, t.Group+" "+t.Title); ok {
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

// withTask seeds the create form from a task (picked here or on the Tasks tab):
// the task defines the hint, so the form drops the hint field. Single base →
// confirm immediately; otherwise open the form focused for base/template/model.
func (m createModel) withTask(t task.Task) createModel {
	tt := t
	m.selectedTask = &tt
	m.hint = fmt.Sprintf("#%s %s", t.ID, t.Title)
	m.seeded = true
	if len(m.baseBranches) == 1 {
		m.baseBranch = m.baseBranches[0]
		m.confirmed = true
		return m
	}
	m.form = m.buildForm()
	m.focused = true
	return m
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
		baseBranch:   baseBranches[0], // default when there's no base select
	}
}

func (m createModel) Update(msg tea.Msg) (createModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.form != nil {
			m.form = m.form.WithWidth(m.formWidth())
		}
		return m, nil
	case taskLoadedMsg:
		m.taskLoading = false
		m.tasks = msg.tasks
		m.taskErr = msg.err
		m.taskCursor = 0
		return m, nil
	}

	if m.pickingTask {
		if k, ok := msg.(tea.KeyMsg); ok {
			return m.updateTaskPicker(k.String())
		}
		return m, nil
	}

	// Idle: navigable until the user commits. enter focuses the form; T opens
	// the task picker. Tab/1-5 fall through to the App (capturing() is false).
	if !m.focused {
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "enter":
				m.focused = true
				return m, m.form.Init()
			case "T":
				if m.taskProvider != nil {
					m.pickingTask = true
					m.taskLoading = true
					return m, listTasksCmd(m.taskProvider, m.repoRoot)
				}
			}
		}
		return m, nil
	}

	// Focused: esc backs out to idle (huh only aborts on ctrl+c, so esc would
	// otherwise do nothing and trap you in the form).
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		m.focused = false
		m.seeded = false
		m.form = m.buildForm()
		return m, nil
	}

	// Focused: the huh form owns input.
	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		m.readForm()
		m.confirmed = true
	case huh.StateAborted:
		// esc backs out to idle; rebuild so re-entry starts fresh.
		m.focused = false
		m.seeded = false
		m.form = m.buildForm()
	}
	return m, cmd
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
	case "o":
		if m.taskCursor < len(vis) && vis[m.taskCursor].URL != "" {
			openURL(vis[m.taskCursor].URL)
		}
	case "enter":
		if m.taskCursor < len(vis) {
			m.pickingTask = false
			m = m.withTask(vis[m.taskCursor])
		}
	}
	return m, nil
}

func (m createModel) View() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(kindTaskStyle.Render("New task")))
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
		b.WriteString(helpStyle.Render("j/k move · / filter · ⏎ select · o open · esc back"))
		return b.String()
	}

	if !m.focused {
		b.WriteString(subtitleStyle.Render("Spin up a worktree for a task."))
		b.WriteString("\n\n")
		parts := []string{"⏎ start"}
		if m.taskProvider != nil {
			parts = append(parts, "T pick task")
		}
		parts = append(parts, "Tab/1-5 switch tab")
		b.WriteString(helpStyle.Render(strings.Join(parts, " · ")))
		return b.String()
	}

	b.WriteString("\n")
	b.WriteString(m.form.View())
	return b.String()
}
