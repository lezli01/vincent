package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// GET /v1/tasks/{id}/workflow is the task's own §5.3 snapshot, not the
// registry entry of the same name — so these run against the real handlers,
// where a client/server drift would show up as a decode that lost a field.

// splicedSnapshot is what an `include` leaves behind (§7.9): the flat body,
// with every spliced step carrying the chain it came through. A registry
// entry would show one collapsed `include` node here; a task's graph shows
// the steps that actually ran (task 051 decision 4).
const splicedSnapshot = `name: shipit
steps:
  - {id: plan, type: agent, prompt: plan it}
  - {id: lint, type: command, run: "go vet ./...", resolved_from: [go-checks]}
  - {id: test, type: command, run: "go test ./...", resolved_from: [go-checks]}
`

func TestGetTaskWorkflowOverTheWire(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	task := &store.Task{
		ProjectID: h.projectID, Title: "spliced", WorkflowName: "shipit",
		WorkflowSnapshot: splicedSnapshot,
		BaseBranch:       "main", BranchName: "vincent/9-spliced", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := c.GetTaskWorkflow(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTaskWorkflow: %v", err)
	}
	if !got.Valid() {
		t.Fatalf("snapshot reported invalid: %+v", got.Errors)
	}
	if got.TaskID != task.ID || got.Name != "shipit" {
		t.Errorf("envelope = %+v, want the task's own workflow", got)
	}
	if len(got.Definition.Steps) != 3 {
		t.Fatalf("steps = %d, want the include already spliced flat", len(got.Definition.Steps))
	}
	// Attribution rides on the step, because after a splice there is no
	// include left to point at.
	if from := got.Definition.Steps[1].ResolvedFrom; len(from) != 1 || from[0] != "go-checks" {
		t.Errorf("resolved_from = %v, want the chain the step was spliced through", from)
	}
	if got.Definition.Steps[0].ResolvedFrom != nil {
		t.Errorf("the task's own step carries a resolved_from: %v", got.Definition.Steps[0].ResolvedFrom)
	}
}

// `edit + retry` rewrites the task's own snapshot (§6), and the next fetch
// must show the rewrite — which is the whole reason this endpoint is not
// GET /v1/workflows/definition with a task_id.
func TestGetTaskWorkflowReflectsAnEditRetryRewrite(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	id := h.snapshotTask(t)

	before, err := c.GetTaskWorkflow(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTaskWorkflow: %v", err)
	}
	if !before.Valid() {
		t.Fatalf("snapshot reported invalid: %+v", before.Errors)
	}

	// The rewrite lands the way `edit + retry` lands one: atomically with a
	// transition, which is the only writer of a task's own snapshot.
	rewritten := "name: rewritten\nsteps:\n  - {id: only, type: command, run: echo hi}\n"
	if _, _, err := h.st.TransitionTask(t.Context(), id,
		store.TaskQueued, store.TaskRunning, store.TaskChange{Snapshot: &rewritten}); err != nil {
		t.Fatalf("rewrite the snapshot: %v", err)
	}

	after, err := c.GetTaskWorkflow(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTaskWorkflow after the rewrite: %v", err)
	}
	if after.Name != "rewritten" || len(after.Definition.Steps) != 1 {
		t.Errorf("after = %+v, want the rewritten snapshot", after.Definition)
	}
}

// A snapshot that does not parse is a 200 with findings and a null
// definition, never a 4xx — the same contract the registry's definition
// endpoint has (task 017 decision 11). The default harness task's placeholder
// snapshot is exactly that case.
func TestGetTaskWorkflowBrokenSnapshotIsA200(t *testing.T) {
	h := newHarness(t)

	got, err := h.client().GetTaskWorkflow(t.Context(), h.taskID)
	if err != nil {
		t.Fatalf("GetTaskWorkflow returned an error for an unparseable snapshot: %v", err)
	}
	if got.Valid() || got.Definition != nil {
		t.Errorf("got = %+v, want no body", got)
	}
	if len(got.Errors) == 0 || got.Error == nil {
		t.Errorf("got = %+v, want findings on a snapshot that does not parse", got)
	}
}
