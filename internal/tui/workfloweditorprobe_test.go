package tui

import (
	"strings"
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
	out.Container = conv(src.Container)
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
		if strings.HasPrefix(row.descend, "steps[") {
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

// editorFixtureWith opens the editor on a definition of the caller's
// choosing, which is what lets a probe drive a shape the registry fixture
// does not have — a fan-out, declared fields, a defaults block.
func editorFixtureWith(t *testing.T, def apiclient.WorkflowDefinition) *workflowsView {
	t.Helper()
	w := newWorkflowsView()
	w.client = offlineClient()
	w.width, w.height = 100, 40
	loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
	w.updateKey(registryKey(t, "i"))
	if w.editor == nil {
		t.Fatal("i did not open the editor layer")
	}
	w.updateEditorMsg(wfEditorLoadedMsg{key: w.editor.key, schema: servedSchema(), def: def})
	if w.editor.def == nil || len(w.editor.rows) == 0 {
		t.Fatalf("the definition did not land: %+v", w.editor)
	}
	return w
}

// dagDefinition is the shape issue #320 is reported against, in the client's
// own types: two declared fields, a defaults block with a container, a step
// that sets `timeout` and `max_retries: 0`, a fan_out carrying both lane
// shapes and an agent merge, and a loop with a break.
func dagDefinition() apiclient.WorkflowDefinition {
	return apiclient.WorkflowDefinition{
		Name: "review", Scope: "global", File: "/tmp/review.yaml",
		Definition: &apiclient.WorkflowBody{
			Name: "review",
			Fields: []apiclient.WorkflowField{
				{Name: "issue", Type: "string", Required: true, Label: "Issue"},
				{Name: "mode", Type: "enum", Values: []string{"fast", "full"}, Default: "full"},
			},
			Defaults: apiclient.WorkflowDefaults{
				Agent: "claude", Model: "sonnet",
				MaxRetries: ptr(1), Timeout: ptr("30m"),
				Container: &apiclient.WorkflowContainerDef{
					Image: ptr("golang:1.24"), Network: ptr(true),
				},
			},
			Steps: []apiclient.WorkflowStepDef{
				{
					ID: "fetch", Type: "agent", Prompt: "read the issue",
					// The two rows the switch never named: a set duration and
					// a retry budget explicitly set to zero.
					Timeout: ptr("2m"), MaxRetries: ptr(0),
				},
				{
					ID: "group", Type: "parallel", MaxParallel: ptr(2),
					Steps: []apiclient.WorkflowStepDef{
						{ID: "lint", Type: "command", Run: "make lint"},
						{ID: "unit", Type: "command", Run: "make test", Env: map[string]string{"CI": "1", "A": "b"}},
					},
				},
				{
					ID: "spread", Type: "fan_out", Schedule: "eager", MaxLanes: ptr(4),
					ForEach: []string{"{{ .Steps.fetch.Result }}"},
					Lanes: []apiclient.WorkflowLaneDef{
						{ID: "api", Steps: []apiclient.WorkflowStepDef{{ID: "api_impl", Type: "agent", Prompt: "do api"}}},
						{ID: "web", Workflow: "other", Needs: []string{"api"}, Fields: map[string]string{"area": "web"}},
					},
					Lane: &apiclient.WorkflowLaneDef{ID: "{{ .Item.id }}", Workflow: "unit"},
					Merge: &apiclient.WorkflowMergeDef{
						OnConflict: "agent",
						Agent:      &apiclient.WorkflowStepDef{ID: "fixup", Type: "agent", Prompt: "resolve it"},
					},
				},
				{
					ID: "repeat", Type: "loop", Count: ptr(3),
					Steps: []apiclient.WorkflowStepDef{
						{ID: "body", Type: "agent", Prompt: "again"},
						{ID: "enough", Type: "break", If: "{{ .Steps.body.Success }}"},
					},
				},
			},
		},
	}
}

// descendTo presses enter on the row that opens target, which is the probe's
// stand-in for a person moving the cursor onto it.
func descendTo(t *testing.T, w *workflowsView, target string) {
	t.Helper()
	for i, r := range w.editor.rows {
		if r.descend == target {
			w.editor.cursor = i
			w.updateKey(registryKey(t, "enter"))
			if w.editor.path != target {
				t.Fatalf("enter on the %s row landed at %q (err %q)", target, w.editor.path, w.editor.err)
			}
			if len(w.editor.rows) == 0 {
				t.Fatalf("%s has no rows: %q", target, w.editor.err)
			}
			return
		}
	}
	t.Fatalf("no row descends into %s: %s", target, rowNames(w))
}

// escTo presses esc once and asserts the breadcrumb it walked back to.
func escTo(t *testing.T, w *workflowsView, want string) {
	t.Helper()
	w.updateKey(registryKey(t, "esc"))
	if w.editor == nil {
		t.Fatalf("esc closed the editor instead of walking back to %q", want)
	}
	if w.editor.path != want {
		t.Fatalf("esc left the breadcrumb at %q, want %q", w.editor.path, want)
	}
}

func rowNames(w *workflowsView) []string {
	out := make([]string, 0, len(w.editor.rows))
	for _, r := range w.editor.rows {
		out = append(out, r.field.Name)
	}
	return out
}

// wantEditorRows asserts the form drew a row per named field. The names are the
// descriptor's, never a hand-written list: a field the daemon would refuse is
// one the form never offers (task 065 decision 3).
func wantEditorRows(t *testing.T, w *workflowsView, names ...string) {
	t.Helper()
	got := map[string]string{}
	for _, r := range w.editor.rows {
		got[r.field.Name] = r.value
	}
	for _, n := range names {
		if _, ok := got[n]; !ok {
			t.Errorf("no %s row at %q: %s", n, w.editor.path, rowNames(w))
		}
	}
}

// A fan_out's lanes were the editor's loudest dead end: the row descended to
// `steps[i].lanes`, which the resolver refused, so the form reported that the
// step was gone (issue #320). Now the list opens, a lane opens from it, and
// esc walks back out one layer at a time.
func TestEditorDescendsIntoALane(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "steps[2]")
	descendTo(t, w, "steps[2].lanes")
	if len(w.editor.rows) != 2 {
		t.Fatalf("the lane list drew %d rows, want one per lane: %s", len(w.editor.rows), rowNames(w))
	}
	if w.editor.rows[0].list != "steps[2].lanes" || w.editor.rows[1].index != 1 {
		t.Errorf("lane rows carry no list metadata for the structural keys: %+v", w.editor.rows)
	}
	descendTo(t, w, "steps[2].lanes[1]")
	// The lane form is the descriptor's Lane block, values and all.
	wantEditorRows(t, w, "id", "if", "needs", "workflow", "steps", "fields", "agent", "model", "effort", "priority")
	for _, r := range w.editor.rows {
		switch r.field.Name {
		case "needs":
			if r.value != "api" {
				t.Errorf("needs = %q, want the lane DAG's edge", r.value)
			}
		case "fields":
			if r.value != "area=web" {
				t.Errorf("fields = %q, want k=v", r.value)
			}
		}
	}
	// parentPath drops the last dotted segment, so a lane's own index goes
	// with it: esc from a lane lands on the step that owns the list.
	escTo(t, w, "steps[2]")
	escTo(t, w, "")
}

// A lane's inline steps are an ordinary body: the types offered inside one
// are the body context's, not the parallel context's.
func TestEditorDescendsIntoALanesInlineStep(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "steps[2]")
	descendTo(t, w, "steps[2].lanes")
	descendTo(t, w, "steps[2].lanes[0]")
	descendTo(t, w, "steps[2].lanes[0].steps[0]")
	wantEditorRows(t, w, "type", "id", "prompt")
	if got := w.editor.contextOf("steps[2].lanes[0].steps[0]"); got != apiclient.WorkflowContextBody {
		t.Errorf("a lane's inline step sits in context %q, want body", got)
	}
	escTo(t, w, "steps[2].lanes[0]")
}

// The single `lane:` template a derived fan-out renders (§7.6, task 080) is
// the same Lane descriptor at a path of its own — and it was invisible twice
// over: the client DTO did not carry it and the resolver did not address it.
func TestEditorDescendsIntoTheLaneTemplate(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "steps[2]")
	// The fan-out's own rows now report the fields the switch never named.
	for _, r := range w.editor.rows {
		switch r.field.Name {
		case "lane":
			if r.value != "{{ .Item.id }}" {
				t.Errorf("the lane row shows %q, want the template's id", r.value)
			}
		case "max_lanes":
			if r.value != "4" {
				t.Errorf("max_lanes = %q, want 4", r.value)
			}
		case "for_each":
			if r.value != "{{ .Steps.fetch.Result }}" {
				t.Errorf("for_each = %q, want the driver", r.value)
			}
		case "schedule":
			if r.value != "eager" {
				t.Errorf("schedule = %q, want eager", r.value)
			}
		}
	}
	descendTo(t, w, "steps[2].lane")
	wantEditorRows(t, w, "id", "workflow", "needs")
	if got := w.editor.rows[0].value; got != "{{ .Item.id }}" {
		t.Errorf("the template's id row = %q", got)
	}
	escTo(t, w, "steps[2]")
}

// The merge block and the step inside it. `merge.agent` is the one place
// §8.2's merge context applies, so its `type` row offers agent and nothing
// else.
func TestEditorDescendsIntoAMergeAndItsAgent(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "steps[2]")
	descendTo(t, w, "steps[2].merge")
	wantEditorRows(t, w, "on_conflict", "agent")
	if w.editor.rows[0].value != "agent" {
		t.Errorf("on_conflict = %q, want the authored policy", w.editor.rows[0].value)
	}
	descendTo(t, w, "steps[2].merge.agent")
	wantEditorRows(t, w, "type", "id", "prompt")
	for _, r := range w.editor.rows {
		if r.field.Name != "type" {
			continue
		}
		if len(r.field.Values) != 1 || r.field.Values[0] != "agent" {
			t.Errorf("the merge resolver's type row offers %v, want just [agent]", r.field.Values)
		}
		if r.path != "steps[2].merge.agent.type" {
			t.Errorf("the row addresses %q, which is not where the daemon would write it", r.path)
		}
	}
	escTo(t, w, "steps[2].merge")
	escTo(t, w, "steps[2]")
}

// The declared-field list: `fields` was a row with no path and no descend, so
// enter on it did nothing at all.
func TestEditorDescendsIntoADeclaredField(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "fields")
	if len(w.editor.rows) != 2 {
		t.Fatalf("the field list drew %d rows, want one per declaration: %s", len(w.editor.rows), rowNames(w))
	}
	if w.editor.rows[1].list != "fields" || w.editor.rows[1].index != 1 {
		t.Errorf("declared-field rows carry no list metadata: %+v", w.editor.rows[1])
	}
	descendTo(t, w, "fields[1]")
	wantEditorRows(t, w, "name", "label", "description", "type", "required", "pattern", "values", "multiple", "default")
	for _, r := range w.editor.rows {
		switch r.field.Name {
		case "values":
			if r.value != "fast, full" {
				t.Errorf("values = %q, want the members comma-joined", r.value)
			}
		case "default":
			if r.value != "full" {
				t.Errorf("default = %q", r.value)
			}
		case "name":
			if r.path != "fields[1].name" {
				t.Errorf("the row addresses %q", r.path)
			}
		}
	}
	escTo(t, w, "")
}

// The defaults block and the §8.6 container inside it, the other row that did
// nothing.
func TestEditorDescendsIntoDefaultsAndItsContainer(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "defaults")
	wantEditorRows(t, w, "agent", "model", "effort", "permission_mode", "on_input",
		"input_timeout", "max_retries", "retry_backoff", "timeout", "container")
	for _, r := range w.editor.rows {
		switch r.field.Name {
		case "agent":
			if r.value != "claude" || r.path != "defaults.agent" {
				t.Errorf("the agent row = %q at %q", r.value, r.path)
			}
		case "timeout":
			if r.value != "30m" {
				t.Errorf("defaults timeout = %q, want the authored duration", r.value)
			}
		}
	}
	descendTo(t, w, "defaults.container")
	wantEditorRows(t, w, "image", "runtime", "mount_agent_config", "network", "extra_mounts")
	for _, r := range w.editor.rows {
		switch r.field.Name {
		case "image":
			if r.value != "golang:1.24" || r.path != "defaults.container.image" {
				t.Errorf("the image row = %q at %q", r.value, r.path)
			}
		case "network":
			if r.value != "true" {
				t.Errorf("network = %q, want true", r.value)
			}
		case "runtime":
			// A pointer the file left absent is the one thing that is unset.
			if r.value != "" {
				t.Errorf("runtime = %q, want unset", r.value)
			}
		}
	}
	escTo(t, w, "defaults")
	escTo(t, w, "")
}

// A group's `steps` row descends into the body list, which the resolver
// refused before this: the row builder had always written that target.
func TestEditorDescendsIntoAGroupsBody(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "steps[1]")
	descendTo(t, w, "steps[1].steps")
	if len(w.editor.rows) != 2 {
		t.Fatalf("the body drew %d rows, want one per member: %s", len(w.editor.rows), rowNames(w))
	}
	if w.editor.rows[0].list != "steps[1].steps" {
		t.Errorf("body rows carry no list metadata: %+v", w.editor.rows[0])
	}
	descendTo(t, w, "steps[1].steps[1]")
	// The member sits in the parallel context wherever it was reached from.
	if got := w.editor.contextOf("steps[1].steps[1]"); got != apiclient.WorkflowContextParallel {
		t.Errorf("context = %q, want parallel", got)
	}
	for _, r := range w.editor.rows {
		if r.field.Name == "env" && r.value != "A=b, CI=1" {
			t.Errorf("env = %q, want k=v with sorted keys", r.value)
		}
	}
	escTo(t, w, "steps[1]")
	escTo(t, w, "")
}
