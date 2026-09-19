package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/sandbye/norn/internal/notify"
	"github.com/sandbye/norn/internal/plan"
	"github.com/sandbye/norn/internal/prompt"
	"github.com/sandbye/norn/internal/runlog"
	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/strand"
)

// Dashboard is a live view of all known worktree sessions across repos.
//
// Refresh cadence: 5s tick. For each row we attempt a fast `gh pr view`
// in the background to learn PR state. State file is the source of truth
// for which sessions exist; git worktree list reconciles dead sessions.
type Dashboard struct {
	cfg    config.Config
	store  *state.Store
	rows   []dashRow
	cursor int
	width  int
	height int
	err    error
	// landing is the strand whose gate is running, so the rail can say what
	// that gate is doing rather than leaving one line on screen for minutes.
	landing string
	// notice is a one-line answer to a key that did nothing, so a no-op key
	// does not read as a hang. Cleared by the next keypress.
	notice string

	// Run-log viewer: a headless role has no session to open, so this is the
	// only way to see what it is doing. Read-only, and refreshed on the tick
	// while a role is still writing to the file.
	// pane is the live strand norn is attached to, if any. It takes every key
	// but one while it is open.
	pane paneState

	// switcher is the go-to-strand picker, reachable from the rail and from
	// inside a pane.
	switcher switcherState

	// showBoard is the task board: one screen answering what is done and what
	// is outstanding, without reading three agent transcripts. boardCursor is
	// the strand selected in it.
	showBoard   bool
	boardCursor int
	// showPlan is the full plan of planRow, read before accepting it.
	showPlan bool
	planRow  dashRow
	// planCursor is the strand the plan view is on; planExpand shows its brief.
	planCursor int
	planExpand bool
	// boardTask pins the board to a task even when the rail's cursor is
	// elsewhere, which is how norn reopens it after the diff viewer.
	boardTask string

	showLog   bool
	logTask   string
	logRole   string
	logRow    dashRow // the role as it was when the viewer opened, for `i`
	logEvents []runlog.Event
	logErr    error
	quit      bool
	result    Result
	lastLoad  time.Time

	// scopeRepo: when non-empty, only sessions for this repo basename are shown.
	// Set automatically when `norn -d` runs inside a git repo. Press `a` to
	// clear and see all repos.
	scopeRepo string

	// prCache: branch -> cached PR result. TTL prevents re-fetch on every tick.
	prCache map[string]prCacheEntry

	filter filterState

	// markFrame animates the theme's rune mark (cycles through RuneMarks).
	markFrame int

	// cleanPath is set by `d`: the app switches to Clean focused on this
	// worktree. Dropping the store row is no longer an action — the row is
	// re-adopted from disk on the next tick, so removal has to mean the worktree.
	cleanPath string

	// Headless "summarize session" overlay (press `s`). Additive — does not
	// affect any existing navigation/launch flow.
	summarizing bool
	// summaryCancel stops the headless run behind the spinner, since `s` sits
	// next to `d` and `S` and a mistyped key must not cost a claude run.
	summaryCancel context.CancelFunc
	summary       string
	summaryBranch string
	summaryPath   string // worktree path, kept for `r` refresh
	summaryErr    error
	summaryCached bool              // current overlay came from cache
	showSummary   bool              // overlay open (opened intentionally with `s`)
	readyBranch   string            // summary finished, waiting to be viewed
	summaryCache  map[string]string // branch -> last summary text
	spinner       spinner.Model

	// agentSeen is the previous live state per worktree path, so a flip into
	// waiting fires once instead of every tick the state holds. nil until the
	// first load, which seeds it without notifying: a thread that was already
	// waiting when norn started is not news.
	agentSeen map[string]claude.AgentState

	// reply is the inline answer to a waiting thread.
	reply replyState
}

// needsUser reports whether a state is one the user has to act on.
func needsUser(s claude.AgentState) bool {
	return s == claude.StateWaiting || s == claude.StateStuck
}

// agentTransitions updates seen and returns the rows that just flipped into a
// state needing the user. A row first seen in that state is recorded silently:
// it is the flip that is worth a ping, not the condition. Returns nothing when
// seen is nil, which is the first load.
func agentTransitions(seen map[string]claude.AgentState, rows []dashRow) []dashRow {
	if seen == nil {
		return nil
	}
	var flipped []dashRow
	for _, r := range rows {
		was, known := seen[r.Path]
		seen[r.Path] = r.AgentState
		if !known {
			continue
		}
		if needsUser(r.AgentState) && !needsUser(was) {
			flipped = append(flipped, r)
		}
	}
	return flipped
}

// seedAgentStates records the current states without reporting any transition.
func seedAgentStates(rows []dashRow) map[string]claude.AgentState {
	seen := make(map[string]claude.AgentState, len(rows))
	for _, r := range rows {
		seen[r.Path] = r.AgentState
	}
	return seen
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
	DetachedAt    string            // short sha when the branch is gone (detached HEAD); "" normally
	PRState       string            // OPEN / DRAFT / MERGED / CLOSED / ""
	PRChecks      string            // ✓ / ✗ / · / ""
	PRPending     bool              // currently being fetched
	AgentState    claude.AgentState // live: working / waiting / idle / "" (ephemeral)
	Next          string            // .state.md `next:` action (ephemeral)
	Goal          string            // .state.md `goal:` one-liner, shown in the detail pane (ephemeral)
	Done          []string          // .state.md `done:` items — recent progress (ephemeral)
	Blocked       string            // .state.md `blocked:` (non-"none"); "" when clear (ephemeral)
	Question      string            // what the agent last said, only when waiting (ephemeral)
	TaskGoal      string            // owning task's goal, "" when standalone (ephemeral)
	TaskTrunk     string            // owning task's trunk branch (ephemeral)
	TaskBlocked   string            // why the owning task needs a person, "" when it does not (ephemeral)
	Bell          bool              // the strand rang the terminal bell and nobody has looked (ephemeral)
	Ahead         int               // commits this strand has that the trunk does not (ephemeral)
	Plan          *plan.Plan        // the fan-out this strand proposes, when it is a planner (ephemeral)
	// Uncommitted is work in the strand's tree that no commit holds. norn
	// merges commits, so this is work `L` cannot see. (ephemeral)
	Uncommitted bool
	// WaitsFor is the strand this one starts after, while that one has not
	// landed. Empty once it can start, so the board can say "waits for tests"
	// instead of leaving a row that looks idle for no reason. (ephemeral)
	WaitsFor string
}

type dashTickMsg time.Time

// landTickMsg redraws the rail while a landing's gate runs.
type landTickMsg time.Time

func landTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return landTickMsg(t) })
}

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
	branch    string
	text      string
	err       error
	cancelled bool
}

// summarizeCmd runs `claude -p` in the worktree to summarize recent work.
// Read-only: only Read + git-read tools are allowed, so no prompts, no mutation.
func summarizeCmd(ctx context.Context, dir, branch string) tea.Cmd {
	return func() tea.Msg {
		const prompt = "Summarize the work done on this branch in 3-6 terse bullet points: " +
			"what changed and why. Base it on `git log` against the default branch and the diff. " +
			"No preamble, just the bullets."
		res, err := claude.Run(ctx, dir, prompt, claude.Options{
			AllowedTools: []string{"Read", "Bash(git log *)", "Bash(git diff *)", "Bash(git status *)"},
		})
		if err != nil {
			if ctx.Err() != nil {
				return summaryMsg{branch: branch, cancelled: true}
			}
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
		// Restart the spinner: leaving the tab ends its tick chain, because only
		// the active view is delegated messages, and coming back to a frozen
		// spinner reads as a hung reply. The handler stops it again when nothing
		// is pending, so this costs one tick when idle.
		d.spinner.Tick,
	)
}

func (d Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if d.pane.open() {
			d.width, d.height = msg.Width, msg.Height
			cols, rows := d.paneSize()
			_ = d.pane.term.Resize(cols, rows)
		}
		d.width = msg.Width
		d.height = msg.Height

	case tea.MouseMsg:
		// The wheel belongs to whatever is under it. In a pane that is the
		// agent's own program, which scrolls its own history far better than a
		// copy of its screen could.
		if d.pane.open() && (msg.Type == tea.MouseWheelUp || msg.Type == tea.MouseWheelDown) {
			d.pane.term.Wheel(msg.Type == tea.MouseWheelUp, msg.X, max(msg.Y-1, 0))
		}
		return d, nil

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
				ctx, cancel := context.WithCancel(context.Background())
				d.summaryCancel = cancel
				return d, tea.Batch(summarizeCmd(ctx, d.summaryPath, d.summaryBranch), d.spinner.Tick)
			}
			d.showSummary = false
			d.summary = ""
			d.summaryErr = nil
			d.summaryCached = false
			return d, nil
		}

		// The plan reader owns the keyboard: it is a thing to read, and L from
		// here is the same accept as on the rail.
		if d.showPlan {
			strands := 0
			if d.planRow.Plan != nil {
				strands = len(d.planRow.Plan.Strands)
			}
			switch s {
			case "esc", "q", "S":
				d.showPlan, d.planExpand = false, false
			case "j", "down":
				if d.planCursor < strands-1 {
					d.planCursor++
				}
			case "k", "up":
				if d.planCursor > 0 {
					d.planCursor--
				}
			case "enter", "right":
				d.planExpand = !d.planExpand
			case "e":
				// $EDITOR already scrolls, searches and edits better than a
				// popover can, and an edit there is what L then creates.
				return d, editPlanCmd(d.planRow.TaskID)
			case "L":
				d.showPlan, d.planExpand = false, false
				d.notice, d.landing = "accepting "+d.planRow.Role+"…", d.planRow.Role
				return d, tea.Batch(landStrandCmd(d.cfg, d.planRow), landTick())
			}
			return d, nil
		}

		// The board owns the keyboard while it is open: it is a place to read
		// and to jump from, so a stray key must not act on the rail behind it.
		if d.showBoard {
			rows := d.boardRows(d.visibleRows())
			switch {
			case s == "esc" || s == "b" || s == "q":
				d.showBoard, d.boardTask = false, ""
			case s == "down" || s == "j":
				if d.boardCursor < len(rows)-1 {
					d.boardCursor++
				}
			case s == "up" || s == "k":
				if d.boardCursor > 0 {
					d.boardCursor--
				}
			case s == "right" || s == "enter":
				if row, ok := boardSelected(rows, d.boardCursor); ok {
					d.showBoard = false
					cols, paneRows := d.paneSize()
					return d, tea.Batch(openPaneCmd(row.TaskID, row.Role, row.Branch, cols, paneRows), paneTick())
				}
			case s == "d":
				// Review this strand's work before it lands. The diff view and
				// its review sink already exist; this only points them at a
				// strand and hands the result back to that strand's session.
				if row, ok := boardSelected(rows, d.boardCursor); ok && row.Branch != row.TaskTrunk {
					d.showBoard, d.quit = false, true
					d.result = Result{
						Action: ResultReview, Path: row.Path,
						Base: row.TaskTrunk, TaskID: row.TaskID, Role: row.Role,
					}
					return d, tea.Quit
				}
			case s == "S":
				// The board is where a split task is judged, so the plan that
				// made the split is readable from here too.
				if row, ok := boardSelected(rows, d.boardCursor); ok && row.Plan != nil {
					d.showPlan, d.planRow, d.planCursor, d.planExpand = true, row, 0, false
				}
			case s == "L":
				if row, ok := boardSelected(rows, d.boardCursor); ok && row.Ahead > 0 {
					d.showBoard = false
					d.notice, d.landing = "landing "+row.Role+"…", row.Role
					return d, tea.Batch(landStrandCmd(d.cfg, row), landTick())
				}
			}
			return d, nil
		}

		// The picker is above everything, including a pane: it is how you get
		// out of one and into another.
		if d.switcher.active {
			switch s {
			case "esc":
				d.switcher = switcherState{}
				return d, nil
			case "enter":
				return d.enterSelected()
			}
			if d.switcher.handleKey(s) {
				d.switcher.matched = switcherMatches(d.rows, d.switcher.query)
				return d, nil
			}
			return d, nil
		}

		// An open pane owns the keyboard. Exactly one key is norn's, and
		// everything else is encoded and handed to the agent untouched.
		if d.pane.open() {
			// Never trap: a pane whose attachment has ended lets any key out,
			// since a pane that eats every key with nothing on the other end
			// can only be escaped by killing the terminal.
			if d.pane.dead() {
				d.pane.close()
				return d, d.loadCmd()
			}
			leader := d.cfg.PaneLeaderKey()
			if d.pane.armed {
				d.pane.armed = false
				switch {
				case paneKeyIs(s, paneKeysLeave):
					d.pane.close()
					return d, d.loadCmd()
				case paneKeyIs(s, paneKeysNext), paneKeyIs(s, paneKeysPrev):
					return d.enterSibling(paneKeyIs(s, paneKeysNext))
				case s == "f":
					d.openSwitcher()
					return d, nil
				case s == "b":
					// The board for the task you are inside, which is the one
					// the pane belongs to rather than whatever the rail's
					// cursor happens to sit on.
					taskID := d.pane.taskID
					d.pane.close()
					d.showBoard, d.boardCursor, d.boardTask = true, 0, taskID
					return d, d.loadCmd()
				case s == leader:
					// Leader twice sends a literal one, so a key the agent
					// binds never becomes unreachable.
				default:
					// Not one of norn's, so the key goes to the agent below.
				}
			} else if s == leader {
				d.pane.armed = true
				return d, nil
			}
			if b := encodeKey(msg); len(b) > 0 {
				if err := d.pane.term.Write(b); err != nil {
					d.pane.err = err
				}
			}
			return d, nil
		}

		// Run-log viewer swallows keys the same way: `r` refreshes, anything
		// else closes it.
		if d.showLog {
			switch s {
			case "esc":
				d.closeLog()
				return d, nil
			case "tab":
				d.reply.grant = d.reply.grant.Next()
				return d, nil
			case "enter":
				text := strings.TrimSpace(d.reply.text)
				if text == "" {
					return d, nil
				}
				if why := replyRefusal(d.logRow); why != "" {
					d.reply.text, d.reply.sent = "", why
					return d, nil
				}
				d.reply.text, d.reply.sent, d.reply.pending = "", "", text
				return d, tea.Batch(
					replyCmd(d.logRow.Path, d.logRow.Branch, text, d.reply.grant, replyLogFor(d.logRow)),
					d.spinner.Tick,
				)
			}
			// Everything else types, the way every agent CLI behaves: a chat
			// pane that swallows a keystroke into an action is a pane you
			// cannot write "r" in.
			d.reply.handleKey(s)
			return d, nil
		}

		// Reply input: while open, letters type into the reply, so it is checked
		// before the filter and before any action key.
		if d.reply.active {
			switch s {
			case "esc":
				d.reply.active, d.reply.text = false, ""
				return d, nil
			case "tab":
				d.reply.grant = d.reply.grant.Next()
				return d, nil
			case "enter":
				text := strings.TrimSpace(d.reply.text)
				if text == "" {
					d.reply.active = false
					return d, nil
				}
				if why := replyRefusal(d.reply.target); why != "" {
					d.reply.active, d.reply.text = false, ""
					d.reply.sent = why
					return d, nil
				}
				path, branch := d.reply.path, d.reply.branch
				d.reply.active, d.reply.text = false, ""
				// Keep the sent text on screen: it is the only record of your own
				// half of the exchange, since the pane only ever shows the agent's.
				d.reply.pending, d.reply.sent = text, ""
				return d, tea.Batch(replyCmd(path, branch, text, d.reply.grant, replyLogFor(d.reply.target)), d.spinner.Tick)
			}
			if d.reply.handleKey(s) {
				return d, nil
			}
			return d, nil
		}

		// A summary runs in the background, so esc is what stops one started by
		// a mistyped key rather than waiting for a run nobody wants.
		if s == "esc" && d.summarizing && !d.filter.active {
			if d.summaryCancel != nil {
				d.summaryCancel()
				d.summaryCancel = nil
			}
			d.summarizing = false
			d.notice = "summary cancelled"
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
			d.notice = ""
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
		case "right":
			// A live strand is the thing itself; a finished one has only its
			// log. Same key for both, because from the rail they are one idea:
			// show me this strand.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.TaskID != "" && row.Role != "" && strand.Alive(row.TaskID, row.Role) {
					cols, rows := d.paneSize()
					d.notice = "attaching to " + row.Role + "…"
					return d, tea.Batch(openPaneCmd(row.TaskID, row.Role, row.Branch, cols, rows), paneTick())
				}
			}
			fallthrough
		case "b":
			// The task board. One screen for the whole task, because a split
			// task's truth is otherwise spread across three transcripts.
			if d.cursor < len(vis) && vis[d.cursor].TaskID != "" {
				d.showBoard, d.boardCursor, d.boardTask = true, 0, vis[d.cursor].TaskID
			} else {
				d.notice = "no task here: this thread is not part of one"
			}
			return d, nil
		case "f":
			d.openSwitcher()
			return d, nil
		case "l":
			// What a headless role is doing, and where you talk to it. Right
			// arrow because the rail is a list and the conversation is what
			// sits to the right of it. Only roles have one: an interactive
			// thread is opened with `o` instead.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.TaskID == "" || row.Role == "" {
					d.notice = "no run log: this thread is not a role in a split task"
					return d, nil
				}
				d.showLog, d.logTask, d.logRole, d.logRow = true, row.TaskID, row.Role, row
				d.logEvents, d.logErr = nil, nil
				// The input is live from the moment it opens: this is a
				// conversation with the role, not a file you happen to be
				// shown. reply.active keeps the keystrokes here rather than
				// letting the dashboard's own keys act on the row behind it.
				d.reply.active, d.reply.text, d.reply.sent = true, "", ""
				d.reply.path, d.reply.branch, d.reply.target = row.Path, row.Branch, row
				d.reply.grant = d.cfg.ReplyGrant()
				return d, loadRunLogCmd(row.TaskID, row.Role)
			}
		case "S":
			// Read the whole plan, not the trimmed version in the detail pane.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.Plan == nil {
					d.notice = planAbsence(row)
					return d, nil
				}
				d.showPlan, d.planRow, d.planCursor, d.planExpand = true, row, 0, false
				return d, nil
			}
		case "P":
			// Approve the pull request. The last gate in the pipeline is a
			// person reading the combined change, and an integrator that could
			// open a PR on its own would make every earlier gate decorative.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.TaskID == "" {
					d.notice = "not part of a task"
					return d, nil
				}
				d.notice = "told the integrator the PR is approved"
				return d, approvePRCmd(d.cfg, row.TaskID)
			}
		case "L":
			// Land this strand on the trunk. Explicit, because with a live pane
			// an exit also means "I quit to look at something", and a merge
			// commit is not undone casually.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				switch {
				case row.TaskID == "" || row.Role == "":
					d.notice = "only a strand lands: this thread is not part of a task"
				// A running strand can land: `L` is your decision, and what
				// merges is what it committed. Refusing while it ran made a
				// planning strand unlandable, since it stays alive waiting for
				// you and its run state never leaves "running".
				case row.Run == state.RunMerged:
					d.notice = row.Role + " is already landed"
				default:
					d.notice, d.landing = "landing "+row.Role+"…", row.Role
					return d, tea.Batch(landStrandCmd(d.cfg, row), landTick())
				}
				return d, nil
			}
		case "R":
			// Start this task's headless roles. Detached, so the rail keeps
			// rendering: the supervisor writes every transition to the store
			// and the rows pick it up on the normal tick.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if row.TaskID == "" {
					d.notice = "not part of a split task: make one with a role in the New tab"
					return d, nil
				}
				d.notice = "starting " + taskLabel(row) + "…"
				cols, rows := d.paneSize()
				return d, startRoleRunCmd(d.cfg, row.TaskID, cols, rows)
			}
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
		case "i":
			// Answer the focused thread without entering it. Gated on waiting:
			// see canReply.
			if d.cursor < len(vis) {
				row := vis[d.cursor]
				if !d.cfg.HeadlessClaude() || !claude.Available() {
					d.reply.sent = "reply needs the claude CLI"
					break
				}
				// Open the input whatever the thread's state. Refusing here left
				// the user typing into the dashboard without noticing, where
				// enter means "cd and quit": the check belongs at send time.
				d.reply.active = true
				d.reply.text = ""
				d.reply.path, d.reply.branch = row.Path, row.Branch
				d.reply.sent = ""
				d.reply.target = row
				d.reply.grant = d.cfg.ReplyGrant()
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
					ctx, cancel := context.WithCancel(context.Background())
					d.summaryCancel = cancel
					return d, tea.Batch(summarizeCmd(ctx, row.Path, row.Branch), d.spinner.Tick)
				}
			}
		case "d":
			// Hand off to Clean, focused on this worktree: that's where removal
			// lives (remote/merged state, stash-before-force, branch handling).
			if d.cursor < len(vis) {
				d.cleanPath = vis[d.cursor].Path
			}
		}

	case dashTickMsg:
		cmds := []tea.Cmd{
			d.loadCmd(),
			tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return dashTickMsg(t) }),
		}
		if d.showLog {
			// The agent is appending to the file as it works, so the viewer
			// follows it rather than showing the moment it was opened.
			cmds = append(cmds, loadRunLogCmd(d.logTask, d.logRole))
		}
		return d, tea.Batch(cmds...)

	case markTickMsg:
		if !active.Spin {
			return d, nil // static theme; let the tick lapse
		}
		d.markFrame++
		return d, markTick()

	case replySentMsg:
		sent := d.reply.pending
		d.reply.pending = ""
		switch {
		case msg.err != nil:
			d.reply.sent = "reply to " + msg.branch + " failed: " + msg.err.Error()
		default:
			d.reply.sent = "answered " + msg.branch + ": " + quoteOneLine(sent)
		}
		// The thread has moved: it was waiting, now it is working again. With
		// the conversation open, reload it too rather than waiting for the
		// tick: the answer is already in the log by the time this arrives.
		if d.showLog {
			return d, tea.Batch(d.loadCmd(), loadRunLogCmd(d.logTask, d.logRole))
		}
		return d, d.loadCmd()

	case dashLoadedMsg:
		// The cursor is an index, but the user is pointing at a thread. Rows
		// reorder on every tick now that they group by state, and a thread that
		// flips to waiting jumps the whole list, so without this the cursor
		// quietly lands on someone else's thread mid-keystroke.
		selected := ""
		if prev := d.visibleRows(); d.cursor >= 0 && d.cursor < len(prev) {
			selected = prev[d.cursor].Path
		}

		d.rows = msg.rows
		d.lastLoad = time.Now()
		if selected != "" {
			for i, r := range d.visibleRows() {
				if r.Path == selected {
					d.cursor = i
					break
				}
			}
		}
		if d.agentSeen == nil {
			d.agentSeen = seedAgentStates(d.rows)
		} else {
			for _, r := range agentTransitions(d.agentSeen, d.rows) {
				if d.cfg.Notify {
					notify.Notify("norn", r.Branch+" is waiting for you")
				}
			}
		}
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
			if d.rows[i].DetachedAt != "" {
				continue // branch is gone; `gh pr view` has nothing to resolve
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
		if d.reply.pending != "" {
			var cmd tea.Cmd
			d.spinner, cmd = d.spinner.Update(msg)
			return d, cmd
		}
		if d.summarizing {
			var cmd tea.Cmd
			d.spinner, cmd = d.spinner.Update(msg)
			return d, cmd
		}

	case logLoadedMsg:
		if msg.role == d.logRole {
			d.logEvents, d.logErr = msg.events, msg.err
		}

	case paneOpenedMsg:
		d.notice = ""
		if msg.err != nil {
			d.err = msg.err
			return d, nil
		}
		d.pane = paneState{term: msg.term, taskID: msg.taskID, role: msg.role, branch: msg.branch}
		return d, paneTick()

	case paneTickMsg:
		if !d.pane.open() {
			return d, nil // the pane closed; let the tick lapse
		}
		// The agent exiting is not norn's cue to close the pane: tmux holds the
		// dead pane, and its last screen is usually the thing you want to read.
		return d, paneTick()

	case planEditedMsg:
		// The file is the plan's truth, so re-read it rather than keeping what
		// was on screen before $EDITOR ran.
		switch {
		case msg.err != nil:
			d.notice = "plan: " + msg.err.Error()
		default:
			d.planRow.Plan = msg.plan
			d.planCursor = min(d.planCursor, max(len(msg.plan.Strands)-1, 0))
			for i := range d.rows {
				if d.rows[i].TaskID == msg.taskID && d.rows[i].Plan != nil {
					d.rows[i].Plan = msg.plan
				}
			}
		}
		return d, nil

	case landTickMsg:
		// Only while a gate runs: the rail's own 5s tick is too slow to show
		// seconds moving, and a faster tick the rest of the time is waste.
		if d.landing != "" && landingInFlight() {
			return d, landTick()
		}
		return d, nil

	case landedMsg:
		d.landing = ""
		switch {
		case msg.blocked:
			d.notice = ""
			d.err = fmt.Errorf("%s: %w", msg.role, msg.err)
		case msg.err != nil:
			d.notice = msg.role + ": " + msg.err.Error()
		default:
			d.notice = fmt.Sprintf("landed %s, %d commit(s) on the trunk", msg.role, msg.commits)
			if len(msg.created) > 0 {
				d.notice += " · created " + strings.Join(msg.created, ", ")
			}
			// Whatever was waiting for this role starts now, from a trunk that
			// contains it.
			cols, rows := d.paneSize()
			return d, tea.Batch(d.loadCmd(), spawnWaitingCmd(d.cfg, msg.taskID, msg.role, cols, rows))
		}
		return d, d.loadCmd()

	case runStartedMsg:
		// A failed start is the only thing worth interrupting for. What the
		// roles do next arrives through the store, on the tick.
		d.err = msg.err
		if msg.err == nil {
			d.notice = "strands running · → enters one"
			return d, d.loadCmd()
		}
		d.notice = ""

	case summaryMsg:
		if msg.cancelled {
			d.summarizing = false
			return d, nil
		}
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

// renderRunLog shows a headless role's own account of itself: one line per
// thing it said or did, newest at the bottom, the way you would watch a session
// you were sitting in.
func (d Dashboard) renderRunLog() string {
	title := headerStyle.Render("Run log: " + d.logRole)
	body := dimStyle.Render("nothing yet — the role has not written to its log")
	switch {
	case d.logErr != nil:
		body = errorStyle.Render("read log: " + d.logErr.Error())
	case len(d.logEvents) > 0:
		rows := max(d.height-8, 6)
		events := d.logEvents
		if len(events) > rows {
			events = events[len(events)-rows:]
		}
		w := max(d.width-8, 30)
		lines := make([]string, 0, len(events))
		for _, ev := range events {
			lines = append(lines, runLogLine(ev, w))
		}
		body = strings.Join(lines, "\n")
	}
	// The exchange in progress, then the prompt. Same shape as the agent CLIs:
	// what has happened scrolls above, you type at the bottom.
	var foot string
	switch {
	case d.reply.pending != "":
		foot = d.spinner.View() + dimStyle.Render(" ") + quoteOneLine(d.reply.pending)
	case d.reply.sent != "":
		foot = dimStyle.Render(truncate(d.reply.sent, max(d.width-8, 20)))
	}
	prompt := cursorStyle.Render("▸ ") + d.reply.text + cursorStyle.Render("▏") +
		dimStyle.Render("   ["+d.reply.grant.Label()+"]")

	out := title + "\n\n" + body
	if foot != "" {
		out += "\n\n" + foot
	}
	return out + "\n\n" + prompt + "\n" +
		dimStyle.Render("⏎ send · tab grant · esc back · follows the run · full JSONL: "+roleLogPath(d.logTask, d.logRole))
}

// runLogLine styles one event by kind: what it said, what it ran, how it ended.
func runLogLine(ev runlog.Event, w int) string {
	switch ev.Kind {
	case runlog.KindTool:
		return dimStyle.Render("  ▸ ") + branchStyle.Render(truncate(ev.Text, w-4))
	case runlog.KindResult:
		return activeStyle.Render("  ● " + truncate(ev.Text, w-4))
	case runlog.KindNote:
		return dimStyle.Render("  · " + truncate(ev.Text, w-4))
	default:
		return "  " + truncate(ev.Text, w-2)
	}
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

	if d.switcher.active {
		return d.renderSwitcher()
	}

	if d.showPlan {
		return d.renderPlanView()
	}

	if d.showBoard {
		return d.renderBoard(d.visibleRows())
	}

	if d.pane.open() {
		return d.renderPane()
	}

	if d.showLog {
		return d.renderRunLog()
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
	sidebarW := max(min(avail/3, 56), 24) // ~a third, balanced, but bounded
	if avail < 72 {                       // narrow panel: split in half, keep detail usable
		sidebarW = max(avail/2, 16)
	}
	detailW := max(avail-sidebarW-3, 20) // 3 = right border + gap
	// Body height tracks the panel frame()'s inner height (capped at frameHeight),
	// minus the header + footer + gaps, so the split fills the panel exactly:
	// tall enough to pin the footer to the bottom, not so tall it overflows and
	// clips the header. ~14 covers the mascot header, two gaps, and the footer.
	innerH := frameInnerHeight(d.height)
	bodyH := max(innerH-14, 6)

	sidebar := lipgloss.NewStyle().
		Width(sidebarW).
		Height(bodyH). // full-height so the right border is a clean vertical rule
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(colorSurface).
		Render(d.renderSidebar(vis, sidebarW, bodyH))

	var detail string
	if d.cursor >= 0 && d.cursor < len(vis) {
		detail = d.renderDetail(vis[d.cursor], detailW)
	}

	// Pin the split to a fixed height so the footer/help below doesn't jump as
	// the selected thread's detail grows or shrinks (goal present, more fields…).
	body := lipgloss.NewStyle().Height(bodyH).Render(
		lipgloss.JoinHorizontal(lipgloss.Top, sidebar, "  ", detail))

	// Reply line above the help: the input while open, otherwise the outcome of
	// the last one, so a failure does not vanish on the next tick.
	if d.reply.active {
		body += "\n\n" + cursorStyle.Render("reply "+d.reply.branch+" ▸ ") + d.reply.text + cursorStyle.Render("▏") +
			dimStyle.Render("   ["+d.reply.grant.Label()+"]")
	} else if d.reply.pending != "" && d.onReplyTarget(vis) {
		// A reply restarts real work, so this can run for minutes. Show the
		// spinner and the text, or it reads as a hang. Only while the cursor is
		// on that thread: it is that thread's state, not the dashboard's.
		body += "\n\n" + d.spinner.View() + dimStyle.Render(" "+d.reply.branch+" ▸ ") + quoteOneLine(d.reply.pending)
	} else if d.reply.sent != "" && d.onReplyTarget(vis) {
		// One line: claude's own errors run long, and a wrapped status pushes the
		// help line around under the table.
		body += "\n\n" + dimStyle.Render(truncate(d.reply.sent, max(avail-2, 20)))
	}

	if line := verifyLine(d.landing); line != "" {
		body += "\n\n" + activeStyle.Render(truncate(line, max(avail-2, 20)))
	} else if d.notice != "" {
		body += "\n\n" + dimStyle.Render(truncate(d.notice, max(avail-2, 20)))
	}

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

// renderHeader is the command-center hero: the "ᚾᛟᚱᚾ" rune mark in white beside
// a live gauge — one status glyph per thread (the same colored glyphs as the
// sidebar) — over the scope/status line. The rune mark is the identity; the
// gauge is the fleet at a glance; color only ever means status.
func (d Dashboard) renderHeader() string {
	scope := "all repos"
	if d.scopeRepo != "" {
		scope = d.scopeRepo
	}
	ident := dimStyle.Render(fmt.Sprintf("%s · scope: %s · %d live", ThreadWord(), scope, len(d.rows)))
	switch {
	case d.summarizing:
		ident += "\n" + d.spinner.View() + dimStyle.Render(" summarizing "+d.summaryBranch+"… esc stops it")
	case d.summaryErr != nil && d.readyBranch != "":
		ident += "\n" + errorStyle.Render("✗ summary failed: "+d.readyBranch+" (s retries)")
	case d.readyBranch != "":
		ident += "\n" + activeStyle.Render("✓ summary ready: "+d.readyBranch+" (press s)")
	}
	runes := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render("ᚾᛟᚱᚾ")
	var dots []string
	for _, r := range d.rows {
		dots = append(dots, stateGlyph(r.AgentState))
	}
	hero := runes
	if len(dots) > 0 {
		hero += "   " + strings.Join(dots, " ")
	}
	return hero + "\n\n" + ident
}

// Thread groups. The rail is a queue, not a log: what needs you sits at the
// top, what is running below it, what is quiet last. Ordering by group beats
// ordering by recency here, because recency does not tell you where to go next.
const (
	groupNeedsYou = iota
	groupWorking
	groupQuiet
)

var groupLabels = map[int]string{
	groupNeedsYou: "NEEDS YOU",
	groupWorking:  "WORKING",
	groupQuiet:    "QUIET",
}

// threadGroup buckets a row by its live agent state. idle and unknown share a
// bucket: both mean "nothing is happening here", and splitting them would put a
// header above a single row for no gain.
//
// A headless role is bucketed by its run state instead, because live agent
// state is read from a claude transcript and a role served by another agent has
// none: without this, a failed codex role would sit in QUIET.
func threadGroup(r dashRow) int {
	switch {
	case r.TaskBlocked != "" || r.Run == state.RunFailed:
		return groupNeedsYou
	// Waiting beats running. A strand whose process is alive but whose agent is
	// asking a question is the definition of needing you, and grouping it by
	// the process left it reading as WORKING until it timed out.
	case needsUser(r.AgentState) || r.Bell:
		return groupNeedsYou
	case r.Run == state.RunRunning, r.AgentState == claude.StateWorking:
		return groupWorking
	default:
		return groupQuiet
	}
}

// groupRows orders rows by group, keeping each group's existing order (activity,
// most recent first). Returns a new slice: the caller's order is a view, and the
// header gauge reads the same slice.
//
// A task's role worktrees move as one cluster: they stay adjacent, and the
// cluster sits in its most urgent member's bucket. Otherwise one waiting role
// would sit in NEEDS YOU with its siblings three headers down, and the task
// would read as unrelated threads again.
func groupRows(rows []dashRow) []dashRow {
	groups, lead := clusterGroups(rows)
	type keyed struct {
		row     dashRow
		group   int
		cluster int // original index of the cluster's first row; own index when standalone
		seq     int
	}
	keys := make([]keyed, len(rows))
	for i, r := range rows {
		k := keyed{row: r, group: groups[i], cluster: i, seq: i}
		if r.TaskID != "" {
			k.cluster = lead[r.TaskID]
		}
		keys[i] = k
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].group != keys[j].group {
			return keys[i].group < keys[j].group
		}
		if keys[i].cluster != keys[j].cluster {
			return keys[i].cluster < keys[j].cluster
		}
		return keys[i].seq < keys[j].seq
	})
	out := make([]dashRow, len(keys))
	for i, k := range keys {
		out[i] = k.row
	}
	return out
}

// clusterGroups returns each row's effective group and, per task, the index of
// its first row. A standalone row's effective group is its own; a task row's is
// the most urgent among its siblings, so every row of a task reports the same
// one. Ordering and the rail's headers both read this, or a header would land
// in the middle of a cluster.
func clusterGroups(rows []dashRow) ([]int, map[string]int) {
	bucket := map[string]int{}
	lead := map[string]int{}
	for i, r := range rows {
		if r.TaskID == "" {
			continue
		}
		g := threadGroup(r)
		if b, seen := bucket[r.TaskID]; !seen || g < b {
			bucket[r.TaskID] = g
		}
		if _, seen := lead[r.TaskID]; !seen {
			lead[r.TaskID] = i
		}
	}
	groups := make([]int, len(rows))
	for i, r := range rows {
		if r.TaskID != "" {
			groups[i] = bucket[r.TaskID]
			continue
		}
		groups[i] = threadGroup(r)
	}
	return groups, lead
}

// sidebarLine is one rendered row of the rail. row indexes into vis, or is -1
// for a group header, which is display only and never selectable.
type sidebarLine struct {
	text string
	row  int
}

// renderSidebar lists the threads (state glyph + branch + a right-aligned age)
// under a header per group, cursor highlighted, scrolled to keep the cursor
// visible. The age carries glanceable recency so the left rail reads as a live
// index, not a bare list.
//
// Scrolling windows over rendered lines rather than rows, because the group
// headers take vertical space too: windowing over rows would let the cursor
// slide off the bottom by as many lines as there are headers on screen.
func (d Dashboard) renderSidebar(vis []dashRow, w, h int) string {
	const ageW = 3
	branchW := max(w-ageW-3, 4) // glyph + two spaces + age column

	// With no live state at all (a non-claude agent, or no transcript yet) every
	// row is quiet, and a "QUIET" header over the whole list says nothing. Fall
	// back to the plain rail in that case.
	groups, _ := clusterGroups(vis)
	grouped := false
	for _, g := range groups {
		if g != groupQuiet {
			grouped = true
			break
		}
	}

	var all []sidebarLine
	lastGroup := -1
	lastTask := ""
	if !grouped {
		all = append(all, sidebarLine{dimStyle.Render(fitCell("THREADS", w)), -1})
	}
	for i, r := range vis {
		if g := groups[i]; grouped && g != lastGroup {
			all = append(all, sidebarLine{dimStyle.Render(fitCell(groupLabels[g], w)), -1})
			lastGroup = g
			lastTask = "" // a cluster split by the filter re-labels under its new header
		}
		// One header per task cluster, its rows indented under it. Standalone
		// rows take neither, so they render exactly as before.
		indent := ""
		if r.TaskID != "" {
			if r.TaskID != lastTask {
				// The header carries the task's progress, because "how far is
				// this" was otherwise only answerable from the board or from
				// git, and it is the question you ask every time you look.
				label := taskLabel(r) + taskProgress(d.rows, r.TaskID)
				all = append(all, sidebarLine{taskHeaderStyle.Render(fitCell("◈ "+label, w)), -1})
			}
			indent = "  "
		}
		lastTask = r.TaskID
		name := r.Branch
		if r.Role != "" {
			name = r.Role // under a task header the role is the distinguishing part
		}
		// A strand's row answers "is this mine to act on", so the right-hand
		// column is its status. Age only survives on a plain worktree, where
		// there is no status to show and staleness is the useful fact.
		status, statusStyle := strandStatus(r)
		tail, tailW := fmt.Sprintf("%*s", ageW, compactAge(r.LastActivityAt)), ageW
		if status != "" {
			tail, tailW = fmt.Sprintf("%-*s", statusWidth, truncate(status, statusWidth)), statusWidth
		}
		nameW := max(branchW-len(indent)-(tailW-ageW), 4)
		if i == d.cursor {
			plain := indent + glyphRune(r.AgentState) + " " + fitCell(name, nameW) + " " + tail
			all = append(all, sidebarLine{lipgloss.NewStyle().Foreground(colorBase).Background(colorLavender).Render(fitCell(plain, w)), i})
			continue
		}
		branch := fitCell(name, nameW)
		switch {
		case r.DetachedAt != "":
			branch = dirtyStyle.Render(branch) // branch deleted under the worktree
		case r.WorktreeAlive:
			branch = branchStyle.Render(branch)
		default:
			branch = dimStyle.Render(branch)
		}
		if status == "" {
			tail = dimStyle.Render(tail)
		} else {
			tail = statusStyle.Render(tail)
		}
		all = append(all, sidebarLine{indent + statusGlyph(r) + " " + branch + " " + tail, i})
	}
	if len(all) == 0 {
		return dimStyle.Render(fitCell("THREADS", w))
	}

	// Window over display lines, anchored on the cursor's own line.
	cursorLine := 0
	for i, l := range all {
		if l.row == d.cursor {
			cursorLine = i
		}
	}
	listH := max(h-1, 3) // 1 for the counter below
	start, end := scrollWindow(cursorLine, len(all), listH)

	lines := make([]string, 0, listH+1)
	for _, l := range all[start:end] {
		lines = append(lines, l.text)
	}
	if len(all) > listH {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  %d/%d", d.cursor+1, len(vis))))
	}
	return strings.Join(lines, "\n")
}

// taskLabel names a task cluster in the rail: its goal, falling back to the
// trunk branch when the task carries no goal yet. A blocked task says so in the
// header, since the conflict sits in the trunk worktree and not in the role row
// whose merge hit it.
func taskLabel(r dashRow) string {
	label := "task"
	switch {
	case r.TaskGoal != "":
		label = r.TaskGoal
	case r.TaskTrunk != "":
		label = r.TaskTrunk
	}
	if r.TaskBlocked != "" {
		label += " · blocked"
	}
	return label
}

// runMark suffixes a role row with what its headless run did, for the states
// worth a glance from the rail: failed and merged. Running needs no mark, since
// the group header already says WORKING.
func runMark(r dashRow) string {
	switch r.Run {
	case state.RunFailed:
		return " ✗"
	case state.RunMerged:
		return " ✓"
	case state.RunDone:
		return " ⤒" // finished and waiting to be landed
	}
	if r.Bell {
		return " !"
	}
	if r.Ahead > 0 {
		// Committed work the trunk does not have. A live strand that has
		// finished says so only inside its own pane, and "still running" is
		// true of the process while being wrong about the work.
		return " ⤒"
	}
	return ""
}

// compactAge is shortAge trimmed for the sidebar's narrow age column ("now"
// instead of "just now").
func compactAge(t time.Time) string {
	if a := shortAge(t); a != "just now" {
		return a
	}
	return "now"
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
	if r.DetachedAt != "" {
		b.WriteString(dirtyStyle.Render(truncate("branch gone · detached at "+r.DetachedAt, w)) + "\n")
	}
	if r.Goal != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Width(w).Foreground(colorText).Render(r.Goal) + "\n")
	}
	// next: the one action to take. It's the field read first, so it's promoted
	// above the rule and wrapped (never truncated) — a long next stays fully
	// readable, hang-indented under a teal arrow.
	if r.Next != "" {
		arrow := lipgloss.NewStyle().Foreground(colorTeal).Bold(true).Render("→ ")
		wrapped := lipgloss.NewStyle().Width(max(w-2, 8)).Foreground(colorText).Render(r.Next)
		for i, ln := range strings.Split(wrapped, "\n") {
			if i == 0 {
				b.WriteString("\n" + arrow + ln + "\n")
			} else {
				b.WriteString("  " + ln + "\n")
			}
		}
	}
	// The open question, when the thread is waiting. It outranks `next` in
	// urgency: `next` is the plan, this is what the thread is stopped on right
	// now, and reading it here is what saves a trip into the session.
	if r.Question != "" {
		b.WriteString("\n" + dimStyle.Render("asked") + "\n")
		for _, ln := range questionLines(r.Question, max(w-2, 8), questionPaneLines) {
			b.WriteString(dimStyle.Render("▏ ") + lipgloss.NewStyle().Foreground(colorText).Render(ln) + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render(strings.Repeat("─", w)) + "\n\n")
	const labelW = 7
	row := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString(dimStyle.Render(fitCell(k, labelW)) + v + "\n")
	}
	// wrapRow is row() for values that can run long: wrapped, hang-indented under
	// the label so a long blocker never overflows the pane.
	wrapRow := func(k, v string, vstyle lipgloss.Style) {
		if v == "" {
			return
		}
		wrapped := vstyle.Width(max(w-labelW, 8)).Render(v)
		for i, ln := range strings.Split(wrapped, "\n") {
			if i == 0 {
				b.WriteString(dimStyle.Render(fitCell(k, labelW)) + ln + "\n")
			} else {
				b.WriteString(strings.Repeat(" ", labelW) + ln + "\n")
			}
		}
	}
	// The plan goes above the state rows, not below them: it is what you are
	// deciding on, and the detail pane is a fixed box that clips whatever sits
	// at the bottom.
	if r.Plan != nil {
		b.WriteString(subtitleStyle.Render("proposes") + "\n")
		if r.Plan.Summary != "" {
			b.WriteString(dimStyle.Render("  "+truncate(r.Plan.Summary, max(w-4, 20))) + "\n")
		}
		const shown = 4 // the pane has no scrollback; the file holds the rest
		for i, st := range r.Plan.Strands {
			if i == shown {
				b.WriteString(dimStyle.Render(fmt.Sprintf("  +%d more", len(r.Plan.Strands)-shown)) + "\n")
				break
			}
			line := "  " + branchStyle.Render(st.Role)
			if st.After != "" {
				line += dimStyle.Render(" after " + st.After)
			}
			if st.Expect != "" {
				line += dimStyle.Render(" · " + st.Expect)
			}
			b.WriteString(line + dimStyle.Render("  "+truncate(oneLine(st.Brief), max(w-len(st.Role)-8, 20))) + "\n")
		}
		b.WriteString(activeStyle.Render("  S reads it · L creates these") + "\n\n")
	}

	row("state", glyphStyle(r.AgentState).Render(stateLabel(r.AgentState)))
	wrapRow("blocked", r.Blocked, dirtyStyle)
	row("pr", prDetail(r))
	row("last", shortAge(r.LastActivityAt))
	if r.TaskID != "" {
		row("task", taskLabel(r))
		row("role", r.Role)
		row("run", r.Run)
		wrapRow("task blocked", r.TaskBlocked, dirtyStyle)
	}
	row("kind", r.Kind)
	row("cu", r.ClickUpID)

	// Recent progress from .state.md `done:` — the last few wins, so a returning
	// session sees what already landed without opening the file.
	if len(r.Done) > 0 {
		start := max(len(r.Done)-3, 0)
		b.WriteString("\n" + dimStyle.Render("recent") + "\n")
		for _, item := range r.Done[start:] {
			b.WriteString("  " + activeStyle.Render("✓") + " " + dimStyle.Render(truncate(item, max(w-4, 8))) + "\n")
		}
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// questionPaneLines caps how much of the agent's last message the detail pane
// shows. Enough to read a question, not enough to bury the rest of the pane.
const questionPaneLines = 6

// questionLines wraps text to width and keeps the last max lines, because a
// question sits at the end of a message, after whatever preceded it.
func questionLines(text string, width, max int) []string {
	wrapped := lipgloss.NewStyle().Width(width).Render(strings.TrimSpace(text))
	lines := strings.Split(wrapped, "\n")
	// Drop trailing blanks first, or the tail window spends itself on padding.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
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
	if d.filter.active {
		return dimStyle.Render("type to filter · ↑/↓ or ctrl+n/p move · ⏎ cd · o open · esc clear")
	}
	if d.reply.active {
		return dimStyle.Render("type your answer · ⏎ send · ⇥ permission · ctrl+u clear · esc cancel")
	}
	// The comment above was a promise the code did not keep: one fixed list,
	// led by the key that leaves norn. Lead with what this row is asking for.
	vis := d.visibleRows()
	keys := "→ enter · b board · ? help"
	if d.cursor < len(vis) {
		switch status, _ := strandStatus(vis[d.cursor]); {
		case status == "needs you":
			keys = "→ enter · i answer · b board · ? help"
		case status == "uncommitted":
			keys = "→ enter · tell it to commit · b board · ? help"
		case strings.HasSuffix(status, "commit(s)"):
			keys = "d review · L land · → enter · b board · ? help"
		case status == "can start":
			keys = "R start · → enter · b board · ? help"
		case status == "landed":
			keys = "P approve pr · b board · ⏎ cd · ? help"
		case status == "failed":
			keys = "→ enter · R restart · b board · ? help"
		}
	}
	return dimStyle.Render(keys)
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

// stateFile is the live task state a worktree's .state.md carries, read once
// per reconcile for the dashboard detail pane. Empty fields when absent, so
// pre-contract threads degrade silently.
type stateFile struct {
	next, goal, blocked string
	done                []string
}

// worktreeState reads a worktree's .state.md once and extracts every field the
// dashboard shows (was two separate reads for next/goal).
func worktreeState(wtPath string) stateFile {
	data, err := os.ReadFile(wtPath + "/.state.md")
	if err != nil {
		return stateFile{}
	}
	s := string(data)
	return stateFile{
		next:    prompt.ExtractNext(s),
		goal:    prompt.ExtractGoal(s),
		blocked: prompt.ExtractBlocked(s),
		done:    prompt.ExtractDone(s),
	}
}

// loadCmd reloads the store and reconciles with live worktree list.
// Fast path only — PR data is fetched async via fetchPRCmd after this returns.
// planProposal is the plan this strand is still proposing: nil unless the
// strand is its repo's planning role, and nil once every strand it names
// exists. The planner is named by the strand's own repo, not by the repo norn
// was started in, or a plan would only ever show in one project.
func planProposal(store *state.Store, planner string, sess state.Session) *plan.Plan {
	if planner == "" || sess.Role != planner {
		return nil
	}
	p, err := plan.Read(sess.TaskID)
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, other := range store.Sessions {
		if other.TaskID == sess.TaskID {
			have[other.Role] = true
		}
	}
	for _, st := range p.Strands {
		if !have[st.Role] {
			return p
		}
	}
	return nil
}

// planAbsence says why there is nothing to read: a plan that was carried out
// is a different answer from a strand that never writes one.
func planAbsence(row dashRow) string {
	if _, err := plan.Read(row.TaskID); err == nil {
		return "this task's plan is already carried out: its strands exist"
	}
	return "no plan here: only a planning strand writes one"
}

// plannerCache resolves each worktree's planning role once per load: the role
// is declared in that repo's own config, and the dashboard spans repos.
type plannerCache map[string]string

func (c plannerCache) of(path string) string {
	if name, ok := c[path]; ok {
		return name
	}
	name := ""
	if cfg, err := config.Load(path); err == nil {
		if planner, ok := cfg.PlanningRole(); ok {
			name = planner
		}
	}
	c[path] = name
	return name
}

func (d Dashboard) loadCmd() tea.Cmd {
	scope := d.scopeRepo
	cfg := d.cfg
	// Live agent state is a claude-only signal (reads Claude Code's transcripts).
	useClaude := d.cfg.AgentCommand() == "claude"
	return func() tea.Msg {
		// Reconcile under the write lock: the dashboard reflects live worktrees,
		// not an append-only log. Drop rows whose path is gone or is the main
		// checkout, collapse duplicate rows sharing a worktree path, and
		// reconcile each survivor's branch/ClickUp id against the live checkout.
		store, err := state.Mutate(func(store *state.Store) bool {
			store.SortByActivity()
			before := len(store.Sessions)
			store.Prune(func(s state.Session) bool {
				return git.CheckoutClass(s.Path) == "worktree"
			})
			store.DedupeByPath()
			changed := len(store.Sessions) != before
			// Pruning rows can orphan a task, which would leave a header over
			// no threads.
			changed = store.PruneTasks() > 0 || changed

			// Adopt worktrees that exist on disk but have no row: a session dropped
			// with `d`, a worktree made by hand, or a store that lost the entry. The
			// dashboard is a view of live threads, so anything checked out belongs in
			// it — otherwise the only place it shows up is Clean, where the only
			// verb is delete.
			if adoptWorktrees(store, cfg.WorktreeDir) {
				changed = true
				store.SortByActivity()
			}

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
			return changed
		})
		if err != nil || store == nil {
			// A busy lock must not blank the dashboard: fall back to a plain
			// read and skip this tick's reconcile writeback.
			if store, err = state.Load(); err != nil || store == nil {
				return dashLoadedMsg{}
			}
		}

		planners := plannerCache{}
		rows := make([]dashRow, 0, len(store.Sessions))
		for _, sess := range store.Sessions {
			if scope != "" && sess.Repo != scope {
				continue
			}
			row := dashRow{Session: sess, WorktreeAlive: true}
			if task := store.FindTask(sess.TaskID); task != nil {
				row.TaskGoal, row.TaskTrunk, row.TaskBlocked = task.Goal, task.Trunk, task.Blocked
			}
			if sess.Role != "" && sess.TaskID != "" {
				row.Bell = strand.Rang(sess.TaskID, sess.Role)
				// The store says what norn last wrote; tmux says what is true.
				// Without this a strand that finished while you were elsewhere
				// still reads as running, and nothing offers to land it.
				row.Run = reconcileRun(sess, &row)
				row.Ahead = strandAhead(store, sess)
				// Uncommitted work is the difference between "wrote nothing"
				// and "wrote something nobody can land", and those look the
				// same on a row that only counts commits.
				row.Uncommitted = git.IsDirty(sess.Path)
				// A plan belongs to the strand that wrote it, and only until it
				// is carried out. Hanging it on every strand of the task asked
				// each of them to create strands that already exist.
				row.Plan = planProposal(store, planners.of(sess.Path), sess)
				// What this strand is waiting for, so a row that has not
				// started reads as sequenced rather than stuck.
				if sess.Run == "" {
					if after := waitFor(cfg, sess.TaskID, sess.Role, rolesOf(store.SessionsForTask(sess.TaskID))); after != "" && !hasLanded(store, sess.TaskID, after) {
						row.WaitsFor = after
					}
				}
			}
			if git.CurrentBranch(sess.Path) == "" {
				// Branch deleted under the worktree: label the sha so the row
				// still reads, and skip the PR lookup (there's no ref to ask about).
				row.DetachedAt = git.DetachedHead(sess.Path)
			}
			st := worktreeState(sess.Path)
			row.Next, row.Goal, row.Done, row.Blocked = st.next, st.goal, st.done, st.blocked
			if useClaude {
				st := claude.Probe(sess.Path)
				row.AgentState, row.Question = st.State, st.Question
			}
			rows = append(rows, row)
		}
		return dashLoadedMsg{rows: groupRows(rows)}
	}
}

// adoptWorktrees adds a session row for every linked worktree under root that
// the store doesn't know about, so a live checkout can never go invisible.
// Reports whether anything was added. The disk scan is filesystem-only; git is
// consulted per *unknown* path, so the steady state costs no processes.
func adoptWorktrees(store *state.Store, root string) bool {
	if root == "" {
		return false
	}
	added := false
	for _, path := range git.WorktreePaths(root) {
		if store.FindByPath(path) != nil {
			continue
		}
		branch := git.CurrentBranch(path)
		if branch == "" {
			// Detached (branch deleted under it): the dir name is the only name
			// left, and it's what the worktree was created as.
			if rel, err := filepath.Rel(root, path); err == nil {
				branch = trimKindPrefix(rel)
			}
		}
		if branch == "" {
			continue
		}
		repo := git.OriginRepoName(path)
		sess := state.Session{
			ID:        state.MakeID(repo, branch),
			Repo:      repo,
			Branch:    branch,
			Kind:      kindFromPath(root, path),
			Path:      path,
			Title:     worktreeTitle(path),
			ClickUpID: git.ClickUpID(branch),
			// Adopted, not started here: date it from the checkout itself so it
			// doesn't jump to the top of an activity-sorted list.
			StartedAt:      pathModTime(path),
			LastActivityAt: pathModTime(path),
		}
		store.UpsertByPath(sess)
		added = true
	}
	return added
}

// kindFromPath reads the worktree kind off the layout (<root>/<kind>/<branch>).
func kindFromPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "task"
	}
	if k, _, ok := strings.Cut(rel, string(filepath.Separator)); ok && (k == "task" || k == "review") {
		return k
	}
	return "task"
}

// trimKindPrefix drops the leading task/ or review/ segment, leaving the branch
// name the worktree dir encodes.
func trimKindPrefix(rel string) string {
	if k, rest, ok := strings.Cut(rel, string(filepath.Separator)); ok && (k == "task" || k == "review") {
		return rest
	}
	return rel
}

func pathModTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Now()
	}
	return fi.ModTime()
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

// closeLog leaves the conversation view and disarms its input.
//
// Both together, always: the input is armed while the view is open, and an exit
// that cleared only the view left every keystroke on the rail going into a
// reply nobody could see, so `enter` sent a message instead of cd'ing.
func (d *Dashboard) closeLog() {
	d.showLog = false
	d.logEvents, d.logErr = nil, nil
	d.reply.active, d.reply.text = false, ""
}

// reconcileRun answers what a strand is actually doing, from tmux rather than
// from what norn last wrote. A store value is a memory; the session is the fact.
func reconcileRun(sess state.Session, row *dashRow) string {
	st, err := strand.Read(sess.TaskID, sess.Role)
	switch {
	case err != nil:
		// No session: either it was never spawned, or it is finished and
		// already landed. Neither is "running".
		if sess.Run == state.RunRunning {
			setStrandRun(sess.Path, "")
			return ""
		}
		return sess.Run
	case st.Running:
		return state.RunRunning
	case st.ExitCode == 0:
		if sess.Run == state.RunMerged {
			return state.RunMerged
		}
		setStrandRun(sess.Path, state.RunDone)
		return state.RunDone
	default:
		setStrandRun(sess.Path, state.RunFailed)
		return state.RunFailed
	}
}

// setStrandRun persists a reconciled state, so the next reader agrees without
// asking tmux again.
func setStrandRun(path, run string) {
	_, _ = state.Mutate(func(s *state.Store) bool { return s.SetRun(path, run, 0) })
}

// strandAhead is how many commits a strand has that the trunk does not.
//
// Asked of git rather than of the store, because the integrating strand merges
// branches itself, and a board that only believed norn's own landing key would
// keep reporting work as outstanding after it had already shipped. Zero means
// nothing is waiting, whether it landed or was never written.
func strandAhead(store *state.Store, sess state.Session) int {
	task := store.FindTask(sess.TaskID)
	trunk := store.FindTaskTrunk(sess.TaskID)
	if task == nil || trunk == nil || sess.Branch == task.Trunk {
		return 0
	}
	n, err := git.CommitsAhead(trunk.Path, task.Trunk, sess.Branch)
	if err != nil {
		return 0
	}
	return n
}

// oneLine flattens a plan's brief for a single detail row: the full text lives
// in the file, and the pane is answering "what is this strand for".
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// taskProgress summarises a task in a few characters: how many strands have
// landed, and how many still hold work the trunk does not have.
func taskProgress(rows []dashRow, taskID string) string {
	var strands, landed, toLand, needsYou, canStart int
	trunkHasPR := false
	for _, r := range rows {
		if r.TaskID != taskID || r.Role == "" {
			continue
		}
		if r.Branch == r.TaskTrunk {
			trunkHasPR = r.PRNumber > 0
			continue // the trunk is what they land on, not one of them
		}
		strands++
		status, _ := strandStatus(r)
		switch {
		case status == "needs you" || status == "uncommitted" || status == "failed":
			needsYou++
		case r.Ahead > 0:
			toLand++
		case r.Run == state.RunMerged || r.Run == state.RunDone:
			landed++
		case status == "can start":
			canStart++
		}
	}
	if strands == 0 {
		return ""
	}
	// One line, and it names the next thing a person does rather than a ratio
	// that is the same whether the task is sequenced or stuck.
	switch {
	case needsYou > 0:
		return fmt.Sprintf(" · %d need you", needsYou)
	case toLand > 0:
		return fmt.Sprintf(" · %d strand(s) to land · L", toLand)
	case canStart > 0:
		return fmt.Sprintf(" · %d can start · R", canStart)
	case landed == strands && !trunkHasPR:
		return " · all landed · ready for review"
	}
	return fmt.Sprintf(" · %d/%d landed", landed, strands)
}
