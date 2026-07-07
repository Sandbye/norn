package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// render3DTorus renders a real 3D rotating torus (the a1k0n "spinning donut"):
// a parametric torus surface, rotated around two axes by A and B, perspective-
// projected with a z-buffer, and shaded by surface luminance through an ASCII
// ramp. Colored in the theme accent. This is honest 3D, not a faked flip.
func render3DTorus(width, height int, A, B float64) string {
	const (
		r1 = 1.0 // tube radius
		r2 = 2.0 // ring radius
		k2 = 5.0 // viewer distance
	)
	k1 := float64(width) * k2 * 3 / (8 * (r1 + r2))
	const ramp = ".,-~:;=!*#$@"

	out := make([][]byte, height)
	zbuf := make([][]float64, height)
	for i := range out {
		out[i] = make([]byte, width)
		for j := range out[i] {
			out[i][j] = ' '
		}
		zbuf[i] = make([]float64, width)
	}

	cosA, sinA := math.Cos(A), math.Sin(A)
	cosB, sinB := math.Cos(B), math.Sin(B)

	for theta := 0.0; theta < 2*math.Pi; theta += 0.07 {
		ct, st := math.Cos(theta), math.Sin(theta)
		for phi := 0.0; phi < 2*math.Pi; phi += 0.02 {
			cp, sp := math.Cos(phi), math.Sin(phi)
			cx := r2 + r1*ct // circle before revolving
			cy := r1 * st

			x := cx*(cosB*cp+sinA*sinB*sp) - cy*cosA*sinB
			y := cx*(sinB*cp-sinA*cosB*sp) + cy*cosA*cosB
			z := k2 + cosA*cx*sp + cy*sinA
			ooz := 1 / z

			xp := int(float64(width)/2 + k1*ooz*x)
			// chars are ~twice as tall as wide, so halve the vertical scale to keep it round.
			yp := int(float64(height)/2 - k1*ooz*y*0.5)

			l := cp*ct*sinB - cosA*ct*sp - sinA*st + cosB*(cosA*st-ct*sinA*sp)
			if l > 0 && xp >= 0 && xp < width && yp >= 0 && yp < height && ooz > zbuf[yp][xp] {
				zbuf[yp][xp] = ooz
				idx := int(l * 8)
				if idx > 11 {
					idx = 11
				}
				out[yp][xp] = ramp[idx]
			}
		}
	}

	st := lipgloss.NewStyle().Foreground(colorLavender)
	rows := make([]string, height)
	for i, row := range out {
		rows[i] = st.Render(string(row))
	}
	return strings.Join(rows, "\n")
}
