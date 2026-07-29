package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/prompt"
	"github.com/sandbye/norn/internal/state"
)

// Dashboard is a live view of all known worktree sessions across repos.
//
// Refresh cadence: 5s tick. For each row we attempt a fast `gh pr view`
// in the background to learn PR state. State file is the source of truth
// for which sessions exist; git worktree list reconciles dead sessions.
type Dashboard struct {
	cfg      config.Config
	store    *state.Store
	rows     []dashRow
	cursor   int
	width    int
	height   int
	err      error
	quit     bool
	result   Result
	lastLoad time.Time

	// scopeRepo: when non-empty, only sessions for this repo basename are shown.
	// Set automatically when `work -d` runs inside a git repo. Press `a` to
	// clear and see all repos.
	scopeRepo string

	// prCache: branch -> cached PR result. TTL prevents re-fetch on every tick.
	prCache map[string]prCacheEntry

	filter filterState

	// markFrame animates the theme's rune mark (cycles through RuneMarks).
	markFrame int

	// confirmDrop gates the `d` (drop session) action behind a y/n prompt.
	confirmDrop bool
	dropTarget  string // session ID pending drop
	dropBranch  string // branch label for the prompt

	// Headless "summarize session" overlay (press `s`). Additive — does not
	// affect any existing navigation/launch flow.
	summarizing   bool
	summary       string
	summaryBranch string
	summaryPath   string // worktree path, kept for `r` refresh
	summaryErr    error
	summaryCached bool              // current overlay came from cache
	showSummary   bool              // overlay open (opened intentionally with `s`)
	readyBranch   string            // summary finished, waiting to be viewed
	summaryCache  map[string]string // branch -> last summary text
	spinner       spinner.Model
}

// visibleRows is d.rows narrowed by the active filter query (fuzzy on branch /
// clickup id / repo), best-ranked first. Empty query returns rows unchanged.
func (d Dashboard) visibleRows() []dashRow {
	if d.filter.query == "" {
		return d.rows
	}
	type scored struct {
		row   dashRow
		score int
	}
	var hits []scored
	for _, r := range d.rows {
		best := -1
		for _, field := range []string{r.Branch, r.Title, r.ClickUpID, r.Repo} {
			if field == "" {
				continue
			}
			if s, ok := fuzzyScore(d.filter.query, field); ok && s > best {
				best = s
			}
		}
		if best > -1 {
			hits = append(hits, scored{r, best})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]dashRow, len(hits))
	for i, h := range hits {
		out[i] = h.row
	}
	return out
}

type prCacheEntry struct {
	State  string
	Checks string
	Number int
	When   time.Time
}

const prCacheTTL = 60 * time.Second

type dashRow struct {
	state.Session
	WorktreeAlive bool
	PRState       string            // OPEN / DRAFT / MERGED / CLOSED / ""
	PRChecks      string            // ✓ / ✗ / · / ""
	PRPending     bool              // currently being fetched
	AgentState    claude.AgentState // live: working / waiting / idle / "" (ephemeral)
	Next          string            // .state.md `next:` action (ephemeral)
	Goal          string            // .state.md `goal:` one-liner, shown in the detail pane (ephemeral)
}

type dashTickMsg time.Time
type markTickMsg struct{}

// markTick schedules the next animation frame (for the spinning 3D mark).
func markTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return markTickMsg{} })
}

type dashLoadedMsg struct {
	rows []dashRow
}

// prFetchedMsg updates a single row's PR data when the async lookup returns.
type prFetchedMsg struct {
	branch string
	entry  prCacheEntry
	ok     bool
}

// summaryMsg carries the result of a headless "summarize session" run.
type summaryMsg struct {
	branch string
	text   string
	err    error
}

// summarizeCmd runs `claude -p` in the worktree to summarize recent work.
// Read-only: only Read + git-read tools are allowed, so no prompts, no mutation.
func summarizeCmd(dir, branch string) tea.Cmd {
	return func() tea.Msg {
		const prompt = "Summarize the work done on this branch in 3-6 terse bullet points: " +
			"what changed and why. Base it on `git log` against the default branch and the diff. " +
			"No preamble, just the bullets."
		res, err := claude.Run(context.Background(), dir, prompt, claude.Options{
			AllowedTools: []string{"Read", "Bash(git log *)", "Bash(git diff *)", "Bash(git status *)"},
		})
		if err != nil {
			return summaryMsg{branch: branch, err: err}
		}
		if res.IsError {
			return summaryMsg{branch: branch, err: fmt.Errorf("claude reported an error")}
		}
		return summaryMsg{branch: branch, text: res.Text}
	}
}

// NewDashboard creates a dashboard. If scopeRepo is non-empty, only sessions
// for that repo basename are shown; press `a` to clear.
func NewDashboard(cfg config.Config, scopeRepo string) Dashboard {
	store, _ := state.Load()
	if store == nil {
		store = &state.Store{}
	}
	store.SortByActivity()
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return Dashboard{
		cfg:          cfg,
		store:        store,
		scopeRepo:    scopeRepo,
		prCache:      map[string]prCacheEntry{},
		summaryCache: map[string]string{},
		spinner:      sp,
	}
}

func (d Dashboard) Result() Result { return d.result }

func (d Dashboard) Init() tea.Cmd {
	return tea.Batch(
		d.loadCmd(),
		tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return dashTickMsg(t) }),
		markTick(),
	)
}

func (d Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height

	case tea.KeyMsg:
		s := msg.String()

		// Summary overlay swallows keys: `r` forces a fresh summary, anything
		// else dismisses. Checked first so it can't interfere with anything else.
		if d.showSummary {
			if s == "r" || s == "R" {
				d.showSummary = false
				d.summary = ""
				d.summaryCached = false
				d.summarizing = true
				return d, tea.Batch(summarizeCmd(d.summaryPath, d.summaryBranch), d.spinner.Tick)
			}
			d.showSummary = false
			d.summary = ""
			d.summaryErr = nil
			d.summaryCached = false
			return d, nil
		}

		// Drop confirmation swallows keys until resolved.
		if d.confirmDrop {
			switch s {
			case "y", "Y", "enter":
				if fresh, err := state.Load(); err == nil && fresh != nil {
					fresh.Remove(d.dropTarget)
					_ = fresh.Save()
				}
				d.confirmDrop = false
				return d, d.loadCmd()
			case "n", "N", "esc":
				d.confirmDrop = false
			case "ctrl+c":
				d.quit = true
				return d, tea.Quit
			}
			return d, nil
		}

		// Filter input: printable/backspace/esc edit the query. While filtering,
		// letters type into the query, so navigation uses arrows/ctrl+n+p and
		// the action letters (r/a/p/t/d) are paused until esc.
		if d.filter.active {
			before := d.filter.query
			if d.filter.handleKey(s) {
				if d.filter.query != before {
					d.cursor = 0
				}
				return d, nil
			}
		} else if s == "/" {
			d.filter.handleKey(s)
			return d, nil
		}

		vis := d.visibleRows()
		switch s {
		case "j", "down", "ctrl+n":
			if d.cursor < len(vis)-1 {
				d.cursor++
			}
		case "k", "up", "ctrl+p":
			if d.cursor > 0 {
				d.cursor--
			}
		case "g":
			d.cursor = 0
		case "G":
			d.cursor = len(vis) - 1
		case "r":
			return d, d.loadCmd()
		case "a":
			if d.scopeRepo != "" {
				d.scopeRepo = ""
				return d, d.loadCmd()
			}
		case "enter":
			// cd into the worktree dir (via the shell wrapper). Default action.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.WorktreeAlive {
					d.quit = true
					d.result = Result{Action: ResultCd, Path: row.Path}
					return d, tea.Quit
				}
			}
		case "o":
			// Open (launch/resume) the agent in the worktree.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.WorktreeAlive {
					d.quit = true
					d.result = Result{Action: ResultResume, Path: row.Path}
					return d, tea.Quit
				}
			}
		case "p":
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.PRNumber > 0 {
					openPRInBrowser(row.Branch, row.Path)
				}
			}
		case "t":
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.ClickUpID != "" {
					openURL("https://app.clickup.com/t/" + row.ClickUpID)
				}
			}
		case "s":
			// Headless summarize of the focused worktree. Show cache instantly
			// if we have one (refresh from the overlay with `r`).
			if d.cursor < len(vis) && !d.summarizing {
				row := vis[d.cursor]
				if row.WorktreeAlive && d.cfg.HeadlessClaude() && claude.Available() {
					d.summaryBranch = row.Branch
					d.summaryPath = row.Path
					if cached, ok := d.summaryCache[row.Branch]; ok {
						d.summary = cached
						d.summaryErr = nil
						d.summaryCached = true
						d.showSummary = true
						if d.readyBranch == row.Branch {
							d.readyBranch = ""
						}
						return d, nil
					}
					// No cache (first run, or retry after an error).
					d.summaryErr = nil
					if d.readyBranch == row.Branch {
						d.readyBranch = ""
					}
					d.summarizing = true
					return d, tea.Batch(summarizeCmd(row.Path, row.Branch), d.spinner.Tick)
				}
			}
		case "d":
			// Drop the session — behind a confirm (mutates the store).
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				d.confirmDrop = true
				d.dropTarget = row.ID
				d.dropBranch = row.Branch
			}
		}

	case dashTickMsg:
		return d, tea.Batch(
			d.loadCmd(),
			tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return dashTickMsg(t) }),
		)

	case markTickMsg:
		if !active.Spin {
			return d, nil // static theme; let the tick lapse
		}
		d.markFrame++
		return d, markTick()

	case dashLoadedMsg:
		d.rows = msg.rows
		d.lastLoad = time.Now()
		if d.cursor >= len(d.rows) {
			d.cursor = max0(len(d.rows) - 1)
		}
		// Apply any cached PR data we already have, then fan out async fetches
		// for rows whose cache is stale or missing.
		var cmds []tea.Cmd
		for i := range d.rows {
			branch := d.rows[i].Branch
			if entry, ok := d.prCache[branch]; ok && time.Since(entry.When) < prCacheTTL {
				d.rows[i].PRState = entry.State
				d.rows[i].PRChecks = entry.Checks
				if entry.Number > 0 {
					d.rows[i].PRNumber = entry.Number
				}
				continue
			}
			if d.rows[i].Status != state.StatusActive && d.rows[i].PRNumber == 0 {
				continue
			}
			d.rows[i].PRPending = true
			cmds = append(cmds, fetchPRCmd(branch))
		}
		if len(cmds) > 0 {
			return d, tea.Batch(cmds...)
		}

	case prFetchedMsg:
		// Cache it, then patch the matching row in-place.
		if msg.ok {
			d.prCache[msg.branch] = msg.entry
		} else {
			// Even on failure, cache an empty result so we don't hammer.
			d.prCache[msg.branch] = prCacheEntry{When: time.Now()}
		}
		for i := range d.rows {
			if d.rows[i].Branch != msg.branch {
				continue
			}
			d.rows[i].PRPending = false
			if msg.ok {
				d.rows[i].PRState = msg.entry.State
				d.rows[i].PRChecks = msg.entry.Checks
				if msg.entry.Number > 0 {
					d.rows[i].PRNumber = msg.entry.Number
				}
			}
		}

	case spinner.TickMsg:
		if d.summarizing {
			var cmd tea.Cmd
			d.spinner, cmd = d.spinner.Update(msg)
			return d, cmd
		}

	case summaryMsg:
		// Non-modal: finishing does NOT pop the overlay. Cache it and flag the
		// row as ready; the user opens it with `s` when they want it.
		d.summarizing = false
		if msg.err != nil {
			d.summaryErr = msg.err
			d.summaryBranch = msg.branch
			d.readyBranch = msg.branch // s will retry (no cache on error)
		} else if msg.text != "" {
			d.summaryCache[msg.branch] = msg.text
			d.readyBranch = msg.branch
		}
	}

	return d, nil
}

func (d Dashboard) View() string {
	if d.quit {
		return ""
	}

	// Summary overlay takes over the screen only when opened intentionally (`s`).
	if d.showSummary {
		title := headerStyle.Render("Summary: " + d.summaryBranch)
		if d.summaryCached {
			title += dimStyle.Render("   (cached)")
		}
		var body string
		if d.summaryErr != nil {
			body = errorStyle.Render("summarize failed: " + d.summaryErr.Error())
		} else {
			body = renderMarkdown(d.summary, d.width)
		}
		return fmt.Sprintf("%s\n\n%s\n\n%s", title, body, dimStyle.Render("r refresh · any key to dismiss"))
	}

	header := d.renderHeader()

	if len(d.rows) == 0 {
		var body string
		if d.scopeRepo != "" {
			body = dimStyle.Render(fmt.Sprintf("no %s in %s yet. spin one up with `norn \"hint\"`, or press `a` for all repos", ThreadWord(), d.scopeRepo))
		} else {
			body = dimStyle.Render(fmt.Sprintf("no %s yet. spin one up with `norn \"hint\"`", ThreadWord()))
		}
		return fmt.Sprintf("%s\n\n%s\n\n%s", header, body, d.dashKeyHelp())
	}

	// Command center: a scannable sidebar of every thread + a detail pane for the
	// selected one. Sized to the panel frame() renders into (min(frameWidth,
	// width-8) minus padding), not the terminal, so it never overflows.
	vis := d.visibleRows()

	avail := frameWidth - 6
	if d.width > 0 {
		if inner := d.width - 8; inner < frameWidth {
			avail = inner - 6
		}
	}
	sidebarW := max(min(avail/3, 40), 24) // ~a third, balanced, but bounded
	if avail < 72 {                       // narrow panel: split in half, keep detail usable
		sidebarW = max(avail/2, 16)
	}
	detailW := max(avail-sidebarW-3, 20) // 3 = right border + gap
	bodyH := max(d.height-12, 6)

	sidebar := lipgloss.NewStyle().
		Width(sidebarW).
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(colorSurface).
		Render(d.renderSidebar(vis, sidebarW, bodyH))

	var detail string
	if d.cursor >= 0 && d.cursor < len(vis) {
		detail = d.renderDetail(vis[d.cursor], detailW)
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, "  ", detail)

	// Filter line above the help.
	if d.filter.active || d.filter.query != "" {
		fl := cursorStyle.Render("/") + d.filter.query
		if d.filter.active {
			fl += cursorStyle.Render("▏")
		}
		if len(vis) == 0 {
			fl += dimStyle.Render("  no matches")
		}
		body += "\n\n" + fl
	}

	if d.err != nil {
		body += "\n" + errorStyle.Render(fmt.Sprintf("error: %v", d.err))
	}

	return fmt.Sprintf("%s\n\n%s\n\n%s", header, body, d.dashKeyHelp())
}

// renderHeader is the compact command-center header: the theme mascot (small)
// beside the wordmark + scope + any summary status. Replaces the big logo/torus
// banner in this view so the sidebar/detail panes get the vertical room.
func (d Dashboard) renderHeader() string {
	scope := "all repos"
	if d.scopeRepo != "" {
		scope = d.scopeRepo
	}
	word := lipgloss.NewStyle().Bold(true).Foreground(colorLavender).Render("norn")
	meta := word + "  " + dimStyle.Render(ThreadWord()+" · scope: "+scope)
	switch {
	case d.summarizing:
		meta += "   " + d.spinner.View() + dimStyle.Render(" summarizing "+d.summaryBranch+"…")
	case d.summaryErr != nil && d.readyBranch != "":
		meta += errorStyle.Render("   ✗ summary failed: " + d.readyBranch + " (s retries)")
	case d.readyBranch != "":
		meta += activeStyle.Render("   ✓ summary ready: " + d.readyBranch + " (press s)")
	}
	if mark := ThemeSprite(); mark != "" && !active.Spin {
		return lipgloss.JoinHorizontal(lipgloss.Center, mark, "  ", meta)
	}
	return meta
}

// renderSidebar lists the threads (state glyph + branch), cursor highlighted,
// scrolled to keep the cursor visible.
func (d Dashboard) renderSidebar(vis []dashRow, w, h int) string {
	lines := []string{dimStyle.Render(fitCell("THREADS", w))}
	listH := max(h-len(lines), 3)
	start, end := scrollWindow(d.cursor, len(vis), listH)
	for i := start; i < end; i++ {
		r := vis[i]
		if i == d.cursor {
			row := fitCell(glyphRune(r.AgentState)+" "+r.Branch, w)
			lines = append(lines, lipgloss.NewStyle().Foreground(colorBase).Background(colorLavender).Render(row))
			continue
		}
		branch := fitCell(r.Branch, w-2)
		if r.WorktreeAlive {
			branch = branchStyle.Render(branch)
		} else {
			branch = dimStyle.Render(branch)
		}
		lines = append(lines, stateGlyph(r.AgentState)+" "+branch)
	}
	if len(vis) > listH {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  %d/%d", d.cursor+1, len(vis))))
	}
	return strings.Join(lines, "\n")
}

// renderDetail is the pane for the selected thread: title + branch, then the
// state / next / PR / activity / kind / CU, each on a labeled row.
func (d Dashboard) renderDetail(r dashRow, w int) string {
	if w < 1 {
		return ""
	}
	title := r.Title
	if title == "" {
		title = r.Branch
	}
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(truncate(title, w)) + "\n")
	b.WriteString(dimStyle.Render(truncate(r.Branch, w)) + "\n")
	if r.Goal != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Width(w).Foreground(colorText).Render(r.Goal) + "\n")
	}
	b.WriteString("\n")
	row := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString(dimStyle.Render(fitCell(k, 7)) + v + "\n")
	}
	row("state", glyphStyle(r.AgentState).Render(stateLabel(r.AgentState)))
	row("next", truncate(r.Next, max(w-8, 8)))
	row("pr", prDetail(r))
	row("last", shortAge(r.LastActivityAt))
	row("kind", r.Kind)
	row("cu", r.ClickUpID)
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// glyphRune / glyphStyle / stateGlyph render the live agent state as a small
// colored dot for the sidebar + detail.
func glyphRune(s claude.AgentState) string {
	switch s {
	case claude.StateWorking:
		return "●"
	case claude.StateWaiting:
		return "◆"
	case claude.StateIdle:
		return "○"
	case claude.StateStuck:
		return "✗"
	default:
		return "·"
	}
}

func glyphStyle(s claude.AgentState) lipgloss.Style {
	switch s {
	case claude.StateWorking:
		return activeStyle
	case claude.StateWaiting:
		return dirtyStyle
	case claude.StateStuck:
		return goneStyle
	default:
		return dimStyle
	}
}

func stateGlyph(s claude.AgentState) string { return glyphStyle(s).Render(glyphRune(s)) }

func stateLabel(s claude.AgentState) string {
	switch s {
	case claude.StateWorking:
		return "working"
	case claude.StateWaiting:
		return "waiting for you"
	case claude.StateIdle:
		return "idle"
	case claude.StateStuck:
		return "stuck"
	default:
		return "—"
	}
}

func prDetail(r dashRow) string {
	if r.PRNumber == 0 {
		return "—"
	}
	s := fmt.Sprintf("#%d", r.PRNumber)
	if r.PRState != "" {
		s += " " + strings.ToLower(r.PRState)
	}
	if r.PRChecks != "" {
		s += " " + r.PRChecks
	}
	return s
}

// dashKeyHelp is context-aware: only shows actions that are available for the
// currently-focused row. Quieter UI, less guesswork.
func (d Dashboard) dashKeyHelp() string {
	if d.confirmDrop {
		return confirmStyle.Render("drop " + d.dropBranch + " from the dashboard? (y/n)")
	}
	if d.filter.active {
		return dimStyle.Render("type to filter · ↑/↓ or ctrl+n/p move · ⏎ cd · o open · esc clear")
	}
	// Concise essentials; the full keymap lives in the global `?` help overlay.
	return dimStyle.Render("⏎ cd · o open · m main · ? help")
}

func openPRInBrowser(branch, repoDir string) {
	// `gh pr view --web` resolves the PR by current branch from inside the repo.
	cmd := exec.Command("gh", "pr", "view", branch, "--web")
	cmd.Dir = repoDir
	_ = cmd.Start()
}

func openURL(url string) {
	cmd := exec.Command("open", url)
	_ = cmd.Start()
}

// worktreeTitle reads the human task title from a worktree's .worktree.md.
// Returns "" if the file is missing or carries no title yet.
func worktreeTitle(wtPath string) string {
	data, err := os.ReadFile(wtPath + "/.worktree.md")
	if err != nil {
		return ""
	}
	return prompt.ExtractTitle(string(data))
}

// renderMarkdown renders markdown (the session summary) via glamour, wrapped to
// the panel's inner width. Falls back to the raw text on any error so the
// overlay never breaks.
func renderMarkdown(md string, termWidth int) string {
	w := frameWidth - 6
	if termWidth > 0 {
		if inner := termWidth - 8; inner < frameWidth {
			w = inner - 6
		}
	}
	if w < 20 {
		w = 20
	}
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(w))
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.TrimRight(out, "\n")
}

// worktreeNext reads the `next:` action from a worktree's .state.md (the live
// state the last session left). "" when absent, so pre-contract threads fall
// back to their title.
func worktreeNext(wtPath string) string {
	data, err := os.ReadFile(wtPath + "/.state.md")
	if err != nil {
		return ""
	}
	return prompt.ExtractNext(string(data))
}

// worktreeGoal reads the `goal:` one-liner from a worktree's .state.md.
func worktreeGoal(wtPath string) string {
	data, err := os.ReadFile(wtPath + "/.state.md")
	if err != nil {
		return ""
	}
	return prompt.ExtractGoal(string(data))
}

// loadCmd reloads the store and reconciles with live worktree list.
// Fast path only — PR data is fetched async via fetchPRCmd after this returns.
func (d Dashboard) loadCmd() tea.Cmd {
	scope := d.scopeRepo
	// Live agent state is a claude-only signal (reads Claude Code's transcripts).
	useClaude := d.cfg.AgentCommand() == "claude"
	return func() tea.Msg {
		store, err := state.Load()
		if err != nil || store == nil {
			return dashLoadedMsg{}
		}

		// Reconcile: the dashboard reflects live worktrees, not an append-only
		// log. Drop rows whose path is gone or is the main checkout, collapse
		// duplicate rows sharing a worktree path, and reconcile each survivor's
		// branch/ClickUp id against the live checkout. Persist if anything moved.
		store.SortByActivity()
		before := len(store.Sessions)
		store.Prune(func(s state.Session) bool {
			return git.CheckoutClass(s.Path) == "worktree"
		})
		store.DedupeByPath()
		changed := len(store.Sessions) != before

		for i := range store.Sessions {
			sess := &store.Sessions[i]
			if b := git.CurrentBranch(sess.Path); b != "" && b != sess.Branch {
				sess.Branch = b
				sess.ID = state.MakeID(sess.Repo, b)
				changed = true
			}
			if sess.ClickUpID == "" {
				if id := git.ClickUpID(sess.Branch); id != "" {
					sess.ClickUpID = id
					changed = true
				}
			}
			// Re-read the title from .worktree.md every load: a bare-hint
			// worktree has none until start-task resolves the task and writes
			// it back, at which point the dashboard picks it up on next refresh.
			if t := worktreeTitle(sess.Path); t != "" && t != sess.Title {
				sess.Title = t
				changed = true
			}
		}
		if changed {
			_ = store.Save()
		}

		rows := make([]dashRow, 0, len(store.Sessions))
		for _, sess := range store.Sessions {
			if scope != "" && sess.Repo != scope {
				continue
			}
			row := dashRow{Session: sess, WorktreeAlive: true}
			row.Next = worktreeNext(sess.Path)
			row.Goal = worktreeGoal(sess.Path)
			if useClaude {
				row.AgentState, _ = claude.Probe(sess.Path)
			}
			rows = append(rows, row)
		}
		return dashLoadedMsg{rows: rows}
	}
}

// fetchPRCmd returns a tea.Cmd that runs `gh pr view <branch>` in the background
// and emits one prFetchedMsg. Concurrent calls run in parallel (tea.Batch).
func fetchPRCmd(branch string) tea.Cmd {
	return func() tea.Msg {
		entry, ok := lookupPR(branch)
		return prFetchedMsg{branch: branch, entry: entry, ok: ok}
	}
}

// lookupPR is best-effort — no network failures should crash the dashboard.
func lookupPR(branch string) (prCacheEntry, bool) {
	cmd := exec.Command("gh", "pr", "view", branch,
		"--json", "number,state,isDraft,statusCheckRollup")
	cmd.Env = nil // inherit
	out, err := cmd.Output()
	if err != nil {
		return prCacheEntry{When: time.Now()}, false
	}
	var data struct {
		Number            int    `json:"number"`
		State             string `json:"state"`
		IsDraft           bool   `json:"isDraft"`
		StatusCheckRollup []struct {
			State string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return prCacheEntry{When: time.Now()}, false
	}
	st := data.State
	if data.IsDraft {
		st = "DRAFT"
	}
	checks := "·"
	if len(data.StatusCheckRollup) > 0 {
		bad := 0
		good := 0
		for _, c := range data.StatusCheckRollup {
			switch c.State {
			case "SUCCESS":
				good++
			case "FAILURE", "TIMED_OUT", "ERROR":
				bad++
			}
		}
		switch {
		case bad > 0:
			checks = "✗"
		case good > 0:
			checks = "✓"
		}
	}
	return prCacheEntry{State: st, Checks: checks, Number: data.Number, When: time.Now()}, true
}

// --- cell helpers ----------------------------------------------------------

// Cell helpers return plain text only. Coloring happens at line level so width
// calculations stay correct.

func shortAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
