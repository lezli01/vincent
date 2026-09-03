package workflowgraph

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// fullStep is a step carrying every field WorkflowStepDef has, which is what
// the coverage test needs to see the whole surface at once. It is deliberately
// not a workflow anyone would write: a step is never all of these at the same
// time, and the modal's job is to show whichever of them the file did set.
func fullStep() apiclient.WorkflowStepDef {
	backoff, timeout, input, check := "30s", "60m", "5m", "2m"
	return apiclient.WorkflowStepDef{
		ID:             "everything",
		Name:           "Run the integration suite on the merged branch",
		Type:           "agent",
		MaxRetries:     intp(2),
		RetryBackoff:   &backoff,
		Timeout:        &timeout,
		If:             `{{ .Fields.deep }}`,
		AllowFailure:   true,
		Prompt:         "Read the diff.\nThen review it.",
		Agent:          "claude",
		Model:          "opus",
		Effort:         "high",
		PermissionMode: "restricted",
		OnInput:        "answer",
		InputTimeout:   &input,
		Check:          "go build ./...",
		CheckTimeout:   &check,
		Run:            "go test ./...\ngo vet ./...",
		Shell:          "/bin/sh",
		Env:            map[string]string{"CI": "1", "TZ": "UTC"},
		Instructions:   "Merge it by hand.",
		Steps:          []apiclient.WorkflowStepDef{step("inner", "command")},
		MaxParallel:    intp(2),
		Lanes:          []apiclient.WorkflowLaneDef{{ID: "api"}},
		// A file sets `lanes:` or `lane:` + `max_lanes:`, never both — but
		// the coverage fixture is the whole surface at once, and the modal
		// has a row for each of the two ways §7.6 spells a lane list.
		Lane:     &apiclient.WorkflowLaneDef{ID: "{{ .Item.id }}"},
		MaxLanes: intp(4),
		Schedule: "eager",
		// Snapshot-only, and in the fixture for that reason: the modal is the
		// one view that shows a step in full, and a derived lane list that
		// did not say what it was derived from would read as a hand-written
		// one (task 080).
		DerivedFrom: &apiclient.WorkflowDerivationDef{
			Lane:    "{{ .Item.id }}",
			ForEach: []string{`{{ .Steps.plan.Result }}`},
		},
		Merge:         &apiclient.WorkflowMergeDef{OnConflict: "block"},
		Count:         intp(3),
		ForEach:       []string{`{{ .Steps.plan.Result }}`},
		MaxIterations: intp(5),
		Workflow:      "go-checks",
		ResolvedFrom:  []string{"outer", "inner"},
	}
}

// jsonLabels maps the DTO's wire names onto the labels the modal prints where
// the two differ. `resolved_from` reads as "from" beside a "workflow" row,
// which is the pairing that makes either of them mean anything.
var jsonLabels = map[string]string{"resolved_from": "from"}

// Every field of WorkflowStepDef reaches the modal. The assertion is by
// reflection rather than by a written list because a list is what goes stale:
// a field added to the DTO would otherwise be invisible in the one view whose
// whole purpose is to show the step in full.
func TestFullDetailShowsEveryStepField(t *testing.T) {
	b := &builder{}
	labels := labelSet(b.stepSections(fullStep()))

	rt := reflect.TypeOf(apiclient.WorkflowStepDef{})
	for i := range rt.NumField() {
		name := jsonName(rt.Field(i))
		if name == "" {
			continue
		}
		if alias, ok := jsonLabels[name]; ok {
			name = alias
		}
		if !labels[name] {
			t.Errorf("WorkflowStepDef.%s (%s) reaches no row of the modal — "+
				"the one view that shows a step in full has to show it",
				rt.Field(i).Name, name)
		}
	}
}

// A value the step authors is its own; a value only `defaults` supplies is
// the effective one and says so; a field neither sets is absent. Showing the
// daemon's own fallback here would print something the file does not say
// (decision 12).
func TestFullDetailMarksInheritedValues(t *testing.T) {
	wf := &apiclient.WorkflowBody{
		Name:     "corpus",
		Defaults: apiclient.WorkflowDefaults{Agent: "claude", Model: "sonnet", MaxRetries: intp(3)},
		Steps: []apiclient.WorkflowStepDef{
			{ID: "review", Type: "agent", Model: "opus"},
		},
	}
	fields := fieldsOf(Build(wf), "review")

	got, ok := fields["agent"]
	if !ok || got.Value != "claude" || !got.Inherited {
		t.Errorf("agent = %+v, want claude marked as inherited", got)
	}
	got, ok = fields["model"]
	if !ok || got.Value != "opus" || got.Inherited {
		t.Errorf("model = %+v, want the authored opus, unmarked", got)
	}
	if got, ok := fields["effort"]; ok {
		t.Errorf("effort = %+v, want it absent — neither the step nor defaults set it", got)
	}
	got, ok = fields["max_retries"]
	if !ok || got.Value != "3" || !got.Inherited {
		t.Errorf("max_retries = %+v, want 3 marked as inherited", got)
	}
}

// The agent-shaped defaults belong to steps that run an agent. Printing
// `agent: claude (inherited)` on a `command` step would state something the
// run will never do.
func TestFullDetailDoesNotInheritAnAgentOntoACommand(t *testing.T) {
	wf := &apiclient.WorkflowBody{
		Name:     "corpus",
		Defaults: apiclient.WorkflowDefaults{Agent: "claude"},
		Steps:    []apiclient.WorkflowStepDef{{ID: "build", Type: "command", Run: "go build ./..."}},
	}
	if got, ok := fieldsOf(Build(wf), "build")["agent"]; ok {
		t.Errorf("a command step inherited an agent: %+v", got)
	}
}

// `enter` is never inert: every node the graph draws — synthetic ones
// included — opens something.
func TestEveryNodeHasFullDetail(t *testing.T) {
	for name, wf := range corpus() {
		t.Run(name, func(t *testing.T) {
			for _, n := range Build(wf).Nodes {
				if len(n.Full) == 0 {
					t.Errorf("node %q (%s) has no detail to open", n.ID, n.Kind)
					continue
				}
				for _, sec := range n.Full {
					if sec.Title == "" || len(sec.Fields) == 0 {
						t.Errorf("node %q has an empty section %q", n.ID, sec.Title)
					}
				}
			}
		})
	}
}

// A merge is work that runs, and the resolver that may settle a conflict
// unread (§16) is a step in its own right — so it is shown as one.
func TestMergeDetailCarriesItsResolver(t *testing.T) {
	fields := fieldsOf(Build(fixtureFanOut()), mergeNodeID("spread"))
	if got := fields["on_conflict"]; got.Value != "agent" {
		t.Errorf("on_conflict = %q, want agent", got.Value)
	}
	if got := fields["prompt"]; got.Value != "resolve" {
		t.Errorf("the resolver's prompt = %q, want it shown in full", got.Value)
	}
}

// A collapsed reference says what it stands for and what it becomes: a lane is
// a child task, an include is steps spliced into this one (§7.9).
func TestReferenceDetailSaysWhatItBecomes(t *testing.T) {
	lane := fieldsOf(Build(fixtureFanOut()), refNodeID("spread", "web"))
	if got := lane["becomes"]; !strings.Contains(got.Value, "child task") {
		t.Errorf("lane becomes = %q, want a child task", got.Value)
	}
	include := fieldsOf(Build(fixtureInclude()), "verify")
	if got := include["becomes"]; !strings.Contains(got.Value, "spliced") {
		t.Errorf("include becomes = %q, want spliced steps", got.Value)
	}
}

// A prompt and a `run:` body reach the modal unwrapped: wrapping is the
// renderer's job at a width this stage does not know.
func TestFullDetailKeepsMultiLineValuesWhole(t *testing.T) {
	b := &builder{}
	fields := labelFields(b.stepSections(fullStep()))
	if got := fields["prompt"].Value; got != "Read the diff.\nThen review it." {
		t.Errorf("prompt = %q, want both lines, unwrapped", got)
	}
	if got := fields["env"].Value; got != "CI=1\nTZ=UTC" {
		t.Errorf("env = %q, want one sorted line per variable", got)
	}
}

// The strip is the glance view and stays exactly what it was: the modal is
// the affordance for everything it drops (task 053). A golden over the whole
// corpus is what makes that assertion cheap to hold.
func TestStripDetailIsUnchanged(t *testing.T) {
	names := make([]string, 0, len(corpus()))
	for name := range corpus() {
		names = append(names, name)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sb, "== %s\n", name)
		for _, n := range Build(corpus()[name]).Nodes {
			fmt.Fprintf(&sb, "%s:", n.ID)
			for _, f := range n.Detail {
				fmt.Fprintf(&sb, " %s=%s", f.Label, f.Value)
			}
			sb.WriteString("\n")
		}
	}
	compareGolden(t, "strip-detail.txt", sb.String())
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

func labelSet(secs []DetailSection) map[string]bool {
	out := map[string]bool{}
	for label := range labelFields(secs) {
		out[label] = true
	}
	return out
}

func labelFields(secs []DetailSection) map[string]DetailField {
	out := map[string]DetailField{}
	for _, sec := range secs {
		for _, f := range sec.Fields {
			out[f.Label] = f
		}
	}
	return out
}

func fieldsOf(d Diagram, id string) map[string]DetailField {
	for _, n := range d.Nodes {
		if n.ID == id {
			return labelFields(n.Full)
		}
	}
	return nil
}
