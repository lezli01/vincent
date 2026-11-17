package store

import (
	"slices"
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
