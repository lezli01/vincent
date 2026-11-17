package store

import (
	"database/sql"
	"runtime"
	"slices"
	"sync"
	"testing"
)

// TestEventHookPublishesInIDOrderAcrossFanOut is issue #963, cause (b).
//
// Every SSE stream dedupes its replay-to-live seam with `ev.ID <= lastID`
// (phase 2 decision, §13.3), which is only lossless if the event hook sees
// ids in ascending order. The daemon's hook appends a children_changed for
// each fan-out ancestor of a task.created from *inside* the hook, so when
// CreateTasks notifies a batch of lanes N..N+k one by one, the ancestor
// event N+k+1 reaches the hook before N+1 does — and every live stream then
// skips N+1..N+k, past a cursor a reconnect will never rewind.
//
// The hook here does what internal/daemon/daemon.go's does: record the
// event, then append children_changed for its ancestors.
func TestEventHookPublishesInIDOrderAcrossFanOut(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	var seen []int64
	s.SetEventHook(func(e *Event) {
		seen = append(seen, e.ID)
		if e.TaskID == nil || (e.Type != EventTaskCreated && e.Type != EventTaskStateChanged) {
			return
		}
		if err := s.EmitChildrenChanged(ctx, *e.TaskID, TaskQueued); err != nil {
			t.Errorf("EmitChildrenChanged: %v", err)
		}
	})

	// A fan-out round: three lanes committed in one CreateTasks.
	var lanes []*Task
	for i, id := range []string{"a", "b", "c"} {
		l := newTask(p.ID, "lane "+id, TaskQueued)
		l.BranchName = "vincent/lane-" + id
		l.ParentTaskID = &root.ID
		idx := 0
		l.ParentStepIndex = &idx
		l.LaneID = id
		l.LaneOrder = i
		lanes = append(lanes, l)
	}
	if err := s.CreateTasks(ctx, lanes, nil); err != nil {
		t.Fatalf("CreateTasks: %v", err)
	}

	// What a live SSE stream does with them: drop anything at or below the
	// last id it wrote.
	var lastID int64
	var delivered, dropped []int64
	for _, id := range seen {
		if id <= lastID {
			dropped = append(dropped, id)
			continue
		}
		delivered = append(delivered, id)
		lastID = id
	}
	if len(dropped) > 0 {
		t.Errorf("hook saw ids %v: a live stream deduping by id delivers %v and silently drops %v (§13.3: miss nothing)",
			seen, delivered, dropped)
	}

	if len(seen) == 0 {
		t.Fatal("the hook saw no events")
	}
	stored, err := s.ListEvents(ctx, EventFilter{AfterID: seen[0] - 1})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(stored) != len(seen) {
		t.Errorf("hook saw %d events, %d were committed", len(seen), len(stored))
	}
}

// TestEventEnqueuedAsADrainEndsIsDelivered is review F1: a drain that left
// its loop, released the queue and only then cleared `draining` let a write
// landing in that gap find a drain apparently in progress and return, so its
// event sat in the queue until some unrelated later write. On a quiet daemon
// that can be a task's last state change.
//
// The write is made from the drained seam, which runs exactly in that gap,
// so the test is deterministic rather than a race it might lose.
func TestEventEnqueuedAsADrainEndsIsDelivered(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	var seen []string
	s.SetEventHook(func(e *Event) { seen = append(seen, e.Type) })
	once := false
	s.pub.drained = func() {
		if once {
			return
		}
		once = true
		if err := s.AppendEvent(ctx, &Event{Type: "test.second"}); err != nil {
			t.Errorf("AppendEvent: %v", err)
		}
	}
	if err := s.AppendEvent(ctx, &Event{Type: "test.first"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if want := []string{"test.first", "test.second"}; !slices.Equal(seen, want) {
		t.Fatalf("hook saw %v once both writes returned, want %v: an event was left in the queue", seen, want)
	}
}

// TestEventHookSeesConcurrentWritersInIDOrder is issue #963, cause (a), and
// review F2/F3: writers on several goroutines — standalone AppendEvents and
// transactions that each commit two events, with a hook that appends from
// inside itself the way the daemon's children_changed relay does — must
// reach the hook in ascending id order, each committed event exactly once,
// all of them by the time every writer has returned.
//
// Before commitTx queued a transaction's events under the lock that spans
// its commit, a transaction could commit id N and another writer could
// commit and deliver N+1 before N was queued; a live stream then drops N.
// That window is a few instructions wide, so this is a stress test: it
// fails only sometimes against the old code. Run it under -race.
func TestEventHookSeesConcurrentWritersInIDOrder(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	var (
		mu   sync.Mutex
		seen []int64
	)
	s.SetEventHook(func(e *Event) {
		mu.Lock()
		seen = append(seen, e.ID)
		mu.Unlock()
		if e.Type == "test.tx" {
			if err := s.AppendEvent(ctx, &Event{Type: "test.relay"}); err != nil {
				t.Errorf("relay AppendEvent: %v", err)
			}
		}
	})

	const writers, rounds = 8, 200
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for range rounds {
				var err error
				if w%2 == 0 {
					err = s.AppendEvent(ctx, &Event{Type: "test.append"})
				} else {
					// What every transactional writer does: commit two
					// events, then notify them.
					a, b := &Event{Type: "test.tx"}, &Event{Type: "test.tx2"}
					err = s.withTx(ctx, func(tx *sql.Tx) error {
						if err := appendEventTx(ctx, tx, a); err != nil {
							return err
						}
						return appendEventTx(ctx, tx, b)
					})
					if err == nil {
						// Callers do work between the commit and their
						// notify; yielding here stands for it, and is what
						// let another writer deliver a higher id first.
						runtime.Gosched()
						s.notify(a, b)
					}
				}
				if err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
			}
		})
	}
	wg.Wait()

	stored, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	want := make([]int64, 0, len(stored))
	for _, e := range stored {
		want = append(want, e.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.IsSorted(seen) {
		for i := 1; i < len(seen); i++ {
			if seen[i] <= seen[i-1] {
				t.Errorf("hook saw id %d after %d: a live stream deduping by id drops it", seen[i], seen[i-1])
				break
			}
		}
	}
	if !slices.Equal(seen, want) {
		t.Errorf("hook saw %d events, %d were committed; every committed event must reach the hook exactly once",
			len(seen), len(want))
	}
}

// TestEventHookPanicLeavesThePublisherAlive is review F3: a hook that
// panics unwinds through drain, which must clear `draining` on the way out.
// Otherwise every later write would find a drain apparently in progress and
// nothing would ever reach the hook again. Events still queued behind the
// one that panicked are delivered by the next write, in id order.
func TestEventHookPanicLeavesThePublisherAlive(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	var seen []string
	s.SetEventHook(func(e *Event) {
		if e.Type == "test.boom" {
			panic("hook failed")
		}
		seen = append(seen, e.Type)
	})

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the hook's panic did not reach the writer")
			}
		}()
		// One transaction, so both events are queued before the first is
		// delivered: the second is left behind by the panic.
		boom, after := &Event{Type: "test.boom"}, &Event{Type: "test.queued"}
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			if err := appendEventTx(ctx, tx, boom); err != nil {
				return err
			}
			return appendEventTx(ctx, tx, after)
		})
		t.Errorf("withTx returned (%v) instead of propagating the hook's panic", err)
	}()

	if err := s.AppendEvent(ctx, &Event{Type: "test.later"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if want := []string{"test.queued", "test.later"}; !slices.Equal(seen, want) {
		t.Fatalf("hook saw %v after the panic, want %v", seen, want)
	}
}
