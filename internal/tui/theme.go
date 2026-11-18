package tui

import (
	"strings"
	"sync/atomic"

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

// iconSetNerd is the tui.icons value that draws Nerd Font glyphs.
const iconSetNerd = "nerd"

// nerdIcons is whether the Nerd Font glyphs are on. Unicode is the default, so
// nobody needs a special font (task 150 decision 8).
var nerdIcons atomic.Bool

// setIconSet installs tui.icons. Anything but nerd, an older daemon's empty
// answer included, is unicode.
func setIconSet(set string) { nerdIcons.Store(set == iconSetNerd) }

// nerdGlyphs maps each Unicode icon to its Nerd Font glyph. A key that starts
// with another key must come first: `◆ bug` is the kind, and `◆` the project.
var nerdGlyphs = strings.NewReplacer(
	"◆ bug", "\uf188 bug",
	"● chore", "\uf0ad chore",
	"✦ feature", "\uf005 feature",
	"▦", "\uf41b", // issue
	"▶", "\uf04b", // task, running
	"◌", "\uf086", // chat
	"◆", "\uf401", // project
	"⟡", "\uf52e", // workflow
	"◉", "\uf233", // daemon
	"⇅", "\uf407", // pull request
	"⇡", "\uf407", // pull request marker
	"⚡", "\uf0e7", // trigger, actions
	"▣", "\uf187", // archive
	"⌕", "\uf002", // filter
	"◷", "\uf017", // time, queued
	"⎇", "\uf418", // branch
	"▤", "\uf0cb", // steps
	"≡", "\uf036", // description
	"◈", "\uf4dd", // agent
	"⚙", "\uf013", // config
	"○", "\uf10c", // open, idle
	"◐", "\uf042", // in progress
	"⇄", "\uf0ec", // hand-off
	"⑂", "\uf126", // fan-out lanes
	"⏸", "\uf04c", // paused
	"✔", "\uf14a", // selected for a bulk action
	"✓", "\uf00c", // done
	"✗", "\uf00d", // failed
	"×", "\uf00d", // failed step
	"■", "\uf04d", // stopped
	"●", "\uf111", // active
)

// ic draws an icon in the configured set. It is applied to text this package
// wrote and never to a title or an agent's words, where a `✓` is what someone
// typed.
func ic(s string) string {
	if !nerdIcons.Load() {
		return s
	}
	return nerdGlyphs.Replace(s)
}

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
