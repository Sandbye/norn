package tui

import "github.com/charmbracelet/huh"

// HuhTheme returns a huh form theme tuned to norn's active palette, so the
// `norn auth` forms match the rest of the UI (frost accent, nord/frog base).
func HuhTheme() *huh.Theme {
	t := huh.ThemeBase()

	f := &t.Focused
	f.Base = f.Base.BorderForeground(colorLavender)
	f.Title = f.Title.Foreground(colorLavender).Bold(true)
	f.Description = f.Description.Foreground(colorOverlay)
	f.SelectSelector = f.SelectSelector.Foreground(colorLavender)
	f.SelectedOption = f.SelectedOption.Foreground(colorLavender)
	f.SelectedPrefix = f.SelectedPrefix.Foreground(colorLavender)
	f.ErrorMessage = f.ErrorMessage.Foreground(colorRed)
	f.TextInput.Prompt = f.TextInput.Prompt.Foreground(colorLavender)
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(colorLavender)
	f.TextInput.Text = f.TextInput.Text.Foreground(colorText)

	b := &t.Blurred
	b.Title = b.Title.Foreground(colorSubtext)
	b.Description = b.Description.Foreground(colorOverlay)

	return t
}
