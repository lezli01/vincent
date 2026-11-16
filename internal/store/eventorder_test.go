package store

import (
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
