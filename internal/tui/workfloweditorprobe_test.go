package tui

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/workflow"
)

// editorFixture opens the structured editor on a loaded entry, with the
// served schema and a definition already in place — the state every binding
// probe below drives.
func editorFixture(t *testing.T) *workflowsView {
	t.Helper()
	w := newWorkflowsView()
	w.client = offlineClient()
	w.width, w.height = 100, 40
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
	w.updateKey(registryKey(t, "i"))
	if w.editor == nil {
		t.Fatal("i did not open the editor layer")
	}
	def := graphDefinition()
	w.updateEditorMsg(wfEditorLoadedMsg{
		key:    w.editor.key,
		schema: servedSchema(),
		def:    def,
	})
	if w.editor.def == nil || len(w.editor.rows) == 0 {
		t.Fatalf("the definition did not land: %+v", w.editor)
	}
	return w
}

// servedSchema is the descriptor the daemon serves, round-tripped through
// the client's own types. Building it from workflow.SchemaDescriptor rather
// than by hand is what keeps this fixture from becoming a third copy of §8.2.
func servedSchema() apiclient.WorkflowSchema {
	src := workflow.SchemaDescriptor()
	out := apiclient.WorkflowSchema{Contexts: src.Contexts}
	conv := func(fields []workflow.SchemaField) []apiclient.WorkflowSchemaField {
		res := make([]apiclient.WorkflowSchemaField, 0, len(fields))
		for _, f := range fields {
			res = append(res, apiclient.WorkflowSchemaField{
				Name: f.Name, Control: f.Control, Values: f.Values,
				Required: f.Required, Help: f.Help,
			})
		}
		return res
	}
	out.TopLevel, out.Defaults = conv(src.TopLevel), conv(src.Defaults)
	out.Field, out.Common = conv(src.Field), conv(src.Common)
	out.Lane, out.Merge = conv(src.Lane), conv(src.Merge)
	for _, s := range src.Steps {
		out.Steps = append(out.Steps, apiclient.WorkflowSchemaStepType{
			Type: s.Type, Fields: conv(s.Fields), Common: s.Common,
			Contexts: s.Contexts, Help: s.Help,
		})
	}
	return out
}

// TestEditorDescendsIntoAStepAndBackOut pins the breadcrumb: enter on a step
// row opens that step's form, and esc walks back out one layer at a time
// (§15's "one layer per press").
func TestEditorDescendsIntoAStepAndBackOut(t *testing.T) {
	w := editorFixture(t)
	step := -1
	for i, row := range w.editor.rows {
		if row.descend != "" {
			step = i
			break
		}
	}
	if step < 0 {
		t.Fatalf("no descendable step row: %+v", w.editor.rows)
	}
	w.editor.cursor = step
	w.updateKey(registryKey(t, "enter"))
	if w.editor.path == "" {
		t.Fatal("enter on a step row did not descend")
	}
	// The step's form draws its type's fields, and only those — a `run:` row
	// on an agent step is a 400 the form must never offer.
	types := map[string]bool{}
	for _, row := range w.editor.rows {
		types[row.field.Name] = true
	}
	if !types["type"] || !types["id"] {
		t.Errorf("the step form is missing its common rows: %+v", w.editor.rows)
	}
	w.updateKey(registryKey(t, "esc"))
	if w.editor.path != "" {
		t.Errorf("esc did not leave the step: still at %q", w.editor.path)
	}
	w.updateKey(registryKey(t, "esc"))
	if w.editor != nil {
		t.Error("a second esc did not close the editor")
	}
}

// TestEditorOffersOnlyLegalStepTypes: the `type` row inside a parallel group
// offers what §8.2 allows there and nothing else, because the descriptor said
// so rather than because the TUI re-derived it.
func TestEditorOffersOnlyLegalStepTypes(t *testing.T) {
	w := editorFixture(t)
	got := w.editor.typesFor(apiclient.WorkflowContextParallel)
	for _, typ := range got {
		switch typ {
		case workflow.StepManual, workflow.StepParallel, workflow.StepFanOut,
			workflow.StepCondition, workflow.StepLoop, workflow.StepBreak:
			t.Errorf("%s is offered inside a parallel group; §8.2 refuses it", typ)
		}
	}
	if len(got) == 0 {
		t.Fatal("no step type is offered inside a parallel group")
	}
	if only := w.editor.typesFor(apiclient.WorkflowContextMerge); len(only) != 1 ||
		only[0] != workflow.StepAgent {
		t.Errorf("merge resolver types = %v, want just [agent]", only)
	}
}

// TestEditorEnumRowCycles covers the enum control (task 058's rows): enter
// steps through the members and past the end to (unset) on an optional row.
func TestEditorEnumRowCycles(t *testing.T) {
	f := apiclient.WorkflowSchemaField{
		Control: apiclient.WorkflowControlEnum,
		Values:  []string{"full-auto", "restricted"},
	}
	if got := cycleEnum(f, "full-auto"); got != "restricted" {
		t.Errorf("cycle = %q, want restricted", got)
	}
	if got := cycleEnum(f, "restricted"); got != unsetMarker {
		t.Errorf("cycle past the end = %q, want %s", got, unsetMarker)
	}
	if got := cycleEnum(f, unsetMarker); got != "full-auto" {
		t.Errorf("cycle from unset = %q, want the first member", got)
	}
	// A required row has no (unset) stop: an absent `type:` is not a
	// workflow the daemon would accept.
	req := apiclient.WorkflowSchemaField{
		Control: apiclient.WorkflowControlEnum, Required: true,
		Values: []string{"agent", "command"},
	}
	if got := cycleEnum(req, "command"); got != "agent" {
		t.Errorf("required cycle = %q, want to wrap to agent", got)
	}
}

// TestRenderYAMLScalarQuotesWhatYAMLWouldMisread: a value that would parse
// back as a boolean, a null or a flow collection is quoted, so what a person
// typed is what the file holds.
func TestRenderYAMLScalarQuotesWhatYAMLWouldMisread(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"review", "review"},
		{"", `""`},
		{"true", `"true"`},
		{"no", `"no"`},
		{"a: b", `"a: b"`},
		{"[x]", `"[x]"`},
		{" pad", `" pad"`},
	} {
		if got := renderYAMLScalar(tc.in); got != tc.want {
			t.Errorf("renderYAMLScalar(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := renderFlowList("api, tui , "); got != "[api, tui]" {
		t.Errorf("renderFlowList = %q, want [api, tui]", got)
	}
}

// createFixture opens the create prompt over a loaded registry with two
// scopes, so a probe has a scope to move between.
func createFixture(t *testing.T) *workflowsView {
	t.Helper()
	w := newWorkflowsView()
	w.client = offlineClient()
	loadedWorkflows(w,
		wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}},
		wfBlock{name: "app", projectID: 1})
	w.updateKey(registryKey(t, "a"))
	if w.create == nil {
		t.Fatal("the create prompt did not open")
	}
	return w
}

// editorRowIndex finds the row a field name owns.
func editorRowIndex(t *testing.T, w *workflowsView, name string) int {
	t.Helper()
	for i, r := range w.editor.rows {
		if r.field.Name == name {
			return i
		}
	}
	t.Fatalf("no %s row", name)
	return 0
}
