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
		cfg:    config.DefaultConfig(),
		layers: []settingLayer{{"Global", filepath.Join(dir, "config.yaml")}},
		rows:   settingRows(),
	}
}

func TestSettingsApplyBool(t *testing.T) {
	m := newTestSettings(t)
	m.applyBool([]string{"ai_naming"}, false)

	b, err := os.ReadFile(m.activePath())
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
	if v, ok := config.OpenEditorValue(m.activePath(), []string{"agent", "command"}); !ok || v != "opencode" {
		t.Fatalf("agent.command = %q, %v", v, ok)
	}

	// Empty value clears the key.
	m.applyString([]string{"agent", "command"}, "")
	if _, ok := config.OpenEditorValue(m.activePath(), []string{"agent", "command"}); ok {
		t.Error("agent.command should have been cleared")
	}
}

func TestSettingsTaskProvider(t *testing.T) {
	m := newTestSettings(t)
	m.applyString([]string{"tasks", "provider"}, "github")
	if v, ok := config.OpenEditorValue(m.activePath(), []string{"tasks", "provider"}); !ok || v != "github" {
		t.Fatalf("tasks.provider = %q, %v", v, ok)
	}

	// Unset provider displays as "none", not blank.
	var providerRow settingRow
	for _, r := range settingRows() {
		if strings.Join(r.keys, ".") == "tasks.provider" {
			providerRow = r
		}
	}
	if providerRow.choices[0] != "github" {
		t.Fatalf("provider row missing/misconfigured: %+v", providerRow)
	}
	if got := resolvedDisplay(config.DefaultConfig(), providerRow); got != "none" {
		t.Errorf("unset provider display = %q, want none", got)
	}
}

func TestSettingsViewRenders(t *testing.T) {
	m := newTestSettings(t)
	out := m.View()
	for _, want := range []string{"Global", "command", "ai_naming", "template"} {
		if !strings.Contains(out, want) {
			t.Errorf("View missing %q", want)
		}
	}
}

func TestSettingsSingleLayer(t *testing.T) {
	m := newTestSettings(t) // only the Global layer
	if len(m.layers) != 1 || m.layerName() != "Global" {
		t.Errorf("expected a single Global layer, got %d (%q)", len(m.layers), m.layerName())
	}
	if m.activePath() != m.layers[0].path {
		t.Errorf("activePath = %q, want %q", m.activePath(), m.layers[0].path)
	}
}

func TestSettingsLayers(t *testing.T) {
	// A repo yields Global + personal + shared layers.
	m := NewSettings(config.DefaultConfig(), t.TempDir())
	if len(m.layers) != 3 {
		t.Fatalf("expected 3 layers for a repo, got %d", len(m.layers))
	}
	if m.layerName() != "Global" {
		t.Errorf("first layer should be Global, got %q", m.layerName())
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
