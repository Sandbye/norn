package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/paths"
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
		{"Agent", "command", []string{"agent", "command"}, kindPicker, []string{"claude", "codex", "opencode", "aider", "gemini"}},
		// Models are per agent, so the row's choices are computed, not static.
		{"Agent", "model", []string{"agent", "model"}, kindPicker, nil},
		{"Agent", "ai_naming", []string{"ai_naming"}, kindBool, nil},
		{"Agent", "pane_leader", []string{"pane_leader"}, kindPicker, []string{"ctrl+a", "ctrl+s", "ctrl+x", "ctrl+space"}},
		{"Worktrees", "worktree_dir", []string{"worktree_dir"}, kindString, nil},
		{"Worktrees", "pr_base", []string{"pr_base"}, kindString, nil},
		{"Worktrees", "branch_base", []string{"branch_base"}, kindString, nil},
		{"Worktrees", "branch_format", []string{"branch_format"}, kindString, nil},
		{"Templates", "template", []string{"template"}, kindPicker, nil},
		{"Tasks", "provider", []string{"tasks", "provider"}, kindPicker, []string{"github", "clickup", "none"}},
		{"Appearance", "theme", []string{"theme"}, kindPicker, nil},
		{"Appearance", "notify", []string{"notify"}, kindBool, nil},
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

// settingLayer is one config file the settings screen can edit.
type settingLayer struct {
	name string
	path string
}

type settingsModel struct {
	cfg      config.Config
	repoRoot string
	layers   []settingLayer // every repo, this repo (you), this repo (team)
	// onlySet narrows the list to what this scope sets itself, the way an
	// editor's "modified" filter does: the common question is "what does this
	// file actually decide", and the full list buries it.
	onlySet bool
	layer   int // index into layers
	rows    []settingRow
	cursor  int

	mode       settingsMode
	input      string
	picker     []string
	pickCursor int
	status     string

	width, height int
}

type editorDoneMsg struct{ err error }

// NewSettings builds the settings model. repoRoot may be "" (global only).
func NewSettings(cfg config.Config, repoRoot string) settingsModel {
	// Named for what they mean rather than for where they live: "which file is
	// this" is a question about norn's internals, and "who does this apply to"
	// is the one you are actually answering.
	layers := []settingLayer{
		{"every repo", filepath.Join(paths.Config(), "config.yaml")},
	}
	if repoRoot != "" {
		if p := config.ProjectConfigPath(repoRoot); p != "" {
			layers = append(layers, settingLayer{"this repo, just me", p})
		}
		layers = append(layers, settingLayer{"this repo, the team", paths.RepoConfig(repoRoot)})
	}
	return settingsModel{
		cfg:      cfg,
		repoRoot: repoRoot,
		layers:   layers,
		rows:     settingRows(),
	}
}

func (m settingsModel) Init() tea.Cmd { return nil }

func (m settingsModel) activePath() string { return m.layers[m.layer].path }
func (m settingsModel) layerName() string  { return m.layers[m.layer].name }

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
	if len(keys) == 1 && keys[0] == "theme" {
		ApplyTheme(m.cfg.Theme) // repaint immediately
	}
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
	case "agent.model":
		if cfg.Agent.Model != "" {
			return cfg.Agent.Model
		}
		return "default"
	case "pane_leader":
		return cfg.PaneLeaderKey()
	case "ai_naming":
		return boolStr(cfg.AINaming)
	case "notify":
		return boolStr(cfg.Notify)
	case "worktree_dir":
		return cfg.WorktreeDir
	case "pr_base":
		return orDash(cfg.PRBase)
	case "branch_base":
		return orDash(cfg.BranchBase)
	case "branch_format":
		if cfg.BranchFormat != "" {
			return cfg.BranchFormat
		}
		return git.DefaultBranchFormat
	case "template":
		if cfg.Template != "" {
			return cfg.Template
		}
		return "task"
	case "tasks.provider":
		if cfg.Tasks.Provider != "" {
			return cfg.Tasks.Provider
		}
		return "none"
	case "theme":
		if cfg.Theme != "" {
			return cfg.Theme
		}
		return "nord"
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
	cmd := config.EditorCommand(path)
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
	// q/esc/ctrl+c aren't handled here: in list mode the App owns them globally
	// (esc → Threads, q → quit). They only reach this model in edit modes.
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visibleRows())-1 {
			m.cursor++
		}
	case "right", "l":
		if len(m.layers) > 1 {
			m.layer = (m.layer + 1) % len(m.layers)
			m.status = ""
		}
	case "left", "h":
		if len(m.layers) > 1 {
			m.layer = (m.layer - 1 + len(m.layers)) % len(m.layers)
			m.status = ""
		}
	case "m":
		m.onlySet = !m.onlySet
		m.cursor = 0
		m.status = ""
	case "r":
		// Remove this key from this scope, so the value falls back to whatever
		// the layer beneath sets. Editing yaml by hand was the only way to undo
		// a setting before.
		r := m.visibleRows()[m.cursor]
		if !m.setHere(r) {
			m.status = strings.Join(r.keys, ".") + " is not set in " + m.layerName()
			break
		}
		m.unset(r.keys)
	case "e":
		return m, m.editYAML()
	case " ", "space":
		r := m.visibleRows()[m.cursor]
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
	r := m.visibleRows()[m.cursor]
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
	switch strings.Join(r.keys, ".") {
	case "template":
		base = prompt.List()
	case "agent.model":
		base = agentModels(m.cfg.AgentCommand())
	case "theme":
		return ThemeNames() // fixed set, no custom
	default:
		base = append(base, r.choices...)
	}
	return append(base, "(custom…)")
}

// agentModels lists the models worth offering for an agent. Claude has stable
// aliases norn can name. For every other vendor it cannot: a hardcoded list of
// model ids is wrong within a release, so codex is offered the model its own
// config already records and everyone else gets the free-text entry.
func agentModels(agent string) []string {
	switch agent {
	case "claude":
		return []string{"sonnet", "opus", "haiku"}
	case "codex":
		if m := codexModel(); m != "" {
			return []string{m}
		}
	}
	return nil
}

// codexModel reads the model from codex's own config, which is the one place
// that is true without norn tracking another vendor's lineup.
func codexModel() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".codex")
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "model")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "=") {
			continue // model_reasoning_effort and friends
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(rest, "=")), `"`)
	}
	return ""
}

func (m settingsModel) updateText(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = sModeList
		m.input = ""
	case "enter":
		r := m.visibleRows()[m.cursor]
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
		r := m.visibleRows()[m.cursor]
		m.applyString(r.keys, choice)
		m.mode = sModeList
	}
	return m, nil
}

func (m settingsModel) View() string {
	var b strings.Builder

	// Layer tabs: highlight the active config file; ←/→ cycles.
	var tabs []string
	for i, l := range m.layers {
		if i == m.layer {
			tabs = append(tabs, selectedStyle.Render(l.name))
		} else {
			tabs = append(tabs, dimStyle.Render(l.name))
		}
	}
	header := dimStyle.Render("writing to ") + strings.Join(tabs, dimStyle.Render(" · "))
	if len(m.layers) > 1 {
		header += dimStyle.Render("   ←/→ change")
	}
	b.WriteString(header + "\n")
	if len(m.layers) > 1 {
		// The precedence, in the order it actually applies, said once rather
		// than left to be inferred from which value happens to show.
		b.WriteString(dimStyle.Render("  most specific wins: this repo just me › this repo the team › every repo") + "\n")
	}
	b.WriteString("\n")

	rows := m.visibleRows()
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  nothing is set in " + m.layerName() + " · m shows everything"))
		return b.String()
	}

	lastSection := ""
	for i, r := range rows {
		if r.section != lastSection {
			b.WriteString(subtitleStyle.Render(r.section) + "\n")
			lastSection = r.section
		}
		// A bar in the gutter for a key this scope sets itself, the way an
		// editor marks a modified line: "does this file decide this" is the
		// question, and a suffix at the end of the row does not answer it at a
		// glance.
		gutter := " "
		if m.setHere(r) {
			gutter = selectedStyle.Render("│")
		}
		cursor := gutter + " "
		if i == m.cursor {
			cursor = gutter + cursorStyle.Render(">")
		}
		val, inherited := m.rowValue(r)
		source := m.sourceOf(r)
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
			shown = dimStyle.Render(valStr + "  " + source)
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
	help := "⏎ edit · space toggle · ←/→ scope · m only set here · r unset · e $EDITOR"
	if m.onlySet {
		help = "⏎ edit · space toggle · ←/→ scope · m show all · r unset · e $EDITOR"
	}
	switch m.mode {
	case sModeText:
		help = "type value · ⏎ save · esc cancel   (empty clears the key)"
	case sModePick:
		help = "j/k move · ⏎ select · esc cancel"
	}
	b.WriteString(helpStyle.Render(help))
	return b.String()
}

// sourceOf says where a row's effective value comes from, in the words the
// layer tabs use. A value with no source is norn's own default.
//
// Shown on every inherited row because "inherited" alone is the question rather
// than the answer: from the team's file, from your own, or from nowhere.
func (m settingsModel) sourceOf(r settingRow) string {
	// Most specific first, which is also the order that decides the winner.
	for i := len(m.layers) - 1; i >= 0; i-- {
		ed, err := config.OpenEditor(m.layers[i].path)
		if err != nil {
			continue
		}
		if _, ok := ed.GetString(r.keys); ok {
			return "· from " + m.layers[i].name
		}
	}
	return "· default"
}

// visibleRows is the list as filtered by the "set here" toggle.
func (m settingsModel) visibleRows() []settingRow {
	if !m.onlySet {
		return m.rows
	}
	var out []settingRow
	for _, r := range m.rows {
		if m.setHere(r) {
			out = append(out, r)
		}
	}
	return out
}

// setHere reports whether the active scope sets this key itself, rather than
// inheriting it. This is what the gutter marks.
func (m settingsModel) setHere(r settingRow) bool {
	ed, err := config.OpenEditor(m.activePath())
	if err != nil {
		return false
	}
	_, ok := ed.GetString(r.keys)
	return ok
}

// unset removes a key from the active scope and reloads, so the row
// immediately shows the value it falls back to.
func (m *settingsModel) unset(keys []string) {
	ed, err := config.OpenEditor(m.activePath())
	if err != nil {
		m.status = err.Error()
		return
	}
	ed.Delete(keys)
	if err := ed.Save(); err != nil {
		m.status = err.Error()
		return
	}
	m.reload()
	m.status = "removed " + strings.Join(keys, ".") + " from " + m.layerName()
}
