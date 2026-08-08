package taskrun

import (
	"context"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// TestStopGracefulRequeuesRunningTask is §12.4's graceful half: a shutdown
// Terminates the live process, the actor classifies the abrupt exit as an
// interruption (not a failure), and the task re-queues at the same step
// before StopGraceful returns.
func TestStopGracefulRequeuesRunningTask(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	task := h.createTask(t, `name: hangwf
steps:
  - id: implement
    type: agent
    prompt: hang until shut down
`)
	h.start(t)
	h.waitForState(t, task.ID, store.TaskRunning)

	// The process is provably up once its PID is journaled.
	deadline := time.Now().Add(30 * time.Second)
	for {
		runs := h.stepRuns(t, task.ID)
		if len(runs) > 0 && runs[len(runs)-1].PID != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent step never journaled a PID")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The daemon's order: admission stops first, then the runner drains, so
	// nothing re-admits the task the moment it returns to queued.
	h.sched.Stop()
	h.runner.StopGraceful(15 * time.Second)

	got, err := h.store.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != store.TaskQueued {
		t.Fatalf("task = %s (block_reason %q), want queued", got.State, got.BlockReason)
	}
	if got.CurrentStep != 0 {
		t.Errorf("current_step = %d, want 0 (re-runs the interrupted step)", got.CurrentStep)
	}
	runs := h.stepRuns(t, task.ID)
	last := runs[len(runs)-1]
	if last.State != store.StepInterrupted || last.FailureReason != ReasonInterrupted {
		t.Errorf("attempt = %s/%q, want interrupted/interrupted", last.State, last.FailureReason)
	}
	if last.PID != nil {
		t.Errorf("finished attempt still carries pid %d", *last.PID)
	}
}
