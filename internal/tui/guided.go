package tui

import "charm.land/lipgloss/v2"

// The root spends two columns on the takeover frame and four rows on the
// application header, footer, and takeover frame. These content thresholds
// therefore turn the guided composition on at a 128×24 terminal.
const (
	guidedTakeoverMinWidth  = 126
	guidedTakeoverMinHeight = 20
	guidedRailMinWidth      = 28
	guidedRailMaxWidth      = 36
)

// guidedTakeover reports whether two panes have enough room to be more useful
// than the compact form or list. Crossing the breakpoint changes composition
// only; the views keep their cursor and sub-layer state.
func guidedTakeover(width, height int) bool {
	return width >= guidedTakeoverMinWidth && height >= guidedTakeoverMinHeight
}

// guidedPaneWidths gives roughly a quarter of the takeover to navigation,
// bounded so neither a very wide terminal nor the breakpoint makes the rail
// dominate the focused work surface.
func guidedPaneWidths(width int) (rail, main int) {
	rail = min(max(width/4, guidedRailMinWidth), guidedRailMaxWidth)
	return rail, max(width-rail, 0)
}

// guidedSurface frames a persistent rail and one focused work surface inside
// the takeover's own outer frame. The two panes consume exactly width cells so
// frame can never trim the right-hand border.
func guidedSurface(
	width, height int,
	railTitle, railBody, mainTitle, mainBody string,
) string {
	railWidth, mainWidth := guidedPaneWidths(width)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		frame(railTitle, railBody, railWidth, height, false),
		frame(mainTitle, mainBody, mainWidth, height, true),
	)
}

// section separates groups inside a focused work surface without adding a
// second set of boxes whose borders would compete with the two-pane hierarchy.
func section(title string) string {
	return "  " + styleTitle.Render(title) + "  " + styleDim.Render("────────")
}
