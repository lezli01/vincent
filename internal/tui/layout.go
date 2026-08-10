package tui

// panelID indexes the three panes of the fused home screen (§15 layout):
// the task table on top, the step timeline and the output|diff pane side by
// side underneath.
type panelID int

const (
	panelTasks panelID = iota
	panelTimeline
	panelOutput
)

// box places one panel on the shell's canvas. x,y are zero-based cell
// coordinates of the top-left corner, w,h the outer size with the border
// included. The coordinates exist for T3.13's hitTest as much as for
// rendering, which is why layout returns geometry rather than strings.
type box struct {
	id         panelID
	x, y, w, h int
}

// §15 states its floors in terminal cells: below 80×20 the shell drops to
// single-panel mode, below 60×15 it renders only the size line. layout is
// given the *panel area* — the terminal minus the shell's fixed chrome
// (the root's header and footer: shellChromeH lines) — so the height floors
// here are the §15 terminal floors shifted by that chrome. Width has no
// chrome, so the width floors are §15's verbatim.
const (
	minTermW = 60
	minTermH = 15

	minShellW = 80
	minShellH = 20

	// shellChromeH is what the root draws around the panel area.
	shellChromeH = 2

	minAreaH3 = minShellH - shellChromeH // three-pane floor
	minAreaH1 = minTermH - shellChromeH  // single-panel floor

	// collapsedBandH is an unfocused bottom band: the title border, one
	// content line, the bottom border (§15: collapse to title + one line).
	collapsedBandH = 3

	// tasksFloorH keeps five task rows visible while a bottom panel has
	// focus — the table is the navigation spine (§15). Outer height: five
	// rows + the table header + the board's own header and action lines +
	// one spare + two border lines.
	tasksFloorH = 11

	// timelineShareW is the timeline's share of the bottom band: the
	// timeline is an index, the output is the content (§15).
	timelineShareW = 0.4
)

// layout is the §15 arrangement as a pure function of the panel-area size
// and the focused panel. It returns nil below the hard floor (the shell
// renders the explicit too-small line instead), a single full-area box in
// single-panel mode, and otherwise the three boxes top to bottom: tasks,
// timeline, output. The accordion is vertical, between the two bands: the
// focused band expands and the other collapses (PR P decision) — collapsing
// the output while navigating the timeline would defeat why they sit side
// by side.
func layout(w, h int, focus panelID) []box {
	if w < minTermW || h < minAreaH1 {
		return nil
	}
	if w < minShellW || h < minAreaH3 {
		return []box{{id: focus, x: 0, y: 0, w: w, h: h}}
	}
	tasksH := tasksFloorH
	if focus == panelTasks {
		tasksH = h - collapsedBandH
	}
	bandH := h - tasksH
	tlW := int(float64(w) * timelineShareW)
	return []box{
		{id: panelTasks, x: 0, y: 0, w: w, h: tasksH},
		{id: panelTimeline, x: 0, y: tasksH, w: tlW, h: bandH},
		{id: panelOutput, x: tlW, y: tasksH, w: w - tlW, h: bandH},
	}
}
