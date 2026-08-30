package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

const enumFieldsYAML = `name: deploy
description: Ship it.
fields:
  - name: environment
    label: Environment
    type: enum
    required: true
    values: [dev, staging, prod]
    default: staging
  - name: channel
    type: enum
    values: [alpha, beta]
    default: beta
  - name: reviewers
    type: enum
    multiple: true
    values: [ana, bo, cy]
steps:
  - {id: approve, type: manual, instructions: review}
`

// TestWorkflowEndpointsExposeEnumFields pins the three new wire keys. A
// pattern cannot be turned into a control; a published list can, which is the
// whole reason the type exists (§8.1.2, task 058).
func TestWorkflowEndpointsExposeEnumFields(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "deploy", enumFieldsYAML)
	h.reg.ReloadGlobal()

	_, body := h.doJSON(t, http.MethodGet, "/v1/workflows", nil)
	var listed struct {
		Workflows []workflowResponse `json:"workflows"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("workflow list: %v (%s)", err, body)
	}
	var fields []workflowFieldResponse
	for _, entry := range listed.Workflows {
		if entry.Name == "deploy" {
			fields = entry.Fields
		}
	}
	if len(fields) != 3 {
		t.Fatalf("fields = %+v", fields)
	}
	if fields[0].Type != "enum" || fields[0].Default != "staging" ||
		strings.Join(fields[0].Values, ",") != "dev,staging,prod" || fields[0].Multiple {
		t.Errorf("environment = %+v", fields[0])
	}
	if !fields[2].Multiple || strings.Join(fields[2].Values, ",") != "ana,bo,cy" {
		t.Errorf("reviewers = %+v", fields[2])
	}

	_, body = h.definition(t, "deploy", 0)
	definition := decodeDefinition(t, body).Definition
	if definition == nil || len(definition.Fields) != 3 {
		t.Fatalf("definition fields = %+v", definition)
	}
	if definition.Fields[1].Default != "beta" {
		t.Errorf("channel default = %+v", definition.Fields[1])
	}
}

// TestWorkflowValidateReportsEnumDeclarationErrors keeps a bad declaration a
// *visibly* invalid workflow rather than a task-creation surprise.
func TestWorkflowValidateReportsEnumDeclarationErrors(t *testing.T) {
	h := newWorkflowHarness(t)
	_, body := h.doJSON(t, http.MethodPost, "/v1/workflows/validate", map[string]any{
		"yaml": `name: bad
fields:
  - {name: environment, type: enum}
  - {name: note, type: string, values: [a]}
  - {name: channel, type: enum, values: [a], default: z}
steps:
  - {id: gate, type: manual, instructions: review}
`,
	})
	var got validateResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("validate: %v (%s)", err, body)
	}
	if got.Valid {
		t.Fatalf("a broken enum declaration validated clean: %s", body)
	}
	paths := map[string]string{}
	for _, e := range got.Errors {
		paths[e.Path] = e.Message
	}
	for _, want := range []string{"fields[0].values", "fields[1].values", "fields[2].default"} {
		if paths[want] == "" {
			t.Errorf("no finding at %s; got %v", want, got.Errors)
		}
	}
}

// TestTaskCreateEnumFields is the daemon's side of decisions 2 and 3: it
// normalizes a multiple value before judging membership, substitutes a
// required default for an omitted key, and never invents an optional one.
func TestTaskCreateEnumFields(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "deploy", enumFieldsYAML)
	h.reg.ReloadGlobal()
	repo := testrepo.Init(t, "main")
	project := h.mustCreate(t, map[string]any{"path": repo})
	projectID := int64(project["id"].(float64))

	create := func(t *testing.T, title string, fields map[string]string) (*http.Response, []byte) {
		t.Helper()
		return h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
			"project_id": projectID, "workflow": "deploy", "title": title, "fields": fields,
		})
	}

	for _, tt := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"non-member", map[string]string{"environment": "nope"}, `got \"nope\"`},
		{"one bad element", map[string]string{"reviewers": "ana,zed"}, `got \"zed\"`},
		{"explicit empty required", map[string]string{"environment": ""}, `field \"environment\" is required`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := create(t, "invalid "+tt.name, tt.fields)
			wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("body %s missing %q", body, tt.want)
			}
		})
	}

	resp, body := create(t, "deploy it", map[string]string{"reviewers": "cy, ana, cy"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created taskResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("created task: %v (%s)", err, body)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/tasks/"+strconv.FormatInt(created.ID, 10), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	var stored taskResponse
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatalf("stored task: %v (%s)", err, body)
	}
	if stored.Fields["environment"] != "staging" {
		t.Errorf("environment = %q; a required field's default must be recorded on the row",
			stored.Fields["environment"])
	}
	if got, ok := stored.Fields["channel"]; ok {
		t.Errorf("channel = %q; an optional default must not be invented server-side", got)
	}
	if stored.Fields["reviewers"] != "ana,cy" {
		t.Errorf("reviewers = %q, want ana,cy in declared order", stored.Fields["reviewers"])
	}
}
