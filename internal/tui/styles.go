package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Catppuccin Mocha palette
	colorBase     = lipgloss.Color("#1e1e2e")
	colorSurface  = lipgloss.Color("#313244")
	colorOverlay  = lipgloss.Color("#45475a")
	colorText     = lipgloss.Color("#cdd6f4")
	colorSubtext  = lipgloss.Color("#a6adc8")
	colorLavender = lipgloss.Color("#b4befe")
	colorBlue     = lipgloss.Color("#89b4fa")
	colorTeal     = lipgloss.Color("#94e2d5")
	colorGreen    = lipgloss.Color("#a6e3a1")
	colorYellow   = lipgloss.Color("#f9e2af")
	colorPeach    = lipgloss.Color("#fab387")
	colorRed      = lipgloss.Color("#f38ba8")
	colorPink     = lipgloss.Color("#f5c2e7")
	colorMauve    = lipgloss.Color("#cba6f7")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorLavender).
			PaddingLeft(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorSubtext).
			PaddingLeft(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMauve).
			PaddingLeft(1).
			PaddingBottom(1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorBlue).
			Bold(true).
			PaddingLeft(1)

	cursorStyle = lipgloss.NewStyle().
			Foreground(colorTeal).
			Bold(true)

	dimStyle = lipgloss.NewStyle().
			Foreground(colorOverlay)

	ageStyle = lipgloss.NewStyle().
			Foreground(colorYellow)

	goneStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)

	activeStyle = lipgloss.NewStyle().
			Foreground(colorGreen)

	branchStyle = lipgloss.NewStyle().
			Foreground(colorBlue)

	kindTaskStyle = lipgloss.NewStyle().
			Foreground(colorTeal)

	kindReviewStyle = lipgloss.NewStyle().
			Foreground(colorPink)

	commitMsgStyle = lipgloss.NewStyle().
			Foreground(colorSubtext)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorOverlay).
			PaddingLeft(1).
			PaddingTop(1)

	confirmStyle = lipgloss.NewStyle().
			Foreground(colorPeach).
			Bold(true).
			PaddingLeft(1)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true).
			PaddingLeft(1)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSurface).
			Padding(0, 1)
)
