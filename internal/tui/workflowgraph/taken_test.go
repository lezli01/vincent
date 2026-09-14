package workflowgraph

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Run-state color (task 097) is read off the canvas as roles and tints, never
// off escape sequences: what a cell *is* does not depend on the color profile
// of the terminal running the tests.

func edgeKey(from, to string, kind EdgeKind) string { return from + ">" + to + "/" + string(kind) }

// takenByKey runs takenEdges over a workflow and keys the answers by edge.
func takenByKey(wf *apiclient.WorkflowBody, o Overlay) map[string]edgeRun {
	d := Build(wf)
	s := Layout(d, DefaultOptions())
	out := map[string]edgeRun{}
	for i, run := range takenEdges(d, s, o) {
		e := s.Edges[i]
		out[edgeKey(e.From, e.To, e.Kind)] = run
	}
	return out
}

func nodes(states ...any) map[string]RunState {
	out := map[string]RunState{}
	for i := 0; i+1 < len(states); i += 2 {
		out[states[i].(string)] = states[i+1].(RunState)
	}
	return out
}

func TestTakenEdges(t *testing.T) {
	ok := RunState{State: "succeeded"}
	it := func(state string, n int) RunState { return RunState{State: state, Iteration: n} }
	apiImpl := lanePrefix("spread", "api") + "api_impl"
	apiTest := lanePrefix("spread", "api") + "api_test"

	for _, tc := range []struct {
		name string
		wf   *apiclient.WorkflowBody
		run  Overlay
		want map[string]bool
	}{
		{
			name: "a condition that holds takes its onward edge, not its false one",
			wf:   fixtureCondition(),
			run:  Overlay{Nodes: nodes("plan", ok, "gate", ok, "deploy", RunState{State: "running"})},
			want: map[string]bool{
				edgeKey("plan", "gate", EdgeFlow):      true,
				edgeKey("gate", "deploy", EdgeFlow):    true,
				edgeKey("gate", EndNodeID, EdgeBranch): false,
				edgeKey("deploy", EndNodeID, EdgeFlow): false,
			},
		},
		{
			// The literal both-ends rule would light `false` here once END is
			// reached even when the condition held. It did not hold: it stopped.
			name: "a condition that stops takes its false edge only",
			wf:   fixtureCondition(),
			run:  Overlay{Done: true, Nodes: nodes("plan", ok, "gate", RunState{State: "stopped"})},
			want: map[string]bool{
				edgeKey("gate", EndNodeID, EdgeBranch): true,
				edgeKey("gate", "deploy", EdgeFlow):    false,
				edgeKey("deploy", EndNodeID, EdgeFlow): false,
			},
		},
		{
			name: "a condition in a loop that holds on its only pass",
			wf:   fixtureNested(),
			run: Overlay{Nodes: nodes(
				"work", it("succeeded", 1), "skip", it("succeeded", 1), "record", it("succeeded", 1),
			)},
			want: map[string]bool{
				edgeKey("repeat", "work", EdgeFlow):   true,
				edgeKey("work", "skip", EdgeFlow):     true,
				edgeKey("skip", "record", EdgeFlow):   true,
				edgeKey("skip", "repeat", EdgeBranch): false,
				edgeKey("record", "repeat", EdgeBack): false,
				edgeKey("repeat", "spread", EdgeFlow): false,
			},
		},
		{
			// The newest row governs: it held on pass one and stopped on pass
			// two, so it is the false edge that shows.
			name: "a condition in a loop that stops on its second pass",
			wf:   fixtureNested(),
			run: Overlay{Nodes: nodes(
				"work", it("succeeded", 2), "skip", it("stopped", 2), "record", it("succeeded", 1),
			)},
			want: map[string]bool{
				edgeKey("skip", "repeat", EdgeBranch): true,
				edgeKey("skip", "record", EdgeFlow):   false,
				edgeKey("record", "repeat", EdgeBack): true,
			},
		},
		{
			name: "a break that leaves",
			wf:   fixtureLoopBreak(),
			run: Overlay{Nodes: nodes(
				"plan", ok, "work", it("succeeded", 1), "enough", it("stopped", 1), "ship", RunState{State: "running"},
			)},
			want: map[string]bool{
				edgeKey("enough", "ship", EdgeBranch): true,
				edgeKey("work", "enough", EdgeFlow):   true,
				edgeKey("enough", "repeat", EdgeBack): false,
			},
		},
		{
			name: "a break that does not leave",
			wf:   fixtureLoopBreak(),
			run: Overlay{Nodes: nodes(
				"plan", ok, "work", it("succeeded", 2), "enough", it("succeeded", 2),
			)},
			want: map[string]bool{
				edgeKey("enough", "ship", EdgeBranch): false,
				edgeKey("enough", "repeat", EdgeBack): true,
				edgeKey("repeat", "ship", EdgeFlow):   false,
			},
		},
		{
			name: "a back-edge after one pass stays dark",
			wf:   fixtureLoop(),
			run:  Overlay{Nodes: nodes("plan", ok, "work", it("succeeded", 1), "verify", it("succeeded", 1))},
			want: map[string]bool{
				edgeKey("plan", "repeat", EdgeFlow):   true,
				edgeKey("repeat", "work", EdgeFlow):   true,
				edgeKey("work", "verify", EdgeFlow):   true,
				edgeKey("verify", "repeat", EdgeBack): false,
			},
		},
		{
			name: "a back-edge after two passes is taken",
			wf:   fixtureLoop(),
			run:  Overlay{Nodes: nodes("plan", ok, "work", it("running", 2), "verify", it("succeeded", 1))},
			want: map[string]bool{
				edgeKey("verify", "repeat", EdgeBack): true,
			},
		},
		{
			name: "a parallel header is reached through its members",
			wf:   fixtureParallel(),
			run:  Overlay{Nodes: nodes("plan", ok, "unit", RunState{State: "running"})},
			want: map[string]bool{
				edgeKey("plan", "checks", EdgeFlow): true,
				edgeKey("checks", "unit", EdgeFlow): true,
				edgeKey("checks", "lint", EdgeFlow): false,
				edgeKey("unit", "ship", EdgeFlow):   false,
			},
		},
		{
			name: "a merge is reached once its fan_out's row is past running",
			wf:   fixtureFanOut(),
			run:  Overlay{Nodes: nodes("plan", ok, "spread", ok, "ship", RunState{State: "running"})},
			want: map[string]bool{
				edgeKey("plan", "spread", EdgeFlow):              true,
				edgeKey(mergeNodeID("spread"), "ship", EdgeFlow): true,
			},
		},
		{
			name: "a merge is not reached while its fan_out runs",
			wf:   fixtureFanOut(),
			run:  Overlay{Nodes: nodes("plan", ok, "spread", RunState{State: "running"})},
			want: map[string]bool{
				edgeKey(mergeNodeID("spread"), "ship", EdgeFlow): false,
			},
		},
		{
			name: "END is reached only by a done task",
			wf:   fixtureSequential(),
			run:  Overlay{Nodes: nodes("plan", ok, "build", ok, "ship", ok)},
			want: map[string]bool{
				edgeKey("build", "ship", EdgeFlow):   true,
				edgeKey("ship", EndNodeID, EdgeFlow): false,
			},
		},
		{
			name: "END is reached by a done task",
			wf:   fixtureSequential(),
			run:  Overlay{Done: true, Nodes: nodes("plan", ok, "build", ok, "ship", ok)},
			want: map[string]bool{
				edgeKey("ship", EndNodeID, EdgeFlow): true,
			},
		},
		{
			// Even an overlay that wrongly carries lane-inner rows lights none of
			// their edges: those steps are the child task's (051 decision 1).
			name: "lane-inner edges are never taken",
			wf:   fixtureFanOut(),
			run: Overlay{Done: true, Nodes: nodes(
				"plan", ok, "spread", ok, "ship", ok, apiImpl, ok, apiTest, ok,
			)},
			want: map[string]bool{
				edgeKey("spread", apiImpl, EdgeFlow):              false,
				edgeKey(apiImpl, apiTest, EdgeFlow):               false,
				edgeKey(apiTest, mergeNodeID("spread"), EdgeFlow): false,
				edgeKey(mergeNodeID("spread"), "ship", EdgeFlow):  true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := takenByKey(tc.wf, tc.run)
			for key, want := range tc.want {
				run, found := got[key]
				if !found {
					t.Fatalf("no edge %s in the scene; edges: %v", key, keys(got))
				}
				if run.taken != want {
					t.Errorf("edge %s taken = %v, want %v", key, run.taken, want)
				}
			}
		})
	}
}

// A `needs:` edge is between two child tasks, never succession in the parent.
func TestNeedsEdgesAreNeverTaken(t *testing.T) {
	d := Build(fixtureLaneDAG())
	ok := RunState{State: "succeeded"}
	run := Overlay{Done: true, Nodes: map[string]RunState{"plan": ok, "spread": ok, "ship": ok}}
	for _, n := range d.Nodes {
		run.Nodes[n.ID] = ok
	}
	s := Layout(d, DefaultOptions())
	needs := 0
	for i, r := range takenEdges(d, s, run) {
		if s.Edges[i].Kind != EdgeNeeds {
			continue
		}
		needs++
		if r.taken {
			t.Errorf("needs edge %s->%s was taken", s.Edges[i].From, s.Edges[i].To)
		}
	}
	if needs == 0 {
		t.Fatal("the lane DAG fixture drew no needs edges")
	}
}

// A taken edge takes its source's state; a derived source has none, so it
// takes its target's.
func TestTakenEdgeTakesItsSourceElseItsTarget(t *testing.T) {
	plan := RunState{State: "succeeded", Attempt: 1}
	unit := RunState{State: "running", Attempt: 1}
	got := takenByKey(fixtureParallel(), Overlay{Nodes: nodes("plan", plan, "unit", unit)})

	if r := got[edgeKey("plan", "checks", EdgeFlow)]; !r.stated || r.state != plan {
		t.Errorf("plan->checks styled by %+v (stated %v), want plan's own state", r.state, r.stated)
	}
	if r := got[edgeKey("checks", "unit", EdgeFlow)]; !r.stated || r.state != unit {
		t.Errorf("checks->unit styled by %+v (stated %v), want unit's: checks has no row", r.state, r.stated)
	}
}

// Where wires share a cell, a taken edge beats an untaken one, the later of
// two taken edges wins, and an arrowhead belongs to the edges that end there.
func TestCrossingPrecedence(t *testing.T) {
	across := RoutedEdge{Points: []Point{{0, 3}, {6, 3}}}
	down := RoutedEdge{Points: []Point{{3, 0}, {3, 6}}}
	into := RoutedEdge{Points: []Point{{3, 0}, {3, 3}}}
	for _, tc := range []struct {
		name  string
		edges []RoutedEdge
		taken []bool
		pens  []int
		want  int
	}{
		{"two taken: the later wins", []RoutedEdge{across, down}, []bool{true, true}, []int{1, 2}, 2},
		{"taken beats a later untaken", []RoutedEdge{across, down}, []bool{true, false}, []int{1, 0}, 1},
		{"taken beats an earlier untaken", []RoutedEdge{across, down}, []bool{false, true}, []int{0, 2}, 2},
		{"an untaken arrowhead is not colored by a crossing wire", []RoutedEdge{across, into}, []bool{true, false}, []int{1, 0}, 0},
		{"a taken arrowhead keeps its color", []RoutedEdge{into, across}, []bool{true, true}, []int{2, 1}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanvas(7, 7)
			for i, e := range tc.edges {
				c.edge(e, unicodeGlyphs, tc.taken[i], tc.pens[i])
			}
			c.paintWires(unicodeGlyphs)
			if got := c.pens[3][3]; got != tc.want {
				t.Errorf("pen at the shared cell = %d, want %d", got, tc.want)
			}
		})
	}
}

// testTheme colors by state the way the host does — the parked task first —
// with one distinct color per state, so a tint can be told apart without a
// terminal.
func testTheme() Theme {
	palette := map[string]string{
		"succeeded": "2", "running": "6", "failed": "1", "blocked": "9", "stopped": "8",
		"skipped": "8", "done": "2", "awaiting_input": "3",
	}
	lookup := func(rs RunState) (lipgloss.Style, bool) {
		for _, key := range []string{rs.Task, rs.State} {
			if c, ok := palette[key]; ok && key != "" {
				return lipgloss.NewStyle().Foreground(lipgloss.Color(c)), true
			}
		}
		return lipgloss.Style{}, false
	}
	return Theme{
		Node:      lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
		Selected:  lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		Frame:     lipgloss.NewStyle().Faint(true),
		Edge:      lipgloss.NewStyle().Faint(true),
		EdgeLabel: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		NodeState: lookup,
		LaneState: lookup,
	}
}

func foreground(c *canvas, x, y int) (color.Color, bool) {
	pen := c.pens[y][x]
	if pen == 0 {
		return nil, false
	}
	return c.tints[pen-1].style.GetForeground(), true
}

// A node takes its newest attempt's style over its border, label row and
// kind row; the task parked on it wins over the step's own state; a node
// never reached keeps its role.
func TestNodeTintByState(t *testing.T) {
	d := Build(fixtureSequential())
	s := Layout(d, DefaultOptions())
	run := Overlay{Nodes: map[string]RunState{
		"plan":  {State: "succeeded"},
		"build": {State: "running", Task: "blocked", Current: true},
	}}
	c := paint(d, s, ViewState{Run: run}, testTheme())

	for _, tc := range []struct {
		id   string
		want color.Color
	}{
		{"plan", lipgloss.Color("2")},
		{"build", lipgloss.Color("9")},
	} {
		n, _ := s.Node(tc.id)
		for _, cell := range []Point{{n.X, n.Y}, {n.X + 2, n.Y + 1}, {n.X + 2, n.Y + 2}} {
			got, tinted := foreground(c, cell.X, cell.Y)
			if !tinted || got != tc.want {
				t.Errorf("%s cell %v = %v (tinted %v), want %v", tc.id, cell, got, tinted, tc.want)
			}
		}
	}
	for _, id := range []string{"ship", EndNodeID} {
		n, _ := s.Node(id)
		if c.pens[n.Y][n.X] != 0 || c.styles[n.Y][n.X] != styleNode {
			t.Errorf("%s: pen %d role %d, want an untinted Node", id, c.pens[n.Y][n.X], c.styles[n.Y][n.X])
		}
	}
}

// A colored node shows its selection by the heavy border alone; an uncolored
// one keeps the Selected role.
func TestSelectedNodeKeepsItsStateColor(t *testing.T) {
	d := Build(fixtureSequential())
	s := Layout(d, DefaultOptions())
	run := Overlay{Nodes: map[string]RunState{"plan": {State: "succeeded"}}}

	for _, tc := range []struct {
		id     string
		role   cellStyle
		tinted bool
	}{
		{"plan", styleNode, true},
		{"ship", styleSelected, false},
	} {
		c := paint(d, s, ViewState{Selected: tc.id, Run: run}, testTheme())
		n, _ := s.Node(tc.id)
		if r := c.runes[n.Y][n.X]; string(r) != unicodeGlyphs.selTopLeft {
			t.Errorf("%s: corner %q, want the selected glyph", tc.id, string(r))
		}
		if got := c.styles[n.Y][n.X]; got != tc.role {
			t.Errorf("%s: role %d, want %d", tc.id, got, tc.role)
		}
		if tinted := c.pens[n.Y][n.X] != 0; tinted != tc.tinted {
			t.Errorf("%s: tinted %v, want %v", tc.id, tinted, tc.tinted)
		}
	}
}

// A lane caption takes the lane lookup's style, and only when a lane has a
// child to speak for.
func TestLaneCaptionTint(t *testing.T) {
	d := Build(fixtureFanOut())
	s := Layout(d, DefaultOptions())
	run := Overlay{Lanes: map[string]RunState{LaneKey("spread", "api"): {State: "done", ChildTaskID: 7}}}
	c := paint(d, s, ViewState{Run: run}, testTheme())
	rows := c.lines(Theme{})
	for y, row := range rows {
		x := strings.Index(row, "api #7")
		if x < 0 {
			continue
		}
		col := ansi.StringWidth(row[:x])
		if got, tinted := foreground(c, col, y); !tinted || got != lipgloss.Color("2") {
			t.Errorf("caption tint = %v (tinted %v), want the done color", got, tinted)
		}
		return
	}
	t.Fatalf("no caption for the api lane:\n%s", strings.Join(rows, "\n"))
}

// An off-graph attempt now says what it did in words, which is what makes it
// safe to color (task 097 decision 3).
func TestOffGraphNodePrintsItsState(t *testing.T) {
	d := AttachOffGraph(Build(fixtureSequential()), []OffGraphRun{{StepID: "follow_up_1", Label: "follow up", Type: "agent"}})
	s := Layout(d, DefaultOptions())
	run := Overlay{Nodes: map[string]RunState{OffNodeID("follow_up_1"): {State: "succeeded"}}}
	c := paint(d, s, ViewState{Run: run}, testTheme())
	n, ok := s.Node(OffNodeID("follow_up_1"))
	if !ok {
		t.Fatal("the off-graph node was not placed")
	}
	label := cells(c.runes[n.Y+1][n.X : n.X+n.W])
	if !strings.Contains(label, "✔") || !strings.Contains(label, "succeeded") {
		t.Errorf("off-graph label row = %q, want its glyph and state", label)
	}
	if c.pens[n.Y][n.X] == 0 {
		t.Error("the off-graph node was not tinted")
	}
}

func keys(m map[string]edgeRun) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
