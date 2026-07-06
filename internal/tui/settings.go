package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/prompt"
)

// settingKind is how a setting row is edited.
type settingKind int

const (
	kindString settingKind = iota // inline text input
	kindBool                      // space/enter toggles
	kindPicker                    // choose from a list (+ custom)
	kindEditor                    // free-form: opens $EDITOR on the YAML
)

type settingRow struct {
	section string
	label   string
	keys    []string // yaml key path
	kind    settingKind
	choices []string // static picker options; template's are dynamic
}

func settingRows() []settingRow {
	return []settingRow{
		{"Agent", "command", []string{"agent", "command"}, kindPicker, []string{"claude", "opencode", "aider", "gemini"}},
		{"Agent", "ai_naming", []string{"ai_naming"}, kindBool, nil},
		{"Worktrees", "worktree_dir", []string{"worktree_dir"}, kindString, nil},
		{"Worktrees", "pr_base", []string{"pr_base"}, kindString, nil},
		{"Worktrees", "branch_base", []string{"branch_base"}, kindString, nil},
		{"Templates", "template", []string{"template"}, kindPicker, nil},
		{"Lists & policy", "base_branches", []string{"base_branches"}, kindEditor, nil},
		{"Lists & policy", "verify", []string{"verify"}, kindEditor, nil},
		{"Lists & policy", "clickup.lists", []string{"clickup", "lists"}, kindEditor, nil},
		{"Lists & policy", "forbid", []string{"forbid"}, kindEditor, nil},
	}
}

type settingsMode int

const (
	sModeList settingsMode = iota
	sModeText
	sModePick
)

type settingsModel struct {
	cfg         config.Config
	repoRoot    string
	globalPath  string
	projectPath string // "" when not inside a repo
	layer       int    // 0 global, 1 project
	rows        []settingRow
	cursor      int

	mode       settingsMode
	input      string
	picker     []string
	pickCursor int
	status     string

	width, height int
	quitting      bool
}

type editorDoneMsg struct{ err error }

// NewSettings builds the settings model. repoRoot may be "" (global only).
func NewSettings(cfg config.Config, repoRoot string) settingsModel {
	home, _ := os.UserHomeDir()
	m := settingsModel{
		cfg:        cfg,
		repoRoot:   repoRoot,
		globalPath: filepath.Join(home, ".config", "work", "config.yaml"),
		rows:       settingRows(),
	}
	if repoRoot != "" {
		m.projectPath = filepath.Join(repoRoot, ".work.yaml")
	}
	return m
}

func (m settingsModel) Init() tea.Cmd { return nil }

func (m settingsModel) activePath() string {
	if m.layer == 1 && m.projectPath != "" {
		return m.projectPath
	}
	return m.globalPath
}

func (m settingsModel) layerName() string {
	if m.layer == 1 && m.projectPath != "" {
		return "Project"
	}
	return "Global"
}

// reload re-reads the merged config so the display reflects the last write.
func (m *settingsModel) reload() {
	if cfg, err := config.Load(m.repoRoot); err == nil {
		m.cfg = cfg
	}
}

func (m *settingsModel) applyString(keys []string, val string) {
	ed, err := config.OpenEditor(m.activePath())
	if err != nil {
		m.status = "error: " + err.Error()
		return
	}
	if val == "" {
		ed.Delete(keys)
	} else {
		ed.SetString(keys, val)
	}
	if err := ed.Save(); err != nil {
		m.status = "error: " + err.Error()
		return
	}
	m.reload()
	m.status = fmt.Sprintf("saved %s → %s", strings.Join(keys, "."), m.layerName())
}

func (m *settingsModel) applyBool(keys []string, b bool) {
	ed, err := config.OpenEditor(m.activePath())
	if err != nil {
		m.status = "error: " + err.Error()
		return
	}
	ed.SetBool(keys, b)
	if err := ed.Save(); err != nil {
		m.status = "error: " + err.Error()
		return
	}
	m.reload()
	m.status = fmt.Sprintf("saved %s → %s", strings.Join(keys, "."), m.layerName())
}

// rowValue returns the value shown for a row and whether it's inherited (set in
// the other layer / a default rather than in the active layer's own file).
func (m settingsModel) rowValue(r settingRow) (val string, inherited bool) {
	if r.kind == kindEditor {
		return resolvedDisplay(m.cfg, r), false
	}
	if ed, err := config.OpenEditor(m.activePath()); err == nil {
		if v, ok := ed.GetString(r.keys); ok {
			return v, false
		}
	}
	return resolvedDisplay(m.cfg, r), true
}

// resolvedDisplay renders the effective (merged) value for a row.
func resolvedDisplay(cfg config.Config, r settingRow) string {
	switch strings.Join(r.keys, ".") {
	case "agent.command":
		return cfg.AgentCommand()
	case "ai_naming":
		return boolStr(cfg.AINaming)
	case "worktree_dir":
		return cfg.WorktreeDir
	case "pr_base":
		return orDash(cfg.PRBase)
	case "branch_base":
		return orDash(cfg.BranchBase)
	case "template":
		if cfg.Template != "" {
			return cfg.Template
		}
		return "task"
	case "base_branches":
		return "[" + strings.Join(cfg.BaseBranches, ", ") + "]"
	case "verify":
		return fmt.Sprintf("%d commands", len(cfg.Verify))
	case "clickup.lists":
		n := 0
		if cfg.ClickUp != nil {
			n = len(cfg.ClickUp.Lists)
		}
		return fmt.Sprintf("%d lists", n)
	case "forbid":
		return fmt.Sprintf("%d rules", len(cfg.Forbid))
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (m settingsModel) editYAML() tea.Cmd {
	path := m.activePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{err} })
}

func (m settingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case editorDoneMsg:
		m.reload()
		if msg.err != nil {
			m.status = "editor: " + msg.err.Error()
		} else {
			m.status = "reloaded from $EDITOR"
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case sModeText:
			return m.updateText(msg)
		case sModePick:
			return m.updatePick(msg)
		default:
			return m.updateList(msg)
		}
	}
	return m, nil
}

func (m settingsModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "tab":
		if m.projectPath != "" {
			m.layer = 1 - m.layer
			m.status = ""
		}
	case "e":
		return m, m.editYAML()
	case " ", "space":
		r := m.rows[m.cursor]
		if r.kind == kindBool {
			cur, _ := m.rowValue(r)
			m.applyBool(r.keys, cur != "on" && cur != "true")
		}
	case "enter":
		return m.enterEdit()
	}
	return m, nil
}

func (m settingsModel) enterEdit() (tea.Model, tea.Cmd) {
	r := m.rows[m.cursor]
	switch r.kind {
	case kindBool:
		cur, _ := m.rowValue(r)
		m.applyBool(r.keys, cur != "on" && cur != "true")
		return m, nil
	case kindEditor:
		return m, m.editYAML()
	case kindString:
		m.mode = sModeText
		if v, ok := config.OpenEditorValue(m.activePath(), r.keys); ok {
			m.input = v
		} else {
			m.input = ""
		}
		return m, nil
	case kindPicker:
		m.mode = sModePick
		m.picker = m.choicesFor(r)
		m.pickCursor = 0
		cur, _ := m.rowValue(r)
		for i, c := range m.picker {
			if c == cur {
				m.pickCursor = i
			}
		}
		return m, nil
	}
	return m, nil
}

func (m settingsModel) choicesFor(r settingRow) []string {
	var base []string
	if strings.Join(r.keys, ".") == "template" {
		base = prompt.List()
	} else {
		base = append(base, r.choices...)
	}
	return append(base, "(custom…)")
}

func (m settingsModel) updateText(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = sModeList
		m.input = ""
	case "enter":
		r := m.rows[m.cursor]
		m.applyString(r.keys, strings.TrimSpace(m.input))
		m.mode = sModeList
		m.input = ""
	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case "space":
		m.input += " "
	default:
		// Single-rune keys only; ignore ctrl/alt/arrow chords.
		if s := msg.String(); len(s) == 1 {
			m.input += s
		}
	}
	return m, nil
}

func (m settingsModel) updatePick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = sModeList
	case "up", "k":
		if m.pickCursor > 0 {
			m.pickCursor--
		}
	case "down", "j":
		if m.pickCursor < len(m.picker)-1 {
			m.pickCursor++
		}
	case "enter":
		choice := m.picker[m.pickCursor]
		if choice == "(custom…)" {
			m.mode = sModeText
			m.input = ""
			return m, nil
		}
		r := m.rows[m.cursor]
		m.applyString(r.keys, choice)
		m.mode = sModeList
	}
	return m, nil
}

func (m settingsModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder

	layer := selectedStyle.Render(" " + m.layerName() + " ")
	other := "Project"
	if m.layer == 1 {
		other = "Global"
	}
	switchHint := ""
	if m.projectPath != "" {
		switchHint = dimStyle.Render("  ⇥ " + other)
	} else {
		switchHint = dimStyle.Render("  (no project config here)")
	}
	b.WriteString(titleStyle.Render("norn settings") + "   " + layer + switchHint + "\n\n")

	lastSection := ""
	for i, r := range m.rows {
		if r.section != lastSection {
			b.WriteString(subtitleStyle.Render(r.section) + "\n")
			lastSection = r.section
		}
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
		}
		val, inherited := m.rowValue(r)
		valStr := val
		if r.kind == kindBool {
			if val == "true" || val == "on" {
				valStr = "✓ on"
			} else {
				valStr = "  off"
			}
		}
		shown := branchStyle.Render(valStr)
		if inherited {
			shown = dimStyle.Render(valStr + " ·inherited")
		}

		label := fmt.Sprintf("%-14s", r.label)
		if i == m.cursor {
			label = selectedStyle.Render(fmt.Sprintf("%-14s", r.label))
		}

		line := "  " + cursor + label + "  " + shown

		// Inline editors under the focused row.
		if i == m.cursor && m.mode == sModeText {
			line += "\n      " + subtitleStyle.Render("edit:") + " " + m.input + cursorStyle.Render("▎")
		}
		b.WriteString(line + "\n")

		if i == m.cursor && m.mode == sModePick {
			for j, c := range m.picker {
				pc := "    "
				lbl := c
				if j == m.pickCursor {
					pc = "    " + cursorStyle.Render("> ")
					lbl = selectedStyle.Render(c)
				}
				b.WriteString("    " + pc + lbl + "\n")
			}
		}
	}

	b.WriteString("\n")
	if m.status != "" {
		b.WriteString(activeStyle.Render(m.status) + "\n")
	}
	help := "j/k move · ⏎ edit · space toggle · e $EDITOR · ⇥ layer · q quit"
	switch m.mode {
	case sModeText:
		help = "type value · ⏎ save · esc cancel   (empty clears the key)"
	case sModePick:
		help = "j/k move · ⏎ select · esc cancel"
	}
	b.WriteString(helpStyle.Render(help))
	return b.String()
}
