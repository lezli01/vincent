package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// replayBacklog is the number of durable events seeded behind the resume
// cursor. It is well short of what a long-lived installation accumulates —
// §17 keeps event rows indefinitely — and is only large enough to make an
// unbounded replay's heap unmistakable next to a bounded one.
const replayBacklog = 50000

// maxReplayHeap is the live heap a Last-Event-ID replay may hold once the
// first byte of the response is on the wire. A replay that reads the backlog
// in fixed pages holds one page (a few hundred KiB at any sane page size);
// one that materializes the whole backlog holds ~360 B per event, so this
// budget is roughly three times a page-shaped implementation's worst case and
// a third of what replayBacklog events cost when materialized at once.
const maxReplayHeap = 6 << 20

// TestEventReplayHeapIsBounded pins §13.3's resume path to a memory budget
// that does not grow with the backlog behind the cursor. The measurement is
// taken as soon as the first response byte arrives, which is the moment that
// separates the two shapes: a paged replay has read one page by then, an
// unbounded one has already read and retained every matching row.
func TestEventReplayHeapIsBounded(t *testing.T) {
	h := newSSEHarness(t)
	seedEvents(t, h, replayBacklog)

	// The same request without a cursor replays nothing, so its heap is the
	// floor this endpoint costs — subtracting it keeps the budget about the
	// replay rather than about the server.
	live := heapAtFirstByte(t, h, "")
	replay := heapAtFirstByte(t, h, "1")

	if replay > live+maxReplayHeap {
		t.Fatalf("replay of %d backlog events held %.1f MiB above a live-only stream (%.1f MiB), want at most %.1f MiB: "+
			"the whole backlog is materialized before the first frame is written",
			replayBacklog, mib(replay-live), mib(live), mib(maxReplayHeap))
	}
}

// seedEvents appends n durable events for the harness task through the real
// store, so the rows and the id sequence are exactly what a resume queries.
func seedEvents(t *testing.T, h *sseHarness, n int) {
	t.Helper()
	// A state change's payload: ids, the new state, and the one-line summary
	// §13.3 allows on a transition into awaiting_input.
	payload := json.RawMessage(`{"from":"running","to":"awaiting_input","summary":"` +
		strings.Repeat("choose one of the offered branches ", 4) + `"}`)
	ctx := context.Background()
	for range n {
		e := &store.Event{
			Type:      "task.state_changed",
			TaskID:    &h.taskID,
			ProjectID: &h.projectID,
			Payload:   payload,
		}
		if err := h.st.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
}

// heapAtFirstByte opens one SSE stream over a raw connection that is never
// drained, and reports the process's live heap the moment the server's first
// byte arrives. Not reading is the point: the handler stays parked inside the
// response it is writing, so whatever the replay retains is still reachable
// when the sample is taken.
func heapAtFirstByte(t *testing.T, h *sseHarness, lastEventID string) uint64 {
	t.Helper()
	conn, err := net.Dial("tcp", h.ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Closing before the test's cleanup unblocks a parked handler: the write
	// fails, the handler returns, and httptest's Close does not wait on it.
	t.Cleanup(func() { _ = conn.Close() })

	req := "GET /v1/events HTTP/1.1\r\nHost: vincent\r\n" +
		"Authorization: Bearer " + testToken + "\r\nAccept: text/event-stream\r\n"
	if lastEventID != "" {
		req += "Last-Event-ID: " + lastEventID + "\r\n"
	}
	if _, err := fmt.Fprint(conn, req+"\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	// Peek rather than Read: one byte proves the response has started
	// without draining what the handler already wrote.
	if _, err := bufio.NewReaderSize(conn, 256).Peek(1); err != nil {
		t.Fatalf("first response byte (cursor %q): %v", lastEventID, err)
	}
	return liveHeap()
}

// liveHeap returns HeapAlloc after collecting twice — once to sweep the
// garbage the request made, once so the first collection's own leftovers do
// not count against the budget.
func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

func mib(b uint64) float64 { return float64(b) / (1 << 20) }
