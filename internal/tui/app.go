package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/prompt"
	"github.com/sandbye/norn/internal/task"
)

type View int

const (
	ViewThreads  View = iota // dashboard (home tab)
	ViewTasks                // tracker tasks browser (tab: Tasks)
	ViewCreate               // new worktree (tab: New)
	ViewClean                // prune (tab)
	ViewSettings             // config (tab)
	ViewCd                   // worktree picker for `--cd` (not a tab)
)

// appTabs is the top-level tab order (left to right). ViewCd is intentionally
// absent — it's a CLI-only picker, not part of the tabbed hub.
var appTabs = []View{ViewThreads, ViewTasks, ViewCreate, ViewClean, ViewSettings}

func tabLabel(v View) string {
	switch v {
	case ViewThreads:
		return "Threads"
	case ViewTasks:
		return "Tasks"
	case ViewCreate:
		return "New"
	case ViewClean:
		return "Clean"
	case ViewSettings:
		return "Settings"
	}
	return ""
}

func tabIndex(v View) int {
	for i, t := range appTabs {
		if t == v {
			return i
		}
	}
	return 0
}

// ResultAction describes what main.go should do after the TUI exits.
type ResultAction int

const (
	ResultNone     ResultAction = iota
	ResultLaunch                // launch new Claude session
	ResultResume                // resume existing Claude session
	ResultCd                    // cd into worktree shell
	ResultSettings              // open the settings view
)

// Result is returned after the TUI exits, telling main.go what to do.
type Result struct {
	Action ResultAction
	Path   string // worktree path
	Model  string // per-session model override for ResultLaunch (empty → config default)
}

type App struct {
	cfg      config.Config
	repoRoot string
	mainDir  string // primary checkout, for "go to main" + post-delete cd
	scope    string
	current  View

	dashboard Dashboard
	tasks     tasksModel
	clean     cleanModel
	create    createModel
	cd        cdModel
	settings  settingsModel

	width    int
	height   int
	err      error
	quit     bool
	showHelp bool // global `?` key overlay
	result   Result
}

func (a App) Result() Result { return a.result }

type worktreesLoadedMsg struct {
	worktrees []git.Worktree
}

type remoteCheckedMsg struct {
	worktrees []git.Worktree
}

type worktreeCreatedMsg struct {
	path  string
	model string
}

type errMsg struct{ err error }

// NewApp builds the unified tabbed program. scope is the repo basename the
// Threads/dashboard tab is scoped to ("" = all repos). initialView is the tab
// (or the ViewCd picker) to open on.
func NewApp(cfg config.Config, repoRoot, scope string, initialView View) App {
	return App{
		cfg:       cfg,
		repoRoot:  repoRoot,
		mainDir:   git.MainCheckout(repoRoot),
		scope:     scope,
		current:   initialView,
		dashboard: NewDashboard(cfg, scope),
		tasks:     newTasksModel(cfg, repoRoot, scope),
		clean:     newCleanModel(),
		create:    newCreateFor(cfg, repoRoot),
		cd:        newCdModel(),
		settings:  NewSettings(cfg, repoRoot),
	}
}

// newCreateFor builds a task create-form seeded with the config's base branches,
// the resolved default template + available templates, and the task provider.
func newCreateFor(cfg config.Config, repoRoot string) createModel {
	c := newCreateModel(orderBases(cfg.BaseBranches, cfg.PRBase))
	c.kind = "task"
	c.template = prompt.Resolve(cfg, "task", "")
	c.templates = moveFront(prompt.List(), c.template) // default first so the select starts on it
	c.model = cfg.Agent.Model
	c.models = moveFront(modelChoices(cfg), c.model) // default first so the select starts on it
	c.taskProvider = providerFor(cfg)
	c.repoRoot = repoRoot
	c.form = c.buildForm()
	return c
}

// modelChoices returns the per-session model options for the New tab. Only
// claude gets a picker (via --model); other agents pass model through Args, so
// they get no choices (empty → picker hidden). "" means the agent's default.
func modelChoices(cfg config.Config) []string {
	if cfg.AgentCommand() != "claude" {
		return nil
	}
	choices := []string{"", "sonnet", "opus", "haiku"}
	// Keep a custom configured default selectable even if it's not an alias.
	if cfg.Agent.Model != "" {
		found := false
		for _, m := range choices {
			if m == cfg.Agent.Model {
				found = true
				break
			}
		}
		if !found {
			choices = append([]string{cfg.Agent.Model}, choices...)
		}
	}
	return choices
}

// providerFor returns the configured task provider, or nil when none is set.
func providerFor(cfg config.Config) task.Provider {
	switch cfg.Tasks.Provider {
	case "github":
		return task.GitHub{}
	case "clickup":
		p := task.ClickUp{}
		if cfg.ClickUp != nil {
			p.Token = cfg.ClickUp.Token
			p.TeamID = cfg.ClickUp.Team
			p.Space = cfg.ClickUp.Space
			p.ListID = cfg.ClickUp.List
		}
		return p
	}
	return nil
}

func orderBases(bases []string, preferred string) []string {
	if preferred == "" {
		return bases
	}
	// If preferred already first, return as-is.
	if len(bases) > 0 && bases[0] == preferred {
		return bases
	}
	out := []string{preferred}
	for _, b := range bases {
		if b != preferred {
			out = append(out, b)
		}
	}
	return out
}

func (a App) Init() tea.Cmd {
	return tea.Batch(
		a.dashboard.Init(),
		loadWorktrees(a.cfg.WorktreeDir, a.repoRoot),
	)
}

// capturing reports whether the active view is currently eating text input, so
// the global tab/quit keys must not steal it.
func (a App) capturing() bool {
	switch a.current {
	case ViewThreads:
		return a.dashboard.filter.active || a.dashboard.showSummary || a.dashboard.confirmDrop
	case ViewTasks:
		return a.tasks.filter.active || a.tasks.confirming
	case ViewCreate:
		return a.create.focused || a.create.pickingTask // capture while typing or picking
	case ViewClean:
		return a.clean.filter.active || a.clean.confirming || a.clean.showResults
	case ViewSettings:
		return a.settings.mode != sModeList
	case ViewCd:
		return true // its own picker; handles its own keys
	}
	return false
}

// gotoTab switches the active tab and returns any refresh command that tab needs.
func (a App) gotoTab(v View) (App, tea.Cmd) {
	// Leaving Settings: re-read config from disk so edits (task provider, agent,
	// template, theme…) take effect live, no restart. newCreateFor below then
	// rebuilds the New tab from the fresh cfg.
	if a.current == ViewSettings && v != ViewSettings {
		if cfg, err := config.Load(a.repoRoot); err == nil {
			a.cfg = cfg
			prev := a.settings
			a.settings = NewSettings(cfg, a.repoRoot)
			a.settings.cursor, a.settings.layer = prev.cursor, prev.layer
			a.settings.width, a.settings.height = a.width, a.height
		}
	}
	a.current = v
	switch v {
	case ViewThreads:
		return a, a.dashboard.Init() // refresh + restart the live tick
	case ViewTasks:
		a.tasks = newTasksModel(a.cfg, a.repoRoot, a.scope)
		a.tasks.width, a.tasks.height = a.width, a.height
		a.tasks.loading = a.tasks.provider != nil
		return a, a.tasks.Init()
	case ViewCreate:
		a.create = newCreateFor(a.cfg, a.repoRoot)
		a.create.width, a.create.height = a.width, a.height
	case ViewClean:
		return a, checkRemote(a.repoRoot, a.clean.worktrees, a.cfg.BaseBranches)
	}
	return a, nil
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		var c1, c2 tea.Cmd
		var m tea.Model
		m, c1 = a.dashboard.Update(msg)
		a.dashboard = m.(Dashboard)
		m, c2 = a.settings.Update(msg)
		a.settings = m.(settingsModel)
		a.create, _ = a.create.Update(msg)
		a.tasks, _ = a.tasks.Update(msg)
		return a, tea.Batch(c1, c2)

	case worktreesLoadedMsg:
		a.err = nil
		a.clean.worktrees = msg.worktrees
		a.cd.worktrees = msg.worktrees
		if a.current == ViewClean {
			return a, checkRemote(a.repoRoot, msg.worktrees, a.cfg.BaseBranches)
		}
		return a, nil

	case remoteCheckedMsg:
		a.clean.worktrees = msg.worktrees
		a.clean.remoteChecked = true
		a.clean.autoSelectDone()
		return a, nil

	case cleanRemovedMsg:
		// If we just removed the worktree we're standing in, don't strand the
		// shell there: cd to the main checkout and exit.
		if a.mainDir != "" && git.CheckoutClass(a.repoRoot) == "dead" {
			a.quit = true
			a.result = Result{Action: ResultCd, Path: a.mainDir}
			return a, tea.Quit
		}
		a.clean = newCleanModel()
		a.clean.showResults = true
		a.clean.results = msg.outcomes
		return a, nil

	case worktreeCreatedMsg:
		a.quit = true
		a.result = Result{Action: ResultLaunch, Path: msg.path, Model: msg.model}
		return a, tea.Quit

	case errMsg:
		a.err = msg.err
		if a.create.creating {
			a.create = a.create.createFailed()
		}
		return a, nil

	case tea.KeyMsg:
		if !a.create.creating {
			a.err = nil // any keystroke dismisses a stale error banner
		}
		if a.showHelp {
			if msg.String() == "ctrl+c" {
				a.quit = true
				return a, tea.Quit
			}
			a.showHelp = false // any other key closes the help overlay
			return a, nil
		}
		if !a.capturing() {
			switch msg.String() {
			case "?":
				a.showHelp = true
				return a, nil
			case "m":
				if a.mainDir != "" {
					a.quit = true
					a.result = Result{Action: ResultCd, Path: a.mainDir}
					return a, tea.Quit
				}
			case "ctrl+c", "q":
				a.quit = true
				return a, tea.Quit
			case "tab":
				return a.gotoTab(a.nextTab())
			case "shift+tab":
				return a.gotoTab(a.prevTab())
			case "1", "2", "3", "4", "5":
				if i := int(msg.String()[0] - '1'); i < len(appTabs) {
					return a.gotoTab(appTabs[i])
				}
			case "esc":
				if a.current != ViewThreads {
					return a.gotoTab(ViewThreads)
				}
				a.quit = true
				return a, tea.Quit
			}
		}
	}

	return a.delegate(msg)
}

func (a App) nextTab() View { return appTabs[(tabIndex(a.current)+1)%len(appTabs)] }
func (a App) prevTab() View {
	return appTabs[(tabIndex(a.current)+len(appTabs)-1)%len(appTabs)]
}

// delegate routes msg to the active sub-view and reconciles its result/back state.
func (a App) delegate(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch a.current {
	case ViewThreads:
		m, c := a.dashboard.Update(msg)
		a.dashboard = m.(Dashboard)
		cmd = c
		if a.dashboard.quit {
			a.result = a.dashboard.Result()
			a.quit = true
			return a, tea.Quit
		}

	case ViewClean:
		a.clean, cmd = a.clean.Update(msg)
		if a.clean.dismissed {
			// User acknowledged the result summary → back to Threads.
			a.clean = newCleanModel()
			return a.gotoTab(ViewThreads)
		}
		// Removal runs off the UI thread: fire once when confirmed, show a
		// "Removing…" state, finish on cleanRemovedMsg. Inline removal froze the
		// TUI for the duration of N worktree deletes.
		if a.clean.done && !a.clean.removing {
			a.clean.removing = true
			// Run git from the main checkout, not repoRoot: if we're cleaning the
			// worktree we're standing in, repoRoot gets deleted mid-loop and every
			// later git call would fail from the dead cwd (partial clean + breakage).
			base := a.mainDir
			if base == "" {
				base = a.repoRoot
			}
			return a, removeWorktreesCmd(base, a.cfg.WorktreeDir, a.clean.toRemove)
		}

	case ViewTasks:
		a.tasks, cmd = a.tasks.Update(msg)
		if a.tasks.chosen != nil {
			t := *a.tasks.chosen
			a.tasks.chosen = nil
			c := newCreateFor(a.cfg, a.repoRoot)
			c.width, c.height = a.width, a.height
			c = c.withTask(t)
			a.create = c
			a.current = ViewCreate
			if a.create.confirmed {
				a.create = a.create.startCreating()
				return a, createWorktree(a.cfg, a.repoRoot, a.create.kind, a.create.hint, a.create.baseBranch, a.create.template, a.create.model, taskRefOf(a.create.selectedTask))
			}
			return a, nil
		}

	case ViewCreate:
		a.create, cmd = a.create.Update(msg)
		if a.create.confirmed {
			a.create = a.create.startCreating()
			return a, createWorktree(a.cfg, a.repoRoot, a.create.kind, a.create.hint, a.create.baseBranch, a.create.template, a.create.model, taskRefOf(a.create.selectedTask))
		}
		if a.create.cancelled {
			return a.gotoTab(ViewThreads)
		}

	case ViewSettings:
		m, c := a.settings.Update(msg)
		a.settings = m.(settingsModel)
		cmd = c

	case ViewCd:
		a.cd, cmd = a.cd.Update(msg)
		if a.cd.chosen != nil {
			a.quit = true
			a.result = Result{Action: ResultCd, Path: a.cd.chosen.Path}
			return a, tea.Quit
		}
		if a.cd.cancelled {
			a.quit = true
			return a, tea.Quit
		}
	}
	return a, cmd
}

// renderMasthead draws norn's shared crown: the wordmark + the top tab bar
// (active tab highlighted) on one line, the tab/help hint pushed to the right
// edge, and a divider rule under it. Every tab wears the same masthead so the
// app reads as one product; the mascot stays the Threads hero signature. w is
// the panel's inner content width, so the hint right-aligns and the rule spans
// the pane. Padding-free tab styles keep the bar from shifting as focus moves.
func renderMasthead(current View, w int) string {
	// Pill nav: the active tab is a filled accent chip, the rest dim. Same
	// padding on both so the bar never shifts as focus moves. The pill reads as
	// a designed nav rather than a plain word list — norn's signature up top.
	activePill := lipgloss.NewStyle().Background(colorLavender).Foreground(colorBase).Bold(true).Padding(0, 1)
	restPill := lipgloss.NewStyle().Foreground(colorOverlay).Padding(0, 1)
	var parts []string
	for i, v := range appTabs {
		name := fmt.Sprintf("%d %s", i+1, tabLabel(v))
		if v == current {
			parts = append(parts, activePill.Render(name))
		} else {
			parts = append(parts, restPill.Render(name))
		}
	}
	// Brand: an accent ribbon + wordmark, tying the bar to the mascot's color.
	brand := lipgloss.NewStyle().Foreground(colorLavender).Render("▌") +
		lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(" norn ")
	left := brand + " " + strings.Join(parts, " ")
	hint := dimStyle.Render("⇥ tab · ? help")

	gap := w - lipgloss.Width(left) - lipgloss.Width(hint)
	if gap < 3 {
		gap = 3
	}
	bar := left + strings.Repeat(" ", gap) + hint

	rule := dimStyle.Render(strings.Repeat("─", max(w, 1)))
	return bar + "\n" + rule
}

// mastheadWidth is the panel's inner content width (frame inner minus its
// horizontal padding), so the masthead rule/hint line up with the pane edges.
func mastheadWidth(width int) int {
	inner := frameWidth
	if width > 0 {
		if max := width - 8; max < frameWidth {
			inner = max
		}
	}
	inner -= 6 // frame's Padding(1, 3)
	if inner < 20 {
		inner = 20
	}
	return inner
}

type keyHint struct{ key, desc string }

// helpFor returns the per-view key list for the global help overlay.
func helpFor(v View) []keyHint {
	switch v {
	case ViewThreads:
		return []keyHint{
			{"⏎", "cd into worktree"}, {"o", "open the agent"}, {"s", "summarize"},
			{"p", "open PR"}, {"t", "open task"}, {"d", "drop session"},
			{"/", "filter"}, {"a", "all repos"}, {"r", "refresh"}, {"j/k g/G", "move"},
		}
	case ViewTasks:
		return []keyHint{
			{"⏎", "worktree from task"}, {"o", "open in browser"}, {"/", "filter"},
			{"r", "refresh"}, {"j/k", "move"}, {"esc", "back"},
		}
	case ViewCreate:
		return []keyHint{{"⏎", "start typing / confirm"}, {"T", "pick a task"}, {"t", "change template"}, {"M", "change model"}, {"↑/↓", "pick base branch"}, {"esc", "cancel"}}
	case ViewClean:
		return []keyHint{
			{"space", "select"}, {"a", "select all"}, {"g", "select done"},
			{"d", "delete selected"}, {"/", "filter"}, {"j/k", "move"},
		}
	case ViewSettings:
		return []keyHint{{"⏎", "edit"}, {"space", "toggle"}, {"←/→", "layer"}, {"e", "$EDITOR"}, {"j/k", "move"}}
	case ViewCd:
		return []keyHint{{"⏎", "cd into worktree"}, {"j/k", "move"}}
	}
	return nil
}

// renderHelp builds the global help card: nav keys + the active view's keys.
func renderHelp(current View) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorLavender).Bold(true)
	var b strings.Builder
	b.WriteString(titleStyle.Render("norn · keys") + "\n\n")
	b.WriteString(subtitleStyle.Render("Navigate") + "\n")
	b.WriteString("  " + keyStyle.Render("⇥ / ⇧⇥ / 1-5") + dimStyle.Render("  switch tab") + "\n")
	b.WriteString("  " + keyStyle.Render("esc") + dimStyle.Render("  back to Threads") + "\n")
	b.WriteString("  " + keyStyle.Render("m") + dimStyle.Render("  cd to the main checkout") + "\n")
	b.WriteString("  " + keyStyle.Render("q") + dimStyle.Render("  quit") + "\n\n")
	b.WriteString(subtitleStyle.Render(tabLabel(current)) + "\n")
	for _, h := range helpFor(current) {
		b.WriteString("  " + keyStyle.Render(fmt.Sprintf("%-11s", h.key)) + dimStyle.Render("  "+h.desc) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("any key to close"))
	return boxStyle.Render(b.String())
}

func (a App) View() string {
	if a.quit {
		return ""
	}

	if a.showHelp {
		return frame(renderHelp(a.current), a.width, a.height)
	}

	var body string
	switch a.current {
	case ViewThreads:
		body = a.dashboard.View()
	case ViewTasks:
		body = a.tasks.View()
	case ViewClean:
		body = a.clean.View()
	case ViewCreate:
		body = a.create.View()
	case ViewSettings:
		body = a.settings.View()
	case ViewCd:
		body = a.cd.View()
	}

	if a.err != nil {
		body += "\n" + errorStyle.Render(fmt.Sprintf("Error: %v", a.err))
	}

	// The Cd picker is a bare CLI mode — no tab chrome.
	if a.current == ViewCd {
		return frame(body, a.width, a.height)
	}
	return frame(renderMasthead(a.current, mastheadWidth(a.width))+"\n\n"+body, a.width, a.height)
}

// Commands

func loadWorktrees(worktreeDir, repoRoot string) tea.Cmd {
	return func() tea.Msg {
		common := ""
		if repoRoot != "" {
			c, err := git.CommonDir(repoRoot)
			if err == nil {
				common = c
			}
		}
		wts, err := git.ListWorktrees(worktreeDir, common)
		if err != nil {
			return errMsg{err}
		}
		return worktreesLoadedMsg{wts}
	}
}

func checkRemote(repoRoot string, wts []git.Worktree, bases []string) tea.Cmd {
	return func() tea.Msg {
		_ = git.FetchPrune(repoRoot)
		// Retroactive: worktrees created before `.norn/` joined the exclude list
		// would otherwise read as dirty forever. info/exclude is repo-shared, so
		// one call covers them all.
		git.ExcludeLocalMeta(repoRoot)
		checked := git.CheckRemoteGone(repoRoot, wts)
		checked = git.CheckMerged(repoRoot, checked, bases)
		checked = git.CheckDirty(checked)
		return remoteCheckedMsg{checked}
	}
}

type cleanRemovedMsg struct{ outcomes []git.RemoveOutcome }

// removeWorktreesCmd runs the (serial, git-lock-sensitive) removals off the UI
// thread so the TUI stays responsive while it works.
func removeWorktreesCmd(repoRoot, worktreeDir string, toRemove []git.RemoveRequest) tea.Cmd {
	return func() tea.Msg {
		var outcomes []git.RemoveOutcome
		for _, req := range toRemove {
			outcomes = append(outcomes, git.RemoveWorktree(repoRoot, req))
		}
		git.CleanEmptyDirs(worktreeDir)
		return cleanRemovedMsg{outcomes: outcomes}
	}
}

// taskRefOf converts a picked task into the prompt's TaskRef (nil-safe).
func taskRefOf(t *task.Task) *prompt.TaskRef {
	if t == nil {
		return nil
	}
	return &prompt.TaskRef{ID: t.ID, Title: t.Title, URL: t.URL, Description: t.Description}
}

func createWorktree(cfg config.Config, repoRoot, kind, hint, base, tmpl, model string, taskRef *prompt.TaskRef) tea.Cmd {
	return func() tea.Msg {
		branch := git.MakeBranch(kind, hint, cfg.BranchFormat)
		if cfg.AINaming && cfg.HeadlessClaude() && claude.Available() && git.BranchLacksSlug(branch) {
			branch = claude.EnrichBranchName(context.Background(), repoRoot, hint, branch, cfg.BranchFormat)
		}
		wtPath, err := git.CreateWorktree(repoRoot, cfg.WorktreeDir, branch, base)
		if err != nil {
			return errMsg{err}
		}
		_ = git.SymlinkEnvFiles(repoRoot, wtPath)

		promptText, err := prompt.Render(cfg, kind, hint, base, prompt.Resolve(cfg, kind, tmpl), taskRef)
		if err != nil {
			return errMsg{fmt.Errorf("brief render failed for %s: %w", branch, err)}
		}
		promptPath := wtPath + "/.worktree.md"
		if err := writeFile(promptPath, promptText); err != nil {
			return errMsg{err}
		}

		return worktreeCreatedMsg{path: wtPath, model: model}
	}
}
