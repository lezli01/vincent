package store

import (
	"reflect"
	"testing"
)

// lane inserts a child task of parent, in the given state.
func lane(t *testing.T, s *Store, projectID, parentID int64, laneID string, order int, state TaskState) *Task {
	t.Helper()
	task := newTask(projectID, "lane "+laneID, state)
	task.ParentTaskID = &parentID
	idx := 0
	task.ParentStepIndex = &idx
	task.LaneID = laneID
	task.LaneOrder = order
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask(%s): %v", laneID, err)
	}
	return task
}

// TestFanOutColumnsRoundTrip: the four columns are the entire difference
// between a lane and a hand-created task, so they had better survive a write
// and a read.
func TestFanOutColumnsRoundTrip(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskRunning)
	if err := s.CreateTask(t.Context(), root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	child := lane(t, s, p.ID, root.ID, "api", 2, TaskQueued)

	got, err := s.GetTask(t.Context(), child.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ParentTaskID == nil || *got.ParentTaskID != root.ID {
		t.Errorf("parent_task_id = %v, want %d", got.ParentTaskID, root.ID)
	}
	if got.ParentStepIndex == nil || *got.ParentStepIndex != 0 {
		t.Errorf("parent_step_index = %v, want 0", got.ParentStepIndex)
	}
	if got.LaneID != "api" || got.LaneOrder != 2 {
		t.Errorf("lane = %q/%d, want api/2", got.LaneID, got.LaneOrder)
	}

	// A root carries none of it, and must not come back with zero values
	// that look like lane 0 of step 0.
	gotRoot, err := s.GetTask(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("GetTask(root): %v", err)
	}
	if gotRoot.ParentTaskID != nil || gotRoot.ParentStepIndex != nil || gotRoot.LaneID != "" {
		t.Errorf("root carries lane columns: %+v", gotRoot)
	}
}

// TestListTasksExcludesLanesByDefault is decision 13: a list is the work
// someone asked for, and a 64-task tree would bury it.
func TestListTasksExcludesLanesByDefault(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	docs := lane(t, s, p.ID, root.ID, "docs", 1, TaskQueued)
	api := lane(t, s, p.ID, root.ID, "api", 0, TaskRunning)

	roots, err := s.ListTasks(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != root.ID {
		t.Fatalf("default list = %d tasks, want only the root", len(roots))
	}

	all, err := s.ListTasks(ctx, TaskFilter{Children: ChildrenInclude})
	if err != nil {
		t.Fatalf("ListTasks(include): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("include_children list = %d tasks, want 3", len(all))
	}

	// One parent's lanes come back in *merge* order, not newest-first:
	// that is the order the join will merge them in.
	lanes, err := s.ListTasks(ctx, TaskFilter{ParentID: root.ID})
	if err != nil {
		t.Fatalf("ListTasks(parent): %v", err)
	}
	gotIDs := []int64{}
	for _, l := range lanes {
		gotIDs = append(gotIDs, l.ID)
	}
	if want := []int64{api.ID, docs.ID}; !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("lanes = %v, want %v in lane_order", gotIDs, want)
	}
}

// TestChildrenOfWalksTheWholeSubtree: the rollup counts descendants at every
// depth, because a root whose lane fanned out again is still waiting on all
// of it.
func TestChildrenOfWalksTheWholeSubtree(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	mid := lane(t, s, p.ID, root.ID, "mid", 0, TaskAwaitingChildren)
	done := lane(t, s, p.ID, root.ID, "done", 1, TaskDone)
	deepBlocked := lane(t, s, p.ID, mid.ID, "deep", 0, TaskBlocked)

	got, err := s.ChildrenOf(ctx, root.ID)
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if got.Total != 3 {
		t.Errorf("Total = %d, want 3 (depth 2 included)", got.Total)
	}
	if got.ByState[TaskDone] != 1 || got.ByState[TaskBlocked] != 1 {
		t.Errorf("ByState = %v, want one done and one blocked", got.ByState)
	}
	if !reflect.DeepEqual(got.Blocked, []int64{deepBlocked.ID}) {
		t.Errorf("Blocked = %v, want the depth-2 lane %d", got.Blocked, deepBlocked.ID)
	}
	if got.Settled != 1 || got.Done() {
		t.Errorf("Settled = %d of %d, Done = %v; a blocked lane holds the join open",
			got.Settled, got.Total, got.Done())
	}
	if got.ByState[TaskDone] != 1 {
		t.Errorf("the done lane %d is missing from %v", done.ID, got.ByState)
	}

	// A task with no children is trivially done: a fan_out that spawned
	// nothing has nothing to wait for.
	leaf, err := s.ChildrenOf(ctx, deepBlocked.ID)
	if err != nil {
		t.Fatalf("ChildrenOf(leaf): %v", err)
	}
	if !leaf.Done() || leaf.Total != 0 {
		t.Errorf("leaf rollup = %+v, want an empty, done rollup", leaf)
	}
}

// TestFanOutAncestorsNearestFirst: children_changed is emitted on every
// ancestor, so the walk has to reach past the immediate parent.
func TestFanOutAncestorsNearestFirst(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	mid := lane(t, s, p.ID, root.ID, "mid", 0, TaskAwaitingChildren)
	deep := lane(t, s, p.ID, mid.ID, "deep", 0, TaskRunning)

	got, err := s.FanOutAncestors(ctx, deep.ID)
	if err != nil {
		t.Fatalf("FanOutAncestors: %v", err)
	}
	if want := []int64{mid.ID, root.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("ancestors = %v, want %v (nearest first)", got, want)
	}
	if got, err := s.FanOutAncestors(ctx, root.ID); err != nil || len(got) != 0 {
		t.Errorf("ancestors of a root = %v (err %v), want none", got, err)
	}
}

// TestNonTerminalDescendantsDeepestFirst: a cascading cancel must not cancel
// a parent before its own children.
func TestNonTerminalDescendantsDeepestFirst(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	mid := lane(t, s, p.ID, root.ID, "mid", 0, TaskAwaitingChildren)
	deep := lane(t, s, p.ID, mid.ID, "deep", 0, TaskRunning)
	lane(t, s, p.ID, root.ID, "finished", 1, TaskDone) // settled: not cancelled

	got, err := s.NonTerminalDescendants(ctx, root.ID)
	if err != nil {
		t.Fatalf("NonTerminalDescendants: %v", err)
	}
	if want := []int64{deep.ID, mid.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("descendants = %v, want %v (deepest first, settled excluded)", got, want)
	}
}
