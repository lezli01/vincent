package api

import (
	"fmt"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// Issue #967: `vincent daemon restore` swaps in an older vincent.db while the
// token and the port survive (the token is deliberately not in a backup), so
// a client that stays open reconnects with a Last-Event-ID taken from the
// *replaced* database — a cursor ahead of every id the restored events table
// holds. §13.3 promises that a reconnecting client "misses nothing"; the
// stream must not swallow the restored daemon's new events as `<= lastID`
// until the AUTOINCREMENT sequence happens to climb past the stale cursor.
//
// The harness's events table stands in for the restored database: the cursor
// the client presents is far past its MAX(id), exactly as after a restore.

func TestEventsCursorAheadOfRestoredTableStillDeliversNewEvents(t *testing.T) {
	h := newSSEHarness(t)
	h.append(t, "task.state_changed")
	high, err := h.st.MaxEventID(t.Context())
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	stale := high + 1000 // the cursor the pre-restore daemon handed out

	c := openSSE(t, h.ts.URL+"/v1/events", fmt.Sprint(stale))
	live := h.append(t, "task.state_changed")
	if live.ID > stale {
		t.Fatalf("test setup: new event id %d already past the stale cursor %d", live.ID, stale)
	}
	if f := c.next(t); f.id != fmt.Sprint(live.ID) {
		t.Fatalf("first frame = %+v, want the restored daemon's new event %d "+
			"(a cursor ahead of MAX(id) must not swallow it)", f, live.ID)
	}
}

func TestTaskStreamCursorAheadOfRestoredTableStillDeliversNewEvents(t *testing.T) {
	h := newSSEHarness(t)
	high, err := h.st.MaxEventID(t.Context())
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	stale := high + 1000

	c := openSSE(t, fmt.Sprintf("%s/v1/tasks/%d/events", h.ts.URL, h.taskID), fmt.Sprint(stale))
	live := h.append(t, "task.state_changed")
	if f := c.next(t); f.id != fmt.Sprint(live.ID) {
		t.Fatalf("first frame = %+v, want the restored daemon's new event %d "+
			"(a cursor ahead of MAX(id) must not swallow it)", f, live.ID)
	}
}

// The chat stream goes through the same startStream and filters its live loop
// on the same `ev.ID <= lastID` (review F2), so it is pinned here too: a
// regression that stopped routing it through startStream, or derived its own
// lastID from the raw cursor, would swallow the restored daemon's chat events.
func TestChatStreamCursorAheadOfRestoredTableStillDeliversNewEvents(t *testing.T) {
	h := newSSEHarness(t)
	c := &store.Chat{
		ProjectID: h.projectID, Title: "restored", Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main",
	}
	if err := h.st.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	high, err := h.st.MaxEventID(t.Context())
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	stale := high + 1000

	sc := openSSE(t, fmt.Sprintf("%s/v1/chats/%d/events", h.ts.URL, c.ID), fmt.Sprint(stale))
	if _, err := h.st.SetChatState(t.Context(), c.ID, chatstate.Running); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	live, err := h.st.MaxEventID(t.Context())
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	if live > stale {
		t.Fatalf("test setup: new event id %d already past the stale cursor %d", live, stale)
	}
	if f := sc.next(t); f.id != fmt.Sprint(live) || f.event != store.EventChatState {
		t.Fatalf("first frame = %+v, want the restored daemon's %s event %d "+
			"(a cursor ahead of MAX(id) must not swallow it)", f, store.EventChatState, live)
	}
}
