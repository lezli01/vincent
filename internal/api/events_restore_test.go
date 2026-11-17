package api

import (
	"context"
	"fmt"
	"net/http"
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

// Issue #1001, the half of #967 a high-water check cannot see: once the
// restored database's sequence has climbed past the stale cursor, the cursor
// looks legitimate. Every durable stream therefore names its event history's
// generation, and a cursor presented with another generation is ignored — no
// replay after it — exactly like one past MAX(id).
func TestEventsCursorFromAnotherGenerationIsIgnored(t *testing.T) {
	h := newSSEHarness(t)
	cursor := h.append(t, "task.state_changed")
	h.append(t, "task.state_changed") // what a replay after the cursor would send
	old, err := h.st.EventGeneration(t.Context())
	if err != nil {
		t.Fatalf("EventGeneration: %v", err)
	}
	gen, err := h.st.BumpEventGeneration(t.Context())
	if err != nil {
		t.Fatalf("BumpEventGeneration: %v", err)
	}

	c, served := openSSEGen(t, h.ts.URL+"/v1/events", cursor.ID, old)
	if served != fmt.Sprint(gen) {
		t.Fatalf("X-Event-Generation = %q, want %d", served, gen)
	}
	live := h.append(t, "task.state_changed")
	if f := c.next(t); f.id != fmt.Sprint(live.ID) {
		t.Fatalf("first frame = %+v, want the live event %d: a cursor from generation %d "+
			"must not be replayed after on generation %d", f, live.ID, old, gen)
	}
}

// The control: a cursor presented with the current generation resumes as
// before.
func TestEventsCursorFromTheSameGenerationReplays(t *testing.T) {
	h := newSSEHarness(t)
	cursor := h.append(t, "task.state_changed")
	next := h.append(t, "task.state_changed")
	gen, err := h.st.EventGeneration(t.Context())
	if err != nil {
		t.Fatalf("EventGeneration: %v", err)
	}
	c, _ := openSSEGen(t, h.ts.URL+"/v1/events", cursor.ID, gen)
	if f := c.next(t); f.id != fmt.Sprint(next.ID) {
		t.Fatalf("first frame = %+v, want the replayed event %d", f, next.ID)
	}
}

// openSSEGen is openSSE resuming from cursor under generation gen; it returns
// the generation the response named.
func openSSEGen(t *testing.T, url string, cursor, gen int64) (*sseClient, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", fmt.Sprint(cursor))
	req.Header.Set(eventGenerationHeader, fmt.Sprint(gen))
	resp, err := streamClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	c := &sseClient{resp: resp, frames: make(chan sseFrame, 256)}
	t.Cleanup(c.close)
	go c.pump()
	return c, resp.Header.Get(eventGenerationHeader)
}
