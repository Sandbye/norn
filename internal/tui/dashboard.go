package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	// prCache: branch -> cached PR result. TTL prevents re-fetch on every tick.
	prCache map[string]prCacheEntry
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

func NewDashboard(cfg config.Config) Dashboard {
	store, _ := state.Load()
	if store == nil {
		store = &state.Store{}
	}
	store.SortByActivity()
	return Dashboard{cfg: cfg, store: store, prCache: map[string]prCacheEntry{}}
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
		switch msg.String() {
		case "ctrl+c", "q":
			d.quit = true
			return d, tea.Quit
		case "j", "down":
			if d.cursor < len(d.rows)-1 {
				d.cursor++
			}
		case "k", "up":
			if d.cursor > 0 {
				d.cursor--
			}
		case "g":
			d.cursor = 0
		case "G":
			d.cursor = len(d.rows) - 1
		case "r":
			return d, d.loadCmd()
		case "enter", "c":
			if d.cursor < len(d.rows) {
				row := d.rows[d.cursor]
				if row.WorktreeAlive {
					d.quit = true
					d.result = Result{Action: ResultResume, Path: row.Path}
					return d, tea.Quit
				}
			}
		case "p":
			if d.cursor < len(d.rows) {
				row := d.rows[d.cursor]
				if row.PRNumber > 0 {
					openPRInBrowser(row.Branch, row.Path)
				}
			}
		case "t":
			if d.cursor < len(d.rows) {
				row := d.rows[d.cursor]
				if row.ClickUpID != "" {
					openURL("https://app.clickup.com/t/" + row.ClickUpID)
				}
			}
		case "d":
			if d.cursor < len(d.rows) {
				row := d.rows[d.cursor]
				// Load fresh so we don't overwrite activity-tick bumps that
				// may have happened since the dashboard opened.
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
	}

	return d, nil
}

func (d Dashboard) View() string {
	if d.quit {
		return ""
	}

	header := titleStyle.Render("work") + " " + dimStyle.Render("dashboard")
	if !d.lastLoad.IsZero() {
		header += dimStyle.Render(fmt.Sprintf("   refreshed %s ago", shortAge(d.lastLoad)))
	}

	if len(d.rows) == 0 {
		body := dimStyle.Render("no sessions yet — start one with `work \"hint\"`")
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

	var rows []string
	rows = append(rows, headerRow)
	for i, r := range d.rows {
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

	if d.err != nil {
		body += "\n" + errorStyle.Render(fmt.Sprintf("error: %v", d.err))
	}

	return fmt.Sprintf("\n%s\n\n%s\n\n%s\n", header, body, d.dashKeyHelp())
}

// dashKeyHelp is context-aware: only shows actions that are available for the
// currently-focused row. Quieter UI, less guesswork.
func (d Dashboard) dashKeyHelp() string {
	parts := []string{"j/k move"}
	if d.cursor < len(d.rows) {
		row := d.rows[d.cursor]
		if row.WorktreeAlive {
			parts = append(parts, "c/⏎ claude")
		}
		if row.PRNumber > 0 {
			parts = append(parts, "p pr")
		}
		if row.ClickUpID != "" {
			parts = append(parts, "t task")
		}
	}
	parts = append(parts, "r refresh", "d drop", "q quit")
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
