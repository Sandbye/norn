package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Nord palette (arctic frost + aurora) — fits the Norse name. Var names are
	// kept from the old Catppuccin mapping; only the values changed.
	// Polar Night / Snow Storm:
	colorBase     = lipgloss.Color("#2e3440") // nord0  darkest bg
	colorSurface  = lipgloss.Color("#3b4252") // nord1
	colorOverlay  = lipgloss.Color("#4c566a") // nord3  dim / help / borders
	colorText     = lipgloss.Color("#eceff4") // nord6  brightest snow
	colorSubtext  = lipgloss.Color("#d8dee9") // nord4
	// Frost:
	colorLavender = lipgloss.Color("#88c0d0") // nord8  frost cyan (titles pop)
	colorBlue     = lipgloss.Color("#81a1c1") // nord9  frost blue (branches)
	colorTeal     = lipgloss.Color("#8fbcbb") // nord7  frost teal (cursor)
	// Aurora:
	colorGreen    = lipgloss.Color("#a3be8c") // nord14
	colorYellow   = lipgloss.Color("#ebcb8b") // nord13
	colorPeach    = lipgloss.Color("#d08770") // nord12 orange
	colorRed      = lipgloss.Color("#bf616a") // nord11
	colorPink     = lipgloss.Color("#b48ead") // nord15 purple
	colorMauve    = lipgloss.Color("#b48ead") // nord15 purple

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

	// Inline existing-comment overlay (--since-review).
	commentTagStyle = lipgloss.NewStyle().
			Foreground(colorPeach).
			Bold(true)

	commentBodyStyle = lipgloss.NewStyle().
			Foreground(colorYellow)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSurface).
			Padding(0, 1)
)
