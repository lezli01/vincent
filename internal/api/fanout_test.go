package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// fanOutProject spins up a harness with a project and returns its id.
func fanOutProject(t *testing.T, h *workflowHarness) int64 {
	t.Helper()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	return int64(p["id"].(float64))
}

// TestTaskCreateResolvesLaneWorkflows is decision 4 at the seam that matters:
// the registry is read once, at creation, and what it said is frozen into the
// task's snapshot. Editing the lane's file afterwards must not reach the task.
func TestTaskCreateResolvesLaneWorkflows(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "module",
		"name: module\nsteps:\n  - {id: impl, type: command, run: make original}\n")
	writeWorkflowFile(t, h.globalDir, "root",
		"name: root\nsteps:\n  - {id: build, type: fan_out, lanes: [{id: api, workflow: module}]}\n")
	h.reg.ReloadGlobal()
	projectID := fanOutProject(t, h)

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": projectID, "title": "fan out", "workflow": "root",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Rewrite the lane's workflow. The snapshot already taken must not move.
	writeWorkflowFile(t, h.globalDir, "module",
		"name: module\nsteps:\n  - {id: impl, type: command, run: make edited}\n")
	h.reg.ReloadGlobal()

	task, err := h.store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	lane := wf.Steps[0].Lanes[0]
	if len(lane.Steps) != 1 {
		t.Fatalf("lane steps = %+v, want the registry's inlined at creation", lane.Steps)
	}
	if got := lane.Steps[0].Run; !strings.Contains(got, "original") {
		t.Errorf("lane run = %q; a later edit to the lane's file reached an existing task", got)
	}
	if lane.ResolvedFrom != "module" {
		t.Errorf("lane resolved_from = %q, want the child's workflow_name recorded", lane.ResolvedFrom)
	}
}

// TestTaskCreateRejectsCyclicLanes: an infinite spawn is a 400 in front of
// the person typing, and the message names the path rather than sending them
// to grep every workflow file they own.
func TestTaskCreateRejectsCyclicLanes(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "alpha",
		"name: alpha\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: beta}]}\n")
	writeWorkflowFile(t, h.globalDir, "beta",
		"name: beta\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: alpha}]}\n")
	h.reg.ReloadGlobal()
	projectID := fanOutProject(t, h)

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": projectID, "title": "loop", "workflow": "alpha",
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	for _, want := range []string{"cycle", "alpha", "beta"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("message %s missing %q", body, want)
		}
	}
}

// TestTaskCreateRejectsTreePastMaxTasks: the bound is enforced where the tree
// is still a document, not after N worktrees exist. It runs against the real
// configured default (64), so the test also proves creation reads the config
// rather than a constant.
func TestTaskCreateRejectsTreePastMaxTasks(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	src := "name: wide\nsteps:\n  - id: f\n    type: fan_out\n    lanes:\n"
	for i := range config.Default().FanOut.MaxTasks + 1 {
		src += fmt.Sprintf("      - {id: l%d, steps: [{id: s%d, type: command, run: make}]}\n", i, i)
	}
	writeWorkflowFile(t, h.globalDir, "wide", src)
	h.reg.ReloadGlobal()
	projectID := fanOutProject(t, h)

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": projectID, "title": "too wide", "workflow": "wide",
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "max_tasks") {
		t.Errorf("message %s does not name the bound it crossed", body)
	}
}
