package tui

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/store"
)

// The board header's slot count, against the real handlers (§11, §15, issue
// #324). §11 defines a slot as a task in a slot-holding state — `running` or
// `awaiting_input`, `taskstate.HoldsSlot` — over every task in the database,
// lanes included, which is what both caps count (`store.CountSlotHolders`).
// The header is the one number on the board that answers "why is nothing
// starting", so it has to be that count and not a different one.

var headerRunningRE = regexp.MustCompile(`(\d+)/(\d+) running`)

// headerSlots reads the header's numerator and denominator back out of the
// rendered line, which is what a human reads.
func headerSlots(t *testing.T, b *board) (int, int) {
	t.Helper()
	line := ansi.Strip(b.headerLine())
	m := headerRunningRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("header %q has no `n/cap running` count", line)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("header numerator %q: %v", m[1], err)
	}
	limit, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatalf("header denominator %q: %v", m[2], err)
	}
	return n, limit
}

// transition commits a state change the way the daemon does, through the
// store, so the board learns about it over the event stream.
func (h *boardLiveHarness) transition(t *testing.T, task *store.Task, to store.TaskState) {
	t.Helper()
	if _, _, err := h.st.TransitionTask(
		context.Background(), task.ID, task.State, to, store.TaskChange{},
	); err != nil {
		t.Fatalf("TransitionTask(%d, %s → %s): %v", task.ID, task.State, to, err)
	}
	task.State = to
}

// boardHas reports whether the board's own list carries this task yet.
func (h *boardLiveHarness) boardHas(id int64) bool {
	b := h.m.views[viewHome].(*shell).board
	for _, task := range b.tasks {
		if task.ID == id {
			return true
		}
	}
	return false
}

// TestBoardHeaderCountsEverySlotHolder is issue #324's report: with two lanes
// running under a parked parent and one root task on a question, every slot
// the scheduler counts is taken, and the header has to say so.
//
// Both halves of §11's definition are exercised at once, because both are
// missing from the same expression (`countRunning`, boardrows.go):
//   - the lanes never reach the client — the board lists roots only (task 014
//     decision 13, §13.2), and lanes arrive on their own request and never
//     enter b.tasks, so no walk of that list can ever see one;
//   - `awaiting_input` holds a slot — its agent process is alive, idle on its
//     stdin (§7.4) — and it is not the literal state `running`.
//
// The expectation is `store.CountSlotHolders`, not a literal: the header's
// numerator has to be the number the scheduler would compute for the same
// task set, or it is not answering the question it is on the board to answer.
func TestBoardHeaderCountsEverySlotHolder(t *testing.T) {
	h := newBoardLiveHarness(t)
	ctx := context.Background()

	// A fan-out parent, parked on its lanes. It holds no slot itself, on
	// purpose (§7.6, TestAwaitingChildrenHoldsNoSlot) — that is not what this
	// test is about, and the reporter says so too.
	parent := h.createTask(t, "fan-out parent")
	h.p.until(20*time.Second, "the parent to appear on the board", func() bool {
		return h.boardHas(parent.ID)
	})
	h.transition(t, parent, store.TaskRunning)
	h.transition(t, parent, store.TaskAwaitingChildren)

	// Two lanes underneath it, both admitted. These are the slots the header
	// has no way to see.
	for i, id := range []string{"a", "b"} {
		lane := h.createLaneTask(t, parent.ID, id, i)
		h.transition(t, lane, store.TaskRunning)
	}

	// And a root task parked on a question: a live agent process, a held slot,
	// and a state that is not `running`.
	asking := h.createTask(t, "asking task")
	h.p.until(20*time.Second, "the asking task to appear on the board", func() bool {
		return h.boardHas(asking.ID)
	})
	h.transition(t, asking, store.TaskRunning)
	h.transition(t, asking, store.TaskAwaitingInput)

	b := h.m.views[viewHome].(*shell).board
	h.p.until(20*time.Second, "the board to see the question", func() bool {
		for _, task := range b.tasks {
			if task.ID == asking.ID && task.State == stateAwaitingInput {
				return true
			}
		}
		return false
	})

	want, err := h.st.CountSlotHolders(ctx)
	if err != nil {
		t.Fatalf("CountSlotHolders: %v", err)
	}
	if want != 3 {
		t.Fatalf("the fixture holds %d slots, want the 2 lanes + 1 awaiting_input", want)
	}

	got, capacity := headerSlots(t, b)
	if got != want {
		t.Errorf("the board header reports %d/%d running, want %d — "+
			"two lanes are running under a parked parent and one root task is "+
			"awaiting_input, and §11 counts all three against the cap",
			got, capacity, want)
	}
}
