package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// TestBoardDistinguishesAHeldQueueFromAnOrdinaryOne drives the whole path —
// store columns, API DTO, client, renderer — through the real handlers. The
// point of §11's hold is unrenderable otherwise: a task waiting on a quota
// window and a task waiting for a slot are both `queued`, and the board has
// to say which.
func TestBoardDistinguishesAHeldQueueFromAnOrdinaryOne(t *testing.T) {
	h := newBoardLiveHarness(t)
	ctx := context.Background()
	h.createTask(t, "waiting for a slot")
	held := h.createTask(t, "waiting on a quota")

	h.p.until(20*time.Second, "both tasks to appear", func() bool {
		got := content(h.m)
		return strings.Contains(got, "waiting for a slot") && strings.Contains(got, "waiting on a quota")
	})

	// The engine's re-queue: running → queued carrying the hold.
	if _, _, err := h.st.TransitionTask(
		ctx, held.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{},
	); err != nil {
		t.Fatalf("TransitionTask(running): %v", err)
	}
	until := time.Now().Add(90 * time.Minute)
	reason := "usage_limit"
	if _, _, err := h.st.TransitionTask(ctx, held.ID, store.TaskRunning, store.TaskQueued,
		store.TaskChange{AdmitNotBefore: &until, QueuedReason: &reason}); err != nil {
		t.Fatalf("TransitionTask(held): %v", err)
	}

	stamp := until.Local().Format("15:04")
	h.p.until(20*time.Second, "the resume time to render on the held row", func() bool {
		return strings.Contains(content(h.m), "queued → "+stamp)
	})

	// The ordinary queue is untouched: exactly one row carries the marker.
	if got := strings.Count(content(h.m), "queued → "); got != 1 {
		t.Errorf("%d rows render a resume time, want 1 — an unheld task must read as a bare queued", got)
	}

	// The detail header has the width the board cell does not, so it names
	// the reason as well as the time.
	_, cmd := h.m.Update(selectTaskMsg{id: held.ID})
	h.p.push(cmd)
	h.p.until(20*time.Second, "the detail header to name the hold", func() bool {
		return strings.Contains(content(h.m), "queued · usage limit → "+stamp)
	})
}
