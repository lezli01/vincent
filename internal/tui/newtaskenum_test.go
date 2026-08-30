package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// formWithEnumFields is the declared-field form with the two enum shapes on
// it: a required single-choice with a default, and an optional multi-choice.
func formWithEnumFields(t *testing.T) *newTask {
	t.Helper()
	n := loadedForm(t)
	for i := range n.workflows {
		if n.workflows[i].Name != "two-step" {
			continue
		}
		n.workflows[i].Fields = []apiclient.WorkflowField{
			{
				Name: "environment", Label: "Environment", Description: "Deployment target.",
				Type: apiclient.WorkflowFieldEnum, Required: true,
				Values: []string{"dev", "staging", "prod"}, Default: "staging",
			},
			{
				Name: "reviewers", Type: apiclient.WorkflowFieldEnum, Multiple: true,
				Values: []string{"ana", "bo", "cy"},
			},
			{Name: "channel", Type: apiclient.WorkflowFieldEnum, Values: []string{"alpha", "beta"}},
		}
	}
	n.setWorkflow("two-step")
	return n
}

// TestNewTaskSeedsDeclaredDefaults pins the client half of decision 3: an
// optional field's default is seeded here and nowhere else, because the daemon
// never invents one.
func TestNewTaskSeedsDeclaredDefaults(t *testing.T) {
	n := formWithEnumFields(t)
	if got := n.fields[0].value; got != "staging" {
		t.Errorf("environment = %q, want the declared default", got)
	}
	if got := n.fields[1].value; got != "" {
		t.Errorf("reviewers = %q, want empty — it declares no default", got)
	}
	// A value the human already entered outranks the default when the form
	// re-syncs, which is what makes comparing two workflows non-destructive.
	n.fields[0].value = "prod"
	n.fields = append(n.fields, kv{key: "owner", value: "ana"})
	n.setWorkflow("adhoc")
	n.setWorkflow("two-step")
	if got := n.fields[0].value; got != "prod" {
		t.Errorf("environment after a workflow switch = %q, want the typed value kept", got)
	}
	if n.fields[3].key != "owner" || n.fields[3].declared {
		t.Errorf("custom field after switching = %+v", n.fields[3])
	}
}

func TestNewTaskEnumPickerCommits(t *testing.T) {
	n := formWithEnumFields(t)
	moveTo(n, ntFields)
	press(n, "enter") // open the fields editor, cursor on environment
	press(n, "enter") // open the value list
	if n.mode != ntFieldPicking || n.pick == nil {
		t.Fatalf("mode = %v, pick = %v; enter on an enum row must open the list", n.mode, n.pick)
	}
	out := n.render(140, 60)
	for _, want := range []string{"enum", "required", "values: dev, staging, prod", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("enum row is missing %q:\n%s", want, out)
		}
	}
	press(n, "down") // staging -> prod (a required field has no "(unset)" row)
	press(n, "enter")
	if n.mode != ntFieldsOpen {
		t.Errorf("mode = %v, want the editor back after a single-choice commit", n.mode)
	}
	if got := n.fieldsEd.rows[0].value; got != "prod" {
		t.Errorf("environment = %q, want prod", got)
	}
	press(n, "esc")
	if got := n.fieldMap()["environment"]; got != "prod" {
		t.Errorf("committed fields = %v", n.fieldMap())
	}
}

// TestNewTaskEnumStepsInPlace pins the boolean-shaped shortcut: a two- or
// three-value field stays a single keypress.
func TestNewTaskEnumStepsInPlace(t *testing.T) {
	n := formWithEnumFields(t)
	moveTo(n, ntFields)
	press(n, "enter")
	press(n, "right")
	if got := n.fieldsEd.rows[0].value; got != "prod" {
		t.Errorf("after right = %q, want prod", got)
	}
	press(n, "right") // wraps: a required field has no empty stop
	if got := n.fieldsEd.rows[0].value; got != "dev" {
		t.Errorf("after wrapping = %q, want dev", got)
	}
	press(n, "left")
	if got := n.fieldsEd.rows[0].value; got != "prod" {
		t.Errorf("after left = %q, want prod", got)
	}
	if n.mode != ntFieldsOpen || n.pick != nil {
		t.Errorf("stepping opened a picker: mode = %v", n.mode)
	}
	// An optional field gets one extra stop at "unset", which is the only way
	// back for a row that cannot be deleted.
	press(n, "down")
	press(n, "down")
	press(n, "right")
	press(n, "right")
	if got := n.fieldsEd.rows[2].value; got != "beta" {
		t.Errorf("optional enum = %q, want beta", got)
	}
	press(n, "right")
	if got := n.fieldsEd.rows[2].value; got != "" {
		t.Errorf("optional enum = %q, want the unset stop", got)
	}
}

// TestNewTaskMultipleEnumTogglesAndCommitsInDeclaredOrder is the encoding
// rule the branch names and template output depend on: the same selection is
// the same string, whatever order it was clicked in.
func TestNewTaskMultipleEnumTogglesAndCommitsInDeclaredOrder(t *testing.T) {
	n := formWithEnumFields(t)
	moveTo(n, ntFields)
	press(n, "enter")
	press(n, "down") // reviewers
	press(n, "right")
	if got := n.fieldsEd.rows[1].value; got != "" {
		t.Errorf("a multiple enum stepped in place to %q; there is no next set", got)
	}
	press(n, "enter")
	if n.mode != ntFieldPicking {
		t.Fatalf("mode = %v, want the value list", n.mode)
	}
	press(n, "down")  // bo
	press(n, "down")  // cy
	press(n, "enter") // toggle cy on; the list stays open
	if n.mode != ntFieldPicking {
		t.Fatalf("mode = %v; a multiple picker must stay open to toggle again", n.mode)
	}
	press(n, "up")
	press(n, "up")
	press(n, "enter") // toggle ana on, after cy
	if got := n.fieldsEd.rows[1].value; got != "ana,cy" {
		t.Errorf("reviewers = %q, want declared order regardless of click order", got)
	}
	if !strings.Contains(n.render(140, 60), "[x]") {
		t.Errorf("no membership marker in the multi picker:\n%s", n.render(140, 60))
	}
	press(n, "enter") // toggle ana back off
	if got := n.fieldsEd.rows[1].value; got != "cy" {
		t.Errorf("reviewers after untoggling = %q, want cy", got)
	}
	press(n, "esc") // close the list, back to the editor
	if n.mode != ntFieldsOpen {
		t.Errorf("mode = %v, want the editor", n.mode)
	}
	press(n, "esc")
	if got := n.fieldMap()["reviewers"]; got != "cy" {
		t.Errorf("committed reviewers = %q", got)
	}
}

// TestNewTaskDeclaredEnumRowsStayLocked keeps the task 022 contract: the
// workflow owns a declared row's key, and the row cannot be deleted.
func TestNewTaskDeclaredEnumRowsStayLocked(t *testing.T) {
	n := formWithEnumFields(t)
	moveTo(n, ntFields)
	press(n, "enter")
	press(n, "d")
	if !strings.Contains(n.fieldsEd.err, "cannot be deleted") {
		t.Errorf("delete error = %q", n.fieldsEd.err)
	}
	if len(n.fieldsEd.rows) != 3 {
		t.Errorf("rows = %d, want the three declarations intact", len(n.fieldsEd.rows))
	}
	// enter opens the list rather than the key editor, so there is no route
	// to renaming the key at all.
	press(n, "enter")
	if n.fieldsEd.editing != 0 {
		t.Errorf("editing = %d, want the list rather than a text editor", n.fieldsEd.editing)
	}
}

// TestEnumFieldValidationMessage holds the local check to the daemon's
// sentence: the TUI mirrors POST /v1/tasks so the message lands on the row,
// and the daemon stays the authoritative gate.
func TestEnumFieldValidationMessage(t *testing.T) {
	def := apiclient.WorkflowField{
		Name: "environment", Label: "Environment", Type: apiclient.WorkflowFieldEnum,
		Values: []string{"dev", "staging", "prod"},
	}
	if got := fieldValidationMessage(kv{key: "environment", value: "prod", declared: true, definition: def}); got != "" {
		t.Errorf("member rejected: %s", got)
	}
	got := fieldValidationMessage(kv{key: "environment", value: "nope", declared: true, definition: def})
	if !strings.Contains(got, "must be one of dev, staging, prod") || !strings.Contains(got, `"nope"`) {
		t.Errorf("message = %q, want the members and the offending element", got)
	}
	multi := def
	multi.Multiple = true
	multi.Values = []string{"ana", "bo", "cy"}
	if got := fieldValidationMessage(kv{key: "r", value: "ana,cy", declared: true, definition: multi}); got != "" {
		t.Errorf("member set rejected: %s", got)
	}
	got = fieldValidationMessage(kv{key: "r", value: "ana,zed", declared: true, definition: multi})
	if !strings.Contains(got, `"zed"`) {
		t.Errorf("message = %q, want the offending element named", got)
	}
}
