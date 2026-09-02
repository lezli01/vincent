package scheduler

// The eager fan-out wake (spec §7.6/§11, task 081 decision 1).

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// park moves a task to `awaiting_children`, optionally recording an eager
// step's wake watermark the way the engine's park does.
func (h *harness) park(t *testing.T, task *store.Task, watermark *int) {
	t.Helper()
	if _, _, err := h.store.TransitionTask(t.Context(), task.ID,
		task.State, store.TaskAwaitingChildren,
		store.TaskChange{SettledChildrenWatermark: watermark}); err != nil {
		t.Fatalf("park task %d: %v", task.ID, err)
	}
}

// lane inserts one queued child task of parent. Every lane starts queued; the
// tests move them with settle, which is what the watermark reacts to.
func (h *harness) lane(t *testing.T, parent *store.Task, id string, order int) *store.Task {
	t.Helper()
	index := 0
	child := &store.Task{
		ProjectID: parent.ProjectID, Title: parent.Title + "-" + id,
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: "vincent/" + parent.Title + "-" + id,
		State:        store.TaskQueued,
		ParentTaskID: &parent.ID, ParentStepIndex: &index,
		LaneID: id, LaneOrder: order,
	}
	if err := h.store.CreateTask(t.Context(), child, nil); err != nil {
		t.Fatalf("CreateTask lane %s: %v", id, err)
	}
	return child
}

func (h *harness) settle(t *testing.T, lane *store.Task) {
	t.Helper()
	if _, _, err := h.store.TransitionTask(t.Context(), lane.ID,
		lane.State, store.TaskDone, store.TaskChange{}); err != nil {
		t.Fatalf("settle lane %d: %v", lane.ID, err)
	}
	lane.State = store.TaskDone
}

// TestEagerParentWakesOncePerLane is decision 1's direct test: the watermark
// is exceeded by a lane settling and by nothing else, so a parent that parks
// again having found nothing to do is *not* re-queued by its own park.
//
// Under the issue's option (a) — "a direct child has settled" — the predicate
// is level-triggered and stays true, so this test would loop forever.
func TestEagerParentWakesOncePerLane(t *testing.T) {
	h := newHarness(t, 4)
	pid := h.project(t, "p", nil)
	parent := h.task(t, pid, "parent", store.TaskRunning, 0, 0)
	a := h.lane(t, parent, "a", 0)
	b := h.lane(t, parent, "b", 1)

	// Parked having seen nothing settled.
	h.park(t, parent, intPtr(0))
	h.sched.resumeSettledParents(t.Context())
	if got := h.state(t, parent.ID); got != store.TaskAwaitingChildren {
		t.Fatalf("parent is %s with no lane settled; want it left parked", got)
	}

	// One lane settles: the count moves past the watermark exactly once.
	h.settle(t, a)
	h.sched.resumeSettledParents(t.Context())
	if got := h.state(t, parent.ID); got != store.TaskQueued {
		t.Fatalf("parent is %s after a lane settled; want queued", got)
	}

	// The admission finds nothing to do and parks again at the new position.
	// This is the spin the watermark exists to prevent: re-queueing here
	// would be the parent's own park waking it.
	reparked, err := h.store.GetTask(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if reparked.SettledChildrenWatermark != nil {
		t.Errorf("the watermark survived the resume: %d; leaving awaiting_children must clear it",
			*reparked.SettledChildrenWatermark)
	}
	h.park(t, reparked, intPtr(1))
	for range 5 {
		h.sched.resumeSettledParents(t.Context())
		if got := h.state(t, parent.ID); got != store.TaskAwaitingChildren {
			t.Fatalf("parent is %s with the count still at its watermark; want it left parked", got)
		}
	}

	// The last lane settles: the subtree is done, and the barrier rule that
	// keeps running for an eager parent would have woken it too.
	h.settle(t, b)
	h.sched.resumeSettledParents(t.Context())
	if got := h.state(t, parent.ID); got != store.TaskQueued {
		t.Fatalf("parent is %s after the last lane settled; want queued", got)
	}
}

// TestBarrierParentIgnoresASettledLane: a parent parked with no watermark —
// every `schedule: barrier` step, and every parent parked before the column
// existed — is woken by nothing short of the whole subtree settling.
func TestBarrierParentIgnoresASettledLane(t *testing.T) {
	h := newHarness(t, 4)
	pid := h.project(t, "p", nil)
	parent := h.task(t, pid, "parent", store.TaskRunning, 0, 0)
	a := h.lane(t, parent, "a", 0)
	b := h.lane(t, parent, "b", 1)
	h.park(t, parent, nil)

	h.settle(t, a)
	h.sched.resumeSettledParents(t.Context())
	if got := h.state(t, parent.ID); got != store.TaskAwaitingChildren {
		t.Fatalf("a barrier parent is %s with one lane still running; want it parked", got)
	}

	h.settle(t, b)
	h.sched.resumeSettledParents(t.Context())
	if got := h.state(t, parent.ID); got != store.TaskQueued {
		t.Fatalf("a barrier parent is %s with every lane settled; want queued", got)
	}
}

// TestEagerWatermarkIgnoresDepthTwo: churn is bounded by the *direct* lane
// count, not by subtree size. A grandchild settling does not move a root's
// count, which is what makes "bounded by the lane count" literally true.
func TestEagerWatermarkIgnoresDepthTwo(t *testing.T) {
	h := newHarness(t, 4)
	pid := h.project(t, "p", nil)
	parent := h.task(t, pid, "parent", store.TaskRunning, 0, 0)
	lane := h.lane(t, parent, "a", 0)
	sub := h.lane(t, lane, "a1", 0)
	h.park(t, parent, intPtr(0))

	h.settle(t, sub)
	h.sched.resumeSettledParents(t.Context())
	if got := h.state(t, parent.ID); got != store.TaskAwaitingChildren {
		t.Fatalf("parent is %s after a depth-2 descendant settled; want it left parked", got)
	}
}
