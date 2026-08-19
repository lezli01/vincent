package workflowgraph

import "sort"

// Layout turns a Diagram into a Scene: coordinates and routed connectors, and
// nothing that knows about terminals, styles or Bubble Tea. It is
// deterministic — equal diagrams produce equal scenes — which is what lets a
// live reload keep a selection where it was (decision 19) and what makes
// geometry testable without rendering anything.
//
// The flow is top-to-bottom and sibling order is source order (decision 5).
// This optimizes for readable structure, not for minimum area or fewest
// crossings.

// Options are the layout's tunables. They are options rather than
// configuration on purpose (decision 20): nobody can choose a node width well
// before having used the default, and a later setting would read from here
// anyway.
type Options struct {
	// NodeWidth is fixed for every node, so ports sit at predictable columns
	// and a join drawing four lanes into one merge lands on a regular grid
	// (decision 17).
	NodeWidth int
	// NodeHeight is fixed too, which keeps ranks aligned: a box is a border,
	// a label line, a type-and-badges line, and a border.
	NodeHeight int
	// RankGap is the vertical space between stacked nodes, where connectors
	// and their arrowheads live.
	RankGap int
	// ColumnGap separates a group's columns.
	ColumnGap int
}

// DefaultOptions are the shipped geometry.
func DefaultOptions() Options {
	return Options{NodeWidth: 22, NodeHeight: 4, RankGap: 2, ColumnGap: 3}
}

// MinWidth is the narrowest terminal that can show a graph: one node plus the
// gutter its frame and connectors need. Below it the workflows screen says so
// rather than flattening the topology into something untrue (decision 8, and
// the threshold of decision 17 — derived here, never hardcoded elsewhere).
func (o Options) MinWidth() int { return o.NodeWidth + 4 }

// Point is a cell coordinate in scene space.
type Point struct{ X, Y int }

// PlacedNode is one node's box.
type PlacedNode struct {
	ID string
	X  int
	Y  int
	W  int
	H  int
}

// PlacedGroup is one frame.
type PlacedGroup struct {
	ID    string
	Kind  GroupKind
	Label string
	X     int
	Y     int
	W     int
	H     int
}

// RoutedEdge is one connector as an orthogonal polyline. Points are the
// corners in order; the renderer draws the segments between them.
type RoutedEdge struct {
	From   string
	To     string
	Kind   EdgeKind
	Label  string
	Points []Point
}

// Scene is a laid-out diagram.
type Scene struct {
	Nodes  []PlacedNode
	Edges  []RoutedEdge
	Groups []PlacedGroup
	Width  int
	Height int
}

// Node returns a placed node by id.
func (s Scene) Node(id string) (PlacedNode, bool) {
	for _, n := range s.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return PlacedNode{}, false
}

// NodeAt returns the node whose box covers a scene cell, which is the hit
// test both click-to-select and the renderer's overlap checks need.
func (s Scene) NodeAt(x, y int) (PlacedNode, bool) {
	for _, n := range s.Nodes {
		if x >= n.X && x < n.X+n.W && y >= n.Y && y < n.Y+n.H {
			return n, true
		}
	}
	return PlacedNode{}, false
}

// block is a laid-out fragment in local coordinates with its origin at (0,0).
// Composing blocks is translation, which is why nothing here needs to know
// where it will end up.
type block struct {
	w, h   int
	nodes  []PlacedNode
	groups []PlacedGroup
}

func (b *block) translate(dx, dy int) {
	for i := range b.nodes {
		b.nodes[i].X += dx
		b.nodes[i].Y += dy
	}
	for i := range b.groups {
		b.groups[i].X += dx
		b.groups[i].Y += dy
	}
}

func (b *block) absorb(other block) {
	b.nodes = append(b.nodes, other.nodes...)
	b.groups = append(b.groups, other.groups...)
}

type layouter struct {
	d    Diagram
	opts Options
	// byHeader finds the group a node heads, which is how a sequence knows
	// that one of its members is a structure rather than a box.
	byHeader map[string]Group
	byID     map[string]Node
}

// Layout places a diagram.
func Layout(d Diagram, opts Options) Scene {
	if opts.NodeWidth <= 0 {
		opts = DefaultOptions()
	}
	l := &layouter{d: d, opts: opts, byHeader: map[string]Group{}, byID: map[string]Node{}}
	for _, g := range d.Groups {
		l.byHeader[g.Header] = g
	}
	for _, n := range d.Nodes {
		l.byID[n.ID] = n
	}
	root := l.sequence(d.Root)
	scene := Scene{Nodes: root.nodes, Groups: root.groups, Width: root.w, Height: root.h}
	l.route(&scene)
	normalize(&scene)
	return scene
}

// sequence stacks blocks vertically, centered on one column. A node that
// heads a group becomes that group's whole block; everything else is a box.
func (l *layouter) sequence(ids []string) block {
	var blocks []block
	for _, id := range ids {
		if g, ok := l.byHeader[id]; ok {
			blocks = append(blocks, l.group(g))
			continue
		}
		blocks = append(blocks, l.leaf(id))
	}
	return stack(blocks, l.opts.RankGap)
}

func (l *layouter) leaf(id string) block {
	return block{
		w:     l.opts.NodeWidth,
		h:     l.opts.NodeHeight,
		nodes: []PlacedNode{{ID: id, X: 0, Y: 0, W: l.opts.NodeWidth, H: l.opts.NodeHeight}},
	}
}

// group lays out a header above a framed body. A fan_out additionally stacks
// its merge below the frame, because the join is a step that runs; a parallel
// group has nothing there, because its join is only its members finishing.
func (l *layouter) group(g Group) block {
	header := l.leaf(g.Header)

	var cols []block
	for _, c := range g.Columns {
		cols = append(cols, l.sequence(c.Nodes))
	}
	inner := row(cols, l.opts.ColumnGap)

	// The frame is a border on every side plus one blank row inside the top
	// and bottom. Those rows are where the connectors fanning into the
	// columns and converging out of them run: without them a horizontal run
	// would have to cross the border it is nowhere near leaving.
	//
	// A fan_out takes one more row above the connectors, because its columns
	// are lanes the workflow language names and guards, and a caption sharing
	// the connector row would be clipped by it.
	captions := 0
	if g.Kind == GroupFanOut {
		captions = 1
	}
	frame := block{w: inner.w + 2, h: inner.h + 4 + captions}
	inner.translate(1, 2+captions)
	frame.absorb(inner)
	frame.groups = append(frame.groups, PlacedGroup{
		ID: g.ID, Kind: g.Kind, Label: g.Label, X: 0, Y: 0, W: frame.w, H: frame.h,
	})

	parts := []block{header, frame}
	if merge := l.mergeOf(g); merge != "" {
		parts = append(parts, l.leaf(merge))
	}
	return stack(parts, l.opts.RankGap)
}

// mergeOf names a fan_out group's join node, and is empty for every other
// group kind.
func (l *layouter) mergeOf(g Group) string {
	if g.Kind != GroupFanOut {
		return ""
	}
	id := mergeNodeID(g.Header)
	if _, ok := l.byID[id]; !ok {
		return ""
	}
	return id
}

// stack composes blocks top to bottom, each centered on the widest.
func stack(blocks []block, gap int) block {
	out := block{}
	for _, b := range blocks {
		out.w = max(out.w, b.w)
	}
	y := 0
	for i, b := range blocks {
		if i > 0 {
			y += gap
		}
		b.translate((out.w-b.w)/2, y)
		out.absorb(b)
		y += b.h
	}
	out.h = y
	return out
}

// row composes blocks left to right, each aligned to the top.
func row(blocks []block, gap int) block {
	out := block{}
	x := 0
	for i, b := range blocks {
		if i > 0 {
			x += gap
		}
		b.translate(x, 0)
		out.absorb(b)
		x += b.w
		out.h = max(out.h, b.h)
	}
	out.w = x
	return out
}

// route draws every edge. A connector that can run straight down into the
// next rank does; anything else — a condition leaving for the END, a break
// leaving its loop, a loop's back-edge — takes a side channel, because a
// straight line would be drawn through whatever sits between.
func (l *layouter) route(s *Scene) {
	placed := map[string]PlacedNode{}
	for _, n := range s.Nodes {
		placed[n.ID] = n
	}
	ch := &channels{}
	for _, e := range l.d.Edges {
		from, okF := placed[e.From]
		to, okT := placed[e.To]
		if !okF || !okT {
			continue
		}
		points := l.direct(from, to, s.Nodes)
		if points == nil {
			points = l.channel(from, to, s.Nodes, ch, e.Kind == EdgeBack)
		}
		s.Edges = append(s.Edges, RoutedEdge{
			From: e.From, To: e.To, Kind: e.Kind, Label: e.Label, Points: points,
		})
	}
}

// direct returns a straight or single-dogleg route when the corridor between
// two boxes is clear, and nil when it is not.
func (l *layouter) direct(from, to PlacedNode, all []PlacedNode) []Point {
	fy := from.Y + from.H
	if to.Y < fy {
		return nil
	}
	fcx, tcx := centerX(from), centerX(to)
	var points []Point
	if fcx == tcx {
		points = []Point{{fcx, fy}, {fcx, to.Y}}
	} else {
		// Turn on the row immediately above the target rather than at the
		// midpoint. Every horizontal run then sits in the blank row a rank
		// or a frame keeps free for it, which is what stops a fan-in from
		// being drawn across a border or a box.
		mid := to.Y - 1
		if mid <= fy {
			mid = fy
		}
		points = []Point{{fcx, fy}, {fcx, mid}, {tcx, mid}, {tcx, to.Y}}
	}
	if blocked(points, all, from.ID, to.ID) {
		return nil
	}
	return points
}

// channel routes around the outside. The channel column sits just past the
// nodes the edge spans vertically — not at the scene's margin — so a loop's
// back-edge stays beside its loop instead of running to the far side of the
// diagram.
func (l *layouter) channel(from, to PlacedNode, all []PlacedNode, ch *channels, left bool) []Point {
	top := min(from.Y, to.Y)
	bottom := max(from.Y+from.H, to.Y+to.H)
	edge := spanEdge(all, top, bottom, left)
	idx := ch.take(top, bottom, left)
	x := edge + 2 + 2*idx
	if left {
		x = edge - 2 - 2*idx
	}
	fy := from.Y + from.H/2
	ty := to.Y + to.H/2
	fromSide, toSide := from.X+from.W, to.X+to.W
	if left {
		fromSide, toSide = from.X-1, to.X-1
	}
	return []Point{{fromSide, fy}, {x, fy}, {x, ty}, {toSide, ty}}
}

// spanEdge is the outermost node edge in a y-range, which is where a channel
// starts counting from.
func spanEdge(all []PlacedNode, top, bottom int, left bool) int {
	edge := 0
	first := true
	for _, n := range all {
		if n.Y+n.H <= top || n.Y >= bottom {
			continue
		}
		v := n.X + n.W
		if left {
			v = n.X
		}
		if first {
			edge, first = v, false
			continue
		}
		if (left && v < edge) || (!left && v > edge) {
			edge = v
		}
	}
	return edge
}

// channels hands out non-overlapping lanes for routed edges, packing them by
// the vertical span each one occupies.
type channels struct {
	right [][2]int
	left  [][2]int
}

func (c *channels) take(top, bottom int, left bool) int {
	lanes := &c.right
	if left {
		lanes = &c.left
	}
	for i, span := range *lanes {
		if bottom <= span[0] || top >= span[1] {
			(*lanes)[i] = [2]int{min(top, span[0]), max(bottom, span[1])}
			return i
		}
	}
	*lanes = append(*lanes, [2]int{top, bottom})
	return len(*lanes) - 1
}

// blocked reports whether a polyline passes through a node that is neither of
// its endpoints.
func blocked(points []Point, all []PlacedNode, fromID, toID string) bool {
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		for _, n := range all {
			if n.ID == fromID || n.ID == toID {
				continue
			}
			if segmentHits(a, b, n) {
				return true
			}
		}
	}
	return false
}

func segmentHits(a, b Point, n PlacedNode) bool {
	x0, x1 := minmax(a.X, b.X)
	y0, y1 := minmax(a.Y, b.Y)
	return x1 >= n.X && x0 < n.X+n.W && y1 >= n.Y && y0 < n.Y+n.H
}

// normalize shifts a scene so nothing sits at a negative coordinate — a left
// channel can route outside the origin — and sizes it to what it now covers.
func normalize(s *Scene) {
	dx, dy := 0, 0
	for _, e := range s.Edges {
		for _, p := range e.Points {
			dx = min(dx, p.X)
			dy = min(dy, p.Y)
		}
	}
	dx, dy = -dx, -dy
	if dx != 0 || dy != 0 {
		for i := range s.Nodes {
			s.Nodes[i].X += dx
			s.Nodes[i].Y += dy
		}
		for i := range s.Groups {
			s.Groups[i].X += dx
			s.Groups[i].Y += dy
		}
		for i := range s.Edges {
			for j := range s.Edges[i].Points {
				s.Edges[i].Points[j].X += dx
				s.Edges[i].Points[j].Y += dy
			}
		}
	}
	w, h := 0, 0
	for _, n := range s.Nodes {
		w = max(w, n.X+n.W)
		h = max(h, n.Y+n.H)
	}
	for _, g := range s.Groups {
		w = max(w, g.X+g.W)
		h = max(h, g.Y+g.H)
	}
	for _, e := range s.Edges {
		for _, p := range e.Points {
			w = max(w, p.X+1)
			h = max(h, p.Y+1)
		}
	}
	s.Width, s.Height = w, h
	// Frames are drawn before the boxes they enclose, outermost first, so a
	// nested group cannot be painted over by its parent.
	sort.SliceStable(s.Groups, func(i, j int) bool {
		return s.Groups[i].W*s.Groups[i].H > s.Groups[j].W*s.Groups[j].H
	})
}

func centerX(n PlacedNode) int { return n.X + n.W/2 }

func minmax(a, b int) (int, int) {
	if a > b {
		return b, a
	}
	return a, b
}
