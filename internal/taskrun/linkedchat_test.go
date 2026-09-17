package taskrun

import (
	"context"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// lockLane gives a lane a worktree and opens a linked chat on it.
func (h *actionHarness) lockLane(t *testing.T, lane *store.Task) *store.Chat {
	t.Helper()
	path := "/wt/lane"
	if err := h.store.SetTaskProgress(t.Context(), lane.ID, nil, &path, nil); err != nil {
		t.Fatalf("SetTaskProgress: %v", err)
	}
	c := &store.Chat{Title: "why", Agent: "claude", PermissionMode: "full_auto"}
	if err := h.store.OpenLinkedChat(t.Context(), lane.ID, lane.State, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	return c
}

// TestRetryCascadeSkipsALockedLane is task 119 decision 4: a parent's retry
// re-admits every unlocked blocked lane, leaves the one an open chat has
// locked blocked and untouched, and does not count it.
func TestRetryCascadeSkipsALockedLane(t *testing.T) {
	h := newActionHarness(t)
	parent := h.task(t, store.TaskAwaitingChildren)
	free := h.lane(t, parent, store.TaskBlocked)
	locked := h.lane(t, parent, store.TaskBlocked)
	h.lockLane(t, locked)

	_, n, err := h.runner.Retry(t.Context(), parent.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if n != 1 {
		t.Errorf("retried_descendants = %d, want 1 — the locked lane did not move", n)
	}
	if s := h.get(t, free.ID).State; s != store.TaskQueued {
		t.Errorf("free lane = %s, want queued", s)
	}
	if s := h.get(t, locked.ID).State; s != store.TaskBlocked {
		t.Errorf("locked lane = %s, want blocked", s)
	}
	if n := h.stateEvents(t, locked.ID); n != 0 {
		t.Errorf("locked lane emitted %d state events, want 0", n)
	}
}

// recordingStopper is a ChatTurnStopper that records what it was asked to stop.
type recordingStopper struct{ stopped []int64 }

func (r *recordingStopper) StopTurn(_ context.Context, chatID int64) {
	r.stopped = append(r.stopped, chatID)
}

// TestCancelCascadeClosesEveryLockedLane: the parent's cancel cascade is
// cancel, so each locked lane has its turn stopped, its chat closed and is
// aborted.
func TestCancelCascadeClosesEveryLockedLane(t *testing.T) {
	h := newActionHarness(t)
	stopper := &recordingStopper{}
	h.runner.deps.ChatTurns = stopper
	parent := h.task(t, store.TaskAwaitingChildren)
	locked := h.lane(t, parent, store.TaskBlocked)
	chat := h.lockLane(t, locked)

	if _, err := h.runner.Cancel(t.Context(), parent.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if s := h.get(t, locked.ID).State; s != store.TaskAborted {
		t.Errorf("locked lane = %s, want aborted", s)
	}
	c, err := h.store.GetChat(t.Context(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != chatstate.Closed {
		t.Errorf("lane's chat = %s, want closed", c.State)
	}
	if len(stopper.stopped) != 1 || stopper.stopped[0] != chat.ID {
		t.Errorf("stopped turns = %v, want [%d]", stopper.stopped, chat.ID)
	}
}
