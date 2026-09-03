package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// loopWorkflow is a two-step workflow whose second step is a `for_each` with
// no bound of its own, so the snapshot's only number is the global ceiling of
// 10 — the wrong denominator issue #317 reports. Its body has two steps, so a
// body position is something other than 1/1.
const loopWorkflow = `name: each
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
        run: echo one
      - id: verify
        type: command
        run: echo two
`

// loopTask creates a task parked on the loop step of loopWorkflow, with one
// body row recording the extent its admission planned.
func loopTask(t *testing.T, h *harness) int64 {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: "looped", WorkflowName: "each",
		WorkflowSnapshot: loopWorkflow, BaseBranch: "main",
		BranchName: "vincent/9-looped", State: store.TaskRunning, CurrentStep: 1,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := h.st.CreateStepRun(t.Context(), &store.StepRun{
		TaskID: task.ID, StepIndex: 1, StepID: "verify", StepType: "command",
		Attempt: 1, Iteration: 2, LoopItem: "beta", LoopTotal: 3,
		State: store.StepRunning,
	}); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return task.ID
}

// The wire round trip, against the real handlers: the loop's real extent and
// the body step it is on come back on the detail rollup, on the list row a
// board reads, and — as loop_total — on the step-run DTO. This is where the
// server's loopResponse and the client's LoopRollup would drift apart if
// either side grew the fields alone.
func TestLoopRollupOverTheWire(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	id := loopTask(t, h)

	detail, err := c.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Loop == nil {
		t.Fatal("no loop rollup on a task parked on a loop step")
	}
	// The list is where a board reads it, so both call sites are proven.
	tasks, err := c.ListTasks(t.Context(), apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var listed *apiclient.LoopRollup
	for i := range tasks {
		if tasks[i].ID == id {
			listed = tasks[i].Loop
		}
	}
	if listed == nil {
		t.Fatal("no loop rollup on the list row")
	}

	for name, got := range map[string]*apiclient.LoopRollup{"detail": detail.Loop, "list": listed} {
		if got.Driver != "for_each" || got.Iteration != 2 || got.Item != "beta" {
			t.Errorf("%s rollup = %+v, want for_each at iteration 2 on beta", name, got)
		}
		if got.Total != 3 {
			t.Errorf("%s total = %d, want the list's real length of 3", name, got.Total)
		}
		if got.MaxIterations != 10 {
			t.Errorf("%s max_iterations = %d, want the ceiling of 10 beside it", name, got.MaxIterations)
		}
		if got.BodyStep != "verify" || got.BodyIndex != 2 || got.BodyTotal != 2 {
			t.Errorf("%s body clause = %s %d/%d, want verify 2/2",
				name, got.BodyStep, got.BodyIndex, got.BodyTotal)
		}
		if want := "loop 2/3 · beta · verify 2/2"; got.Display() != want {
			t.Errorf("%s Display() = %q, want %q", name, got.Display(), want)
		}
	}

	// The per-row extent rides the step-run DTO, which is what a client
	// reading rows directly counts against.
	if len(detail.Steps) != 1 {
		t.Fatalf("steps = %d, want the one body row", len(detail.Steps))
	}
	if got := detail.Steps[0].LoopTotal; got != 3 {
		t.Errorf("step run loop_total = %d, want 3", got)
	}
}
