package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// TestBulkArchiveLive is task 011's done-when, against the real handlers: two
// tasks are selected on the board, one `A` asks once, and one `y` archives
// both. Nothing here is stubbed — the transitions are the daemon's, so this is
// what proves the client-side fan-out is still an ordinary §6 action per task.
func TestBulkArchiveLive(t *testing.T) {
	h := newActionLiveHarness(t)
	ctx := context.Background()

	ids := make([]int64, 0, 2)
	for _, title := range []string{"finished-one", "finished-two"} {
		task := h.createParkedTask(t, title)
		if _, _, err := h.st.TransitionTask(ctx, task.ID,
			store.TaskQueued, store.TaskDone, store.TaskChange{}); err != nil {
			t.Fatalf("park %s at done: %v", title, err)
		}
		ids = append(ids, task.ID)
	}

	h.p.until(30*time.Second, "the board to show both finished tasks", func() bool {
		out := content(h.m)
		return strings.Contains(out, "finished-one") && strings.Contains(out, "finished-two")
	})

	// Select everything the board is showing, and check the offer before
	// acting on it: the key has to say how many tasks it moves.
	h.press(t, "V")
	h.p.until(10*time.Second, "the selection to be counted on screen", func() bool {
		return strings.Contains(content(h.m), "archive (2)")
	})

	h.press(t, "A")
	if !strings.Contains(content(h.m), "2 selected tasks") {
		t.Fatalf("A did not ask about the selection:\n%s", content(h.m))
	}
	h.press(t, "y")

	for _, id := range ids {
		h.p.until(60*time.Second, "both tasks to be archived", func() bool {
			return h.state(t, id) == store.TaskArchived
		})
	}
	// A finished batch hands the selection back empty: what the daemon
	// accepted is no longer waiting to be dealt with.
	h.p.until(30*time.Second, "the selection to empty as the archives land", func() bool {
		return !h.m.views[viewHome].(*shell).board.hasMarks()
	})
}
