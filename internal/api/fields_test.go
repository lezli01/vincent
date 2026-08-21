package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

const declaredFieldsYAML = `name: release
description: Prepare a release.
fields:
  - name: ticket
    label: Ticket
    description: Issue tracker key.
    required: true
    pattern: '^OPS-[0-9]+$'
  - name: retries
    type: integer
  - name: ratio
    type: number
  - name: dry-run
    type: boolean
steps:
  - {id: approve, type: manual, instructions: review}
`

func TestWorkflowEndpointsExposeDeclaredFields(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "release", declaredFieldsYAML)
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
		if entry.Name == "release" {
			fields = entry.Fields
		}
	}
	if len(fields) != 4 || fields[0].Name != "ticket" || fields[0].Type != "string" ||
		!fields[0].Required || fields[0].Pattern != `^OPS-[0-9]+$` {
		t.Fatalf("list fields = %+v", fields)
	}

	_, body = h.definition(t, "release", 0)
	definition := decodeDefinition(t, body).Definition
	if definition == nil || len(definition.Fields) != 4 {
		t.Fatalf("definition fields = %+v", definition)
	}
	if definition.Fields[1].Name != "retries" || definition.Fields[1].Type != "integer" {
		t.Errorf("second definition field = %+v", definition.Fields[1])
	}
}

func TestTaskCreateValidatesDeclaredFieldsAndKeepsAdditionalFields(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "release", declaredFieldsYAML)
	h.reg.ReloadGlobal()
	repo := testrepo.Init(t, "main")
	project := h.mustCreate(t, map[string]any{"path": repo})
	projectID := int64(project["id"].(float64))

	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"missing required", nil, `field \"ticket\" is required`},
		{"pattern", map[string]string{"ticket": "BUG-4"}, "must match pattern"},
		{"integer", map[string]string{"ticket": "OPS-4", "retries": "many"}, "base-10 integer"},
		{"number", map[string]string{"ticket": "OPS-4", "ratio": "0x1p2"}, "finite decimal number"},
		{"boolean", map[string]string{"ticket": "OPS-4", "dry-run": "yes"}, "true or false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
				"project_id": projectID,
				"workflow":   "release",
				"title":      "invalid " + tt.name,
				"fields":     tt.fields,
			})
			wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("body %s missing %q", body, tt.want)
			}
		})
	}

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": projectID,
		"workflow":   "release",
		"title":      "valid release",
		"fields": map[string]string{
			"ticket": "OPS-42", "retries": "3", "dry-run": "false",
			"owner": "alice", // not declared: the task field map remains open
		},
	})
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
	if stored.Fields["owner"] != "alice" || stored.Fields["ticket"] != "OPS-42" {
		t.Errorf("stored fields = %v; declared and additional fields must both survive", stored.Fields)
	}
}

func TestTaskCreateUsesOnlyTheSelectedWorkflowFieldContract(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "internal", `name: internal
fields:
  - {name: secret, required: true}
steps:
  - {id: work, type: manual, instructions: review}
`)
	writeWorkflowFile(t, h.globalDir, "include-root", `name: include-root
steps:
  - {id: nested, type: include, workflow: internal}
`)
	writeWorkflowFile(t, h.globalDir, "fanout-root", `name: fanout-root
steps:
  - id: spread
    type: fan_out
    lanes:
      - {id: child, workflow: internal}
`)
	h.reg.ReloadGlobal()
	projectID := fanOutProject(t, h)

	for _, name := range []string{"include-root", "fanout-root"} {
		t.Run(name, func(t *testing.T) {
			resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
				"project_id": projectID,
				"workflow":   name,
				"title":      "run " + name,
			})
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("create: %d %s; nested declarations must not leak into the root contract",
					resp.StatusCode, body)
			}
		})
	}
}
