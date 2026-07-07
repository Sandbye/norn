package tui

import "testing"

func TestApplyTheme(t *testing.T) {
	defer ApplyTheme("nord") // restore for other tests

	ApplyTheme("frog")
	if Avatar() != "🐸" {
		t.Errorf("frog avatar = %q, want 🐸", Avatar())
	}
	if colorLavender != frogPalette.Lavender {
		t.Error("frog palette not applied to color vars")
	}

	ApplyTheme("nord")
	if Avatar() != "" {
		t.Errorf("nord avatar = %q, want empty", Avatar())
	}

	ApplyTheme("nonsense") // unknown falls back to nord
	if active.Name != "nord" {
		t.Errorf("unknown theme = %q, want nord fallback", active.Name)
	}
}

func TestThemeNames(t *testing.T) {
	names := ThemeNames()
	if len(names) < 2 || names[0] != "nord" {
		t.Errorf("ThemeNames = %v", names)
	}
}
