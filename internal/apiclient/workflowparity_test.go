package apiclient

import (
	"reflect"
	"testing"

	"github.com/lezli01/vincent/internal/workflow"
)

// TestWorkflowDefinitionClientCoversEveryField is the drift guard on the
// second hand-written mirror. internal/api's DTO restates internal/workflow
// (task 017 decision 4) and internal/api/workflowdef_test.go's
// TestWorkflowDefinitionCoversEveryField holds *that* copy honest; these types
// then restate the server's DTO, and until this test nothing walked the second
// hop. That is how `lane:` and `max_lanes:` reached the wire in task 080 and
// never reached a client: the editor's fan-out form could not see them, and no
// test noticed (issue #320).
//
// The model is the authority for both hops, so this reflects over
// internal/workflow directly rather than over the server's unexported DTO —
// which is unexported precisely so it stays the server's own. A test-only
// import of the parser's package is what a drift guard is for; the package's
// *_live_test.go files already wire these types to the real handlers.
//
// A field deliberately left off the client belongs in the omit set below, with
// the reason.
func TestWorkflowDefinitionClientCoversEveryField(t *testing.T) {
	// FieldDefinition's three unexported members are decode bookkeeping: they
	// remember the *shape* a `default:` or `values:` node had so validation
	// can report it at its own source path (task 058). A workflow that
	// reaches the wire has already passed validation, so they are always
	// zero by then and carry nothing a client could use.
	omit := map[string]map[string]string{
		"FieldDefinition": {
			"defaultShape": "decode bookkeeping, consumed by validation before the wire",
			"valuesShape":  "decode bookkeeping, consumed by validation before the wire",
			"defaultSeq":   "decode bookkeeping, consumed by validation before the wire",
		},
	}
	pairs := []struct {
		name  string
		model any
		dto   any
	}{
		{"Workflow", workflow.Workflow{}, WorkflowBody{}},
		{"FieldDefinition", workflow.FieldDefinition{}, WorkflowField{}},
		{"Defaults", workflow.Defaults{}, WorkflowDefaults{}},
		{"Step", workflow.Step{}, WorkflowStepDef{}},
		{"Lane", workflow.Lane{}, WorkflowLaneDef{}},
		{"Merge", workflow.Merge{}, WorkflowMergeDef{}},
	}
	for _, p := range pairs {
		mapped := map[string]bool{}
		dt := reflect.TypeOf(p.dto)
		for i := range dt.NumField() {
			mapped[dt.Field(i).Name] = true
		}
		mt := reflect.TypeOf(p.model)
		for i := range mt.NumField() {
			f := mt.Field(i).Name
			if reason, ok := omit[p.name][f]; ok {
				t.Logf("%s.%s deliberately off the client: %s", p.name, f, reason)
				continue
			}
			if !mapped[f] {
				t.Errorf("workflow.%s.%s has no counterpart in the client DTO — map it, "+
					"or record why it stays off the client", p.name, f)
			}
		}
	}
}

// The container override is its own struct on both sides of the wire, so it
// gets its own pass: `defaults.container` is what the editor's container form
// reads, and a key missing here renders as `(unset)` however the file spells
// it.
func TestWorkflowContainerDefCoversEveryField(t *testing.T) {
	mapped := map[string]bool{}
	dt := reflect.TypeOf(WorkflowContainerDef{})
	for i := range dt.NumField() {
		mapped[dt.Field(i).Name] = true
	}
	mt := reflect.TypeOf(workflow.Defaults{}.Container).Elem()
	for i := range mt.NumField() {
		if f := mt.Field(i).Name; !mapped[f] {
			t.Errorf("config.ContainerOverride.%s has no counterpart in WorkflowContainerDef", f)
		}
	}
}
