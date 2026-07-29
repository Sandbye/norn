package tui

import "github.com/charmbracelet/lipgloss"

// palette is a named set of semantic colors plus an avatar glyph shown in the
// dashboard header. Themes swap the palette; ApplyTheme rebuilds every style.
type palette struct {
	Name   string
	Avatar string // shown next to the dashboard title ("" = none)

	// Lexicon: a theme re-tells norn's "many threads, one tree" metaphor.
	// Frog turns threads into lily pads and the tree into a mushroom.
	ThreadWord string // plural noun for worktree sessions ("threads" / "lily pads")
	TreeWord   string // the git-root metaphor ("tree" / "mushroom")

	// Spin, when true, animates the header mark on a tick (reserved).
	Spin bool

	Base    lipgloss.Color // darkest bg
	Surface lipgloss.Color
	Overlay lipgloss.Color // dim / help / borders
	Text    lipgloss.Color // brightest
	Subtext lipgloss.Color

	Lavender lipgloss.Color // primary accent: titles, frame border, cursor bg
	Blue     lipgloss.Color // branches
	Teal     lipgloss.Color // cursor

	Green  lipgloss.Color
	Yellow lipgloss.Color
	Peach  lipgloss.Color
	Red    lipgloss.Color
	Pink   lipgloss.Color

	// Diff row washes — subtle line-fill backgrounds for the diff/review viewer.
	RemovedBG  lipgloss.Color
	AddedBG    lipgloss.Color
	HunkBG     lipgloss.Color
	TokenHiRm  lipgloss.Color
	TokenHiAdd lipgloss.Color
}

// nordPalette — arctic frost + aurora (https://www.nordtheme.com/).
var nordPalette = palette{
	Name:       "nord",
	Avatar:     "",
	ThreadWord: "threads",
	TreeWord:   "tree",
	Base:       lipgloss.Color("#2e3440"), // nord0
	Surface:    lipgloss.Color("#3b4252"), // nord1
	Overlay:    lipgloss.Color("#4c566a"), // nord3
	Text:       lipgloss.Color("#eceff4"), // nord6
	Subtext:    lipgloss.Color("#d8dee9"), // nord4
	Lavender:   lipgloss.Color("#88c0d0"), // nord8 frost cyan
	Blue:       lipgloss.Color("#81a1c1"), // nord9
	Teal:       lipgloss.Color("#8fbcbb"), // nord7
	Green:      lipgloss.Color("#a3be8c"), // nord14
	Yellow:     lipgloss.Color("#ebcb8b"), // nord13
	Peach:      lipgloss.Color("#d08770"), // nord12
	Red:        lipgloss.Color("#bf616a"), // nord11
	Pink:       lipgloss.Color("#b48ead"), // nord15

	// Diff washes — muted aurora over polar night, frost-tinted hunks.
	RemovedBG:  lipgloss.Color("#3b2b30"),
	AddedBG:    lipgloss.Color("#2f3a33"),
	HunkBG:     lipgloss.Color("#333d4a"),
	TokenHiRm:  lipgloss.Color("#5a3138"),
	TokenHiAdd: lipgloss.Color("#3f5142"),
}

// frogPalette — mossy forest greens with a warm accent. Ribbit.
var frogPalette = palette{
	Name:       "frog",
	Avatar:     "🐸",
	ThreadWord: "lily pads",
	TreeWord:   "mushroom",
	Base:       lipgloss.Color("#14261a"), // deep forest
	Surface:    lipgloss.Color("#1d3324"),
	Overlay:    lipgloss.Color("#5c8a4a"), // fern (dim/help)
	Text:       lipgloss.Color("#eaf4e0"),
	Subtext:    lipgloss.Color("#cfe1c0"),
	Lavender:   lipgloss.Color("#77c043"), // frog green — titles/border/cursor bg
	Blue:       lipgloss.Color("#52b788"), // mint — branches
	Teal:       lipgloss.Color("#95d5b2"), // cursor
	Green:      lipgloss.Color("#90be6d"),
	Yellow:     lipgloss.Color("#f2e8cf"),
	Peach:      lipgloss.Color("#dda15e"),
	Red:        lipgloss.Color("#bc4749"),
	Pink:       lipgloss.Color("#b5838d"),

	// Diff washes — muted red/green over deep forest, mossy hunks.
	RemovedBG:  lipgloss.Color("#3a2426"),
	AddedBG:    lipgloss.Color("#20301f"),
	HunkBG:     lipgloss.Color("#243a2b"),
	TokenHiRm:  lipgloss.Color("#54282c"),
	TokenHiAdd: lipgloss.Color("#31491f"),
}

var themes = map[string]palette{
	"nord": nordPalette,
	"frog": frogPalette,
}

// ThemeNames lists the built-in theme names (for the settings picker).
func ThemeNames() []string { return []string{"nord", "frog"} }

// active is the current palette; the color vars below mirror it so existing
// call sites (colorBase, colorLavender, …) keep working unchanged.
var active = nordPalette

var (
	colorBase     lipgloss.Color
	colorSurface  lipgloss.Color
	colorOverlay  lipgloss.Color
	colorText     lipgloss.Color
	colorSubtext  lipgloss.Color
	colorLavender lipgloss.Color
	colorBlue     lipgloss.Color
	colorTeal     lipgloss.Color
	colorGreen    lipgloss.Color
	colorYellow   lipgloss.Color
	colorPeach    lipgloss.Color
	colorRed      lipgloss.Color
	colorPink     lipgloss.Color
	colorMauve    lipgloss.Color

	colorRemovedBG  lipgloss.Color
	colorAddedBG    lipgloss.Color
	colorHunkBG     lipgloss.Color
	colorTokenHiRm  lipgloss.Color
	colorTokenHiAdd lipgloss.Color
)

// Style vars, rebuilt by buildStyles whenever the theme changes.
var (
	titleStyle       lipgloss.Style
	subtitleStyle    lipgloss.Style
	headerStyle      lipgloss.Style
	selectedStyle    lipgloss.Style
	cursorStyle      lipgloss.Style
	dimStyle         lipgloss.Style
	ageStyle         lipgloss.Style
	goneStyle        lipgloss.Style
	dirtyStyle       lipgloss.Style
	activeStyle      lipgloss.Style
	branchStyle      lipgloss.Style
	kindTaskStyle    lipgloss.Style
	kindReviewStyle  lipgloss.Style
	commitMsgStyle   lipgloss.Style
	helpStyle        lipgloss.Style
	confirmStyle     lipgloss.Style
	errorStyle       lipgloss.Style
	commentTagStyle  lipgloss.Style
	commentBodyStyle lipgloss.Style
	boxStyle         lipgloss.Style
)

func init() { ApplyTheme("nord") }

// ApplyTheme selects a palette by name (unknown → nord) and rebuilds all styles.
// Call once at startup, before the TUI runs.
func ApplyTheme(name string) {
	p, ok := themes[name]
	if !ok {
		p = nordPalette
	}
	active = p

	colorBase = p.Base
	colorSurface = p.Surface
	colorOverlay = p.Overlay
	colorText = p.Text
	colorSubtext = p.Subtext
	colorLavender = p.Lavender
	colorBlue = p.Blue
	colorTeal = p.Teal
	colorGreen = p.Green
	colorYellow = p.Yellow
	colorPeach = p.Peach
	colorRed = p.Red
	colorPink = p.Pink
	colorMauve = p.Pink

	colorRemovedBG = p.RemovedBG
	colorAddedBG = p.AddedBG
	colorHunkBG = p.HunkBG
	colorTokenHiRm = p.TokenHiRm
	colorTokenHiAdd = p.TokenHiAdd

	buildStyles()
}

// Avatar returns the current theme's dashboard avatar glyph ("" = none).
func Avatar() string { return active.Avatar }

// ThreadWord / TreeWord return the active theme's lexicon for worktree sessions
// and the git-root metaphor (e.g. "lily pads" / "mushroom" on the frog theme).
func ThreadWord() string { return active.ThreadWord }
func TreeWord() string   { return active.TreeWord }

func buildStyles() {
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorLavender).PaddingLeft(1)
	subtitleStyle = lipgloss.NewStyle().Foreground(colorSubtext).PaddingLeft(1)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colorMauve).PaddingLeft(1).PaddingBottom(1)
	selectedStyle = lipgloss.NewStyle().Foreground(colorBlue).Bold(true).PaddingLeft(1)
	cursorStyle = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
	dimStyle = lipgloss.NewStyle().Foreground(colorOverlay)
	ageStyle = lipgloss.NewStyle().Foreground(colorYellow)
	goneStyle = lipgloss.NewStyle().Foreground(colorRed).Bold(true)

	dirtyStyle = lipgloss.NewStyle().Foreground(colorPeach)
	activeStyle = lipgloss.NewStyle().Foreground(colorGreen)
	branchStyle = lipgloss.NewStyle().Foreground(colorBlue)
	kindTaskStyle = lipgloss.NewStyle().Foreground(colorTeal)
	kindReviewStyle = lipgloss.NewStyle().Foreground(colorPink)
	commitMsgStyle = lipgloss.NewStyle().Foreground(colorSubtext)
	helpStyle = lipgloss.NewStyle().Foreground(colorOverlay).PaddingLeft(1).PaddingTop(1)
	confirmStyle = lipgloss.NewStyle().Foreground(colorPeach).Bold(true).PaddingLeft(1)
	errorStyle = lipgloss.NewStyle().Foreground(colorRed).Bold(true).PaddingLeft(1)
	commentTagStyle = lipgloss.NewStyle().Foreground(colorPeach).Bold(true)
	commentBodyStyle = lipgloss.NewStyle().Foreground(colorYellow)
	boxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorSurface).Padding(0, 1)
}
