package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
		Attempt: 1, Iteration: 3, LoopItem: "gamma", LoopTotal: 4, State: store.StepSucceeded,
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
	if steps[0].LoopTotal != 4 {
		t.Errorf("loop_total = %d, want 4 — the extent the admission planned", steps[0].LoopTotal)
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

// extentSnapshotYAML is the shape issue #317 reports wrong: a `for_each` with
// no `count:` and no step-level `max_iterations:`, so the only number the
// snapshot has is the global ceiling — which is not this loop's. The body has
// two steps, so a body position is something other than 1/1.
const extentSnapshotYAML = `name: each
steps:
  - id: discover
    type: command
    run: ls
  - id: visit
    type: loop
    for_each: '{{ .Steps.discover.Result }}'
    steps:
      - id: touch
        type: command
        run: echo {{ .Loop.Item }}
      - id: verify
        type: command
        run: echo ok
`

// loopBodyRun is one body row of the loop at step 1 of extentSnapshotYAML,
// recording the extent that snapshot's list has: three items, which is the
// number the ceiling of 10 was standing in for.
func loopBodyRun(taskID int64, stepID string, iteration int, item string) *store.StepRun {
	return &store.StepRun{
		TaskID: taskID, StepIndex: 1, StepID: stepID, StepType: "command",
		Attempt: 1, Iteration: iteration, LoopItem: item, LoopTotal: 3,
		State: store.StepSucceeded,
	}
}

// TestLoopRollupReportsTheRealExtent is issue #317's third fault. A 3-item
// `for_each` under a ceiling of 10 was reporting `loop 2/10`: the ceiling is
// a bound, not a denominator. The extent comes off the row the admission
// wrote, and the ceiling keeps reporting itself beside it — they are two
// numbers and neither stands in for the other.
func TestLoopRollupReportsTheRealExtent(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	task := h.seedTask(t, extentSnapshotYAML, 1)

	// Nothing has run yet: the rollup exists, but it has no extent to report
	// and no body step to name. Guessing either would read as an answer.
	before := h.loopOf(t, task.ID)
	if before == nil {
		t.Fatal("loop rollup absent while the current step is a loop")
	}
	if before.Total != 0 || before.BodyStep != "" || before.BodyIndex != 0 || before.BodyTotal != 0 {
		t.Errorf("rollup before the first row = %+v, want no extent and no body clause", before)
	}
	if before.MaxIterations != 10 {
		t.Errorf("max_iterations = %d, want the global ceiling of 10", before.MaxIterations)
	}

	for _, run := range []*store.StepRun{
		loopBodyRun(task.ID, "touch", 1, "alpha"),
		loopBodyRun(task.ID, "verify", 1, "alpha"),
		loopBodyRun(task.ID, "touch", 2, "beta"),
		loopBodyRun(task.ID, "verify", 2, "beta"),
	} {
		if err := h.store.CreateStepRun(t.Context(), run); err != nil {
			t.Fatalf("create step run: %v", err)
		}
	}

	got := h.loopOf(t, task.ID)
	if got.Total != 3 {
		t.Errorf("total = %d, want 3 — the list's real length", got.Total)
	}
	if got.MaxIterations != 10 {
		t.Errorf("max_iterations = %d, want 10 — the ceiling keeps its own meaning", got.MaxIterations)
	}
	if got.Iteration != 2 || got.Item != "beta" {
		t.Errorf("iteration/item = %d/%q, want 2/beta", got.Iteration, got.Item)
	}
	// The newest row of the highest iteration is the body step in progress.
	if got.BodyStep != "verify" || got.BodyIndex != 2 || got.BodyTotal != 2 {
		t.Errorf("body clause = %s %d/%d, want verify 2/2",
			got.BodyStep, got.BodyIndex, got.BodyTotal)
	}

	// The board reads the same rollup off the list endpoint.
	if rolled := h.listLoopOf(t, task.ID); rolled == nil || rolled.Total != 3 || rolled.BodyStep != "verify" {
		t.Errorf("list loop rollup = %+v, want the same extent and body step", rolled)
	}
}

// TestLoopRollupFallsBackToTheBoundForALegacyRow: a row written before the
// extent column existed carries 0. The snapshot's own bound is then the only
// number left, and it is the one the rollup reported for that row's whole
// life — so it is what it keeps reporting, rather than dropping the counter.
func TestLoopRollupFallsBackToTheBoundForALegacyRow(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	task := h.seedTask(t, loopSnapshotYAML, 1)
	if err := h.store.CreateStepRun(t.Context(), &store.StepRun{
		TaskID: task.ID, StepIndex: 1, StepID: "touch", StepType: "command",
		Attempt: 1, Iteration: 2, LoopItem: "beta", State: store.StepSucceeded,
	}); err != nil {
		t.Fatalf("create step run: %v", err)
	}
	got := h.loopOf(t, task.ID)
	if got.Total != 6 {
		t.Errorf("total = %d, want the step's own max_iterations of 6", got.Total)
	}
}

// TestLoopRollupOmitsTheBodyClauseItCannotPlace: a row whose step id is not
// one of the snapshot's body ids — a repair row, or any row at all once the
// snapshot no longer parses — gets no body clause. A position counted against
// a body the row did not run in is worse than no position.
func TestLoopRollupOmitsTheBodyClauseItCannotPlace(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	task := h.seedTask(t, extentSnapshotYAML, 1)
	if err := h.store.CreateStepRun(t.Context(), loopBodyRun(task.ID, "repair", 1, "alpha")); err != nil {
		t.Fatalf("create step run: %v", err)
	}
	got := h.loopOf(t, task.ID)
	if got.BodyStep != "" || got.BodyIndex != 0 || got.BodyTotal != 0 {
		t.Errorf("body clause = %+v, want none — `repair` is not a body step", got)
	}
	// Everything else the rollup knows is still reported.
	if got.Iteration != 1 || got.Total != 3 || got.Driver != "for_each" {
		t.Errorf("rollup = %+v, want driver/iteration/total intact", got)
	}
}

// TestLoopRollupWithoutBodyIDsDegrades is the same rule reached from the
// other side: a definition carrying no body order at all, which is what a
// snapshot the parser can no longer make sense of leaves behind. The rollup
// degrades to driver and iteration rather than to a body position it has
// nothing to count against.
func TestLoopRollupWithoutBodyIDsDegrades(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	task := h.seedTask(t, extentSnapshotYAML, 1)
	if err := h.store.CreateStepRun(t.Context(), loopBodyRun(task.ID, "touch", 1, "alpha")); err != nil {
		t.Fatalf("create step run: %v", err)
	}
	srv := &Server{deps: Deps{
		Store:  h.store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	summary := snapshotSummary{
		stepTotal: 2,
		stepNames: []string{"discover", "visit"},
		steps: []stepDefinition{
			{index: 0, id: "discover", stepType: "command"},
			{index: 1, id: "visit", stepType: "loop", loop: &loopDefinition{driver: "for_each", total: 10}},
		},
	}
	got := srv.loopRollup(t.Context(), task, summary)
	if got == nil {
		t.Fatal("loop rollup absent")
	}
	if got.Driver != "for_each" || got.Iteration != 1 {
		t.Errorf("rollup = %+v, want driver for_each at iteration 1", got)
	}
	if got.BodyStep != "" || got.BodyIndex != 0 || got.BodyTotal != 0 {
		t.Errorf("body clause = %+v, want none — there is no body order to place it in", got)
	}
}

// listLoopOf is the rollup as the board reads it, off the list endpoint.
func (h *taskHarness) listLoopOf(t *testing.T, id int64) *loopResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var list []listTaskResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("list body: %v", err)
	}
	for i := range list {
		if list[i].ID == id {
			return list[i].Loop
		}
	}
	t.Fatalf("task %d not in the list", id)
	return nil
}
