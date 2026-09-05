package taskrun

import (
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// lane creates a descendant of parent in a given state — what a `fan_out`
// step leaves behind, without running one.
func (h *actionHarness) lane(
	t *testing.T, parent *store.Task, state store.TaskState,
) *store.Task {
	t.Helper()
	index := 0
	child := &store.Task{
		ProjectID: h.projectID, Title: "lane of " + parent.Title,
		WorkflowName: "test", WorkflowSnapshot: actionSnapshot,
		BaseBranch:   parent.BranchName,
		State:        state,
		ParentTaskID: &parent.ID, ParentStepIndex: &index,
	}
	resolve := func(id int64) (string, error) { return worktree.BranchName(id, child.Title), nil }
	if err := h.store.CreateTask(t.Context(), child, resolve); err != nil {
		t.Fatalf("create lane: %v", err)
	}
	return child
}

// stateEvents counts the `task.state_changed` events written for one task,
// which is how a test asserts a row was never touched: a transition cannot
// happen without one (§13.3).
func (h *actionHarness) stateEvents(t *testing.T, id int64) int {
	t.Helper()
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{
		Types: []string{store.EventTaskStateChanged}, TaskID: id,
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return len(events)
}

// TestRetryFromParkedParentCascadesToLanes is task 088's whole point: the one
// action a parked parent used to accept was `cancel`, which ends the work.
// `retry` now re-admits every blocked lane holding the join open, in one call.
func TestRetryFromParkedParentCascadesToLanes(t *testing.T) {
	h := newActionHarness(t)
	parent := h.task(t, store.TaskAwaitingChildren)
	first := h.lane(t, parent, store.TaskBlocked)
	second := h.lane(t, parent, store.TaskBlocked)

	got, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if n != 2 {
		t.Errorf("re-admitted %d lanes, want 2", n)
	}
	if got.State != store.TaskAwaitingChildren {
		t.Errorf("parent state = %s, want awaiting_children — the join is still open", got.State)
	}
	for _, lane := range []*store.Task{first, second} {
		if s := h.get(t, lane.ID).State; s != store.TaskQueued {
			t.Errorf("lane %d state = %s, want queued", lane.ID, s)
		}
	}

	// The parent's row is not written at all. A stamped retry_cursor_at would
	// hand the join a fresh §7.2 budget nobody asked for, and a
	// task.state_changed whose from and to are equal is a transition that did
	// not happen.
	after := h.get(t, parent.ID)
	if after.RetryCursorAt != nil {
		t.Errorf("retry_cursor_at = %v, want unset — nothing was retried on the parent", after.RetryCursorAt)
	}
	if n := h.stateEvents(t, parent.ID); n != 0 {
		t.Errorf("parent emitted %d state events, want 0", n)
	}
}

// TestRetryFromParkedParentReachesNestedLanes covers the depth the recursive
// CTE already walks: one retry at the root reaches a blocked lane two levels
// down, and the parked parent in between is left alone to converge on its own.
func TestRetryFromParkedParentReachesNestedLanes(t *testing.T) {
	h := newActionHarness(t)
	root := h.task(t, store.TaskAwaitingChildren)
	mid := h.lane(t, root, store.TaskAwaitingChildren)
	leaf := h.lane(t, mid, store.TaskBlocked)

	_, n, err := h.runner.Retry(t.Context(), root.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if n != 1 {
		t.Errorf("re-admitted %d lanes, want 1", n)
	}
	if s := h.get(t, leaf.ID).State; s != store.TaskQueued {
		t.Errorf("leaf state = %s, want queued — a nested lane is still a blocked descendant", s)
	}
	// The mid parent is not in rollup.Blocked and must not be touched: it
	// re-parks or reads its lane as "not yet" on its own.
	got := h.get(t, mid.ID)
	if got.State != store.TaskAwaitingChildren {
		t.Errorf("mid state = %s, want awaiting_children", got.State)
	}
	if got.RetryCursorAt != nil {
		t.Errorf("mid retry_cursor_at = %v, want unset", got.RetryCursorAt)
	}
	if n := h.stateEvents(t, mid.ID); n != 0 {
		t.Errorf("mid emitted %d state events, want 0", n)
	}
}

// TestRetryFromParkedParentLeavesUnblockedLanes pins the set the cascade
// walks. Only `blocked` descendants are re-admitted: a gate is a human's
// decision to make, a pause is a human's decision already made, and queued,
// running and done lanes are working or finished.
func TestRetryFromParkedParentLeavesUnblockedLanes(t *testing.T) {
	h := newActionHarness(t)
	parent := h.task(t, store.TaskAwaitingChildren)
	untouched := map[store.TaskState]*store.Task{}
	for _, state := range []store.TaskState{
		store.TaskAwaitingGate, store.TaskPaused, store.TaskRunning,
		store.TaskQueued, store.TaskDone,
	} {
		untouched[state] = h.lane(t, parent, state)
	}

	_, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if n != 0 {
		t.Errorf("re-admitted %d lanes, want 0 — none of them is blocked", n)
	}
	for want, lane := range untouched {
		got := h.get(t, lane.ID)
		if got.State != want {
			t.Errorf("lane in %s moved to %s", want, got.State)
		}
		if n := h.stateEvents(t, lane.ID); n != 0 {
			t.Errorf("lane in %s emitted %d state events, want 0", want, n)
		}
	}
}

// TestRetryCascadeSkipsALaneThatMoved covers the gap the rollup cannot close:
// the CTE reads the subtree once, and a lane can leave `blocked` before the
// walk reaches it. That is not a failure of the cascade — the rest of the tree
// is still re-admitted — and the lane that moved is not counted.
func TestRetryCascadeSkipsALaneThatMoved(t *testing.T) {
	h := newActionHarness(t)
	parent := h.task(t, store.TaskAwaitingChildren)
	first := h.lane(t, parent, store.TaskBlocked)
	second := h.lane(t, parent, store.TaskBlocked)

	// The walk is sorted ascending, so `first` is retried before `second` is
	// read. Its commit is the moment a concurrent human cancels the other.
	var once sync.Once
	h.store.SetEventHook(func(e *store.Event) {
		if e.Type != store.EventTaskStateChanged || e.TaskID == nil || *e.TaskID != first.ID {
			return
		}
		once.Do(func() {
			if _, _, err := h.store.TransitionTask(t.Context(), second.ID,
				store.TaskBlocked, store.TaskAborted, store.TaskChange{}); err != nil {
				t.Errorf("abort the second lane: %v", err)
			}
		})
	})
	t.Cleanup(func() { h.store.SetEventHook(nil) })

	_, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if n != 1 {
		t.Errorf("re-admitted %d lanes, want 1 — the second one had moved", n)
	}
	if s := h.get(t, first.ID).State; s != store.TaskQueued {
		t.Errorf("first lane state = %s, want queued", s)
	}
	if s := h.get(t, second.ID).State; s != store.TaskAborted {
		t.Errorf("second lane state = %s, want aborted — the cascade must not resurrect it", s)
	}
}

// TestRetryFromBlockedStillCascades keeps the `blocked` path whole: the
// parent's own retry happens exactly as it did, and the lanes come with it.
// The reachable case is a parent blocked for its own reason — an eager or DAG
// fan-out on `merge_conflict` — with a separately blocked lane under it.
func TestRetryFromBlockedStillCascades(t *testing.T) {
	h := newActionHarness(t)
	parent := h.task(t, store.TaskBlocked)
	lane := h.lane(t, parent, store.TaskBlocked)
	before := time.Now()

	got, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got.State != store.TaskQueued {
		t.Errorf("parent state = %s, want queued", got.State)
	}
	if got.RetryCursorAt == nil || got.RetryCursorAt.Before(before.Add(-time.Second)) {
		t.Errorf("retry_cursor_at = %v, want stamped by this retry", got.RetryCursorAt)
	}
	if n != 1 {
		t.Errorf("re-admitted %d lanes, want 1", n)
	}
	if s := h.get(t, lane.ID).State; s != store.TaskQueued {
		t.Errorf("lane state = %s, want queued", s)
	}
}

// TestRetryFromParkedParentRefusesAnOverride: a parked parent's cursor is on
// its `fan_out` step, which carries neither a prompt nor a command. There is
// nothing for an override to rewrite, and rewriting the lanes' steps from here
// is not what `edit + retry` means.
func TestRetryFromParkedParentRefusesAnOverride(t *testing.T) {
	h := newActionHarness(t)
	parent := h.task(t, store.TaskAwaitingChildren)
	lane := h.lane(t, parent, store.TaskBlocked)

	_, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{Prompt: "try harder"})
	e, ok := AsParkedOverride(err)
	if !ok {
		t.Fatalf("err = %v, want *ParkedOverrideError", err)
	}
	if e.TaskID != parent.ID {
		t.Errorf("reported task %d, want %d", e.TaskID, parent.ID)
	}
	if n != 0 {
		t.Errorf("re-admitted %d lanes, want 0 — the call was refused", n)
	}
	if s := h.get(t, lane.ID).State; s != store.TaskBlocked {
		t.Errorf("lane state = %s, want blocked — a refused retry cascades nothing", s)
	}
}

// TestRetryCascadeCountsANestedSubtreeOnce: a blocked lane that had itself
// fanned out and blocked again re-admits its own subtree through its own
// Retry, and the outer walk then finds that grandchild already queued. The
// count is the size of the set re-admitted, not of the set walked.
func TestRetryCascadeCountsANestedSubtreeOnce(t *testing.T) {
	h := newActionHarness(t)
	parent := h.task(t, store.TaskAwaitingChildren)
	mid := h.lane(t, parent, store.TaskBlocked)
	leaf := h.lane(t, mid, store.TaskBlocked)

	_, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if n != 2 {
		t.Errorf("re-admitted %d lanes, want 2 — the leaf is counted once, by mid's own retry", n)
	}
	for _, task := range []*store.Task{mid, leaf} {
		if s := h.get(t, task.ID).State; s != store.TaskQueued {
			t.Errorf("task %d state = %s, want queued", task.ID, s)
		}
	}
}
