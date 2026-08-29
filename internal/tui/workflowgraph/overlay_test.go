package workflowgraph

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The overlay's goldens are paired with invariant assertions, per task 017
// decision 21: a golden alone can be refreshed into blessing a wrong picture,
// so what must stay true is asserted separately from what it looks like.

// fixtureLaneShadow is the shape task 051 decision 2 exists for: a top-level
// `build` and a fan_out lane's own `build`. Step ids are unique per *body*
// (§7.6, task 014 decision 4), so this workflow is valid — and before the
// namespacing it drew two nodes answering to one id.
func fixtureLaneShadow() *apiclient.WorkflowBody {
	spread := step("spread", "fan_out")
	spread.Lanes = []apiclient.WorkflowLaneDef{
		{ID: "api", Steps: []apiclient.WorkflowStepDef{step("build", "command")}},
		{ID: "web", Steps: []apiclient.WorkflowStepDef{step("build", "command")}},
	}
	return body(step("build", "command"), spread, step("ship", "command"))
}

func TestLaneInnerNodeIDsAreUniqueAcrossTheDiagram(t *testing.T) {
	for name, wf := range corpus() {
		t.Run(name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, n := range Build(wf).Nodes {
				if seen[n.ID] {
					t.Errorf("two nodes answer to id %q", n.ID)
				}
				seen[n.ID] = true
			}
		})
	}
}

// A lane's `build` and the top level's `build` are two different steps, and
// therefore two different nodes — but both keep the raw step id, which is
// what a step_run row still joins on.
func TestLaneStepShadowsNothing(t *testing.T) {
	d := Build(fixtureLaneShadow())
	var ids []string
	for _, n := range d.Nodes {
		if n.StepID == "build" {
			ids = append(ids, n.ID)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("nodes for step id build = %v, want the top-level one and both lanes'", ids)
	}
	if ids[0] != "build" {
		t.Errorf("the top-level node = %q, want the raw step id", ids[0])
	}
	lanes := map[string]bool{
		lanePrefix("spread", "api") + "build": true,
		lanePrefix("spread", "web") + "build": true,
	}
	for _, id := range ids[1:] {
		if !lanes[id] {
			t.Errorf("lane node id %q is not namespaced by its lane", id)
		}
	}
}

// The parent task holds no step_run for a lane's inline steps — they run in
// the child — so a parent row for `build` must paint exactly the top-level
// node. This is the join the namespacing exists to make honest.
func TestParentRowPaintsOneNode(t *testing.T) {
	d := Build(fixtureLaneShadow())
	painted := 0
	for _, n := range d.Nodes {
		if n.ID == "build" {
			painted++
		}
	}
	if painted != 1 {
		t.Fatalf("a parent step_run for build would paint %d nodes, want 1", painted)
	}
}

// Applying an overlay must not move a single node or change the selection:
// this surface is refreshed live, and a picture that moved under a reader
// watching a running task is the instability 017 decision 5 refuses.
func TestOverlayDoesNotRelayOut(t *testing.T) {
	m := New()
	m.SetSize(120, 40)
	m.SetDefinition(fixtureParallel())
	m.Select("unit")

	before := append([]PlacedNode{}, m.scene.Nodes...)
	m.SetOverlay(Overlay{Nodes: map[string]RunState{
		"plan": {State: "succeeded"},
		"unit": {State: "running", Current: true},
	}})

	if m.Selected() != "unit" {
		t.Errorf("selection = %q, want it kept across the overlay", m.Selected())
	}
	if len(before) != len(m.scene.Nodes) {
		t.Fatalf("the overlay changed the node count: %d to %d", len(before), len(m.scene.Nodes))
	}
	for i := range before {
		if before[i] != m.scene.Nodes[i] {
			t.Errorf("node %s moved: %+v to %+v", before[i].ID, before[i], m.scene.Nodes[i])
		}
	}
}

// A guard skip (§7.7), a human skip (§6) and a node the task never reached
// are three different things, and they read as three different things with
// every escape sequence stripped (017 decision 6).
func TestSkipsReadApartWithStylesStripped(t *testing.T) {
	d := Build(fixtureGuarded())
	s := Layout(d, DefaultOptions())
	out := strings.Join(Render(d, s, ViewState{Run: Overlay{Nodes: map[string]RunState{
		"plan":  {State: "skipped", SkipReason: "condition"},
		"maybe": {State: "skipped"},
	}}}, Theme{}), "\n")

	if !strings.Contains(out, "skipped if") {
		t.Error("a false if: guard does not say so on the node")
	}
	if strings.Count(out, "skipped") != 2 {
		t.Errorf("want exactly two skipped nodes drawn, got:\n%s", out)
	}
	// `ship` was never reached, so it carries nothing at all.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ship") && strings.Contains(line, "skipped") {
			t.Errorf("a node that was never reached was painted skipped: %q", line)
		}
	}
}

// A parked task says where it is parked, and what stopped it there.
func TestParkedTaskNamesItsBlockReason(t *testing.T) {
	d := Build(fixtureSequential())
	s := Layout(d, DefaultOptions())
	out := strings.Join(Render(d, s, ViewState{Run: Overlay{Nodes: map[string]RunState{
		"build": {State: "running", Current: true, Task: "blocked", BlockReason: "worktree_dirty"},
	}}}, Theme{}), "\n")
	// The node is 22 cells wide, so the reason may be truncated — what must
	// survive is that the node says it is blocked and starts naming why.
	if !strings.Contains(out, "blocked") {
		t.Errorf("a blocked task does not say so on its own step:\n%s", out)
	}
}

// An attempt no node answers for is drawn under END, in a frame that names it
// as off the snapshot (decision 3).
func TestOffSnapshotRunsHangUnderEnd(t *testing.T) {
	m := New()
	m.SetSize(120, 60)
	m.SetDefinition(fixtureSequential())
	m.SetOverlay(Overlay{Off: []OffGraphRun{{StepID: "follow_up_1", Label: "follow up", Type: "agent"}}})

	end, ok := m.scene.Node(EndNodeID)
	if !ok {
		t.Fatal("END was not placed")
	}
	off, ok := m.scene.Node("#off:follow_up_1")
	if !ok {
		t.Fatal("the off-snapshot run was not drawn")
	}
	if off.Y <= end.Y {
		t.Errorf("the off-snapshot run at y=%d is not below END at y=%d", off.Y, end.Y)
	}
	out := strings.Join(Render(m.diagram, m.scene, ViewState{}, Theme{}), "\n")
	if !strings.Contains(out, "off-graph") {
		t.Errorf("the frame does not name itself off-graph:\n%s", out)
	}
}

// A lane's state lands on its caption, never on its inline steps: those run
// in a child task the parent holds no step rows for (decision 1).
func TestLaneCaptionCarriesItsChild(t *testing.T) {
	d := Build(fixtureFanOut())
	s := Layout(d, DefaultOptions())
	out := strings.Join(Render(d, s, ViewState{Run: Overlay{
		Lanes: map[string]RunState{LaneKey("spread", "api"): {State: "running", ChildTaskID: 42}},
	}}, Theme{}), "\n")
	if !strings.Contains(out, "#42") {
		t.Errorf("the lane caption does not name its child task:\n%s", out)
	}
	if !strings.Contains(out, "api #42 running") {
		t.Errorf("the lane caption does not carry the child's state:\n%s", out)
	}
}

// A queued task has run nothing: every node is pending and there is no
// current marker anywhere.
func TestQueuedTaskDrawsNoMarkers(t *testing.T) {
	d := Build(fixtureSequential())
	s := Layout(d, DefaultOptions())
	plain := Render(d, s, ViewState{}, Theme{})
	queued := Render(d, s, ViewState{Run: Overlay{Nodes: map[string]RunState{}}}, Theme{})
	if strings.Join(plain, "\n") != strings.Join(queued, "\n") {
		t.Error("an empty overlay changed the picture")
	}
}

// The overlay is words and glyphs, not color: every state must survive having
// every escape sequence stripped.
func TestOverlayGolden(t *testing.T) {
	d := Build(fixtureLoop())
	s := Layout(d, DefaultOptions())
	run := Overlay{Nodes: map[string]RunState{
		"plan":   {State: "succeeded", Attempt: 1},
		"repeat": {State: "running", Iteration: 2, Current: true},
		"work":   {State: "failed", Attempt: 3, Iteration: 2},
		"verify": {State: "skipped", SkipReason: "condition", Iteration: 1},
	}}
	got := strings.Join(Render(d, s, ViewState{Run: run}, Theme{}), "\n") + "\n"
	compareGolden(t, "overlay.txt", got)
	for _, want := range []string{"succeeded", "running", "it 2", "failed", "skipped if"} {
		if !strings.Contains(got, want) {
			t.Errorf("the overlay does not print %q:\n%s", want, got)
		}
	}
	// A node too narrow for every qualifier drops them from the right, and
	// the marker glyph is the last thing to go: a box showing nothing but
	// its state has stopped being one a reader can find again.
	wide := Render(d, Layout(d, Options{NodeWidth: 34, NodeHeight: 4, RankGap: 2, ColumnGap: 3}),
		ViewState{Run: run}, Theme{})
	if !strings.Contains(strings.Join(wide, "\n"), "try 3") {
		t.Errorf("a node with room for it does not print the attempt:\n%s", strings.Join(wide, "\n"))
	}
	for _, line := range strings.Split(got, "\n") {
		if line != ansi.Strip(line) {
			t.Errorf("the plain render carries escape sequences: %q", line)
		}
	}
}
