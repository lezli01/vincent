package workflowgraph

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Render paints a Scene onto terminal cells. It is a pure projection of the
// scene plus visual state (decision 9): it decides nothing about topology,
// and running it twice on the same inputs gives the same picture.
//
// Meaning is carried by characters, never by color (decision 6). Box shapes
// tell node from frame, frame weights tell a `parallel` group from a `fan_out`
// from a `loop`, edge labels spell `true` and `false`, and the selected node
// is drawn with a heavy border. Styles are applied afterwards over spans that
// already read correctly with every style stripped.

// glyphSet is every structural character in one place, so a future
// terminal-compatibility mode can swap an ASCII palette in without touching
// layout (decision 6).
type glyphSet struct {
	nodeTopLeft, nodeTopRight, nodeBottomLeft, nodeBottomRight string
	selTopLeft, selTopRight, selBottomLeft, selBottomRight     string
	nodeH, nodeV, selH, selV                                   string

	// Frames differ by weight so a group's kind survives a screenshot with
	// no color: light for a parallel group, heavy for a fan_out, double for
	// a loop (decision 18).
	frameLight, frameHeavy, frameDouble frameGlyphs

	// junction maps a cell's occupied directions to its glyph, so a corner,
	// a tee and a crossing are one mechanism rather than three guesses.
	junction                                  map[dirSet]string
	arrowDown, arrowUp, arrowLeft, arrowRight string
}

type frameGlyphs struct {
	tl, tr, bl, br, h, v string
}

var unicodeGlyphs = glyphSet{
	nodeTopLeft: "╭", nodeTopRight: "╮", nodeBottomLeft: "╰", nodeBottomRight: "╯",
	selTopLeft: "┏", selTopRight: "┓", selBottomLeft: "┗", selBottomRight: "┛",
	nodeH: "─", nodeV: "│", selH: "━", selV: "┃",
	frameLight:  frameGlyphs{tl: "┌", tr: "┐", bl: "└", br: "┘", h: "─", v: "│"},
	frameHeavy:  frameGlyphs{tl: "┏", tr: "┓", bl: "┗", br: "┛", h: "━", v: "┃"},
	frameDouble: frameGlyphs{tl: "╔", tr: "╗", bl: "╚", br: "╝", h: "═", v: "║"},
	junction: map[dirSet]string{
		dirN | dirS: "│", dirE | dirW: "─",
		dirN | dirE: "└", dirN | dirW: "┘",
		dirS | dirE: "┌", dirS | dirW: "┐",
		dirN | dirS | dirE: "├", dirN | dirS | dirW: "┤",
		dirE | dirW | dirS: "┬", dirE | dirW | dirN: "┴",
		dirN | dirS | dirE | dirW: "┼",
		dirN:                      "│", dirS: "│", dirE: "─", dirW: "─",
	},
	arrowDown: "▼", arrowUp: "▲", arrowLeft: "◀", arrowRight: "▶",
}

// dirSet is the set of directions an edge cell continues in.
type dirSet uint8

const (
	dirN dirSet = 1 << iota
	dirS
	dirE
	dirW
)

// cellStyle names what a cell is, so styling is a pass over spans rather than
// a decision taken while drawing.
type cellStyle uint8

const (
	styleBlank cellStyle = iota
	styleNode
	styleSelected
	styleFrame
	styleEdge
	styleEdgeLabel
)

// Theme maps cell roles to Lip Gloss styles. A zero Theme renders plain text,
// which is what the golden tests compare.
type Theme struct {
	Node      lipgloss.Style
	Selected  lipgloss.Style
	Frame     lipgloss.Style
	Edge      lipgloss.Style
	EdgeLabel lipgloss.Style
}

// ViewState is what the viewer knows that the diagram does not: which node is
// selected, and — for a task's graph rather than a registry entry's — what
// each node's step actually did (task 051). A zero Run is the definition
// viewer the workflows screen still opens.
type ViewState struct {
	Selected string
	Run      Overlay
}

// Render returns the scene as one string per row, unwrapped and uncropped —
// cropping is the viewport's job (decision 8).
func Render(d Diagram, s Scene, st ViewState, th Theme) []string {
	c := newCanvas(s.Width, s.Height)
	g := unicodeGlyphs

	byGroup := map[string]Group{}
	for _, grp := range d.Groups {
		byGroup[grp.ID] = grp
	}
	for _, grp := range s.Groups {
		c.frame(grp, g)
		c.captions(byGroup[grp.ID], s, st.Run)
	}
	for _, e := range s.Edges {
		c.edge(e, g)
	}
	c.paintWires(g)
	byID := map[string]Node{}
	for _, n := range d.Nodes {
		byID[n.ID] = n
	}
	for _, pn := range s.Nodes {
		c.node(pn, byID[pn.ID], pn.ID == st.Selected, st.Run.Nodes[pn.ID], g)
	}
	c.paintLabels()
	return c.lines(th)
}

type canvas struct {
	w, h   int
	runes  [][]rune
	styles [][]cellStyle
	// wires accumulates every edge's directions per cell before any glyph is
	// chosen. Two connectors sharing a cell then resolve to a tee or a
	// crossing instead of one silently overwriting the other.
	wires  map[Point]dirSet
	arrows map[Point]string
	// labels are drawn last, onto blank cells only: a `true` printed over a
	// connector is worse than one that had to move.
	labels []pendingLabel
}

type pendingLabel struct {
	at   Point
	text string
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: max(w, 0), h: max(h, 0), wires: map[Point]dirSet{}, arrows: map[Point]string{}}
	c.runes = make([][]rune, c.h)
	c.styles = make([][]cellStyle, c.h)
	for y := range c.runes {
		c.runes[y] = make([]rune, c.w)
		c.styles[y] = make([]cellStyle, c.w)
		for x := range c.runes[y] {
			c.runes[y][x] = ' '
		}
	}
	return c
}

// continuation marks the second column of a double-width character. The grid
// is one cell per *column*, so a wide rune has to claim both of the columns it
// paints or every glyph after it on the row is pushed right by one.
const continuation = rune(0)

func (c *canvas) set(x, y int, s string, style cellStyle) {
	if s == "" {
		return
	}
	c.put(x, y, []rune(s)[0], style)
	for i := 1; i < ansi.StringWidth(s); i++ {
		c.put(x+i, y, continuation, style)
	}
}

func (c *canvas) put(x, y int, r rune, style cellStyle) {
	if x < 0 || y < 0 || y >= c.h || x >= c.w {
		return
	}
	c.runes[y][x] = r
	c.styles[y][x] = style
}

// text writes a string cell by cell, advancing by display width rather than
// by rune so a wide character occupies the two columns it actually paints.
func (c *canvas) text(x, y int, s string, style cellStyle) {
	for _, r := range s {
		g := string(r)
		c.set(x, y, g, style)
		x += max(1, ansi.StringWidth(g))
	}
}

func (c *canvas) frame(g PlacedGroup, gl glyphSet) {
	f := gl.frameLight
	switch g.Kind {
	case GroupFanOut:
		f = gl.frameHeavy
	case GroupLoop:
		f = gl.frameDouble
	case GroupParallel:
		f = gl.frameLight
	}
	right, bottom := g.X+g.W-1, g.Y+g.H-1
	for x := g.X; x <= right; x++ {
		c.set(x, g.Y, f.h, styleFrame)
		c.set(x, bottom, f.h, styleFrame)
	}
	for y := g.Y; y <= bottom; y++ {
		c.set(g.X, y, f.v, styleFrame)
		c.set(right, y, f.v, styleFrame)
	}
	c.set(g.X, g.Y, f.tl, styleFrame)
	c.set(right, g.Y, f.tr, styleFrame)
	c.set(g.X, bottom, f.bl, styleFrame)
	c.set(right, bottom, f.br, styleFrame)

	// The kind is spelled on the border, because a frame weight alone is not
	// something a reader can name (decision 18).
	label := " " + string(g.Kind) + " "
	if g.W > ansi.StringWidth(label)+4 {
		c.text(g.X+2, g.Y, label, styleFrame)
	}
}

// edge records one connector's cells. Nothing is painted here: glyphs are
// chosen once every edge has been recorded, so crossings and shared runs
// resolve rather than overwrite.
// captions names a fan_out's lanes above their columns. A lane is a thing the
// workflow language names and may guard — a child task of its own — so its id
// and its `if` belong on screen rather than only in the inspector.
func (c *canvas) captions(g Group, s Scene, run Overlay) {
	if g.Kind != GroupFanOut {
		return
	}
	frame, ok := placedGroup(s, g.ID)
	if !ok {
		return
	}
	for _, col := range g.Columns {
		if len(col.Nodes) == 0 || col.Label == "" {
			continue
		}
		first, found := s.Node(col.Nodes[0])
		if !found {
			continue
		}
		text := col.Label
		if len(col.Badges) > 0 {
			text += " " + strings.Join(col.Badges, " ")
		}
		// A lane's run state lands here rather than on its inline steps: they
		// run in a child task, so the parent holds no step_run for them
		// (task 051 decision 1).
		if rs, ok := run.Lanes[col.Key]; ok {
			text = laneCaption(text, rs)
		}
		c.text(first.X, frame.Y+1, truncate(text, first.W), styleFrame)
	}
}

// laneCaption appends a lane's child task and its state to the caption that
// already carries the lane id and its guard.
func laneCaption(text string, rs RunState) string {
	if rs.ChildTaskID > 0 {
		text += " #" + strconv.FormatInt(rs.ChildTaskID, 10)
	}
	if rs.State != "" {
		text += " " + rs.State
	}
	return text
}

func placedGroup(s Scene, id string) (PlacedGroup, bool) {
	for _, g := range s.Groups {
		if g.ID == id {
			return g, true
		}
	}
	return PlacedGroup{}, false
}

func (c *canvas) edge(e RoutedEdge, gl glyphSet) {
	cells := polyline(e.Points)
	for i := 1; i < len(cells); i++ {
		a, b := cells[i-1], cells[i]
		c.wires[a] |= toward(a, b)
		c.wires[b] |= toward(b, a)
	}
	if len(cells) >= 2 {
		last, prev := cells[len(cells)-1], cells[len(cells)-2]
		c.arrows[last] = arrowGlyph(prev, last, gl)
	}
	if e.Label != "" {
		at := cells[0]
		if len(e.Points) > 1 {
			at = e.Points[1]
		}
		c.labels = append(c.labels, pendingLabel{at: at, text: e.Label})
	}
}

// polyline expands corner points into the full run of cells.
func polyline(points []Point) []Point {
	if len(points) == 0 {
		return nil
	}
	out := []Point{points[0]}
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		switch {
		case a.X == b.X:
			for y := a.Y + sign(b.Y-a.Y); ; y += sign(b.Y - a.Y) {
				out = append(out, Point{a.X, y})
				if y == b.Y {
					break
				}
			}
		case a.Y == b.Y:
			for x := a.X + sign(b.X-a.X); ; x += sign(b.X - a.X) {
				out = append(out, Point{x, a.Y})
				if x == b.X {
					break
				}
			}
		}
	}
	return out
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

func toward(from, to Point) dirSet {
	switch {
	case to.Y < from.Y:
		return dirN
	case to.Y > from.Y:
		return dirS
	case to.X > from.X:
		return dirE
	case to.X < from.X:
		return dirW
	}
	return 0
}

func arrowGlyph(prev, at Point, gl glyphSet) string {
	switch toward(prev, at) {
	case dirN:
		return gl.arrowUp
	case dirS:
		return gl.arrowDown
	case dirE:
		return gl.arrowRight
	case dirW:
		return gl.arrowLeft
	}
	return ""
}

// paintWires resolves the recorded cells into glyphs. It runs after the
// frames and before the boxes: a connector may cross a frame border, and no
// connector may be drawn over a node.
func (c *canvas) paintWires(gl glyphSet) {
	keys := make([]Point, 0, len(c.wires))
	for p := range c.wires {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Y != keys[j].Y {
			return keys[i].Y < keys[j].Y
		}
		return keys[i].X < keys[j].X
	})
	for _, p := range keys {
		glyph, ok := gl.junction[c.wires[p]]
		if !ok {
			continue
		}
		if arrow, isHead := c.arrows[p]; isHead && arrow != "" {
			glyph = arrow
		}
		c.set(p.X, p.Y, glyph, styleEdge)
	}
}

// paintLabels writes each branch's `true` or `false` near its bend, at the
// first free spot it can find. It runs after everything else, so "free" means
// free — a label printed over a connector is worse than one that moved, and a
// crowded channel is exactly where the two would collide.
func (c *canvas) paintLabels() {
	for _, l := range c.labels {
		width := ansi.StringWidth(l.text)
		if !c.placeLabel(l, width) {
			continue
		}
	}
}

func (c *canvas) placeLabel(l pendingLabel, width int) bool {
	for _, dy := range []int{0, -1, 1, -2, 2} {
		for dx := 1; dx <= 8; dx++ {
			for _, x := range []int{l.at.X + dx, l.at.X - dx - width + 1} {
				if c.free(x, l.at.Y+dy, width) {
					c.text(x, l.at.Y+dy, l.text, styleEdgeLabel)
					return true
				}
			}
		}
	}
	return false
}

// free reports whether a run of cells is blank and on the canvas.
func (c *canvas) free(x, y, width int) bool {
	if y < 0 || y >= c.h || x < 0 || x+width > c.w {
		return false
	}
	for i := range width {
		if c.runes[y][x+i] != ' ' {
			return false
		}
	}
	return true
}

func (c *canvas) node(p PlacedNode, n Node, selected bool, rs RunState, gl glyphSet) {
	tl, tr, bl, br := gl.nodeTopLeft, gl.nodeTopRight, gl.nodeBottomLeft, gl.nodeBottomRight
	h, v := gl.nodeH, gl.nodeV
	style := styleNode
	if selected {
		tl, tr, bl, br = gl.selTopLeft, gl.selTopRight, gl.selBottomLeft, gl.selBottomRight
		h, v = gl.selH, gl.selV
		style = styleSelected
	}
	right, bottom := p.X+p.W-1, p.Y+p.H-1
	for x := p.X; x <= right; x++ {
		c.set(x, p.Y, h, style)
		c.set(x, bottom, h, style)
	}
	for y := p.Y; y <= bottom; y++ {
		c.set(p.X, y, v, style)
		c.set(right, y, v, style)
		if y > p.Y && y < bottom {
			for x := p.X + 1; x < right; x++ {
				c.set(x, y, " ", style)
			}
		}
	}
	c.set(p.X, p.Y, tl, style)
	c.set(right, p.Y, tr, style)
	c.set(p.X, bottom, bl, style)
	c.set(right, bottom, br, style)

	inner := p.W - 4 // one border and one space of padding on each side
	if inner < 1 {
		return
	}
	c.text(p.X+2, p.Y+1, labelLine(n, rs, inner), style)
	c.text(p.X+2, p.Y+2, kindLine(n, inner), style)
}

// labelLine is a node's first row: its marker and label on the left, its run
// state pushed to the right. Without an overlay it is the label alone, which
// is what the workflows screen has always drawn.
//
// When both cannot fit, the *state* wins and the label truncates — the
// opposite of kindLine's trade, and for the opposite reason: a truncated
// label is recoverable by selecting the node, while a state that fell off the
// row is the one thing this surface exists to say.
func labelLine(n Node, rs RunState, width int) string {
	glyph := stateGlyph(rs)
	label := n.Label
	if glyph != "" {
		label = glyph + " " + label
	}
	// The qualifiers give way before the state does, and the state gives way
	// before the marker glyph does: `try 3` is recoverable from the
	// inspector, and a node showing nothing but its state has stopped being
	// a node a reader can find again.
	words := stateWords(rs)
	for len(words) > 1 && ansi.StringWidth(strings.Join(words, " ")) > width-4 {
		words = words[:len(words)-1]
	}
	joined := strings.Join(words, " ")
	if joined == "" {
		return truncate(label, width)
	}
	ww := ansi.StringWidth(joined)
	if ww >= width-2 {
		return truncate(glyph+" "+joined, width)
	}
	label = truncate(label, width-ww-1)
	return label + strings.Repeat(" ", width-ansi.StringWidth(label)-ww) + joined
}

// kindLine is a node's second row: its type on the left, its badges pushed to
// the right. The type is what a reader names the box by, so the badges are
// what gives way when the two cannot both fit.
func kindLine(n Node, width int) string {
	kind := kindLabel(n.Kind)
	badge := strings.Join(n.Badges, " ")
	if badge == "" {
		return truncate(kind, width)
	}
	bw := ansi.StringWidth(badge)
	kw := ansi.StringWidth(kind)
	// The badge earns its place only when the type still fits beside it
	// whole. Truncating `condition` to `condi…` to keep an `if` badge would
	// trade the word a reader names the box by for one they can rediscover
	// by selecting it.
	if kw+1+bw > width {
		return truncate(kind, width)
	}
	return kind + strings.Repeat(" ", width-kw-bw) + badge
}

// kindLabel is what a node prints as its type. It is the §8.2 word for every
// authored step, so the picture and the YAML use one vocabulary; only the
// synthetic kinds, which have no YAML spelling, are shortened to fit.
func kindLabel(k NodeKind) string {
	if k == KindWorkflowRef {
		return "workflow"
	}
	return string(k)
}

// truncate cuts to a display width, not a rune or byte count, so a label of
// wide characters occupies the columns it really paints (decision 6).
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// cells joins a row's runes, dropping the placeholders that stand for the
// second column of a wide character.
func cells(rs []rune) string {
	var b strings.Builder
	for _, r := range rs {
		if r == continuation {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (c *canvas) lines(th Theme) []string {
	styles := map[cellStyle]lipgloss.Style{
		styleNode:      th.Node,
		styleSelected:  th.Selected,
		styleFrame:     th.Frame,
		styleEdge:      th.Edge,
		styleEdgeLabel: th.EdgeLabel,
	}
	out := make([]string, 0, c.h)
	for y := range c.runes {
		var b strings.Builder
		x := 0
		for x < c.w {
			style := c.styles[y][x]
			start := x
			for x < c.w && c.styles[y][x] == style {
				x++
			}
			run := cells(c.runes[y][start:x])
			if s, ok := styles[style]; ok {
				run = s.Render(run)
			}
			b.WriteString(run)
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}
