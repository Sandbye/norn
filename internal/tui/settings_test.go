package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/config"
)

// newTestSettings builds a settings model pointed at a temp global config file
// so tests never touch the real ~/.config/work/config.yaml.
func newTestSettings(t *testing.T) settingsModel {
	t.Helper()
	dir := t.TempDir()
	return settingsModel{
		cfg:        config.DefaultConfig(),
		globalPath: filepath.Join(dir, "config.yaml"),
		rows:       settingRows(),
	}
}

func TestSettingsApplyBool(t *testing.T) {
	m := newTestSettings(t)
	m.applyBool([]string{"ai_naming"}, false)

	b, err := os.ReadFile(m.globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ai_naming: false") {
		t.Errorf("ai_naming not written:\n%s", b)
	}
	if !strings.HasPrefix(m.status, "saved") {
		t.Errorf("status = %q, want saved…", m.status)
	}
}

func TestSettingsApplyStringAndClear(t *testing.T) {
	m := newTestSettings(t)
	m.applyString([]string{"agent", "command"}, "opencode")
	if v, ok := config.OpenEditorValue(m.globalPath, []string{"agent", "command"}); !ok || v != "opencode" {
		t.Fatalf("agent.command = %q, %v", v, ok)
	}

	// Empty value clears the key.
	m.applyString([]string{"agent", "command"}, "")
	if _, ok := config.OpenEditorValue(m.globalPath, []string{"agent", "command"}); ok {
		t.Error("agent.command should have been cleared")
	}
}

func TestSettingsViewRenders(t *testing.T) {
	m := newTestSettings(t)
	out := m.View()
	for _, want := range []string{"norn settings", "Global", "command", "ai_naming", "template"} {
		if !strings.Contains(out, want) {
			t.Errorf("View missing %q", want)
		}
	}
}

func TestSettingsLayerToggleNoProject(t *testing.T) {
	m := newTestSettings(t) // no projectPath
	if m.activePath() != m.globalPath {
		t.Error("active path should be global")
	}
	if m.layerName() != "Global" {
		t.Errorf("layerName = %q", m.layerName())
	}
	// With no project, activePath stays global even if layer flips.
	m.layer = 1
	if m.activePath() != m.globalPath {
		t.Error("with no project config, active path must stay global")
	}
}

func TestSettingsChoices(t *testing.T) {
	m := newTestSettings(t)
	agentRow := m.rows[0] // agent.command
	choices := m.choicesFor(agentRow)
	if choices[len(choices)-1] != "(custom…)" {
		t.Errorf("last choice should be custom, got %v", choices)
	}
	if choices[0] != "claude" {
		t.Errorf("first agent choice = %q", choices[0])
	}
}
