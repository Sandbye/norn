package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// logoArt is the "norn" wordmark (figlet "block" font), embedded so there is no
// runtime dependency on figlet.
const logoArt = `_|_|_|      _|_|    _|  _|_|  _|_|_|
_|    _|  _|    _|  _|_|      _|    _|
_|    _|  _|    _|  _|        _|    _|
_|    _|    _|_|    _|        _|    _|`

// Logo returns the norn wordmark colored in the active theme's accent.
func Logo() string {
	st := lipgloss.NewStyle().Foreground(colorLavender).Bold(true)
	lines := strings.Split(logoArt, "\n")
	for i, l := range lines {
		lines[i] = st.Render(l)
	}
	return strings.Join(lines, "\n")
}
