package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DiffFile is one file in the change set, exposed so callers can construct
// the view without depending on internal parsing.
type DiffFile struct {
	Path    string
	Added   int
	Removed int
}

// PRMeta is GitHub PR metadata used in the header when viewing a PR's diff.
type PRMeta struct {
	Number  int
	Title   string
	Author  string
	BaseRef string
	HeadRef string
	Commits int
}

// DiffView is the standalone file-diff TUI. Two construction modes:
//   - NewDiffView: local branch vs target ref. Per-file diffs loaded lazily.
//   - NewPRDiffView: a remote PR via gh. Per-file diffs are pre-fetched into a
//     map (one gh call total) — no per-file network round-trips.
type DiffView struct {
	repoRoot string
	target   string // "origin/<branch>" for local mode; "" for PR mode
	commits  int

	// PR mode: when non-nil, headers + diff source come from this.
	prMeta      *PRMeta
	prFileDiffs map[string]string

	files  []DiffFile
	cursor int

	mode       diffMode
	parsed     []parsedDiffLine // populated when a file's diff loads
	fileCursor int              // focused line index
	fileScroll int              // top of visible viewport
	visualStart int             // -1 when not in visual mode; else parsed index where selection began

	width  int
	height int

	warn string

	// Review state (PR mode only)
	pending        []PendingComment
	commentArea    textarea.Model
	commentLineIdx int // index into d.parsed for the comment being authored, or -1 for file-level
	commentEndIdx  int // for multi-line selection: last index in selection (== commentLineIdx for single)
	editingIdx     int // index into d.pending we're editing, or -1 for new

	reviewArea  textarea.Model
	reviewEvent string // "COMMENT" | "APPROVE" | "REQUEST_CHANGES"
	statusMsg   string // shown briefly after submit / errors
	submitting  bool   // true while a review POST is in flight
	spinner     spinner.Model
}

type diffMode int

const (
	modeList diffMode = iota
	modeFile
	modeComment // overlay: writing a comment on the focused line
	modeReview  // overlay: composing the review submission
)

// PendingComment is one comment buffered before submitting a review.
//
// Three shapes:
//   - line: Line + Side, SubjectType "line"
//   - multi-line: above + StartLine + StartSide (must equal Side)
//   - file-level: SubjectType "file", no line/side
type PendingComment struct {
	Path        string
	Line        int    // ignored for file-level
	Side        string // "LEFT" / "RIGHT" — ignored for file-level
	StartLine   int    // 0 when single-line
	StartSide   string // "LEFT" / "RIGHT" — empty when single-line
	SubjectType string // "line" (default) or "file"
	Body        string
}

// parsedDiffLine is one row of the file view.
type parsedDiffLine struct {
	kind    diffLineKind
	oldNum  int // 0 when not applicable
	newNum  int
	content string // line body without the leading +/- prefix
}

type diffLineKind int

const (
	kindMeta    diffLineKind = iota // diff --git, +++, ---, index, "new file"…
	kindHunk                        // @@ ... @@
	kindContext                     // unchanged
	kindAdded
	kindRemoved
)

type fileLoadedMsg struct {
	idx     int
	content string // raw diff text
}

func NewDiffView(repoRoot, target string, commits int, files []DiffFile, warn string) DiffView {
	return DiffView{
		repoRoot: repoRoot,
		target:   target,
		commits:  commits,
		files:    files,
		warn:     warn,
	}
}

// NewPRDiffView constructs the view for a remote PR. Per-file diffs are passed
// in already-fetched so the TUI doesn't shell out per file.
func NewPRDiffView(repoRoot string, meta PRMeta, files []DiffFile, perFileDiff map[string]string) DiffView {
	return DiffView{
		repoRoot:    repoRoot,
		commits:     meta.Commits,
		prMeta:      &meta,
		prFileDiffs: perFileDiff,
		files:       files,
		reviewEvent: "COMMENT",
		visualStart: -1,
		editingIdx:  -1,
	}
}

func (d DiffView) Init() tea.Cmd { return nil }

func (d DiffView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height
	case fileLoadedMsg:
		if msg.idx == d.cursor {
			d.parsed = parseDiff(msg.content)
			d.fileScroll = 0
			// Skip the leading meta + hunk rows so the cursor lands on the
			// first real code line, where comments are actually meaningful.
			d.fileCursor = firstCodeRow(d.parsed)
			d.adjustFileScroll()
		}
	case tea.KeyMsg:
		switch d.mode {
		case modeList:
			return d.updateList(msg)
		case modeFile:
			return d.updateFile(msg)
		case modeComment:
			return d.updateCommentMode(msg)
		case modeReview:
			return d.updateReviewMode(msg)
		}
	case reviewSubmittedMsg:
		d.submitting = false
		if msg.err != nil {
			d.statusMsg = "✗ submit failed: " + msg.err.Error()
			// Keep the user in review mode so they can retry / edit.
		} else {
			d.statusMsg = fmt.Sprintf("✓ review posted (%d comment(s))", len(d.pending))
			d.pending = nil
			d.mode = modeFile
		}
		return d, nil

	case spinner.TickMsg:
		if d.submitting {
			var cmd tea.Cmd
			d.spinner, cmd = d.spinner.Update(msg)
			return d, cmd
		}
	}

	// Forward unhandled events to the active text area so cursors blink, etc.
	var cmd tea.Cmd
	switch d.mode {
	case modeComment:
		d.commentArea, cmd = d.commentArea.Update(msg)
	case modeReview:
		d.reviewArea, cmd = d.reviewArea.Update(msg)
	}
	return d, cmd
}

// isCodeKind reports whether a parsed diff row is a real code line that the
// cursor (and `c` to comment) should target. Meta + hunk rows are skipped.
func isCodeKind(k diffLineKind) bool {
	return k == kindContext || k == kindAdded || k == kindRemoved
}

// firstCodeRow returns the index of the first code row in p, or 0 if none.
func firstCodeRow(p []parsedDiffLine) int {
	for i, r := range p {
		if isCodeKind(r.kind) {
			return i
		}
	}
	return 0
}

// nextCodeRow walks from `from` in `step` (+1 or -1), returning the next index
// whose row is a code line. If none exists in that direction, the cursor
// stops at the current position (no wrap).
func nextCodeRow(p []parsedDiffLine, from, step int) int {
	if len(p) == 0 {
		return 0
	}
	i := from + step
	for i >= 0 && i < len(p) {
		if isCodeKind(p[i].kind) {
			return i
		}
		i += step
	}
	return from
}

// adjustFileScroll keeps the cursor inside the viewport.
func (d *DiffView) adjustFileScroll() {
	vh := d.viewportHeight()
	if d.fileCursor < d.fileScroll {
		d.fileScroll = d.fileCursor
	}
	if d.fileCursor >= d.fileScroll+vh {
		d.fileScroll = d.fileCursor - vh + 1
	}
}

// --- review / comment overlays --------------------------------------------

type reviewSubmittedMsg struct {
	err error
}

func newCommentArea(width int) textarea.Model {
	ta := textarea.New()
	// Convention nudge — see ~/.claude/.../memory/review-comment-conventions.md
	ta.Placeholder = "nit | suggestion | issue (blocking) | question | praise: body…"
	ta.Focus()
	ta.ShowLineNumbers = false
	if width > 8 {
		ta.SetWidth(width - 4)
	}
	ta.SetHeight(6)
	return ta
}

func newReviewArea(width int) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "overall review summary (optional)"
	ta.Focus()
	ta.ShowLineNumbers = false
	if width > 8 {
		ta.SetWidth(width - 4)
	}
	ta.SetHeight(5)
	return ta
}

func (d DiffView) updateCommentMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		d.mode = modeFile
		return d, nil
	case "ctrl+s":
		body := strings.TrimSpace(d.commentArea.Value())
		if body == "" {
			return d, nil
		}
		pc := PendingComment{
			Path: d.files[d.cursor].Path,
			Body: body,
		}
		if d.commentLineIdx < 0 {
			pc.SubjectType = "file"
		} else {
			lo, hi := d.commentLineIdx, d.commentEndIdx
			if lo > hi {
				lo, hi = hi, lo
			}
			endRow := d.parsed[hi]
			pc.SubjectType = "line"
			pc.Side = "RIGHT"
			pc.Line = endRow.newNum
			if endRow.kind == kindRemoved {
				pc.Side = "LEFT"
				pc.Line = endRow.oldNum
			}
			// Multi-line: set start_line + start_side if the range spans >1 row.
			if lo != hi {
				startRow := d.parsed[lo]
				pc.StartSide = "RIGHT"
				pc.StartLine = startRow.newNum
				if startRow.kind == kindRemoved {
					pc.StartSide = "LEFT"
					pc.StartLine = startRow.oldNum
				}
				// GitHub: start/end must share a side. If they differ, drop the
				// start hint and just use the end as a single-line comment.
				if pc.StartSide != pc.Side {
					pc.StartLine = 0
					pc.StartSide = ""
				}
			}
		}
		if d.editingIdx >= 0 && d.editingIdx < len(d.pending) {
			d.pending[d.editingIdx] = pc
		} else {
			d.pending = append(d.pending, pc)
		}
		d.mode = modeFile
		return d, nil
	}
	var cmd tea.Cmd
	d.commentArea, cmd = d.commentArea.Update(msg)
	return d, cmd
}

func (d DiffView) updateReviewMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While a submit is in flight, ignore all keypresses except ctrl+c. This
	// prevents a double-submit when the user impatiently re-hits ctrl+s before
	// the spinner has rendered.
	if d.submitting {
		if msg.String() == "ctrl+c" {
			return d, tea.Quit
		}
		return d, nil
	}
	switch msg.String() {
	case "esc":
		d.mode = modeFile
		return d, nil
	case "tab":
		// Cycle COMMENT -> APPROVE -> REQUEST_CHANGES
		switch d.reviewEvent {
		case "COMMENT":
			d.reviewEvent = "APPROVE"
		case "APPROVE":
			d.reviewEvent = "REQUEST_CHANGES"
		default:
			d.reviewEvent = "COMMENT"
		}
		return d, nil
	case "ctrl+s":
		body := strings.TrimSpace(d.reviewArea.Value())
		// GitHub rules:
		//   APPROVE       — body + comments both optional
		//   COMMENT       — needs at least body OR comments
		//   REQUEST_CHANGES — body strongly recommended (and required if no comments)
		switch d.reviewEvent {
		case "COMMENT":
			if body == "" && len(d.pending) == 0 {
				d.statusMsg = "comment review needs a body or inline comments"
				return d, nil
			}
		case "REQUEST_CHANGES":
			if body == "" && len(d.pending) == 0 {
				d.statusMsg = "request-changes needs a body or inline comments"
				return d, nil
			}
		}
		// Lock submit and start a spinner so the user sees we're working.
		d.submitting = true
		d.statusMsg = ""
		sp := spinner.New()
		sp.Spinner = spinner.Dot
		sp.Style = lipgloss.NewStyle().Foreground(colorBlue)
		d.spinner = sp
		return d, tea.Batch(
			d.spinner.Tick,
			submitReviewCmd(d.prMeta.Number, d.reviewArea.Value(), d.reviewEvent, d.pending),
		)
	}
	var cmd tea.Cmd
	d.reviewArea, cmd = d.reviewArea.Update(msg)
	return d, cmd
}

// submitReviewCmd posts the pending review to GitHub via `gh api`. One single
// POST to /pulls/<n>/reviews with all inline comments + summary + event.
func submitReviewCmd(prNum int, summary, event string, comments []PendingComment) tea.Cmd {
	return func() tea.Msg {
		// Each comment is either line-level (path + line + side + body) or
		// file-level (path + subject_type=file + body). GitHub rejects the
		// payload if line/side are present on a file-level entry, so omit them.
		type apiComment struct {
			Path        string `json:"path"`
			Line        int    `json:"line,omitempty"`
			Side        string `json:"side,omitempty"`
			StartLine   int    `json:"start_line,omitempty"`
			StartSide   string `json:"start_side,omitempty"`
			SubjectType string `json:"subject_type,omitempty"`
			Body        string `json:"body"`
		}
		payload := struct {
			Body     string       `json:"body,omitempty"`
			Event    string       `json:"event"`
			Comments []apiComment `json:"comments,omitempty"`
		}{
			Body:  strings.TrimSpace(summary),
			Event: event,
		}
		for _, c := range comments {
			a := apiComment{Path: c.Path, Body: c.Body}
			if c.SubjectType == "file" {
				a.SubjectType = "file"
			} else {
				a.Line = c.Line
				a.Side = c.Side
				if c.StartLine > 0 && c.StartSide != "" {
					a.StartLine = c.StartLine
					a.StartSide = c.StartSide
				}
			}
			payload.Comments = append(payload.Comments, a)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return reviewSubmittedMsg{err: err}
		}

		// gh needs repo context for `/repos/{owner}/{repo}/...`; pass the
		// :owner/:repo placeholder via {owner}/{repo} which gh resolves.
		args := []string{
			"api", "-X", "POST",
			fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews", prNum),
			"--input", "-",
		}
		cmd := exec.Command("gh", args...)
		cmd.Stdin = strings.NewReader(string(raw))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return reviewSubmittedMsg{err: fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))}
		}
		return reviewSubmittedMsg{}
	}
}

func (d DiffView) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return d, tea.Quit
	case "j", "down":
		if d.cursor < len(d.files)-1 {
			d.cursor++
		}
	case "k", "up":
		if d.cursor > 0 {
			d.cursor--
		}
	case "g":
		d.cursor = 0
	case "G":
		d.cursor = len(d.files) - 1
	case "enter", "l", "right":
		if len(d.files) > 0 {
			d.mode = modeFile
			d.parsed = nil
			d.fileScroll = 0
			return d, d.loadCurrentFileCmd()
		}
	}
	return d, nil
}

// loadCurrentFileCmd dispatches to either the local git command or the
// already-fetched PR diff map.
func (d DiffView) loadCurrentFileCmd() tea.Cmd {
	idx := d.cursor
	path := d.files[idx].Path
	if d.prMeta != nil {
		content := d.prFileDiffs[path]
		return func() tea.Msg { return fileLoadedMsg{idx: idx, content: content} }
	}
	return loadFileDiffCmd(d.repoRoot, d.target, idx, path)
}

func (d DiffView) updateFile(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return d, tea.Quit
	case "c":
		// Line- or range-level comment. If a pending comment already exists on
		// this line/range, open the overlay pre-filled to edit. If visualStart
		// is set, comment spans visualStart..fileCursor.
		if d.prMeta == nil || d.fileCursor >= len(d.parsed) {
			return d, nil
		}
		row := d.parsed[d.fileCursor]
		if !(row.kind == kindContext || row.kind == kindAdded || row.kind == kindRemoved) {
			return d, nil
		}
		d.mode = modeComment
		d.commentLineIdx = d.fileCursor
		d.commentEndIdx = d.fileCursor
		if d.visualStart >= 0 {
			d.commentLineIdx = d.visualStart
			d.commentEndIdx = d.fileCursor
			d.visualStart = -1
		}
		// Look for an existing pending comment to edit.
		d.editingIdx = d.findPendingForCursor()
		d.commentArea = newCommentArea(d.width)
		if d.editingIdx >= 0 {
			d.commentArea.SetValue(d.pending[d.editingIdx].Body)
		}
		return d, textarea.Blink
	case "C":
		// File-level comment.
		if d.prMeta != nil {
			d.mode = modeComment
			d.commentLineIdx = -1
			d.commentEndIdx = -1
			d.editingIdx = -1
			d.commentArea = newCommentArea(d.width)
			return d, textarea.Blink
		}
		return d, nil
	case "D", "x":
		// Delete pending comment on the focused row (or file-level pending if
		// that's all the file has at this position).
		if d.prMeta != nil {
			if idx := d.findPendingForCursor(); idx >= 0 {
				d.pending = append(d.pending[:idx], d.pending[idx+1:]...)
				d.statusMsg = "deleted comment"
			}
		}
		return d, nil
	case "v":
		// Toggle visual mode — anchor a multi-line selection at the cursor.
		if d.prMeta != nil {
			if d.visualStart < 0 {
				d.visualStart = d.fileCursor
			} else {
				d.visualStart = -1
			}
		}
		return d, nil
	case "R":
		if d.prMeta != nil {
			d.mode = modeReview
			d.reviewArea = newReviewArea(d.width)
			return d, textarea.Blink
		}
		return d, nil
	case "esc", "h", "left":
		d.mode = modeList
	case "j", "down":
		d.fileCursor = nextCodeRow(d.parsed, d.fileCursor, +1)
		d.adjustFileScroll()
	case "k", "up":
		d.fileCursor = nextCodeRow(d.parsed, d.fileCursor, -1)
		d.adjustFileScroll()
	case "d", "ctrl+d":
		d.fileCursor += d.viewportHeight() / 2
		if d.fileCursor >= len(d.parsed) {
			d.fileCursor = max0(len(d.parsed) - 1)
		}
		d.adjustFileScroll()
	case "u", "ctrl+u":
		d.fileCursor -= d.viewportHeight() / 2
		if d.fileCursor < 0 {
			d.fileCursor = 0
		}
		d.adjustFileScroll()
	case "g":
		d.fileCursor = 0
		d.fileScroll = 0
	case "G":
		d.fileCursor = max0(len(d.parsed) - 1)
		d.adjustFileScroll()
	case "n":
		if d.cursor < len(d.files)-1 {
			d.cursor++
			d.parsed = nil
			return d, d.loadCurrentFileCmd()
		}
	case "p":
		if d.cursor > 0 {
			d.cursor--
			d.parsed = nil
			return d, d.loadCurrentFileCmd()
		}
	}
	return d, nil
}

func (d DiffView) viewportHeight() int {
	if d.height < 10 {
		return 10
	}
	return d.height - 5
}

func (d DiffView) View() string {
	switch d.mode {
	case modeList:
		return d.renderList()
	case modeFile:
		return d.renderFile()
	case modeComment:
		return d.renderCommentOverlay()
	case modeReview:
		return d.renderReviewOverlay()
	}
	return ""
}

func (d DiffView) renderCommentOverlay() string {
	var b strings.Builder
	b.WriteString("\n" + titleStyle.Render("work") + " " + subtitleStyle.Render("comment"))

	switch {
	case d.commentLineIdx < 0:
		b.WriteString(dimStyle.Render(fmt.Sprintf("   %s (file-level)", d.files[d.cursor].Path)))
		b.WriteString("\n\n")

	case d.commentLineIdx < len(d.parsed):
		row := d.parsed[d.commentLineIdx]
		side := "RIGHT"
		ln := row.newNum
		if row.kind == kindRemoved {
			side = "LEFT"
			ln = row.oldNum
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("   %s:%d (%s)", d.files[d.cursor].Path, ln, side)))
		b.WriteString("\n\n")
		// Show 2 lines of context above + the focused line + 2 below, so the
		// user has a glance at what they're commenting on without leaving the
		// overlay.
		b.WriteString(d.renderLineContext(d.commentLineIdx, d.commentLineIdx, 2))
		b.WriteString("\n")
	}

	b.WriteString(boxStyle.Render(d.commentArea.View()))
	b.WriteString("\n" + helpStyle.Render("ctrl+s save · esc cancel"))
	return b.String()
}

// renderLineContext renders rows around a focus index with the same renderer
// used in file view, so the user sees the comment's target with full syntax +
// row colouring. `lo` and `hi` mark a possible range (for multi-line); pass
// the same index twice for a single line.
func (d DiffView) renderLineContext(lo, hi, ctx int) string {
	if lo > hi {
		lo, hi = hi, lo
	}
	from := lo - ctx
	if from < 0 {
		from = 0
	}
	to := hi + ctx + 1
	if to > len(d.parsed) {
		to = len(d.parsed)
	}
	lexer := lexers.Match(d.files[d.cursor].Path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	masks := computeWordHighlights(d.parsed)
	var b strings.Builder
	for i := from; i < to; i++ {
		focused := i >= lo && i <= hi
		b.WriteString(d.renderRow(d.parsed[i], masks[i], lexer, focused, false) + "\n")
	}
	return b.String()
}

func (d DiffView) renderReviewOverlay() string {
	var b strings.Builder
	b.WriteString("\n" + titleStyle.Render("work") + " " + subtitleStyle.Render("review"))
	if d.prMeta != nil {
		b.WriteString(dimStyle.Render(fmt.Sprintf("   PR #%d · %d pending comment(s)", d.prMeta.Number, len(d.pending))))
	}
	b.WriteString("\n\n")

	// Event picker (tab to cycle)
	events := []string{"COMMENT", "APPROVE", "REQUEST_CHANGES"}
	var pickerParts []string
	for _, e := range events {
		if e == d.reviewEvent {
			pickerParts = append(pickerParts, selectedStyle.Render("["+e+"]"))
		} else {
			pickerParts = append(pickerParts, dimStyle.Render(" "+e+" "))
		}
	}
	b.WriteString("  " + strings.Join(pickerParts, "  ") + "\n\n")

	// Pending comments preview
	if len(d.pending) > 0 {
		b.WriteString(dimStyle.Render("  Pending:") + "\n")
		for _, c := range d.pending {
			snippet := c.Body
			if i := strings.IndexByte(snippet, '\n'); i >= 0 {
				snippet = snippet[:i] + "…"
			}
			if len(snippet) > 60 {
				snippet = snippet[:59] + "…"
			}
			loc := fmt.Sprintf("%s:%d", c.Path, c.Line)
			if c.SubjectType == "file" {
				loc = fmt.Sprintf("%s (file-level)", c.Path)
			}
			b.WriteString(fmt.Sprintf("  • %s  %s\n", loc, snippet))
		}
		b.WriteString("\n")
	}

	b.WriteString(boxStyle.Render(d.reviewArea.View()))
	b.WriteString("\n")

	if d.submitting {
		b.WriteString(lipgloss.NewStyle().Foreground(colorBlue).Bold(true).PaddingLeft(1).
			Render(d.spinner.View()+" submitting review to GitHub…") + "\n")
	} else if d.statusMsg != "" {
		// Surface failures (or any leftover hint) just above the help line.
		b.WriteString(helpStyle.Render(d.statusMsg) + "\n")
	}
	b.WriteString(helpStyle.Render("tab cycle event · ctrl+s submit · esc cancel"))
	return b.String()
}

// --- list mode -------------------------------------------------------------

func (d DiffView) renderList() string {
	var b strings.Builder

	hdr := titleStyle.Render("work") + " " + subtitleStyle.Render("diff")
	if d.prMeta != nil {
		// PR mode header: #N · title · author · base ← head
		hdr += dimStyle.Render(fmt.Sprintf("   PR #%d · %s ← %s · @%s · %d commit(s) · %d file(s)",
			d.prMeta.Number, d.prMeta.BaseRef, d.prMeta.HeadRef, d.prMeta.Author, d.commits, len(d.files)))
		b.WriteString("\n" + hdr + "\n")
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(colorText).Bold(true).PaddingLeft(1).Render(d.prMeta.Title) + "\n")
	} else {
		hdr += dimStyle.Render(fmt.Sprintf("   vs %s · %d commit(s) · %d file(s)",
			strings.TrimPrefix(d.target, "origin/"), d.commits, len(d.files)))
		b.WriteString("\n" + hdr + "\n")
	}

	if d.warn != "" {
		b.WriteString("\n" + errorStyle.Render(d.warn) + "\n")
	}
	if len(d.files) == 0 {
		b.WriteString("\n" + dimStyle.Render("no changes vs target") + "\n\n" + helpStyle.Render("q quit") + "\n")
		return b.String()
	}
	b.WriteString("\n")

	pathW := 0
	for _, f := range d.files {
		if w := lipgloss.Width(f.Path); w > pathW {
			pathW = w
		}
	}
	if pathW > d.width-25 && d.width > 30 {
		pathW = d.width - 25
	}

	for i, f := range d.files {
		marker := "  "
		if i == d.cursor {
			marker = cursorStyle.Render("› ")
		}
		path := truncateLeft(f.Path, pathW)
		path = padPlain(path, pathW)
		stats := fmt.Sprintf(" +%-4d -%-4d", f.Added, f.Removed)

		var line string
		if i == d.cursor {
			line = marker + selectedStyle.Render(path) + activeStyle.Render(stats)
		} else {
			line = marker + lipgloss.NewStyle().Foreground(colorText).Render(path) + dimStyle.Render(stats)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("j/k move · enter/l view file · q quit") + "\n")
	return b.String()
}

// --- file mode -------------------------------------------------------------

// Row washes — subtle backgrounds that fill the line width.
var (
	colorRemovedBG  = lipgloss.Color("#3a1f29")
	colorAddedBG    = lipgloss.Color("#1f3a2a")
	colorHunkBG     = lipgloss.Color("#1a2733")
	colorTokenHiRm  = lipgloss.Color("#5e1f30") // brighter red token bg
	colorTokenHiAdd = lipgloss.Color("#1f5e3a") // brighter green token bg
)

func (d DiffView) renderFile() string {
	var b strings.Builder

	f := d.files[d.cursor]
	hdr := titleStyle.Render("work") + " " + subtitleStyle.Render("diff") +
		dimStyle.Render(fmt.Sprintf("   %s", f.Path)) +
		activeStyle.Render(fmt.Sprintf("  +%d", f.Added)) +
		errorStyle.Render(fmt.Sprintf("  -%d", f.Removed))
	b.WriteString("\n" + hdr + "\n\n")

	if d.parsed == nil {
		b.WriteString(dimStyle.Render("loading…") + "\n\n" + helpStyle.Render("esc/h back · q quit") + "\n")
		return b.String()
	}

	// Pre-compute word-level change masks for adjacent removed/added pairs.
	// We only highlight changed tokens; pure additions/removals stay row-bg only.
	hiMasks := computeWordHighlights(d.parsed)

	// Choose lexer once per file.
	lexer := lexers.Match(f.Path)
	if lexer == nil {
		lexer = lexers.Fallback
	}

	vh := d.viewportHeight()
	end := d.fileScroll + vh
	if end > len(d.parsed) {
		end = len(d.parsed)
	}
	// Visual-mode range
	vLo, vHi := -1, -1
	if d.visualStart >= 0 {
		vLo, vHi = d.visualStart, d.fileCursor
		if vLo > vHi {
			vLo, vHi = vHi, vLo
		}
	}

	for i := d.fileScroll; i < end; i++ {
		hasComment := d.lineHasPending(i)
		focused := i == d.fileCursor || (vLo >= 0 && i >= vLo && i <= vHi)
		b.WriteString(d.renderRow(d.parsed[i], hiMasks[i], lexer, focused, hasComment) + "\n")
	}

	if len(d.parsed) > 0 {
		pages := (len(d.parsed) + vh - 1) / vh
		page := (d.fileScroll / vh) + 1
		if page > pages {
			page = pages
		}
		pct := (d.fileScroll + vh) * 100 / len(d.parsed)
		if pct > 100 {
			pct = 100
		}
		b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("[page %d/%d · %d%%]", page, pages, pct)))
	}
	help := "j/k line · d/u page · g/G top/bottom · n/p next/prev file · esc back · q quit"
	if d.prMeta != nil {
		vmark := ""
		if d.visualStart >= 0 {
			vmark = " · VISUAL"
		}
		help = fmt.Sprintf(
			"j/k · d/u · n/p · v select%s · c comment · C file · D delete · R review (%d) · esc · q",
			vmark, len(d.pending))
	}
	b.WriteString("\n" + helpStyle.Render(help))
	if d.statusMsg != "" {
		b.WriteString("\n" + helpStyle.Render(d.statusMsg))
	}
	b.WriteString("\n")
	return b.String()
}

// lineHasPending reports whether a pending comment targets this row (including
// rows inside a multi-line range).
func (d DiffView) lineHasPending(idx int) bool {
	if idx >= len(d.parsed) {
		return false
	}
	row := d.parsed[idx]
	path := d.files[d.cursor].Path
	for _, c := range d.pending {
		if c.Path != path || c.SubjectType == "file" {
			continue
		}
		ln := row.newNum
		if c.Side == "LEFT" {
			ln = row.oldNum
		}
		if c.StartLine > 0 {
			if ln >= c.StartLine && ln <= c.Line {
				return true
			}
		} else if ln == c.Line {
			return true
		}
	}
	return false
}

// findPendingForCursor returns the index of the pending comment whose target
// covers the focused line, or -1 if none.
func (d DiffView) findPendingForCursor() int {
	if d.fileCursor >= len(d.parsed) {
		return -1
	}
	row := d.parsed[d.fileCursor]
	path := d.files[d.cursor].Path
	for i, c := range d.pending {
		if c.Path != path || c.SubjectType == "file" {
			continue
		}
		ln := row.newNum
		if c.Side == "LEFT" {
			ln = row.oldNum
		}
		if c.StartLine > 0 {
			if ln >= c.StartLine && ln <= c.Line {
				return i
			}
		} else if ln == c.Line {
			return i
		}
	}
	return -1
}

// renderRow lays out one parsed line with gutter + prefix cell + syntax-coloured
// body, all sharing the same row background. focus = current cursor row.
// commented = a pending review comment targets this row.
func (d DiffView) renderRow(p parsedDiffLine, mask []bool, lexer chroma.Lexer, focus, commented bool) string {
	gutter := formatGutter(p)
	if commented {
		// Replace the leading space with a chat marker so the gutter shows ●
		gutter = "●" + gutter[1:]
	}

	switch p.kind {
	case kindMeta:
		return dimStyle.Render(strings.Repeat(" ", 11)) +
			lipgloss.NewStyle().Foreground(colorLavender).Render(p.content)
	case kindHunk:
		style := lipgloss.NewStyle().Background(colorHunkBG).Foreground(colorTeal)
		line := lipgloss.NewStyle().Background(colorHunkBG).Foreground(colorSubtext).Render(strings.Repeat(" ", 11)) +
			style.Render(p.content)
		return padToWidth(line, d.width, colorHunkBG)
	}

	var (
		rowBG     lipgloss.Color
		tokenHiBG lipgloss.Color
		prefix    string
	)
	switch p.kind {
	case kindAdded:
		rowBG = colorAddedBG
		tokenHiBG = colorTokenHiAdd
		prefix = "+"
	case kindRemoved:
		rowBG = colorRemovedBG
		tokenHiBG = colorTokenHiRm
		prefix = "-"
	default:
		rowBG = colorBase
		prefix = " "
	}

	gutterStyle := lipgloss.NewStyle().Background(rowBG).Foreground(colorSubtext)
	prefixStyle := lipgloss.NewStyle().Background(rowBG).Foreground(colorSubtext)
	if focus {
		// Subtle cyan tint on the gutter to show the cursor row without
		// changing the row body — keeps diff colors honest.
		gutterStyle = gutterStyle.Foreground(colorTeal).Bold(true)
		prefixStyle = prefixStyle.Foreground(colorTeal).Bold(true)
	}
	if commented {
		gutterStyle = gutterStyle.Foreground(colorPeach).Bold(true)
	}

	body := renderCodeLine(p.content, lexer, rowBG, tokenHiBG, mask)

	line := gutterStyle.Render(gutter) + prefixStyle.Render(" "+prefix+" ") + body
	return padToWidth(line, d.width, rowBG)
}

// padToWidth fills the rest of the row with `bg`-coloured spaces so the
// background wash extends to the right margin.
func padToWidth(line string, width int, bg lipgloss.Color) string {
	if width <= 0 {
		return line
	}
	cur := lipgloss.Width(line)
	if cur >= width {
		return line
	}
	pad := strings.Repeat(" ", width-cur)
	return line + lipgloss.NewStyle().Background(bg).Render(pad)
}

// formatGutter renders "  108  108 " — two 4-char number columns. Empty cells
// for added (no old) or removed (no new) lines.
func formatGutter(p parsedDiffLine) string {
	oldStr := "    "
	newStr := "    "
	if p.oldNum > 0 {
		oldStr = fmt.Sprintf("%4d", p.oldNum)
	}
	if p.newNum > 0 {
		newStr = fmt.Sprintf("%4d", p.newNum)
	}
	return " " + oldStr + " " + newStr + " "
}

// --- syntax + word-diff rendering -----------------------------------------

// renderCodeLine tokenises with chroma and emits each token coloured by its
// type, on the row background. A `mask` (one bool per byte of p.content) marks
// changed regions; those bytes get the brighter token-highlight background.
func renderCodeLine(content string, lexer chroma.Lexer, rowBG, tokenHiBG lipgloss.Color, mask []bool) string {
	it, err := lexer.Tokenise(nil, content)
	if err != nil {
		// fall back to flat text
		return lipgloss.NewStyle().Background(rowBG).Foreground(colorText).Render(content)
	}
	tokens := it.Tokens()

	var b strings.Builder
	pos := 0
	for _, tok := range tokens {
		fg := tokenFG(tok.Type)
		val := tok.Value
		if val == "" {
			continue
		}
		// Split the token by changed-mask runs so highlight tracks word boundaries.
		runs := splitByMask(val, mask, pos)
		for _, r := range runs {
			bg := rowBG
			if r.hi {
				bg = tokenHiBG
			}
			b.WriteString(lipgloss.NewStyle().Background(bg).Foreground(fg).Render(r.text))
		}
		pos += len(val)
	}
	return b.String()
}

type maskedRun struct {
	text string
	hi   bool
}

func splitByMask(s string, mask []bool, offset int) []maskedRun {
	if len(mask) == 0 {
		return []maskedRun{{text: s, hi: false}}
	}
	var runs []maskedRun
	cur := strings.Builder{}
	curHi := false
	for i := 0; i < len(s); i++ {
		hi := false
		if offset+i < len(mask) {
			hi = mask[offset+i]
		}
		if cur.Len() > 0 && hi != curHi {
			runs = append(runs, maskedRun{text: cur.String(), hi: curHi})
			cur.Reset()
		}
		cur.WriteByte(s[i])
		curHi = hi
	}
	if cur.Len() > 0 {
		runs = append(runs, maskedRun{text: cur.String(), hi: curHi})
	}
	return runs
}

// computeWordHighlights pairs up adjacent (removed, added) blocks and computes
// per-byte change masks via a simple word LCS. Returns a slice aligned with
// d.parsed where each entry is a mask over that line's content bytes.
func computeWordHighlights(rows []parsedDiffLine) [][]bool {
	masks := make([][]bool, len(rows))
	i := 0
	for i < len(rows) {
		if rows[i].kind != kindRemoved {
			i++
			continue
		}
		// Collect contiguous removed block
		rmStart := i
		for i < len(rows) && rows[i].kind == kindRemoved {
			i++
		}
		// Followed by added block?
		addStart := i
		for i < len(rows) && rows[i].kind == kindAdded {
			i++
		}
		if addStart == rmStart || addStart == i {
			continue // no pairing
		}
		// Pair lines 1:1 within the block (truncate to min length).
		rmCount := addStart - rmStart
		addCount := i - addStart
		n := rmCount
		if addCount < n {
			n = addCount
		}
		for k := 0; k < n; k++ {
			rm := rows[rmStart+k].content
			ad := rows[addStart+k].content
			rmMask, adMask := wordDiffMasks(rm, ad)
			masks[rmStart+k] = rmMask
			masks[addStart+k] = adMask
		}
	}
	return masks
}

// wordDiffMasks returns per-byte boolean masks marking which bytes of each
// line are part of a changed word. Algorithm: split into tokens (run of
// word-chars OR a single non-word char), LCS, mark non-LCS tokens.
func wordDiffMasks(a, b string) (am, bm []bool) {
	at := tokenize(a)
	bt := tokenize(b)
	lcs := tokenLCS(at, bt)

	am = make([]bool, len(a))
	bm = make([]bool, len(b))

	mark := func(toks []tokenSpan, mask []bool, common []bool) {
		for i, t := range toks {
			if common[i] {
				continue
			}
			for k := t.start; k < t.end && k < len(mask); k++ {
				mask[k] = true
			}
		}
	}
	mark(at, am, lcs.aCommon)
	mark(bt, bm, lcs.bCommon)
	return
}

type tokenSpan struct {
	val   string
	start int
	end   int
}

func tokenize(s string) []tokenSpan {
	var out []tokenSpan
	i := 0
	for i < len(s) {
		c := s[i]
		if isWord(c) {
			start := i
			for i < len(s) && isWord(s[i]) {
				i++
			}
			out = append(out, tokenSpan{val: s[start:i], start: start, end: i})
		} else {
			out = append(out, tokenSpan{val: string(c), start: i, end: i + 1})
			i++
		}
	}
	return out
}

func isWord(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

type lcsResult struct {
	aCommon []bool
	bCommon []bool
}

// tokenLCS marks which tokens of a and b participate in the LCS.
func tokenLCS(a, b []tokenSpan) lcsResult {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return lcsResult{make([]bool, n), make([]bool, m)}
	}
	// DP table of LCS lengths
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1].val == b[j-1].val {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] > dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	aCommon := make([]bool, n)
	bCommon := make([]bool, m)
	i, j := n, m
	for i > 0 && j > 0 {
		switch {
		case a[i-1].val == b[j-1].val:
			aCommon[i-1] = true
			bCommon[j-1] = true
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]:
			i--
		default:
			j--
		}
	}
	return lcsResult{aCommon, bCommon}
}

// --- chroma token → catppuccin colour --------------------------------------

func tokenFG(t chroma.TokenType) lipgloss.Color {
	switch t.Category() {
	case chroma.Keyword:
		return colorMauve
	case chroma.LiteralString, chroma.LiteralStringDouble, chroma.LiteralStringSingle:
		return colorGreen
	case chroma.LiteralNumber:
		return colorPeach
	case chroma.Comment:
		return colorOverlay
	case chroma.Operator:
		return colorPink
	case chroma.Punctuation:
		return colorSubtext
	case chroma.Name:
		// NameAttribute, NameFunction, NameClass have specific TokenTypes,
		// but Category() collapses them — handle individually below.
		switch t {
		case chroma.NameFunction, chroma.NameClass, chroma.NameNamespace, chroma.NameBuiltin:
			return colorBlue
		case chroma.NameAttribute, chroma.NameTag, chroma.NameDecorator:
			return colorYellow
		}
		return colorText
	case chroma.LiteralStringInterpol:
		return colorYellow
	}
	switch t {
	case chroma.GenericHeading, chroma.GenericSubheading:
		return colorLavender
	}
	return colorText
}

// --- diff parsing ----------------------------------------------------------

// parseDiff walks the raw `git diff` output and produces structured rows with
// old/new line numbers attached. Uses plain unified diff format (no word-diff
// markers) — word-level highlighting happens later in computeWordHighlights.
func parseDiff(raw string) []parsedDiffLine {
	var out []parsedDiffLine
	oldNum, newNum := 0, 0

	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "+++"),
			strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "new file"),
			strings.HasPrefix(line, "deleted file"),
			strings.HasPrefix(line, "rename "),
			strings.HasPrefix(line, "similarity index"),
			strings.HasPrefix(line, "Binary files"):
			out = append(out, parsedDiffLine{kind: kindMeta, content: line})

		case strings.HasPrefix(line, "@@"):
			o, n := parseHunkHeader(line)
			oldNum, newNum = o, n
			out = append(out, parsedDiffLine{kind: kindHunk, content: line})

		case strings.HasPrefix(line, "+"):
			out = append(out, parsedDiffLine{
				kind:    kindAdded,
				newNum:  newNum,
				content: line[1:],
			})
			newNum++

		case strings.HasPrefix(line, "-"):
			out = append(out, parsedDiffLine{
				kind:    kindRemoved,
				oldNum:  oldNum,
				content: line[1:],
			})
			oldNum++

		case strings.HasPrefix(line, " "):
			out = append(out, parsedDiffLine{
				kind:    kindContext,
				oldNum:  oldNum,
				newNum:  newNum,
				content: line[1:],
			})
			oldNum++
			newNum++
		}
	}
	return out
}

func parseHunkHeader(line string) (oldStart, newStart int) {
	if !strings.HasPrefix(line, "@@") {
		return 1, 1
	}
	end := strings.Index(line[2:], "@@")
	if end < 0 {
		return 1, 1
	}
	header := line[2 : 2+end]
	for _, p := range strings.Fields(header) {
		switch {
		case strings.HasPrefix(p, "-"):
			oldStart = parseLeadingInt(p[1:])
		case strings.HasPrefix(p, "+"):
			newStart = parseLeadingInt(p[1:])
		}
	}
	if oldStart == 0 {
		oldStart = 1
	}
	if newStart == 0 {
		newStart = 1
	}
	return
}

func parseLeadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n := 0
	for i := 0; i < end; i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// --- file loader -----------------------------------------------------------

func loadFileDiffCmd(repoRoot, target string, idx int, path string) tea.Cmd {
	return func() tea.Msg {
		// Plain unified diff — no --word-diff. Word-level highlighting is
		// computed in-process so we control row layout.
		cmd := exec.Command("git", "-C", repoRoot, "diff", target+"...HEAD", "--", path)
		out, err := cmd.Output()
		if err != nil {
			return fileLoadedMsg{idx: idx, content: fmt.Sprintf("error: %v\n", err)}
		}
		return fileLoadedMsg{idx: idx, content: string(out)}
	}
}

// --- small helpers ---------------------------------------------------------

func padPlain(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func truncateLeft(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-(n-1):])
}
