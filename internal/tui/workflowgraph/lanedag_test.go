package workflowgraph

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The lane DAG (§7.6, tasks 080 and 081): `needs:` edges between lane
// columns, the rounds those edges imply, an eager schedule, and the record of
// a list that was derived rather than written.
//
// Task 051's non-goal stands over all of it: nothing here unrolls a lane or
// an iteration into extra nodes. The edges and the grouping are drawn over
// the authored lane columns, and there are exactly as many nodes as before.

func laneColumns(t *testing.T, wf *apiclient.WorkflowBody) map[string]Column {
	t.Helper()
	out := map[string]Column{}
	for _, g := range Build(wf).Groups {
		if g.Kind != GroupFanOut {
			continue
		}
		for _, c := range g.Columns {
			out[c.ID] = c
		}
	}
	return out
}

func edgesOfKind(d Diagram, kind EdgeKind) []Edge {
	var out []Edge
	for _, e := range d.Edges {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// A lane that needs another is entered from that lane, not from the fan_out
// header: it is spawned once its dependency is done *and merged*, and an edge
// from the header would say it starts in round one.
func TestLaneNeedsAreDrawnBetweenColumns(t *testing.T) {
	d := Build(fixtureLaneDAG())
	needs := edgesOfKind(d, EdgeNeeds)
	want := map[string]bool{
		"spread.api/api_work→spread.wire/wire_work": false,
		"spread.db/db_work→spread.wire/wire_work":   false,
	}
	for _, e := range needs {
		key := e.From + "→" + e.To
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected needs edge %s", key)
			continue
		}
		want[key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("no needs edge %s", key)
		}
	}
	for _, e := range d.Edges {
		if e.From == "spread" && e.To == "spread.wire/wire_work" {
			t.Error("the header enters a dependent lane; it is spawned by the lanes it needs")
		}
	}
}

// The waves are the topological levels of that graph — derived, never
// authored — and the layout stacks them, so a dependent lane sits below every
// lane it needs.
func TestLaneWavesGroupAndStack(t *testing.T) {
	wf := fixtureLaneDAG()
	cols := laneColumns(t, wf)
	for id, want := range map[string]int{"api": 1, "db": 1, "wire": 2} {
		if got := cols[id].Wave; got != want {
			t.Errorf("lane %q is in wave %d, want %d", id, got, want)
		}
	}
	_, s := layoutOf(wf)
	api := placed(t, s, "spread.api/api_work")
	db := placed(t, s, "spread.db/db_work")
	wire := placed(t, s, "spread.wire/wire_work")
	if wire.Y < api.Y+api.H || wire.Y < db.Y+db.H {
		t.Errorf("wave 2 (%d) is not stacked below wave 1 (%d, %d)", wire.Y, api.Y, db.Y)
	}
	if api.Y != db.Y {
		t.Errorf("two lanes of one wave are on different rows: %d and %d", api.Y, db.Y)
	}
	// The rounds are named on the captions, which is where a lane's own
	// identity already lives.
	render := strings.Join(Render(Build(wf), s, ViewState{}, Theme{}), "\n")
	for _, want := range []string{"api w1", "db w1", "wire w2"} {
		if !strings.Contains(render, want) {
			t.Errorf("the picture does not caption %q:\n%s", want, render)
		}
	}
}

// `schedule: eager` is a fact about the shape of the run — a lane's
// dependents start before its siblings finish — so it is on the node, without
// selecting it. `barrier` is the default and is marked by nothing.
func TestEagerScheduleIsMarked(t *testing.T) {
	if got := badges(fixtureLaneDAG().Steps[1]); len(got) == 0 || got[0] != "eager" {
		t.Errorf("badges = %v, want an eager marker", got)
	}
	flat := fixtureFanOut()
	for _, b := range badges(flat.Steps[1]) {
		if b == "eager" {
			t.Error("a fan_out that names no schedule was marked eager")
		}
	}
}

// A derived lane list says what it was derived from, on the frame that
// encloses the lanes the derivation produced. A hand-authored list says
// nothing, because there is nothing to say.
func TestDerivedLaneListIsMarkedWithItsSource(t *testing.T) {
	var note string
	for _, g := range Build(fixtureLaneDAG()).Groups {
		if g.Kind == GroupFanOut {
			note = g.Note
		}
	}
	if !strings.Contains(note, "derived from") || !strings.Contains(note, ".Steps.plan.Result") {
		t.Errorf("the frame's note is %q, want what the lanes were derived from", note)
	}
	for _, g := range Build(fixtureFanOut()).Groups {
		if g.Kind == GroupFanOut && g.Note != "" {
			t.Errorf("a hand-authored lane list carries the note %q", g.Note)
		}
	}
	// Wide enough for the whole sentence — and for it to end before the
	// header's connector crosses the border — it prints the whole sentence.
	wf := fixtureLaneDAG()
	d := Build(wf)
	opts := DefaultOptions()
	opts.NodeWidth = 60
	got := strings.Join(Render(d, Layout(d, opts), ViewState{}, Theme{}), "\n")
	if !strings.Contains(got, "derived from {{ .Steps.plan.Result }}") {
		t.Errorf("a frame with room did not print the derivation:\n%s", got)
	}
}

// The one that matters: a lane list where nothing needs anything is one wave,
// draws no needs edge, and is laid out exactly as it was before the DAG
// existed. The golden corpus holds the picture; this holds the model.
func TestAFlatLaneListIsUnchanged(t *testing.T) {
	wf := fixtureFanOut()
	d := Build(wf)
	if got := edgesOfKind(d, EdgeNeeds); len(got) > 0 {
		t.Errorf("a flat lane list drew %d needs edges", len(got))
	}
	for id, col := range laneColumns(t, wf) {
		if col.Wave != 1 {
			t.Errorf("lane %q is in wave %d; every lane of a flat list is in round 1", id, col.Wave)
		}
	}
	if got := len(waveOrder(Build(wf).Groups[0].Columns)); got != 1 {
		t.Errorf("a flat list laid out in %d waves, want 1", got)
	}
}

// A lane's whole run state reaches its caption — the child task, what it is
// doing, and the reason it is parked — because a lane's steps run in the
// child and the parent paints none of them (task 051 decision 1). A caption
// runs into the gap beside its column for the same reason: `blocked worktr…`
// has lost the half worth printing.
func TestLaneCaptionCarriesTheChildsBlockReason(t *testing.T) {
	wf := body(step("plan", "agent"), func() apiclient.WorkflowStepDef {
		spread := step("spread", "fan_out")
		spread.Lanes = []apiclient.WorkflowLaneDef{
			{ID: "api", Steps: []apiclient.WorkflowStepDef{step("api_work", "agent")}},
			{ID: "web", Steps: []apiclient.WorkflowStepDef{step("web_work", "agent")}},
		}
		return spread
	}())
	d := Build(wf)
	opts := DefaultOptions()
	opts.NodeWidth = 40
	run := Overlay{Lanes: map[string]RunState{
		LaneKey("spread", "api"): {
			State: "blocked", Task: "blocked", BlockReason: "worktree_dirty", ChildTaskID: 42,
		},
		LaneKey("spread", "web"): {State: "running", ChildTaskID: 43},
	}}
	got := strings.Join(Render(d, Layout(d, opts), ViewState{Run: run}, Theme{}), "\n")
	for _, want := range []string{"api #42 blocked worktree_dirty", "web #43 running"} {
		if !strings.Contains(got, want) {
			t.Errorf("the picture does not caption %q:\n%s", want, got)
		}
	}
}
