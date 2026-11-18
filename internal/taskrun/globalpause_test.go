package taskrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// globalPauseSnapshot is a step long enough to pause during, and a second
// step whose row proves the task did or did not move past the boundary.
const globalPauseSnapshot = `name: test
steps:
  - id: slow
    type: command
    run: sleep 3
  - id: after
    type: command
    run: git --version
`

// TestGlobalPauseParksAtTheStepBoundary drives the global pause (task 149)
// through a real runner and scheduler: a running task finishes its step and
// parks, marked as the pause's; a task created meanwhile stays queued; a
// task a human paused stays paused after `continue`; and continue resumes
// the parked one, which then runs to the end.
func TestGlobalPauseParksAtTheStepBoundary(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	ctx := t.Context()

	running := h.createTask(t, globalPauseSnapshot)
	h.waitForState(t, running.ID, store.TaskRunning)
	// Wait for the step itself to start: a pause that lands before it is
	// taken at the boundary ahead of step 0, which proves less.
	wait.Until(t, "the first step to start", func() bool { return len(h.stepRuns(t, running.ID)) > 0 })
	human := h.createTask(t, globalPauseSnapshot)
	if _, err := h.runner.Pause(ctx, human.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	h.waitForState(t, human.ID, store.TaskPaused)

	st, err := h.runner.PauseAll(ctx, "maintenance")
	if err != nil {
		t.Fatalf("PauseAll: %v", err)
	}
	if st.State != GlobalPausing || st.Tally.Running != 1 {
		t.Fatalf("PauseAll = %+v, want pausing with one task still running", st)
	}
	late := h.createTask(t, globalPauseSnapshot)

	parked := h.waitForState(t, running.ID, store.TaskPaused)
	if !parked.GlobalPause || parked.PauseRequested {
		t.Errorf("parked task: global_pause=%v pause_requested=%v, want true/false", parked.GlobalPause, parked.PauseRequested)
	}
	if parked.CurrentStep != 1 {
		t.Errorf("parked at step %d, want 1 — the running step finishes first", parked.CurrentStep)
	}
	if st, err := h.runner.GlobalPauseState(ctx); err != nil || st.State != GlobalPaused {
		t.Fatalf("GlobalPauseState = %+v, %v; want paused", st, err)
	}
	if got := h.task(t, late.ID).State; got != store.TaskQueued {
		t.Fatalf("task created during the pause is %s, want queued", got)
	}

	st, resumed, err := h.runner.ContinueAll(ctx)
	if err != nil || resumed != 1 || st.State != GlobalActive {
		t.Fatalf("ContinueAll = %+v, %d, %v; want active with one task resumed", st, resumed, err)
	}
	h.waitForState(t, running.ID, store.TaskDone)
	h.waitForState(t, late.ID, store.TaskDone)
	if got := h.task(t, human.ID); got.State != store.TaskPaused || got.GlobalPause {
		t.Fatalf("human-paused task after continue: %s (global_pause=%v), want paused and unmarked", got.State, got.GlobalPause)
	}
}
