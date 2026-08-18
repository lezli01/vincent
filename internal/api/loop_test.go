package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// loopSnapshotYAML is a two-step workflow whose second step is a `for_each`
// loop, so the rollup has a step to be present on and an index to be absent
// on.
const loopSnapshotYAML = `name: each
steps:
  - id: discover
    type: command
    run: ls
  - id: visit
    type: loop
    for_each: '{{ .Steps.discover.Result }}'
    max_iterations: 6
    steps:
      - id: touch
        type: command
        run: echo {{ .Loop.Item }}
`

// TestLoopRollup covers decision 14: visibility into a running loop comes
// from a rollup on the task endpoints, not from forty durable events saying
// what forty rows already say. The rollup is derived per request — the
// persisted loop cursor decision 7 declined — so the test drives it by
// writing rows.
func TestLoopRollup(t *testing.T) {
	h := newTaskHarness(t, 0, false)

	// Cursor on step 0, which is not a loop: no rollup at all.
	plain := h.seedTask(t, loopSnapshotYAML, 0)
	if got := h.loopOf(t, plain.ID); got != nil {
		t.Fatalf("loop rollup = %+v on a non-loop step, want absent", got)
	}

	task := h.seedTask(t, loopSnapshotYAML, 1)
	for iteration, item := range []string{"alpha", "beta"} {
		run := &store.StepRun{
			TaskID: task.ID, StepIndex: 1, StepID: "touch", StepType: "command",
			Attempt: 1, Iteration: iteration + 1, LoopItem: item, State: store.StepSucceeded,
		}
		if err := h.store.CreateStepRun(t.Context(), run); err != nil {
			t.Fatalf("create step run: %v", err)
		}
	}

	got := h.loopOf(t, task.ID)
	if got == nil {
		t.Fatal("loop rollup absent while the current step is a loop")
	}
	if got.Driver != "for_each" {
		t.Errorf("driver = %q, want for_each", got.Driver)
	}
	if got.Iteration != 2 {
		t.Errorf("iteration = %d, want 2 — the highest a row carries", got.Iteration)
	}
	if got.MaxIterations != 6 {
		t.Errorf("max_iterations = %d, want the step's own 6", got.MaxIterations)
	}
	if got.Item != "beta" {
		t.Errorf("item = %q, want beta — the item the current iteration is on", got.Item)
	}

	// The board reads the same rollup off the list endpoint, or `loop 4/10`
	// would need a second request per row.
	resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var list []listTaskResponse
	if err := json.Unmarshal(body, &list); err != nil || len(list) == 0 {
		t.Fatalf("list body: %v (%d rows)", err, len(list))
	}
	var rolled *loopResponse
	for i := range list {
		if list[i].ID == task.ID {
			rolled = list[i].Loop
		}
	}
	if rolled == nil || rolled.Iteration != 2 {
		t.Errorf("list loop rollup = %+v, want iteration 2", rolled)
	}
}

// TestStepRunCarriesItsPosition pins the two columns migration 0009 added on
// the wire. Without them a client cannot tell two attempts of one body step
// apart, because they share the loop's step_index and step_id.
func TestStepRunCarriesItsPosition(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	created := h.createTask(t, map[string]any{"title": "loops", "workflow": "adhoc"})
	run := &store.StepRun{
		TaskID: created.ID, StepIndex: 0, StepID: "touch", StepType: "command",
		Attempt: 1, Iteration: 3, LoopItem: "gamma", State: store.StepSucceeded,
	}
	if err := h.store.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("create step run: %v", err)
	}
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/steps", created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("steps: %d %s", resp.StatusCode, body)
	}
	var steps []stepRunResponse
	if err := json.Unmarshal(body, &steps); err != nil || len(steps) != 1 {
		t.Fatalf("steps body: %v (%d rows)", err, len(steps))
	}
	if steps[0].Iteration != 3 {
		t.Errorf("iteration = %d, want 3", steps[0].Iteration)
	}
	if steps[0].LoopItem == nil || *steps[0].LoopItem != "gamma" {
		t.Errorf("loop_item = %v, want gamma", steps[0].LoopItem)
	}
}

// seedTask inserts a task with a chosen snapshot and step cursor, straight
// through the store. Creation over HTTP would need the loop workflow to be in
// a registry scope, and what these tests are about is what the endpoints make
// of a task once it exists.
func (h *taskHarness) seedTask(t *testing.T, snapshot string, currentStep int) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID:        h.projectID,
		Title:            "loops",
		WorkflowName:     "each",
		WorkflowSnapshot: snapshot,
		BaseBranch:       "main",
		State:            store.TaskQueued,
		CurrentStep:      currentStep,
	}
	resolve := func(id int64) (string, error) { return fmt.Sprintf("vincent/%d-loops", id), nil }
	if err := h.store.CreateTask(t.Context(), task, resolve); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (h *taskHarness) loopOf(t *testing.T, id int64) *loopResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d %s", resp.StatusCode, body)
	}
	var detail taskResponse
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("detail body: %v", err)
	}
	return detail.Loop
}
