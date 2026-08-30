package apiclient_test

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

const enumYAML = `name: deploy
description: ship it
fields:
  - name: environment
    label: Environment
    type: enum
    required: true
    values: [dev, staging, prod]
    default: staging
  - name: reviewers
    type: enum
    multiple: true
    values: [ana, bo, cy]
steps:
  - {id: gate, type: manual, instructions: review}
`

// TestEnumFieldsOverTheWire pins the server DTO and the client model against
// each other for `values`, `multiple` and `default` (§8.1.2, task 058). The
// TUI builds a picker out of these three keys, so a rename on either side
// would silently turn every enum row back into a free-text box — exactly what
// the type exists to stop. Only the real handler encoding into the real client
// type proves they agree.
func TestEnumFieldsOverTheWire(t *testing.T) {
	c := newDefinitionClient(t, map[string]string{"deploy": enumYAML})

	entries, err := c.ListWorkflows(t.Context(), 0)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	var listed []apiclient.WorkflowField
	for _, e := range entries {
		if e.Name == "deploy" {
			listed = e.Fields
		}
	}
	def, err := c.GetWorkflowDefinition(t.Context(), 0, "deploy")
	if err != nil {
		t.Fatalf("GetWorkflowDefinition: %v", err)
	}
	if def.Definition == nil {
		t.Fatal("definition is nil for a valid workflow")
	}
	for name, fields := range map[string][]apiclient.WorkflowField{
		"list": listed, "definition": def.Definition.Fields,
	} {
		if len(fields) != 2 {
			t.Fatalf("%s fields = %+v, want 2", name, fields)
		}
		env := fields[0]
		if env.Type != apiclient.WorkflowFieldEnum {
			t.Errorf("%s environment type = %q, want %q", name, env.Type, apiclient.WorkflowFieldEnum)
		}
		if got := strings.Join(env.Values, apiclient.WorkflowFieldSeparator); got != "dev,staging,prod" {
			t.Errorf("%s environment values = %q, want the declared order", name, got)
		}
		if env.Default != "staging" || env.Multiple {
			t.Errorf("%s environment = %+v", name, env)
		}
		if !fields[1].Multiple || fields[1].Default != "" {
			t.Errorf("%s reviewers = %+v", name, fields[1])
		}
	}
}
