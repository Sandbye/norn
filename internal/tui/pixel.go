package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderPixels draws a pixel grid using the upper-half-block trick: each text
// cell stacks two vertical pixels (▀ with fg = top pixel, bg = bottom pixel),
// doubling vertical resolution. grid rows should be equal length. A rune with
// no entry in colors (by convention '.') is transparent → the panel background.
func renderPixels(grid []string, colors map[rune]lipgloss.Color) string {
	at := func(r rune) lipgloss.Color {
		if c, ok := colors[r]; ok {
			return c
		}
		return colorBase // transparent
	}
	var b strings.Builder
	for y := 0; y+1 < len(grid); y += 2 {
		top, bot := grid[y], grid[y+1]
		for x := 0; x < len(top) && x < len(bot); x++ {
			b.WriteString(lipgloss.NewStyle().
				Foreground(at(rune(top[x]))).
				Background(at(rune(bot[x]))).
				Render("▀"))
		}
		if y+2 < len(grid) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ThemeSprite renders the active theme's mascot from its palette, or "" when
// the theme defines none. Themes carry their own Sprite grid + SpriteColors, so
// adding a mascot is pure data — no code here changes.
func ThemeSprite() string {
	if len(active.Sprite) == 0 {
		return ""
	}
	return renderPixels(active.Sprite, active.SpriteColors)
}

// --- built-in sprites -------------------------------------------------------

// treeGrid is norn's "one tree" (the world tree / git root): a still, rounded
// broadleaf with a shaded green canopy and a brown trunk with roots (13×16 px).
var treeGrid = []string{
	".....ggg.....",
	"...gGggggd...",
	"..gGgggggdd..",
	".gGGgggggddd.",
	"gGGggggggdddd",
	"gGgggggggdddd",
	"ggggggggggddd",
	".ggggggggddd.",
	"..ggggggddd..",
	"...gggdddd...",
	"....ddddd....",
	"......t......",
	"......T......",
	"......t......",
	".....tTt.....",
	"....tt.tt....",
}

var treeColors = map[rune]lipgloss.Color{
	'g': lipgloss.Color("#6a994e"), // leaf green
	'G': lipgloss.Color("#a7c957"), // sunlit highlight
	'd': lipgloss.Color("#386641"), // canopy shadow
	't': lipgloss.Color("#7f5539"), // trunk
	'T': lipgloss.Color("#5a3b28"), // trunk shadow
}

// frogGrid is the frog theme's mascot: a tall, narrow sitting frog (5×12 px).
var frogGrid = []string{
	"ww.ww",
	"wk.kw",
	"#ggg#",
	"#ggg#",
	"#gGg#",
	"#gGg#",
	"#ggg#",
	"#ggg#",
	"#gGg#",
	".ggg.",
	"#g.g#",
	"#g.g#",
}

var frogColors = map[rune]lipgloss.Color{
	'#': lipgloss.Color("#2d5016"), // outline
	'g': lipgloss.Color("#77c043"), // body
	'G': lipgloss.Color("#9dd35a"), // belly highlight
	'w': lipgloss.Color("#f2f7ec"), // eye white
	'k': lipgloss.Color("#14261a"), // pupil
	// '.' has no entry → transparent (panel background)
}

