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
	// A repo yields three layers, and they are named for who a setting applies
	// to rather than for the file it lands in: which file is norn's business,
	// who it affects is the question being answered.
	m := NewSettings(config.DefaultConfig(), t.TempDir())
	if len(m.layers) != 3 {
		t.Fatalf("expected 3 layers for a repo, got %d", len(m.layers))
	}
	if m.layerName() != "every repo" {
		t.Errorf("first layer should be the broadest, got %q", m.layerName())
	}
	// Most specific last, since that is the order ←/→ walks and the order that
	// decides which value wins.
	if got := m.layers[len(m.layers)-1].name; got != "this repo, the team" {
		t.Errorf("last layer = %q", got)
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

// The gutter and the filter answer the question the layer tabs cannot: does
// THIS scope set this key, or is it inherited? Editors solve it with a modified
// marker plus a filter, and norn had neither.
func TestSettingsSetHereAndFilter(t *testing.T) {
	m := newTestSettings(t)

	agent := m.rows[0] // agent.command
	if m.setHere(agent) {
		t.Fatal("a fresh scope reports a key as set here")
	}
	m.applyString(agent.keys, "codex")
	if !m.setHere(agent) {
		t.Fatal("a key written to this scope is not marked as set here")
	}

	m.onlySet = true
	vis := m.visibleRows()
	if len(vis) != 1 || vis[0].label != agent.label {
		t.Fatalf("the filter showed %d rows, want only the one set here", len(vis))
	}

	// Unsetting falls back to the layer beneath rather than writing a blank.
	m.unset(agent.keys)
	if m.setHere(agent) {
		t.Fatal("the key survived being unset")
	}
	if len(m.visibleRows()) != 0 {
		t.Fatal("the filter still shows a key this scope no longer sets")
	}
}
