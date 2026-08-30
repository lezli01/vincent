package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// commented is a hand-written workflow with the shapes a marshal round trip
// destroys: a header comment, a trailing comment, blank lines between blocks
// and a `|` block scalar. Every test below that edits it asserts on the bytes
// it did not touch (task 065 decision 1).
const commented = `# review — do not lose these comments.
name: review
description: Review a change

defaults:
  agent: claude   # everybody has it

steps:
  - id: plan
    type: agent
    prompt: |
      Plan the work.
      Then stop.

  # the actual work
  - id: work
    type: command
    run: go build ./...
`

func decodeWrite(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode write response: %v (%s)", err, body)
	}
	return out
}

func TestWorkflowSchemaServesEveryStepType(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.doJSON(t, http.MethodGet, "/v1/workflows/schema", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var schema workflow.Schema
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if len(schema.Steps) != len(workflow.StepTypes) {
		t.Errorf("schema serves %d step types, %d exist", len(schema.Steps), len(workflow.StepTypes))
	}
	if len(schema.TopLevel) == 0 || len(schema.Common) == 0 || len(schema.Lane) == 0 {
		t.Errorf("schema is missing a section: %+v", schema)
	}
}

func TestWorkflowCreateGlobalFromSkeleton(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/workflows",
		map[string]any{"scope": "global", "name": "fresh"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	out := decodeWrite(t, body)
	if out["name"] != "fresh" || out["scope"] != "global" {
		t.Errorf("response = %v, want the fresh global entry", out)
	}
	if out["version"] == "" {
		t.Error("no version token: the next PATCH would have nothing to send")
	}
	// The write is in force before the answer, the way PATCH /v1/config is.
	_, list := h.doJSON(t, http.MethodGet, "/v1/workflows", nil)
	entry := findWorkflow(t, decodeWorkflowList(t, list), "fresh")
	if entry.Scope != "global" || entry.Error != nil {
		t.Errorf("registry entry = %+v, want a valid global fresh", entry)
	}
	src, err := os.ReadFile(filepath.Join(h.globalDir, "fresh.yaml"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(src), "name: fresh") {
		t.Errorf("skeleton name not rewritten:\n%s", src)
	}
	if !strings.Contains(string(src), "#") {
		t.Error("the skeleton's comments did not survive the write")
	}
}

func TestWorkflowCreateRefusesDuplicateNameAndFile(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "taken", manualYAML("taken", "first"))
	h.reg.ReloadGlobal()

	// Same file name.
	resp, body := h.doJSON(t, http.MethodPost, "/v1/workflows",
		map[string]any{"scope": "global", "name": "taken"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", resp.StatusCode, body)
	}
	// A different file declaring the same name is the §5.2 duplicate the
	// registry would list as an error rather than run.
	writeWorkflowFile(t, h.globalDir, "alias", manualYAML("taken", "second"))
	h.reg.ReloadGlobal()
	resp, body = h.doJSON(t, http.MethodPost, "/v1/workflows",
		map[string]any{"scope": "global", "name": "other", "from": "taken"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("fork onto a declared name: status %d, want 409: %s", resp.StatusCode, body)
	}
}

func TestWorkflowForkBuiltinIntoProjectShadowsIt(t *testing.T) {
	h := newWorkflowHarness(t)
	repo := testrepo.Init(t, "main")
	p := h.mustCreate(t, map[string]any{"path": repo})
	id := int64(p["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost, "/v1/workflows", map[string]any{
		"scope": "project", "project_id": id, "name": "adhoc-fork", "from": "adhoc",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	out := decodeWrite(t, body)
	// A fork keeps the source's own name:, because keeping it is what makes
	// the copy shadow the original under §5.2.
	if out["name"] != "adhoc" {
		t.Errorf("fork name = %v, want the source's own name", out["name"])
	}
	_, list := h.doJSON(t, http.MethodGet, "/v1/workflows?project_id="+strconv.FormatInt(id, 10), nil)
	entry := findWorkflow(t, decodeWorkflowList(t, list), "adhoc")
	if entry.Scope != "project" {
		t.Errorf("adhoc scope = %q, want the project fork to shadow the built-in", entry.Scope)
	}
}

// patchWorkflow is the read-then-write a form does: read the version, send
// the ops.
func (h *workflowHarness) patchWorkflow(t *testing.T, name, version string,
	ops []map[string]any,
) (*http.Response, []byte) {
	t.Helper()
	return h.doJSON(t, http.MethodPatch, "/v1/workflows?name="+name,
		map[string]any{"version": version, "ops": ops})
}

// versionOf reads the token GET /v1/workflows/definition hands back, which
// is what a form holds while it is open.
func (h *workflowHarness) versionOf(t *testing.T) string {
	t.Helper()
	const name = "review"
	_, body := h.doJSON(t, http.MethodGet, "/v1/workflows/definition?name="+name, nil)
	var out struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode definition: %v (%s)", err, body)
	}
	if out.Version == "" {
		t.Fatalf("no version for %s: %s", name, body)
	}
	return out.Version
}

func TestWorkflowPatchPreservesEverythingItDidNotTouch(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "review", commented)
	h.reg.ReloadGlobal()
	path := filepath.Join(h.globalDir, "review.yaml")

	resp, body := h.patchWorkflow(t, "review", h.versionOf(t), []map[string]any{
		{"op": "set", "path": "steps[1].run", "value": "go test ./..."},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := strings.Replace(commented, "run: go build ./...", "run: go test ./...", 1)
	if string(got) != want {
		t.Errorf("the file changed outside the edited line:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// The version the write hands back is the one the next PATCH must carry.
	if decodeWrite(t, body)["version"] != h.versionOf(t) {
		t.Error("the version in the response does not match the file on disk")
	}
}

func TestWorkflowPatchRejectionLeavesTheFileAlone(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "review", commented)
	h.reg.ReloadGlobal()
	path := filepath.Join(h.globalDir, "review.yaml")

	for _, tc := range []struct {
		name string
		ops  []map[string]any
	}{
		// Each of these is a §8.2 rule the endpoint enforces once, and the
		// forms never offer because the descriptor said so.
		{"manual under parallel", []map[string]any{
			{"op": "set", "path": "steps[1].type", "value": "parallel"},
			{"op": "insert", "path": "steps[1].steps[0]", "item": []map[string]any{
				{"key": "id", "value": "gate"},
				{"key": "type", "value": "manual"},
				{"key": "instructions", "value": "wait"},
			}},
		}},
		{"break outside a loop", []map[string]any{
			{"op": "insert", "path": "steps[2]", "item": []map[string]any{
				{"key": "id", "value": "stop"},
				{"key": "type", "value": "break"},
				{"key": "if", "value": "'true'"},
			}},
		}},
		{"if on an include", []map[string]any{
			{"op": "insert", "path": "steps[2]", "item": []map[string]any{
				{"key": "id", "value": "inc"},
				{"key": "type", "value": "include"},
				{"key": "workflow", "value": "adhoc"},
				{"key": "if", "value": "'true'"},
			}},
		}},
		{"merge agent without on_conflict agent", []map[string]any{
			{"op": "set", "path": "steps[1].type", "value": "fan_out"},
			{"op": "insert", "path": "steps[1].lanes[0]", "item": []map[string]any{
				{"key": "id", "value": "one"}, {"key": "workflow", "value": "adhoc"},
			}},
			{"op": "set", "path": "steps[1].merge.on_conflict", "value": "block"},
			{"op": "set", "path": "steps[1].merge.agent.id", "value": "fix"},
			{"op": "set", "path": "steps[1].merge.agent.type", "value": "agent"},
			{"op": "set", "path": "steps[1].merge.agent.prompt", "value": "fix it"},
		}},
		{"unresolvable path", []map[string]any{
			{"op": "set", "path": "steps[9].prompt", "value": "x"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			resp, body := h.patchWorkflow(t, "review", h.versionOf(t), tc.ops)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), `"code"`) {
				t.Errorf("not the §13.1 envelope: %s", body)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(after) != string(before) {
				t.Errorf("a refused patch changed the file:\n%s", after)
			}
		})
	}
}

func TestWorkflowPatchStaleVersionIs409(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "review", commented)
	h.reg.ReloadGlobal()

	// Two reads, one write, then the second write with the first version.
	first := h.versionOf(t)
	second := h.versionOf(t)
	if first != second {
		t.Fatalf("two reads of an unchanged file gave different versions")
	}
	resp, body := h.patchWorkflow(t, "review", first, []map[string]any{
		{"op": "set", "path": "description", "value": "once"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first write: status %d: %s", resp.StatusCode, body)
	}
	resp, body = h.patchWorkflow(t, "review", second, []map[string]any{
		{"op": "set", "path": "description", "value": "twice"},
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale write: status %d, want 409: %s", resp.StatusCode, body)
	}
	var envelope errorBody
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode 409: %v (%s)", err, body)
	}
	// The current token travels in details so the client can offer a reload
	// without a second round trip.
	if envelope.Error.Details["version"] != h.versionOf(t) {
		t.Errorf("409 details = %v, want the current version", envelope.Error.Details)
	}
}

func TestWorkflowPatchRefusesBuiltinAndMissing(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.patchWorkflow(t, "adhoc", "whatever", []map[string]any{
		{"op": "set", "path": "description", "value": "x"},
	})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "fork") {
		t.Fatalf("built-in patch: status %d: %s", resp.StatusCode, body)
	}
	resp, _ = h.patchWorkflow(t, "nope", "v", []map[string]any{
		{"op": "set", "path": "description", "value": "x"},
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing patch: status %d, want 404", resp.StatusCode)
	}
	resp, _ = h.patchWorkflow(t, "adhoc", "", []map[string]any{
		{"op": "set", "path": "description", "value": "x"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing version: status %d, want 400", resp.StatusCode)
	}
}

func TestWorkflowCreateRejectsBadScopeAndName(t *testing.T) {
	h := newWorkflowHarness(t)
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"no name", map[string]any{"scope": "global"}, http.StatusBadRequest},
		{"builtin scope", map[string]any{"scope": "builtin", "name": "x"}, http.StatusBadRequest},
		{"unknown scope", map[string]any{"scope": "elsewhere", "name": "x"}, http.StatusBadRequest},
		{"traversal", map[string]any{"scope": "global", "name": "../escape"}, http.StatusBadRequest},
		{"project without id", map[string]any{"scope": "project", "name": "x"}, http.StatusBadRequest},
		{"unknown project", map[string]any{"scope": "project", "project_id": 999, "name": "x"}, http.StatusNotFound},
		{"unknown fork source", map[string]any{"scope": "global", "name": "x", "from": "nope"}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := h.doJSON(t, http.MethodPost, "/v1/workflows", tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
		})
	}
}
