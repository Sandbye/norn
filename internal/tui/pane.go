package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

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
	foot := dimStyle.Render("  " + leader + " ← back · ↓/↑ strand · f go to · b board")
	if d.pane.armed {
		foot = cursorStyle.Render("  "+leader+" ▸ ") + dimStyle.Render("← back · ↓/↑ strand · f go to · b board · "+leader+" literal")
	}
	if d.pane.dead() {
		foot = errorStyle.Render("this attachment ended") + dimStyle.Render(" · any key returns to threads")
	}
	col, row := d.pane.term.Cursor()
	screen := drawCursor(strings.TrimRight(body, "\n"), col, row)
	return fmt.Sprintf("%s\n%s\n\n%s", title, indent(screen, paneMargin), foot)
}

// taskSpawnedMsg reports that a split create spawned every strand, so the app
// can open the trunk's pane instead of handing over the terminal.
type taskSpawnedMsg struct {
	taskID      string
	trunkRole   string
	trunkBranch string
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

// drawCursor marks the agent's cursor cell in the rendered screen.
//
// Drawn rather than placed: Bubble Tea v1 writes its own cursor move after the
// view, so an escape sequence inside the frame is overwritten and the real
// cursor ends up parked on norn's footer. Inverting the cell puts it where the
// agent thinks it is, which is the only place it is any use.
func drawCursor(body string, col, row int) string {
	lines := strings.Split(body, "\n")
	if row < 0 || row >= len(lines) {
		return body
	}
	// Column is in cells, and the line carries styling, so walk the visible
	// runes rather than indexing bytes.
	plain := paneANSI.ReplaceAllString(lines[row], "")
	runes := []rune(plain)
	if col < 0 {
		return body
	}
	for len(runes) <= col {
		runes = append(runes, ' ')
	}
	under := string(runes[col])
	if under == "" {
		under = " "
	}
	lines[row] = string(runes[:col]) + cursorCellStyle.Render(under) + string(runes[col+1:])
	return strings.Join(lines, "\n")
}

// paneHeader is the frame that says you are still in norn. One line naming the
// tree and this strand, one listing the task's other strands with their state,
// so being inside an agent does not cost you the view of the rest.
func (d Dashboard) paneHeader() string {
	mark := lipgloss.NewStyle().Bold(true).Foreground(colorLavender).Render("ᚾᛟᚱᚾ")
	line := "  " + mark + "  " + headerStyle.Render("⟡ "+d.pane.role)
	if d.pane.branch != "" {
		line += dimStyle.Render("  " + d.pane.branch)
	}

	var chips []string
	for _, r := range d.rows {
		if r.TaskID != d.pane.taskID || r.Role == "" {
			continue
		}
		chip := r.Role + strings.TrimSpace(runMark(r))
		switch {
		case r.Role == d.pane.role:
			chips = append(chips, cursorStyle.Render(chip))
		case r.Bell || r.Run == state.RunFailed:
			chips = append(chips, dirtyStyle.Render(chip))
		case r.Run == state.RunMerged:
			chips = append(chips, activeStyle.Render(chip))
		default:
			chips = append(chips, dimStyle.Render(chip))
		}
	}
	if len(chips) == 0 {
		return line
	}
	return line + "\n" + dimStyle.Render("    strands: ") + strings.Join(chips, dimStyle.Render(" · "))
}

// indent shifts a rendered screen right, without touching its styling.
func indent(body string, by int) string {
	pad := strings.Repeat(" ", by)
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}
