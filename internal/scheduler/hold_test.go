package scheduler

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// TestAdmissionHoldDefersThenReleases is the §11 hold, both legs: not
// admitted while the clock is before the instant, admitted on the first walk
// after it. The clock is injected rather than slept through — a test that
// waited out a real hold would either be slow or be measuring the tick.
func TestAdmissionHoldDefersThenReleases(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	task := h.task(t, p, "held", store.TaskQueued, 0, time.Minute)

	start := time.Now().UTC()
	h.clock = start
	h.hold(t, task, start.Add(15*time.Minute), "usage_limit")

	h.sched.admit(t.Context())
	if got := h.admitter.ids(); len(got) != 0 {
		t.Fatalf("admitted %v before the hold expired", got)
	}

	// One second before: still held. The boundary is worth pinning because
	// an off-by-one here is a respawn loop, not a rounding error.
	h.clock = start.Add(15*time.Minute - time.Second)
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); len(got) != 0 {
		t.Fatalf("admitted %v one second before the hold expired", got)
	}

	h.clock = start.Add(15 * time.Minute)
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); len(got) != 1 || got[0] != task.ID {
		t.Fatalf("admitted %v after the hold expired, want [%d]", got, task.ID)
	}
	if got := h.state(t, task.ID); got != store.TaskRunning {
		t.Fatalf("state = %s, want running", got)
	}
}

// TestAdmissionHoldClearsOnAdmission proves the columns do not outlive the
// queued period they describe: the hold is dropped by the very transition
// that admits the task, so a later re-queue starts clean.
func TestAdmissionHoldClearsOnAdmission(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	task := h.task(t, p, "held", store.TaskQueued, 0, time.Minute)

	start := time.Now().UTC()
	h.clock = start
	h.hold(t, task, start.Add(-time.Minute), "usage_limit") // already expired

	h.sched.admit(t.Context())

	after, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after.AdmitNotBefore != nil || after.QueuedReason != "" {
		t.Fatalf("hold survived admission: admit_not_before=%v queued_reason=%q",
			after.AdmitNotBefore, after.QueuedReason)
	}
}

// TestHeldTaskDoesNotBlockTheQueue is the point of releasing the slot: the
// held task sorts first, and the walk must step over it rather than stop at
// it — otherwise one quota-walled task starves everything behind it.
func TestHeldTaskDoesNotBlockTheQueue(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	held := h.task(t, p, "held", store.TaskQueued, 0, 3*time.Minute)
	next := h.task(t, p, "next", store.TaskQueued, 0, time.Minute)

	start := time.Now().UTC()
	h.clock = start
	h.hold(t, held, start.Add(15*time.Minute), "usage_limit")

	h.sched.admit(t.Context())

	got := h.admitter.ids()
	if len(got) != 1 || got[0] != next.ID {
		t.Fatalf("admitted %v, want only [%d] — the held task must be stepped over", got, next.ID)
	}
}

// TestPauseOnHeldTaskStillParks guards the decision to evaluate the hold in
// the walk rather than in ListAdmissible's SQL. Filtering held tasks out of
// the query would leave this task showing `queued` until its hold expired,
// which is precisely the "showing queued while the human asked for paused"
// lie the pause check exists to prevent.
func TestPauseOnHeldTaskStillParks(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	task := h.task(t, p, "held", store.TaskRunning, 0, time.Minute)
	if _, err := h.store.RequestPause(t.Context(), task.ID); err != nil {
		t.Fatalf("RequestPause: %v", err)
	}
	// The engine's usage-limit re-queue: running → queued carrying the hold.
	start := time.Now().UTC()
	h.clock = start
	until := start.Add(15 * time.Minute)
	reason := "usage_limit"
	if _, _, err := h.store.TransitionTask(t.Context(), task.ID,
		store.TaskRunning, store.TaskQueued,
		store.TaskChange{AdmitNotBefore: &until, QueuedReason: &reason}); err != nil {
		t.Fatalf("re-queue with a hold: %v", err)
	}

	h.sched.admit(t.Context())

	if got := h.admitter.ids(); len(got) != 0 {
		t.Fatalf("admitted %v; a pending pause must park even a held task", got)
	}
	if got := h.state(t, task.ID); got != store.TaskPaused {
		t.Fatalf("state = %s, want paused", got)
	}
	after, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after.AdmitNotBefore != nil || after.QueuedReason != "" {
		t.Errorf("hold survived parking: admit_not_before=%v queued_reason=%q",
			after.AdmitNotBefore, after.QueuedReason)
	}
}
