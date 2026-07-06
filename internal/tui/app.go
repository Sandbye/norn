package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/prompt"
)

type View int

const (
	ViewMenu View = iota
	ViewClean
	ViewCreate
	ViewCd
)

// ResultAction describes what main.go should do after the TUI exits.
type ResultAction int

const (
	ResultNone    ResultAction = iota
	ResultLaunch               // launch new Claude session
	ResultResume               // resume existing Claude session
	ResultCd                   // cd into worktree shell
)

// Result is returned after the TUI exits, telling main.go what to do.
type Result struct {
	Action ResultAction
	Path   string // worktree path
}

type App struct {
	cfg      config.Config
	repoRoot string
	current  View

	menu   menuModel
	clean  cleanModel
	create createModel
	cd     cdModel

	width  int
	height int
	err    error
	quit   bool
	result Result
}

func (a App) Result() Result { return a.result }

type worktreesLoadedMsg struct {
	worktrees []git.Worktree
}

type remoteCheckedMsg struct {
	worktrees []git.Worktree
}

type worktreeCreatedMsg struct {
	path string
}

type errMsg struct{ err error }

func NewApp(cfg config.Config, repoRoot string, initialView View) App {
	return NewAppMode(cfg, repoRoot, initialView, false)
}

// NewAppMode is NewApp with an explicit menu cd-mode (enter = cd, l = launch).
// Used by `work -d`.
func NewAppMode(cfg config.Config, repoRoot string, initialView View, cdMode bool) App {
	// Surface pr_base as the default choice in the base-branch picker by
	// putting it first. Keeps "branch base" and "PR target" identical.
	bases := orderBases(cfg.BaseBranches, cfg.PRBase)
	menu := newMenuModel()
	menu.cdMode = cdMode
	return App{
		cfg:      cfg,
		repoRoot: repoRoot,
		current:  initialView,
		menu:     menu,
		clean:    newCleanModel(),
		create:   newCreateModel(bases),
		cd:       newCdModel(),
	}
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
	return loadWorktrees(a.cfg.WorktreeDir, a.repoRoot)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			a.quit = true
			return a, tea.Quit
		}

	case worktreesLoadedMsg:
		a.menu.worktrees = msg.worktrees
		a.clean.worktrees = msg.worktrees
		a.cd.worktrees = msg.worktrees
		if a.current == ViewClean {
			return a, checkRemote(a.repoRoot, msg.worktrees, a.cfg.BaseBranches)
		}

	case remoteCheckedMsg:
		a.clean.worktrees = msg.worktrees
		a.clean.remoteChecked = true
		a.clean.autoSelectDone()

	case worktreeCreatedMsg:
		a.quit = true
		a.result = Result{Action: ResultLaunch, Path: msg.path}
		return a, tea.Quit

	case errMsg:
		a.err = msg.err
	}

	var cmd tea.Cmd
	switch a.current {
	case ViewMenu:
		a.menu, cmd = a.menu.Update(msg)
		if a.menu.chosen != nil {
			switch a.menu.chosen.action {
			case actionResume:
				a.quit = true
				a.result = Result{Action: ResultResume, Path: a.menu.chosen.worktree.Path}
				return a, tea.Quit
			case actionCd:
				a.quit = true
				a.result = Result{Action: ResultCd, Path: a.menu.chosen.worktree.Path}
				return a, tea.Quit
			case actionNewTask:
				a.current = ViewCreate
				a.create.kind = "task"
			case actionNewReview:
				a.current = ViewCreate
				a.create.kind = "review"
			case actionClean:
				a.current = ViewClean
				cmd = checkRemote(a.repoRoot, a.clean.worktrees, a.cfg.BaseBranches)
			case actionQuit:
				a.quit = true
				return a, tea.Quit
			}
			a.menu.chosen = nil
		}

	case ViewClean:
		a.clean, cmd = a.clean.Update(msg)
		if a.clean.done {
			for _, wt := range a.clean.toRemove {
				git.RemoveWorktree(a.repoRoot, wt.Path, wt.Branch)
			}
			git.CleanEmptyDirs(a.cfg.WorktreeDir)
			a.clean = newCleanModel()
			a.current = ViewMenu
			cmd = loadWorktrees(a.cfg.WorktreeDir, a.repoRoot)
		}
		if a.clean.cancelled {
			a.clean.cancelled = false
			a.current = ViewMenu
		}

	case ViewCreate:
		a.create, cmd = a.create.Update(msg)
		if a.create.confirmed {
			return a, createWorktree(
				a.cfg, a.repoRoot,
				a.create.kind,
				a.create.hint,
				a.create.baseBranch,
			)
		}
		if a.create.cancelled {
			a.create = newCreateModel(a.cfg.BaseBranches)
			a.current = ViewMenu
		}

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

func (a App) View() string {
	if a.quit {
		return ""
	}

	header := titleStyle.Render("work") + " " + dimStyle.Render("worktree manager")
	var body string

	switch a.current {
	case ViewMenu:
		body = a.menu.View()
	case ViewClean:
		body = a.clean.View()
	case ViewCreate:
		body = a.create.View()
	case ViewCd:
		body = a.cd.View()
	}

	if a.err != nil {
		body += "\n" + errorStyle.Render(fmt.Sprintf("Error: %v", a.err))
	}

	return fmt.Sprintf("\n%s\n\n%s\n", header, body)
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
		checked := git.CheckRemoteGone(repoRoot, wts)
		checked = git.CheckMerged(repoRoot, checked, bases)
		return remoteCheckedMsg{checked}
	}
}

func createWorktree(cfg config.Config, repoRoot, kind, hint, base string) tea.Cmd {
	return func() tea.Msg {
		branch := git.MakeBranch(kind, hint)
		if cfg.AINaming && cfg.HeadlessClaude() && claude.Available() && git.BranchLacksSlug(branch) {
			branch = claude.EnrichBranchName(context.Background(), repoRoot, hint, branch)
		}
		wtPath, err := git.CreateWorktree(repoRoot, cfg.WorktreeDir, branch, base)
		if err != nil {
			return errMsg{err}
		}
		_ = git.SymlinkEnvFiles(repoRoot, wtPath)

		promptText, _ := prompt.Render(cfg, kind, hint, base, prompt.Resolve(cfg, kind, ""))
		promptPath := wtPath + "/.worktree.md"
		if err := writeFile(promptPath, promptText); err != nil {
			return errMsg{err}
		}

		return worktreeCreatedMsg{path: wtPath}
	}
}
