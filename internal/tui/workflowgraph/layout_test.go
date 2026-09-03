package workflowgraph

import (
	"reflect"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Geometry is asserted as invariants rather than coordinates: a golden render
// can be refreshed into blessing a wrong picture, and these are the
// properties that must hold whatever the drawing looks like.

func layoutOf(wf *apiclient.WorkflowBody) (Diagram, Scene) {
	d := Build(wf)
	return d, Layout(d, DefaultOptions())
}

func placed(t *testing.T, s Scene, id string) PlacedNode {
	t.Helper()
	n, ok := s.Node(id)
	if !ok {
		t.Fatalf("node %q was not placed", id)
	}
	return n
}

func TestLayoutIsDeterministic(t *testing.T) {
	for name, wf := range corpus() {
		d := Build(wf)
		first := Layout(d, DefaultOptions())
		second := Layout(d, DefaultOptions())
		if !reflect.DeepEqual(first, second) {
			t.Errorf("%s: two layouts of one diagram differ", name)
		}
	}
}

func corpus() map[string]*apiclient.WorkflowBody {
	return map[string]*apiclient.WorkflowBody{
		"sequential": fixtureSequential(),
		"guarded":    fixtureGuarded(),
		"condition":  fixtureCondition(),
		"parallel":   fixtureParallel(),
		"fanout":     fixtureFanOut(),
		"loop":       fixtureLoop(),
		"loopbreak":  fixtureLoopBreak(),
		"nested":     fixtureNested(),
		"wide":       fixtureWideLabels(),
		"laneshadow": fixtureLaneShadow(),
		"lanedag":    fixtureLaneDAG(),
	}
}

// The primary flow is top-to-bottom and an ordinary sequence shares one
// column, which is what makes a straight connector possible (decision 5).
func TestLayoutStacksASequence(t *testing.T) {
	_, s := layoutOf(fixtureSequential())
	prev := placed(t, s, "plan")
	for _, id := range []string{"build", "ship", EndNodeID} {
		n := placed(t, s, id)
		if n.Y <= prev.Y {
			t.Errorf("%s at y=%d is not below %s at y=%d", id, n.Y, prev.ID, prev.Y)
		}
		if centerX(n) != centerX(prev) {
			t.Errorf("%s centered at %d, want the sequence column %d", id, centerX(n), centerX(prev))
		}
		prev = n
	}
}

// Every node is the same width, which is what puts ports on a predictable
// grid (decision 17).
func TestLayoutNodesShareOneWidth(t *testing.T) {
	opts := DefaultOptions()
	for name, wf := range corpus() {
		d := Build(wf)
		s := Layout(d, opts)
		for _, n := range s.Nodes {
			if n.W != opts.NodeWidth || n.H != opts.NodeHeight {
				t.Errorf("%s: node %s is %dx%d, want %dx%d",
					name, n.ID, n.W, n.H, opts.NodeWidth, opts.NodeHeight)
			}
		}
	}
}

// Sibling order is source order, left to right.
func TestLayoutParallelMembersShareARank(t *testing.T) {
	_, s := layoutOf(fixtureParallel())
	lint, unit, e2e := placed(t, s, "lint"), placed(t, s, "unit"), placed(t, s, "e2e")
	if lint.Y != unit.Y || unit.Y != e2e.Y {
		t.Errorf("members at y=%d,%d,%d, want one rank", lint.Y, unit.Y, e2e.Y)
	}
	if lint.X >= unit.X || unit.X >= e2e.X {
		t.Errorf("members at x=%d,%d,%d, want source order", lint.X, unit.X, e2e.X)
	}
	header := placed(t, s, "checks")
	if header.Y >= lint.Y {
		t.Error("the group header is not above its members")
	}
}

// A frame encloses its columns, and a fan_out's merge sits below the frame
// because the join happens after every lane has finished.
func TestLayoutFanOutFramesLanesAboveTheMerge(t *testing.T) {
	_, s := layoutOf(fixtureFanOut())
	var frame PlacedGroup
	for _, g := range s.Groups {
		if g.Kind == GroupFanOut {
			frame = g
		}
	}
	if frame.W == 0 {
		t.Fatal("no fan_out frame was placed")
	}
	lane := lanePrefix("spread", "api")
	for _, id := range []string{lane + "api_impl", lane + "api_test", refNodeID("spread", "web")} {
		n := placed(t, s, id)
		if n.X <= frame.X || n.X+n.W >= frame.X+frame.W ||
			n.Y <= frame.Y || n.Y+n.H >= frame.Y+frame.H {
			t.Errorf("%s at (%d,%d,%d,%d) is not inside the frame (%d,%d,%d,%d)",
				id, n.X, n.Y, n.W, n.H, frame.X, frame.Y, frame.W, frame.H)
		}
	}
	merge := placed(t, s, mergeNodeID("spread"))
	if merge.Y < frame.Y+frame.H {
		t.Errorf("merge at y=%d is not below the frame ending at %d", merge.Y, frame.Y+frame.H)
	}
	// The lanes hang below the header and the merge hangs below them, so the
	// two lane sequences do not share a column.
	api := placed(t, s, lane+"api_impl")
	web := placed(t, s, refNodeID("spread", "web"))
	if api.X == web.X {
		t.Error("both lanes were placed in one column")
	}
}

// A loop body sits inside the loop's frame, and the back-edge leaves to the
// side rather than running back up through the body.
func TestLayoutLoopBackEdgeRoutesBesideTheBody(t *testing.T) {
	d, s := layoutOf(fixtureLoop())
	var back RoutedEdge
	for _, e := range s.Edges {
		if e.Kind == EdgeBack {
			back = e
		}
	}
	if len(back.Points) == 0 {
		t.Fatal("the loop has no routed back-edge")
	}
	if back.To != "repeat" {
		t.Errorf("back-edge targets %q, want the loop header", back.To)
	}
	header := placed(t, s, "repeat")
	body := placed(t, s, "work")
	leftmost := min(header.X, body.X)
	for _, p := range back.Points {
		if p.X >= leftmost && p.Y > header.Y+header.H && p.Y < body.Y {
			t.Errorf("back-edge point %+v runs through the body's column", p)
		}
	}
	// And the diagram it came from still says the same thing.
	hasEdge(t, d, "verify->repeat[back]")
}

// Nothing overlaps: two boxes sharing cells would make the picture a lie and
// the hit test ambiguous.
func TestLayoutNodesDoNotOverlap(t *testing.T) {
	for name, wf := range corpus() {
		_, s := layoutOf(wf)
		for i, a := range s.Nodes {
			for _, b := range s.Nodes[i+1:] {
				if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
					t.Errorf("%s: %s and %s overlap", name, a.ID, b.ID)
				}
			}
		}
	}
}

// Every routed edge starts and ends on a node that exists, and stays on the
// canvas after normalization.
func TestLayoutEdgesAreOnTheCanvas(t *testing.T) {
	for name, wf := range corpus() {
		_, s := layoutOf(wf)
		for _, e := range s.Edges {
			if _, ok := s.Node(e.From); !ok {
				t.Errorf("%s: edge from missing node %q", name, e.From)
			}
			if _, ok := s.Node(e.To); !ok {
				t.Errorf("%s: edge to missing node %q", name, e.To)
			}
			for _, p := range e.Points {
				if p.X < 0 || p.Y < 0 || p.X >= s.Width || p.Y >= s.Height {
					t.Errorf("%s: edge %s->%s leaves the canvas at %+v (%dx%d)",
						name, e.From, e.To, p, s.Width, s.Height)
				}
			}
		}
	}
}

func TestNodeAtHitTest(t *testing.T) {
	_, s := layoutOf(fixtureSequential())
	n := placed(t, s, "build")
	for _, p := range []Point{{n.X, n.Y}, {n.X + n.W - 1, n.Y + n.H - 1}, {centerX(n), n.Y + 1}} {
		got, ok := s.NodeAt(p.X, p.Y)
		if !ok || got.ID != "build" {
			t.Errorf("NodeAt%+v = %q/%v, want build", p, got.ID, ok)
		}
	}
	if _, ok := s.NodeAt(n.X-1, n.Y); ok {
		t.Error("NodeAt reported a node one cell outside its box")
	}
}

// The narrow-terminal threshold is derived from the node width, so raising
// one cannot leave the other behind (decision 17).
func TestMinWidthFollowsNodeWidth(t *testing.T) {
	if got, want := DefaultOptions().MinWidth(), DefaultOptions().NodeWidth+4; got != want {
		t.Errorf("MinWidth() = %d, want %d", got, want)
	}
	wide := Options{NodeWidth: 40, NodeHeight: 4, RankGap: 2, ColumnGap: 3}
	if wide.MinWidth() <= DefaultOptions().MinWidth() {
		t.Error("a wider node did not widen the threshold")
	}
}

// A zero Options is the default rather than a division by zero: a caller that
// forgets to size the layout gets a picture, not a panic.
func TestLayoutZeroOptionsFallsBackToDefaults(t *testing.T) {
	d := Build(fixtureSequential())
	if got, want := Layout(d, Options{}), Layout(d, DefaultOptions()); !reflect.DeepEqual(got, want) {
		t.Error("zero Options did not fall back to the defaults")
	}
}
