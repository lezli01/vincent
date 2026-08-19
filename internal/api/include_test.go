package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/workflow"
)

// createTaskFrom posts a task on the named workflow and returns the response.
func createTaskFrom(t *testing.T, h *workflowHarness, projectID int64, name string) (*http.Response, []byte) {
	t.Helper()
	return h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": projectID, "title": "include", "workflow": name,
	})
}

// snapshotOf creates a task and returns its parsed snapshot.
func snapshotOf(t *testing.T, h *workflowHarness, projectID int64, name string) *workflow.Workflow {
	t.Helper()
	resp, body := createTaskFrom(t, h, projectID, name)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	task, err := h.store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	return wf
}

// TestTaskCreateExpandsIncludes is §7.9 at the seam that matters: the registry
// is read once, at creation, and what it said is frozen into the snapshot.
// Editing the included file afterwards must not reach the task.
func TestTaskCreateExpandsIncludes(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "checks",
		"name: checks\nsteps:\n  - {id: lint, type: command, run: make original}\n")
	writeWorkflowFile(t, h.globalDir, "root",
		"name: root\nsteps:\n"+
			"  - {id: work, type: command, run: make work}\n"+
			"  - {id: verify, type: include, workflow: checks}\n")
	h.reg.ReloadGlobal()
	projectID := fanOutProject(t, h)

	wf := snapshotOf(t, h, projectID, "root")
	if len(wf.Steps) != 2 {
		t.Fatalf("snapshot steps = %d, want the include replaced by its callee's one step", len(wf.Steps))
	}
	spliced := wf.Steps[1]
	if spliced.ID != "lint" || spliced.Type != workflow.StepCommand {
		t.Fatalf("spliced step = %+v, want the callee's lint command", spliced)
	}
	if !strings.Contains(spliced.Run, "original") {
		t.Errorf("run = %q, want what the registry said at creation", spliced.Run)
	}
	if strings.Join(spliced.ResolvedFrom, ",") != "checks" {
		t.Errorf("resolved_from = %v, want [checks]", spliced.ResolvedFrom)
	}

	// The snapshot is the durable copy (§5.3): a later edit must not move it.
	writeWorkflowFile(t, h.globalDir, "checks",
		"name: checks\nsteps:\n  - {id: lint, type: command, run: make edited}\n")
	h.reg.ReloadGlobal()
	again := snapshotOf(t, h, projectID, "root")
	if !strings.Contains(again.Steps[1].Run, "edited") {
		t.Error("a task created after the edit did not pick the new file up")
	}
}

// TestTaskCreateRejectsBadIncludes: every one of these is something the person
// creating the task can act on, so each is a 400 in front of them rather than
// a step that fails six hours in.
func TestTaskCreateRejectsBadIncludes(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "missing",
		"name: missing\nsteps:\n  - {id: c, type: include, workflow: nowhere}\n")
	writeWorkflowFile(t, h.globalDir, "alpha",
		"name: alpha\nsteps:\n  - {id: c, type: include, workflow: beta}\n")
	writeWorkflowFile(t, h.globalDir, "beta",
		"name: beta\nsteps:\n  - {id: c, type: include, workflow: alpha}\n")
	writeWorkflowFile(t, h.globalDir, "shared",
		"name: shared\nsteps:\n  - {id: clash, type: command, run: make}\n")
	writeWorkflowFile(t, h.globalDir, "collides",
		"name: collides\nsteps:\n"+
			"  - {id: clash, type: command, run: make mine}\n"+
			"  - {id: c, type: include, workflow: shared}\n")
	h.reg.ReloadGlobal()
	projectID := fanOutProject(t, h)

	// The wants are matched against the JSON body, so quoted names appear
	// escaped: `\"nowhere\"` is what a reader of the response sees.
	for _, tc := range []struct{ workflow, want string }{
		{"missing", `\"nowhere\" not found`},
		{"alpha", "workflow cycle"},
		{"collides", `step id \"clash\" twice`},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			resp, body := createTaskFrom(t, h, projectID, tc.workflow)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("create: %d %s, want 400", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Errorf("body = %s, want it to mention %q", body, tc.want)
			}
		})
	}
}

// TestTaskCreateRevalidatesExpansion is decision 9: an include may appear
// anywhere a step may, so the nesting rules it can break are only decidable
// once the steps are in place. A loop fragment included *into* a loop body is
// the case that has no other way of being caught.
func TestTaskCreateRevalidatesExpansion(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "innerloop",
		"name: innerloop\nsteps:\n"+
			"  - {id: l, type: loop, count: 2, steps: [{id: work, type: command, run: make}]}\n")
	writeWorkflowFile(t, h.globalDir, "nests",
		"name: nests\nsteps:\n"+
			"  - {id: outer, type: loop, count: 2, steps: [{id: c, type: include, workflow: innerloop}]}\n")
	h.reg.ReloadGlobal()
	projectID := fanOutProject(t, h)

	resp, body := createTaskFrom(t, h, projectID, "nests")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create: %d %s, want 400 — loops do not nest", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "once its includes are expanded") {
		t.Errorf("body = %s, want it to say the expansion is what failed", body)
	}
}

// TestWorkflowListReportsIncludes: a client shows what a workflow depends on
// without resolving anything itself.
func TestWorkflowListReportsIncludes(t *testing.T) {
	h := newInputHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "checks",
		"name: checks\nsteps:\n  - {id: lint, type: command, run: make}\n")
	writeWorkflowFile(t, h.globalDir, "root",
		"name: root\nsteps:\n  - {id: c, type: include, workflow: checks}\n")
	h.reg.ReloadGlobal()

	resp, body := h.doJSON(t, http.MethodGet, "/v1/workflows", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var list struct {
		Workflows []struct {
			Name     string   `json:"name"`
			Includes []string `json:"includes"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, w := range list.Workflows {
		switch w.Name {
		case "root":
			if strings.Join(w.Includes, ",") != "checks" {
				t.Errorf("root includes = %v, want [checks]", w.Includes)
			}
		case "checks":
			if len(w.Includes) != 0 {
				t.Errorf("checks includes = %v, want none", w.Includes)
			}
		}
	}
}
