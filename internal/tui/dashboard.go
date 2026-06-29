package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/work/internal/claude"
	"github.com/sandbye/work/internal/config"
	"github.com/sandbye/work/internal/git"
	"github.com/sandbye/work/internal/state"
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

	// Headless "summarize session" overlay (press `s`). Additive — does not
	// affect any existing navigation/launch flow.
	summarizing   bool
	summary       string
	summaryBranch string
	summaryPath   string            // worktree path, kept for `r` refresh
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
		for _, field := range []string{r.Branch, r.ClickUpID, r.Repo} {
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
	PRState       string // OPEN / DRAFT / MERGED / CLOSED / ""
	PRChecks      string // ✓ / ✗ / · / ""
	PRPending     bool   // currently being fetched
}

type dashTickMsg time.Time
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
		case "ctrl+c", "q", "esc":
			// esc only reaches here when not filtering (active filter eats it).
			d.quit = true
			return d, tea.Quit
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
		case "l", "c":
			// Launch/resume Claude in the worktree.
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
				if row.WorktreeAlive && claude.Available() {
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
			// Drop the session from the dashboard.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if fresh, err := state.Load(); err == nil && fresh != nil {
					fresh.Remove(row.ID)
					_ = fresh.Save()
				}
				return d, d.loadCmd()
			}
		}

	case dashTickMsg:
		return d, tea.Batch(
			d.loadCmd(),
			tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return dashTickMsg(t) }),
		)

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
			body = d.summary
		}
		return fmt.Sprintf("\n%s\n\n%s\n\n%s\n", title, body, dimStyle.Render("r refresh · any key to dismiss"))
	}

	header := titleStyle.Render("work") + " " + dimStyle.Render("dashboard")
	scope := "all repos"
	if d.scopeRepo != "" {
		scope = d.scopeRepo
	}
	header += dimStyle.Render(fmt.Sprintf("   scope: %s", scope))
	if !d.lastLoad.IsZero() {
		header += dimStyle.Render(fmt.Sprintf("   refreshed %s ago", shortAge(d.lastLoad)))
	}
	// Non-modal summary status — dashboard stays usable while a summary runs.
	switch {
	case d.summarizing:
		header += "   " + d.spinner.View() + dimStyle.Render(" summarizing "+d.summaryBranch+"…")
	case d.summaryErr != nil && d.readyBranch != "":
		header += errorStyle.Render("   ✗ summary failed: " + d.readyBranch + " (s retries)")
	case d.readyBranch != "":
		header += activeStyle.Render("   ✓ summary ready: " + d.readyBranch + " (press s)")
	}

	if len(d.rows) == 0 {
		var body string
		if d.scopeRepo != "" {
			body = dimStyle.Render(fmt.Sprintf("no sessions in %s yet — start one with `work \"hint\"`, or press `a` for all repos", d.scopeRepo))
		} else {
			body = dimStyle.Render("no sessions yet — start one with `work \"hint\"`")
		}
		return fmt.Sprintf("\n%s\n\n%s\n\n%s\n", header, body, d.dashKeyHelp())
	}

	// Columns are rendered as fixed-width plain text, then the full line gets
	// one outer style for cursor / dead state. This keeps padding correct —
	// ANSI escape codes are invisible to rune counts.
	const (
		branchW   = 38
		kindW     = 8
		clickupW  = 12
		prW       = 12
		statusW   = 14
		activityW = 10
	)
	colWidths := []int{branchW, kindW, clickupW, prW, statusW, activityW}
	headers := []string{"BRANCH", "KIND", "CU", "PR", "STATUS", "LAST"}

	hdrStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cdd6f4"))
	headerLine := joinCells(headers, colWidths)
	headerRow := hdrStyle.Render(headerLine)

	vis := d.visibleRows()

	var rows []string
	rows = append(rows, headerRow)
	for i, r := range vis {
		cells := []string{
			r.Branch,
			r.Kind,
			clickupCell(r.ClickUpID),
			prCell(r),
			statusCell(r),
			shortAge(r.LastActivityAt),
		}
		line := joinCells(cells, colWidths)
		switch {
		case i == d.cursor:
			line = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#1e1e2e")).
				Background(lipgloss.Color("#89b4fa")).
				Render(line)
		case !r.WorktreeAlive:
			line = dimStyle.Render(line)
		}
		rows = append(rows, line)
	}

	body := strings.Join(rows, "\n")

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

	return fmt.Sprintf("\n%s\n\n%s\n\n%s\n", header, body, d.dashKeyHelp())
}

// dashKeyHelp is context-aware: only shows actions that are available for the
// currently-focused row. Quieter UI, less guesswork.
func (d Dashboard) dashKeyHelp() string {
	if d.filter.active {
		return dimStyle.Render("type to filter · ↑/↓ or ctrl+n/p move · ⏎ cd · l claude · esc clear")
	}
	vis := d.visibleRows()
	parts := []string{"j/k move", "/ filter"}
	if d.cursor < len(vis) {
		row := vis[d.cursor]
		if row.WorktreeAlive {
			parts = append(parts, "⏎ cd", "l claude")
			if claude.Available() {
				parts = append(parts, "s summary")
			}
		}
		if row.PRNumber > 0 {
			parts = append(parts, "p pr")
		}
		if row.ClickUpID != "" {
			parts = append(parts, "t task")
		}
	}
	parts = append(parts, "r refresh", "d drop")
	if d.scopeRepo != "" {
		parts = append(parts, "a all repos")
	}
	parts = append(parts, "q quit")
	return dimStyle.Render(strings.Join(parts, " · "))
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

// loadCmd reloads the store and reconciles with live worktree list.
// Fast path only — PR data is fetched async via fetchPRCmd after this returns.
func (d Dashboard) loadCmd() tea.Cmd {
	cfg := d.cfg
	scope := d.scopeRepo
	return func() tea.Msg {
		store, err := state.Load()
		if err != nil || store == nil {
			return dashLoadedMsg{}
		}

		// Build a set of live worktree paths so we can mark dead ones.
		alive := map[string]bool{}
		common := ""
		wts, _ := git.ListWorktrees(cfg.WorktreeDir, common)
		for _, wt := range wts {
			alive[wt.Path] = true
		}

		store.SortByActivity()
		rows := make([]dashRow, 0, len(store.Sessions))
		for _, sess := range store.Sessions {
			if scope != "" && sess.Repo != scope {
				continue
			}
			rows = append(rows, dashRow{Session: sess, WorktreeAlive: alive[sess.Path]})
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
		Number  int    `json:"number"`
		State   string `json:"state"`
		IsDraft bool   `json:"isDraft"`
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

func clickupCell(id string) string {
	if id == "" {
		return "—"
	}
	return "CU-" + id
}

func prCell(r dashRow) string {
	if r.PRPending && r.PRNumber == 0 {
		return "…"
	}
	if r.PRNumber == 0 {
		return "—"
	}
	s := fmt.Sprintf("#%d", r.PRNumber)
	if r.PRChecks != "" {
		s += " " + r.PRChecks
	}
	return s
}

func statusCell(r dashRow) string {
	if !r.WorktreeAlive {
		return "dead"
	}
	if r.PRState != "" {
		return strings.ToLower(r.PRState)
	}
	return r.Status
}

// joinCells fits each cell to its column width: truncates with `…` if too long,
// pads with spaces if too short. Operates on plain text only.
func joinCells(cells []string, widths []int) string {
	var b strings.Builder
	for i, c := range cells {
		w := widths[i]
		runes := []rune(c)
		switch {
		case len(runes) > w-1 && w > 1:
			c = string(runes[:w-2]) + "…"
			runes = []rune(c)
		}
		b.WriteString(c)
		if len(runes) < w {
			b.WriteString(strings.Repeat(" ", w-len(runes)))
		}
	}
	return b.String()
}

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
