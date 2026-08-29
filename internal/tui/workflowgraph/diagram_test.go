package workflowgraph

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

func nodeIDs(d Diagram) []string {
	out := make([]string, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		out = append(out, n.ID)
	}
	sort.Strings(out)
	return out
}

// edgeSet renders the edges as comparable strings, which is how a topology
// assertion stays readable when it fails.
func edgeSet(d Diagram) []string {
	out := make([]string, 0, len(d.Edges))
	for _, e := range d.Edges {
		s := e.From + "->" + e.To + "[" + string(e.Kind) + "]"
		if e.Label != "" {
			s += ":" + e.Label
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func hasEdge(t *testing.T, d Diagram, want string) {
	t.Helper()
	for _, got := range edgeSet(d) {
		if got == want {
			return
		}
	}
	t.Errorf("missing edge %s; have %v", want, edgeSet(d))
}

func noEdge(t *testing.T, d Diagram, unwanted string) {
	t.Helper()
	for _, got := range edgeSet(d) {
		if got == unwanted {
			t.Errorf("unexpected edge %s", unwanted)
		}
	}
}

func node(t *testing.T, d Diagram, id string) Node {
	t.Helper()
	for _, n := range d.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %q; have %v", id, nodeIDs(d))
	return Node{}
}

func group(t *testing.T, d Diagram, header string) Group {
	t.Helper()
	for _, g := range d.Groups {
		if g.Header == header {
			return g
		}
	}
	t.Fatalf("no group headed by %q", header)
	return Group{}
}

func TestBuildSequential(t *testing.T) {
	d := Build(fixtureSequential())
	if got, want := nodeIDs(d), []string{"#end", "build", "plan", "ship"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nodes = %v, want %v", got, want)
	}
	if got, want := d.Root, []string{"plan", "build", "ship", EndNodeID}; !reflect.DeepEqual(got, want) {
		t.Errorf("root = %v, want source order ending at END", got)
	}
	hasEdge(t, d, "plan->build[flow]")
	hasEdge(t, d, "build->ship[flow]")
	hasEdge(t, d, "ship->#end[flow]")
	if len(d.Edges) != 3 {
		t.Errorf("edges = %v, want exactly the three successions", edgeSet(d))
	}
}

func TestBuildNilBodyIsEmpty(t *testing.T) {
	d := Build(nil)
	if len(d.Nodes) != 0 || len(d.Edges) != 0 {
		t.Errorf("nil body produced %d nodes and %d edges, want none", len(d.Nodes), len(d.Edges))
	}
}

// A guard on an ordinary step means skip-and-continue, so it is a badge and
// not a second branch (decision 5). A check is the same: presence on the
// node, text in the inspector (decision 15).
func TestBuildGuardIsABadgeNotABranch(t *testing.T) {
	d := Build(fixtureGuarded())
	maybe := node(t, d, "maybe")
	if !reflect.DeepEqual(maybe.Badges, []string{"if"}) {
		t.Errorf("badges = %v, want [if]", maybe.Badges)
	}
	if len(d.Edges) != 3 {
		t.Errorf("edges = %v, want a plain three-step chain", edgeSet(d))
	}
	plan := node(t, d, "plan")
	if !reflect.DeepEqual(plan.Badges, []string{"chk"}) {
		t.Errorf("check badges = %v, want [chk]", plan.Badges)
	}
	if detail(plan, "check") != "go build ./..." {
		t.Errorf("check detail = %q, want the command itself", detail(plan, "check"))
	}
	if detail(maybe, "if") == "" {
		t.Error("the guard expression is not in the inspector detail")
	}
}

func detail(n Node, label string) string {
	for _, f := range n.Detail {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

func TestBuildConditionFalseEndsTheSequence(t *testing.T) {
	d := Build(fixtureCondition())
	hasEdge(t, d, "gate->#end[branch]:false")
	hasEdge(t, d, "gate->deploy[flow]")
	hasEdge(t, d, "deploy->#end[flow]")
}

// A parallel group has no merge node: its join is the members finishing, not
// an operation that runs (§7.5).
func TestBuildParallelHasNoMergeNode(t *testing.T) {
	d := Build(fixtureParallel())
	for _, n := range d.Nodes {
		if n.Kind == KindMerge {
			t.Fatalf("parallel produced a merge node %q", n.ID)
		}
	}
	g := group(t, d, "checks")
	if g.Kind != GroupParallel || len(g.Columns) != 3 {
		t.Fatalf("group = %+v, want three parallel columns", g)
	}
	for i, want := range []string{"lint", "unit", "e2e"} {
		if got := g.Columns[i].Nodes; len(got) != 1 || got[0] != want {
			t.Errorf("column %d = %v, want [%s] in source order", i, got, want)
		}
		hasEdge(t, d, "checks->"+want+"[flow]")
		hasEdge(t, d, want+"->ship[flow]")
	}
	if b := node(t, d, "checks").Badges; !reflect.DeepEqual(b, []string{"max 2"}) {
		t.Errorf("badges = %v, want [max 2]", b)
	}
}

func TestBuildFanOutLanesAndMerge(t *testing.T) {
	d := Build(fixtureFanOut())
	merge := mergeNodeID("spread")
	ref := refNodeID("spread", "web")
	// A lane's inline steps are namespaced by the lane they run in (task 050
	// decision 2): the parent's `build` and a lane's `build` are two steps,
	// and were two nodes answering to one id until they were.
	impl := lanePrefix("spread", "api") + "api_impl"
	test := lanePrefix("spread", "api") + "api_test"

	hasEdge(t, d, "spread->"+impl+"[flow]")
	hasEdge(t, d, impl+"->"+test+"[flow]")
	hasEdge(t, d, test+"->"+merge+"[flow]")
	hasEdge(t, d, "spread->"+ref+"[flow]")
	hasEdge(t, d, ref+"->"+merge+"[flow]")
	hasEdge(t, d, merge+"->ship[flow]")

	// The referenced workflow stays one collapsed node: opening it is
	// navigation, not rendering (017 non-goals).
	refNode := node(t, d, ref)
	if refNode.Kind != KindWorkflowRef || refNode.Label != "web-feature" {
		t.Errorf("reference node = %+v, want the named workflow collapsed", refNode)
	}
	if m := node(t, d, merge); !reflect.DeepEqual(m.Badges, []string{"agent"}) {
		t.Errorf("merge badges = %v, want [agent] for on_conflict: agent", m.Badges)
	}
	g := group(t, d, "spread")
	if g.Columns[1].ID != "web" || !reflect.DeepEqual(g.Columns[1].Badges, []string{"if"}) {
		t.Errorf("lane column = %+v, want the lane's id and its guard", g.Columns[1])
	}
}

// A merge left unspelled is `block`, and block earns no badge: the difference
// worth seeing without selecting is the one where an agent may resolve a
// conflict unread (decision 15).
func TestBuildMergeBlockHasNoBadge(t *testing.T) {
	wf := fixtureFanOut()
	wf.Steps[1].Merge = nil
	d := Build(wf)
	m := node(t, d, mergeNodeID("spread"))
	if len(m.Badges) != 0 {
		t.Errorf("badges = %v, want none for the block default", m.Badges)
	}
	if detail(m, "on_conflict") != "block" {
		t.Errorf("on_conflict detail = %q, want block", detail(m, "on_conflict"))
	}
}

func TestBuildLoopBackEdgeAndExit(t *testing.T) {
	d := Build(fixtureLoop())
	hasEdge(t, d, "repeat->work[flow]")
	hasEdge(t, d, "work->verify[flow]")
	hasEdge(t, d, "verify->repeat[back]")
	// The loop leaves where it decides not to iterate again: at the header.
	hasEdge(t, d, "repeat->ship[flow]")
	if b := node(t, d, "repeat").Badges; !reflect.DeepEqual(b, []string{"×3", "max 5"}) {
		t.Errorf("badges = %v, want the driver and its bound", b)
	}
}

// How many iterations a for_each becomes is discovered at run time, so the
// badge names the driver and the body is drawn once.
func TestBuildForEachNamesTheDriverOnly(t *testing.T) {
	d := Build(fixtureLoopBreak())
	if b := node(t, d, "repeat").Badges; !reflect.DeepEqual(b, []string{"for_each"}) {
		t.Errorf("badges = %v, want [for_each]", b)
	}
}

// A break leaves the loop for what follows it. Routing it back to the header
// would draw the one thing a break means not to happen.
func TestBuildBreakExitsTheLoop(t *testing.T) {
	d := Build(fixtureLoopBreak())
	hasEdge(t, d, "enough->ship[branch]:true")
	hasEdge(t, d, "enough->repeat[back]")
	noEdge(t, d, "enough->repeat[branch]:true")
}

// A condition inside a loop body ends that iteration, which is what continue
// means (§7.8) — so its false edge lands on the loop header, never on END.
func TestBuildConditionInLoopEndsTheIteration(t *testing.T) {
	d := Build(fixtureNested())
	hasEdge(t, d, "skip->repeat[branch]:false")
	noEdge(t, d, "skip->#end[branch]:false")
	hasEdge(t, d, "record->repeat[back]")
}

// Exactly one END exists and only at the top level (decision 16).
func TestBuildHasOneEndNode(t *testing.T) {
	for name, wf := range map[string]*apiclient.WorkflowBody{
		"sequential": fixtureSequential(),
		"condition":  fixtureCondition(),
		"parallel":   fixtureParallel(),
		"fan_out":    fixtureFanOut(),
		"loop":       fixtureLoop(),
		"break":      fixtureLoopBreak(),
		"nested":     fixtureNested(),
	} {
		d := Build(wf)
		ends := 0
		for _, n := range d.Nodes {
			if n.Kind == KindEnd {
				ends++
			}
		}
		if ends != 1 {
			t.Errorf("%s: %d END nodes, want exactly one", name, ends)
		}
	}
}

// Node identity is semantic, and every synthetic id is namespaced so it
// cannot collide with an authored step id — which the workflow language does
// not restrict (decision 6, round 3).
func TestBuildIdsAreStableAndNamespaced(t *testing.T) {
	first := Build(fixtureFanOut())
	second := Build(fixtureFanOut())
	if !reflect.DeepEqual(nodeIDs(first), nodeIDs(second)) {
		t.Fatal("two builds of one workflow disagree on node ids")
	}
	for _, n := range first.Nodes {
		if n.StepID != "" && Synthetic(n.ID) {
			t.Errorf("authored step %q got a synthetic id %q", n.StepID, n.ID)
		}
		if n.StepID == "" && !Synthetic(n.ID) {
			t.Errorf("synthetic node %q is not namespaced", n.ID)
		}
	}
	// A workflow whose steps are named after the synthetic artifacts still
	// produces distinct ids.
	wf := body(step("end", "agent"), step("merge", "command"))
	d := Build(wf)
	if len(nodeIDs(d)) != 3 {
		t.Errorf("nodes = %v, want the two steps plus a distinct END", nodeIDs(d))
	}
	for _, id := range nodeIDs(d) {
		if id == EndNodeID && !strings.HasPrefix(id, syntheticPrefix) {
			t.Errorf("END id %q lost its namespace", id)
		}
	}
}

// TestIncludeDrawsAsACollapsedRef is task 019 decision 12: an include reuses
// the node kind a fan_out lane's `workflow:` already has, because both are the
// same statement — "this is another workflow" — and expanding either is
// navigation rather than rendering (task 017 non-goals).
//
// It is labelled with the workflow it splices in rather than with its own id:
// at node size the callee's name is the useful half, and the id is in the
// inspector.
func TestIncludeDrawsAsACollapsedRef(t *testing.T) {
	d := Build(fixtureInclude())
	for _, id := range []string{"verify", "recheck"} {
		n := node(t, d, id)
		if n.Kind != KindWorkflowRef {
			t.Errorf("%s kind = %q, want %q", id, n.Kind, KindWorkflowRef)
		}
		if n.Label != "go-checks" {
			t.Errorf("%s label = %q, want the included workflow's name", id, n.Label)
		}
		if len(n.Detail) == 0 {
			t.Errorf("%s has no inspector detail", id)
		}
	}
	// The include is an ordinary member of its sequence: it does not branch,
	// so `plan → verify → repeat` still reads straight through.
	hasEdge(t, d, "plan->verify[flow]")
	hasEdge(t, d, "verify->repeat[flow]")
}
