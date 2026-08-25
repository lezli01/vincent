package scheduler

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// openRun journals a `running` step run against a task, the way the engine
// does before spawning a process — and the way a crashed daemon leaves one
// behind.
func openRun(t *testing.T, h *harness, taskID int64) *store.StepRun {
	t.Helper()
	run := &store.StepRun{
		TaskID: taskID, StepIndex: 0, StepID: "s", StepType: "agent",
		Attempt: 1, State: store.StepRunning,
	}
	if err := h.store.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return run
}

// A queued task whose previous attempt is still marked `running` was never
// reconciled: §12.4 finalizes the old run *before* the task returns to the
// queue. Admitting it would start a second attempt against a first one the
// database still calls live, so the scheduler refuses it — and keeps walking,
// because the rest of the queue is not at fault (issue #142).
func TestAdmitRefusesUnreconciledTask(t *testing.T) {
	h := newHarness(t, 4)
	projectID := h.project(t, "p", nil)
	// Higher priority, so the refusal is proven not to swallow the walk: this
	// one is looked at first.
	stuck := h.task(t, projectID, "stuck", store.TaskQueued, 10, time.Hour)
	healthy := h.task(t, projectID, "healthy", store.TaskQueued, 0, 0)
	openRun(t, h, stuck.ID)

	h.sched.admit(t.Context())

	got := h.admitter.ids()
	if len(got) != 1 || got[0] != healthy.ID {
		t.Fatalf("admitted = %v, want only the reconciled task %d", got, healthy.ID)
	}
	if s := h.state(t, stuck.ID); s != store.TaskQueued {
		t.Errorf("unreconciled task = %s, want it left queued", s)
	}

	// The refusal is about the contradiction, not about the task: close the
	// run and the next walk admits it like any other.
	if _, err := h.store.TerminalizeOpenStepRuns(
		t.Context(), stuck.ID, store.StepInterrupted, "interrupted"); err != nil {
		t.Fatalf("TerminalizeOpenStepRuns: %v", err)
	}
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); len(got) != 2 || got[1] != stuck.ID {
		t.Errorf("admitted = %v, want %d admitted once reconciled", got, stuck.ID)
	}
}
