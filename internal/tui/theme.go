package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The redesign's design tokens (task 150.1).
//
// Tones are named ANSI colours, never fixed hex values (task 150 decision 9):
// §15's Colour rule has the palette degrade under NO_COLOR and on 16-colour
// terminals, and a named colour follows the terminal's own light or dark
// theme. Nothing may be told apart by tone alone, which is why a selected card
// also changes its edges and every state carries a glyph.
var (
	toneBlue    = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	toneMagenta = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	toneCyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	tonePill    = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Background(lipgloss.Color("8"))
)

// boxEdges is the set of characters a box is drawn with.
type boxEdges struct{ tl, tr, bl, br, h, v string }

var (
	// edgesRounded is every ordinary box.
	edgesRounded = boxEdges{"╭", "╮", "╰", "╯", "─", "│"}
	// edgesThick is the selected box.
	edgesThick = boxEdges{"┏", "┓", "┗", "┛", "━", "┃"}
)

// drawBox draws a box of outer size w×h, with title set into the top edge at
// the left and right at the right. Body lines are cut to the inner width
// behind one cell of padding on each side, and lines past the height are
// dropped. It returns exactly h lines of exactly w cells.
func drawBox(title, right string, body []string, w, h int, e boxEdges, border lipgloss.Style) []string {
	if w < 2 || h < 2 {
		return nil
	}
	inner := w - 2
	out := make([]string, 0, h)
	out = append(out, boxTop(title, right, inner, e, border))
	side := border.Render(e.v)
	text := max(inner-2, 0)
	for i := range h - 2 {
		line := ""
		if i < len(body) {
			line = ansi.Truncate(body[i], text, "…")
		}
		pad := strings.Repeat(" ", max(text-ansi.StringWidth(line), 0))
		if inner < 2 {
			out = append(out, side+strings.Repeat(" ", inner)+side)
			continue
		}
		out = append(out, side+" "+line+pad+" "+side)
	}
	return append(out, border.Render(e.bl+strings.Repeat(e.h, inner)+e.br))
}

// boxTop is a box's top edge. The left title wins the room: the right one is
// dropped whole rather than cut, because a count cut in half is a wrong count.
func boxTop(title, right string, inner int, e boxEdges, border lipgloss.Style) string {
	left := ""
	if title != "" {
		left = " " + ansi.Truncate(title, max(inner-3, 0), "…") + " "
	}
	used := 1 + ansi.StringWidth(left)
	tail := ""
	if right != "" && used+ansi.StringWidth(right)+3 <= inner {
		tail = " " + right + " " + border.Render(e.h)
	}
	fill := max(inner-used-ansi.StringWidth(tail), 0)
	return border.Render(e.tl+e.h) + left + border.Render(strings.Repeat(e.h, fill)) + tail + border.Render(e.tr)
}
