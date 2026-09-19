package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/pty"
	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/strand"
)

// A live strand, rendered in norn. The pane attaches to the strand's tmux
// session in a pseudo-terminal and draws whatever the agent draws, so norn
// needs to understand nothing about that agent: its prompts, its slash
// commands, its permission dialogs and anything it ships next all work because
// nothing here interprets them.
//
// paneRefresh is how often the screen is re-read while a pane is open. Fast
// enough that typing feels local, and only ever one pane is open.
const paneRefresh = 40 * time.Millisecond

// paneMargin is the breathing room norn keeps around an agent's screen. One
// column: enough that text is not welded to the edge, cheap enough that the
// agent still lays out for something close to the real terminal.
const paneMargin = 1

// Inside a pane every keystroke belongs to the agent, so norn's own bindings
// sit behind a leader, the way tmux does it: leader, then one key. Pressing the
// leader twice sends a literal one through, so nothing the agent binds becomes
// unreachable.
//
// paneKeys are what the leader unlocks. They mirror the rail's own navigation:
// `→` enters a strand, so `←` leaves it, and `↓`/`↑` move between strands the
// way they move the cursor. Deliberately few, and the vim pair is kept as an
// alias because the rail has it too.
var (
	paneKeysLeave = []string{"left", "h", "esc", "q"}
	paneKeysNext  = []string{"down", "j"}
	paneKeysPrev  = []string{"up", "k"}
)

// paneKeyIs reports whether a key is one of a binding's spellings.
func paneKeyIs(s string, keys []string) bool {
	for _, k := range keys {
		if s == k {
			return true
		}
	}
	return false
}

// paneRow is the dashboard row for the strand in the pane, so a key pressed
// inside a pane can act on it without the person first finding its row.
func (d Dashboard) paneRow() (dashRow, bool) {
	for _, r := range d.rows {
		if r.TaskID == d.pane.taskID && r.Role == d.pane.role {
			return r, true
		}
	}
	return dashRow{}, false
}

// paneState is the attached strand, or the zero value when none is.
type paneState struct {
	term   *pty.Term
	taskID string
	role   string
	branch string
	err    error
	ended  bool // the attachment is gone: tmux exited, or the strand was killed

	// armed is set by the leader: the next key is norn's, not the agent's.
	armed bool
}

func (p paneState) open() bool { return p.term != nil }

// dead reports that nothing is listening any more, so keys must not be sent
// into it. A pane whose attachment died and still swallowed every key is how
// you end up force-quitting the terminal to escape norn.
func (p paneState) dead() bool {
	if p.term == nil {
		return true
	}
	select {
	case <-p.term.Done():
		return true
	default:
		return false
	}
}

// paneTickMsg repaints an open pane.
type paneTickMsg time.Time

// paneTick schedules the next repaint.
func paneTick() tea.Cmd {
	return tea.Tick(paneRefresh, func(t time.Time) tea.Msg { return paneTickMsg(t) })
}

// paneOpenedMsg carries the attached terminal back to the dashboard.
type paneOpenedMsg struct {
	taskID, role, branch string
	term                 *pty.Term
	err                  error
}

// openPaneCmd attaches to a strand's session in a pseudo-terminal sized to the
// space the pane will occupy.
func openPaneCmd(taskID, role, branch string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		if !strand.Available() {
			return paneOpenedMsg{taskID: taskID, role: role, branch: branch, err: strand.ErrNoTmux}
		}
		if !strand.Alive(taskID, role) {
			return paneOpenedMsg{taskID: taskID, role: role, branch: branch,
				err: fmt.Errorf("%w for %s", strand.ErrNoSession, role)}
		}
		term, err := pty.Start(strand.AttachCmd(taskID, role), cols, rows)
		return paneOpenedMsg{taskID: taskID, role: role, branch: branch, term: term, err: err}
	}
}

// close detaches norn from the strand. The agent keeps running: it belongs to
// tmux, and closing a pane is leaving the room, not ending the work.
func (p *paneState) close() {
	if p.term != nil {
		_ = p.term.Close()
	}
	*p = paneState{}
}

// encodeKey turns a Bubble Tea key into the bytes a terminal would send. This
// is the whole input path: no allowlist, no interpretation, no norn-specific
// handling, because the moment norn starts deciding which keys an agent may
// receive is the moment a pane stops being the real thing.
func encodeKey(msg tea.KeyMsg) []byte {
	switch msg.Type {
	case tea.KeyRunes:
		if msg.Alt {
			return append([]byte{0x1b}, []byte(string(msg.Runes))...)
		}
		return []byte(string(msg.Runes))
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyShiftTab:
		return []byte("\x1b[Z")
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	}
	// Control keys are contiguous: ctrl+a is 0x01 through ctrl+z at 0x1a, and
	// bubbletea names them in that order, so one arithmetic case covers the
	// whole range rather than twenty-six.
	if b, ok := ctrlByte(msg.String()); ok {
		return []byte{b}
	}
	return nil
}

// ctrlByte maps "ctrl+a".."ctrl+z" to their control bytes.
func ctrlByte(name string) (byte, bool) {
	rest, ok := strings.CutPrefix(name, "ctrl+")
	if !ok || len(rest) != 1 {
		return 0, false
	}
	c := rest[0]
	if c < 'a' || c > 'z' {
		return 0, false
	}
	return c - 'a' + 1, true
}

// renderPane draws the attached strand: its screen, and one line saying whose
// it is and how to get back.
func (d Dashboard) renderPane() string {
	title := d.paneHeader()
	body := d.pane.term.Screen()
	leader := d.cfg.PaneLeaderKey()
	foot := paneBarStyle.Width(max(d.width, 1)).Render("  " + leader + " ← back · ↓/↑ strand · d review · L land · f go to · b task")
	if d.pane.armed {
		foot = paneArmedStyle.Width(max(d.width, 1)).Render("  " + leader + " ▸  ← back · ↓/↑ strand · d review · L land · f go to · b task · " + leader + " literal")
	}
	if d.pane.dead() {
		foot = paneArmedStyle.Width(max(d.width, 1)).Render("  this attachment ended · any key returns to threads")
	}
	col, row := d.pane.term.Cursor()
	screen := drawCursor(strings.TrimRight(body, "\n"), col, row)
	// The screen sits between the two bars with one blank line above it, so the
	// agent's own output is not welded to norn's chrome, and the footer hugs
	// the bottom the way the header hugs the top.
	return fmt.Sprintf("%s\n\n%s\n%s", title, indent(screen, paneMargin), foot)
}

// taskSpawnedMsg reports that a split create spawned every strand, so the app
// can open the trunk's pane instead of handing over the terminal.
type taskSpawnedMsg struct {
	taskID      string
	trunkRole   string
	trunkBranch string
	failed      []string // strands that could not start, named with their error
}

// paneSize is the terminal the agent is told it has. The pane is the window
// minus the frame norn draws around it.
func (d Dashboard) paneSize() (cols, rows int) {
	return d.paneSizeFor(d.width, d.height)
}

// paneSizeFor is paneSize for a caller that knows the window but not the
// dashboard, which is the create path: it spawns the strands before the rail
// has ever rendered them.
func (d Dashboard) paneSizeFor(width, height int) (cols, rows int) {
	// Full width, and the height minus the one title line, the one footer line
	// and the blank line above each. An agent's UI is written for a terminal,
	// so anything norn keeps for itself is a column the agent does not get.
	// The agent gets the terminal minus norn's own chrome: two lines above
	// (mark, strands), two below (blank, keys), and a column of margin each
	// side so its output does not run into the edge of the screen.
	cols, rows = width-2*paneMargin, height-5
	if cols < 20 {
		cols = 20
	}
	if rows < 5 {
		rows = 5
	}
	return cols, rows
}

// enterSibling moves to the next or previous strand of the same task without
// going back to the rail, which is the move you make constantly once a task has
// three of them: read one, check another, come back.
func (d Dashboard) enterSibling(forward bool) (tea.Model, tea.Cmd) {
	var roles []dashRow
	for _, r := range d.rows {
		if r.TaskID == d.pane.taskID && r.Role != "" {
			roles = append(roles, r)
		}
	}
	if len(roles) < 2 {
		return d, nil
	}
	at := 0
	for i, r := range roles {
		if r.Role == d.pane.role {
			at = i
		}
	}
	step := len(roles) - 1
	if forward {
		step = 1
	}
	next := roles[(at+step)%len(roles)]

	cols, rows := d.paneSize()
	d.pane.close()
	return d, tea.Batch(openPaneCmd(next.TaskID, next.Role, next.Branch, cols, rows), paneTick())
}

// paneANSI matches the styling the emulator emits, so the cursor can be placed
// by visible column rather than by byte offset.
var paneANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// paneHeader is the bar that says you are still in norn while somebody else's
// UI fills the screen. One filled line rather than two dim ones: a glance has
// to say this is a strand of a task, not a bare terminal.
func (d Dashboard) paneHeader() string {
	left := paneMarkStyle.Render(" ᚾᛟᚱᚾ ") + paneTitleStyle.Render(" ⟡ "+d.pane.role+" ")
	if d.pane.branch != "" {
		// The task segment, not the whole branch: a truncated 40-character ref
		// says less than the part that differs between strands.
		leaf := d.pane.branch
		if i := strings.LastIndexByte(leaf, '/'); i >= 0 && i+1 < len(leaf) {
			leaf = leaf[:i]
		}
		left += paneBarStyle.Render("· " + truncate(leaf, max(d.width/3, 12)) + " ")
	}

	var chips []string
	for _, r := range d.rows {
		if r.TaskID != d.pane.taskID || r.Role == "" {
			continue
		}
		chip := " " + r.Role + runMark(r) + " "
		switch {
		case r.Role == d.pane.role:
			chips = append(chips, paneChipHereStyle.Render(chip))
		case r.Bell || r.Run == state.RunFailed:
			chips = append(chips, paneChipNeedsStyle.Render(chip))
		default:
			chips = append(chips, paneBarStyle.Render(chip))
		}
	}
	right := strings.Join(chips, paneBarStyle.Render("·")) + paneBarStyle.Render(" ")

	gap := d.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return paneBarStyle.Width(max(d.width, 1)).Render(left)
	}
	return left + paneBarStyle.Render(strings.Repeat(" ", gap)) + right
}

// drawCursor marks the agent's cursor cell in the rendered screen.
//
// Drawn rather than placed: Bubble Tea v1 writes its own cursor move after the
// view, so an escape inside the frame is overwritten and the real cursor ends
// up parked on norn's footer.
//
// The line's own styling is kept, which matters more than it sounds: an agent
// draws its input placeholder in faint text, and a version of this that
// stripped the line's escapes made that hint render like something you had
// typed.
func drawCursor(body string, col, row int) string {
	lines := strings.Split(body, "\n")
	if row < 0 || row >= len(lines) || col < 0 {
		return body
	}

	var out strings.Builder
	line := lines[row]
	visible := 0
	for i := 0; i < len(line); {
		if loc := paneANSI.FindStringIndex(line[i:]); loc != nil && loc[0] == 0 {
			out.WriteString(line[i : i+loc[1]]) // an escape occupies no column
			i += loc[1]
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if visible == col {
			out.WriteString(cursorCell(string(r)))
		} else {
			out.WriteRune(r)
		}
		visible++
		i += size
	}
	// A cursor past the end of the drawn text sits on empty space.
	for visible <= col {
		if visible == col {
			out.WriteString(cursorCell(" "))
		} else {
			out.WriteByte(' ')
		}
		visible++
	}

	lines[row] = out.String()
	return strings.Join(lines, "\n")
}

// cursorCell marks one cell as the cursor, turning off only what it turned on.
//
// Written by hand rather than with lipgloss, which closes a style with a full
// reset (\x1b[0m): inside an agent's line that also cancels whatever was
// active, so the faint text of an input placeholder rendered from the cursor
// onwards as ordinary white text, and read as something the user had typed.
// \x1b[27m ends reverse video and nothing else.
func cursorCell(s string) string { return "\x1b[7m" + s + "\x1b[27m" }

// indent shifts a rendered screen right, without touching its styling.
func indent(body string, by int) string {
	pad := strings.Repeat(" ", by)
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}
