package workflowgraph

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// A fan_out's lanes are spelled three ways (§7.6, task 080) and the picture
// has to keep them apart, because they say different things about what will
// run: a hand-authored `lanes:` list is the lanes themselves, a registry
// entry's `lane:` template is one shape the step will render a list from, and
// a task snapshot's materialized list is the lanes that rendering produced.
//
// Until this file existed the middle one drew no column at all — the lanes
// came from st.Lanes alone — so a registry entry that fans out over an issue's
// items showed a fan_out that spawns nothing.

// fanOutGroup returns the one fan_out frame in a diagram.
func fanOutGroup(t *testing.T, d Diagram) Group {
	t.Helper()
	for _, g := range d.Groups {
		if g.Kind == GroupFanOut {
			return g
		}
	}
	t.Fatal("the diagram has no fan_out frame")
	return Group{}
}

func incoming(d Diagram, id string) []Edge {
	var out []Edge
	for _, e := range d.Edges {
		if e.To == id {
			out = append(out, e)
		}
	}
	return out
}

// The template is one column, labelled with the id template as authored — a
// definition viewer renders nothing, and `{{ .Item.id }}` is what the file
// says. Its steps are drawn in that column, and the join below the frame is
// reached from them: a merge nothing flows into would say the step spawns no
// work, which is exactly the bug.
func TestALaneTemplateDrawsOneColumn(t *testing.T) {
	d := Build(fixtureLaneTemplate())
	g := fanOutGroup(t, d)
	if len(g.Columns) != 1 {
		t.Fatalf("a lane template drew %d columns, want the one it stands for", len(g.Columns))
	}
	col := g.Columns[0]
	if col.Label != "{{ .Item.id }}" {
		t.Errorf("the column is labelled %q, want the id template as authored", col.Label)
	}
	if col.Key != LaneKey("spread", "{{ .Item.id }}") {
		t.Errorf("the column's key is %q, want it namespaced by its fan_out", col.Key)
	}
	want := []string{
		lanePrefix("spread", "{{ .Item.id }}") + "work",
		lanePrefix("spread", "{{ .Item.id }}") + "verify",
	}
	if strings.Join(col.Nodes, ",") != strings.Join(want, ",") {
		t.Errorf("the column holds %v, want the template's own steps %v", col.Nodes, want)
	}
	// The template's inline steps go through the same builder the list case
	// uses, so they are real nodes rather than a label.
	for _, id := range want {
		if fieldsOf(d, id) == nil {
			t.Errorf("node %q was not built", id)
		}
	}
	if got := incoming(d, mergeNodeID("spread")); len(got) == 0 {
		t.Error("the join has no incoming edge; nothing reaches the merge")
	}
	if got := len(incoming(d, want[0])); got != 1 {
		t.Errorf("the column is entered by %d edges, want the one from the header", got)
	}
}

// One template is one wave and no DAG: the lanes it stands for do not exist
// yet, so there are no siblings to depend on and nothing to stack.
func TestALaneTemplateHasOneWaveAndNoNeedsEdges(t *testing.T) {
	d := Build(fixtureLaneTemplate())
	g := fanOutGroup(t, d)
	if got := g.Columns[0].Wave; got != 1 {
		t.Errorf("the template is in wave %d, want the single round 1", got)
	}
	if got := edgesOfKind(d, EdgeNeeds); len(got) > 0 {
		t.Errorf("a lane template drew %d needs edges", len(got))
	}
	if got := len(waveOrder(g.Columns)); got != 1 {
		t.Errorf("a lane template laid out in %d waves, want 1", got)
	}
}

// A `needs:` on the template names lanes that are not in this picture — the
// siblings do not exist until the step runs — so it is dropped rather than
// drawn, and the column is still entered from the header. Drawing it would
// orphan the only column the frame has.
func TestALaneTemplateIgnoresItsOwnNeeds(t *testing.T) {
	wf := fixtureLaneTemplate()
	wf.Steps[1].Lane.Needs = []string{"someone-else"}
	d := Build(wf)
	if got := fanOutGroup(t, d).Columns[0].Needs; len(got) > 0 {
		t.Errorf("the column depends on %v; a template has no siblings", got)
	}
	entry := lanePrefix("spread", "{{ .Item.id }}") + "work"
	var fromHeader bool
	for _, e := range incoming(d, entry) {
		if e.From == "spread" {
			fromHeader = true
		}
	}
	if !fromHeader {
		t.Error("the template's column is not entered from the fan_out header")
	}
	// The DTO the caller handed in is untouched: the graph is a projection.
	if got := wf.Steps[1].Lane.Needs; len(got) != 1 {
		t.Errorf("Build mutated the definition's lane template: needs = %v", got)
	}
}

// The frame says which of the three lists it encloses. The two derived ones
// name the templates the list comes from and never a count — a definition
// reader cannot know one — and a hand-authored list says nothing at all,
// which is what keeps its frame drawn exactly as it always was.
func TestTheThreeLaneListsReadApartOnTheFrame(t *testing.T) {
	for _, tc := range []struct {
		name string
		wf   *apiclient.WorkflowBody
		want string
	}{
		{"hand-authored", fixtureFanOut(), ""},
		{"template", fixtureLaneTemplate(), "templated from {{ .Steps.plan.Result }}, at most 4"},
		{"materialized", fixtureLaneDAG(), "derived from {{ .Steps.plan.Result }}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fanOutGroup(t, Build(tc.wf)).Note; got != tc.want {
				t.Errorf("the frame's note is %q, want %q", got, tc.want)
			}
		})
	}
}

// `max_lanes` is the one bound on the list the file does state, so the frame
// states it — and a template without one claims no cap.
func TestALaneTemplateNamesItsCapOnlyWhenItHasOne(t *testing.T) {
	wf := fixtureLaneTemplate()
	wf.Steps[1].MaxLanes = nil
	got := fanOutGroup(t, Build(wf)).Note
	if got != "templated from {{ .Steps.plan.Result }}" {
		t.Errorf("the frame's note is %q, want the derivation with no cap", got)
	}
	// Wide enough for the whole sentence, the frame prints the whole
	// sentence. A one-column frame usually is not, so the fallback is the
	// case that matters: `templated` alone still separates this list from a
	// `derived` one, which is the half a reader cannot recover from anywhere
	// else on the picture.
	if got := frameNote(fixtureLaneTemplate(), 140); got != "templated from {{ .Steps.plan.Result }}, at most 4" {
		t.Errorf("a frame with room printed %q, want the whole derivation", got)
	}
	if got := frameNote(fixtureLaneTemplate(), 60); got != "templated" {
		t.Errorf("a narrow frame printed %q, want the one-word fallback", got)
	}
}

// frameNote renders wf at a given node width and returns what the fan_out
// frame's border says after the group's kind, which is the only place the
// derivation reaches the screen without selecting anything.
func frameNote(wf *apiclient.WorkflowBody, width int) string {
	d := Build(wf)
	opts := DefaultOptions()
	opts.NodeWidth = width
	for _, line := range Render(d, Layout(d, opts), ViewState{}, Theme{}) {
		_, note, ok := strings.Cut(line, "fan_out · ")
		if !ok {
			continue
		}
		note, _, _ = strings.Cut(note, " ━")
		return note
	}
	return ""
}

// The modal is the one view that shows a step in full, so the fields a
// registry entry uses to declare a derived fan-out are rows of it: the
// templates the list comes from, the cap, and that the list is derived rather
// than declared.
func TestALaneTemplateReachesTheStepDetail(t *testing.T) {
	fields := fieldsOf(Build(fixtureLaneTemplate()), "spread")
	if got := fields["for_each"].Value; got != "{{ .Steps.plan.Result }}" {
		t.Errorf("for_each = %q on a fan_out, want the template it iterates", got)
	}
	if got := fields["max_lanes"].Value; got != "4" {
		t.Errorf("max_lanes = %q, want 4", got)
	}
	if got := fields["lane"].Value; !strings.Contains(got, "for_each") ||
		!strings.Contains(got, "derived") {
		t.Errorf("lane = %q, want it said the list is derived from for_each", got)
	}
	if _, ok := fields["lanes"]; ok {
		t.Error("an underived fan_out claimed a lane list it does not have")
	}
	// "0 lanes" on the join would say it merges nothing, which is the one
	// thing it is certain not to do.
	merge := fieldsOf(Build(fixtureLaneTemplate()), mergeNodeID("spread"))
	if got := merge["merges"].Value; !strings.Contains(got, "derives") {
		t.Errorf("the join merges %q, want what it will merge rather than a count", got)
	}
}
