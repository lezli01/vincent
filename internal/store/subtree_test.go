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

// TestEmitChildrenChangedReachesEveryAncestor is decision 14: the per-task
// SSE stream filters on task_id alone, so a root's stream would never see a
// depth-2 transition unless the event is emitted on the root too.
func TestEmitChildrenChangedReachesEveryAncestor(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	mid := lane(t, s, p.ID, root.ID, "mid", 0, TaskAwaitingChildren)
	deep := lane(t, s, p.ID, mid.ID, "deep", 0, TaskRunning)

	if err := s.EmitChildrenChanged(ctx, deep.ID, TaskDone); err != nil {
		t.Fatalf("EmitChildrenChanged: %v", err)
	}
	events, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskChildrenChanged}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	got := map[int64]bool{}
	for _, e := range events {
		if e.TaskID != nil {
			got[*e.TaskID] = true
		}
	}
	if !got[mid.ID] || !got[root.ID] {
		t.Errorf("children_changed reached %v, want both %d and %d", got, mid.ID, root.ID)
	}
	if len(events) != 2 {
		t.Errorf("emitted %d events, want one per ancestor", len(events))
	}

	// A task with no ancestors writes nothing: every ordinary task takes
	// this path, and it must cost one indexed lookup and no rows.
	before := len(events)
	if err := s.EmitChildrenChanged(ctx, root.ID, TaskDone); err != nil {
		t.Fatalf("EmitChildrenChanged(root): %v", err)
	}
	after, _ := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskChildrenChanged}})
	if len(after) != before {
		t.Errorf("a root emitted %d children_changed events, want none", len(after)-before)
	}
}

// TestSettledChildrenCountsDirectLanesOnly: the eager wake watermark is
// compared against this number, so a depth-2 descendant settling must not move
// it (task 081 decision 1). That is what bounds an eager step's wake churn by
// its own lane count rather than by the size of the tree below it.
func TestSettledChildrenCountsDirectLanesOnly(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskRunning)
	if err := s.CreateTask(t.Context(), root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	a := lane(t, s, p.ID, root.ID, "a", 0, TaskDone)
	b := lane(t, s, p.ID, root.ID, "b", 1, TaskRunning)
	// A grandchild, settled: counted by ChildrenOf, not by SettledChildren.
	lane(t, s, p.ID, a.ID, "a1", 0, TaskAborted)

	got, err := s.SettledChildren(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("SettledChildren: %v", err)
	}
	if got != 1 {
		t.Errorf("SettledChildren = %d, want 1 (the one settled direct lane)", got)
	}
	rollup, err := s.ChildrenOf(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if rollup.Settled != 2 {
		t.Errorf("rollup counts %d settled over the subtree, want 2", rollup.Settled)
	}

	// A task with no children at all: zero, not an error — a fan_out that
	// spawned nothing has nothing to wait for.
	leaf, err := s.SettledChildren(t.Context(), b.ID)
	if err != nil || leaf != 0 {
		t.Errorf("SettledChildren of a childless task = %d, %v; want 0, nil", leaf, err)
	}
}

// TestWatermarkClearsLeavingAwaitingChildren: the watermark describes *this*
// parked period, so leaving `awaiting_children` always drops it — the same
// construction admit_not_before uses. Without it a barrier park could inherit
// an earlier eager step's number and be woken by nothing.
func TestWatermarkClearsLeavingAwaitingChildren(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskRunning)
	if err := s.CreateTask(t.Context(), root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Zero is a real position — an eager parent parked before any lane
	// settled — so it must survive the write rather than read as "unset".
	zero := 0
	parked, _, err := s.TransitionTask(t.Context(), root.ID, TaskRunning, TaskAwaitingChildren,
		TaskChange{SettledChildrenWatermark: &zero})
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if parked.SettledChildrenWatermark == nil || *parked.SettledChildrenWatermark != 0 {
		t.Fatalf("watermark = %v, want 0", parked.SettledChildrenWatermark)
	}
	reloaded, err := s.GetTask(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if reloaded.SettledChildrenWatermark == nil || *reloaded.SettledChildrenWatermark != 0 {
		t.Fatalf("watermark did not round-trip: %v", reloaded.SettledChildrenWatermark)
	}

	resumed, _, err := s.TransitionTask(t.Context(), root.ID, TaskAwaitingChildren, TaskQueued,
		TaskChange{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.SettledChildrenWatermark != nil {
		t.Errorf("watermark survived the resume: %d", *resumed.SettledChildrenWatermark)
	}
	// A barrier park after it writes nothing, and reads back as NULL.
	barrier, _, err := s.TransitionTask(t.Context(), root.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	barrier, _, err = s.TransitionTask(t.Context(), barrier.ID, TaskRunning, TaskAwaitingChildren,
		TaskChange{})
	if err != nil {
		t.Fatalf("barrier park: %v", err)
	}
	if barrier.SettledChildrenWatermark != nil {
		t.Errorf("a barrier park carries a watermark: %d", *barrier.SettledChildrenWatermark)
	}
}

// spend writes one finished step run on taskID reporting cost, or reporting no
// cost at all when cost is nil — the row a codex- or cursor-shaped attempt
// leaves (§9). Attempts are numbered by the caller; step_runs has no unique
// key on them, so a test may seed several on one step.
func spend(t *testing.T, s *Store, taskID int64, attempt int, cost *float64) {
	t.Helper()
	run := &StepRun{
		TaskID: taskID, StepIndex: 0, StepID: "work", StepType: "agent",
		Attempt: attempt, State: StepSucceeded, CostUSD: cost,
	}
	if err := s.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun(task %d): %v", taskID, err)
	}
}

// TestTreeCostIsTheSameFromEveryNode is task 115's shared budget: the tree cap
// is compared against one figure however deep the task asking sits, so the
// root, a lane and a grandchild must all climb to the same root and sum the
// same subtree — a lane's sibling and its sibling's children included, which a
// walk up the ancestor chain alone would miss. An archived lane still counts:
// archiving a lane does not refund what it spent. Every cost is a dyadic
// fraction so the sums compare exactly.
func TestTreeCostIsTheSameFromEveryNode(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	spend(t, s, root.ID, 1, float64Ptr(0.5))
	mid := lane(t, s, p.ID, root.ID, "mid", 0, TaskAwaitingChildren)
	spend(t, s, mid.ID, 1, float64Ptr(0.25))
	sibling := lane(t, s, p.ID, root.ID, "sibling", 1, TaskRunning)
	// Two attempts: a retry spends again (§17).
	spend(t, s, sibling.ID, 1, float64Ptr(1))
	spend(t, s, sibling.ID, 2, float64Ptr(0.125))
	archived := lane(t, s, p.ID, root.ID, "archived", 2, TaskArchived)
	spend(t, s, archived.ID, 1, float64Ptr(2))
	grandchild := lane(t, s, p.ID, mid.ID, "deep", 0, TaskRunning)
	spend(t, s, grandchild.ID, 1, float64Ptr(4))
	// A row that reported nothing adds nothing, and does not hide the rest.
	spend(t, s, grandchild.ID, 2, nil)

	const want = 0.5 + 0.25 + 1 + 0.125 + 2 + 4
	for name, id := range map[string]int64{
		"root": root.ID, "lane": mid.ID, "sibling": sibling.ID,
		"archived lane": archived.ID, "grandchild": grandchild.ID,
	} {
		gotRoot, cost, err := s.TreeCost(ctx, id)
		if err != nil {
			t.Fatalf("TreeCost(%s): %v", name, err)
		}
		if gotRoot != root.ID {
			t.Errorf("TreeCost(%s) root = %d, want %d", name, gotRoot, root.ID)
		}
		if !cost.HasCost || cost.CostUSD != want {
			t.Errorf("TreeCost(%s) = %+v, want %v with HasCost", name, cost, want)
		}
	}
}

// TestTreeCostKeepsTreesApart: the budget is per tree, so a second root's spend
// in the same project must not reach the first tree's figure, nor the other
// way round. A task on its own is a tree of one and is its own root.
func TestTreeCostKeepsTreesApart(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	first := newTask(p.ID, "first", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, first, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	firstLane := lane(t, s, p.ID, first.ID, "first-a", 0, TaskRunning)
	spend(t, s, firstLane.ID, 1, float64Ptr(1.5))

	second := newTask(p.ID, "second", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, second, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	spend(t, s, second.ID, 1, float64Ptr(0.25))
	secondLane := lane(t, s, p.ID, second.ID, "second-a", 0, TaskRunning)
	spend(t, s, secondLane.ID, 1, float64Ptr(8))

	alone := newTask(p.ID, "alone", TaskRunning)
	if err := s.CreateTask(ctx, alone, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	spend(t, s, alone.ID, 1, float64Ptr(3))

	for _, c := range []struct {
		name     string
		id, root int64
		want     float64
	}{
		{"first tree's lane", firstLane.ID, first.ID, 1.5},
		{"second tree's lane", secondLane.ID, second.ID, 8.25},
		{"a task with no parent and no lanes", alone.ID, alone.ID, 3},
	} {
		gotRoot, cost, err := s.TreeCost(ctx, c.id)
		if err != nil {
			t.Fatalf("TreeCost(%s): %v", c.name, err)
		}
		if gotRoot != c.root || !cost.HasCost || cost.CostUSD != c.want {
			t.Errorf("TreeCost(%s) = root %d, %+v; want root %d, %v with HasCost",
				c.name, gotRoot, cost, c.root, c.want)
		}
	}
}

// TestTreeCostUnreportedIsNotZero is §17's rule for the tree: a tree whose
// every step run left cost NULL — codex and cursor report none — and a tree
// that has run nothing yet both report HasCost false, so the cap stays inert
// on them instead of reading a confident $0.00.
func TestTreeCostUnreportedIsNotZero(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	unreported := newTask(p.ID, "unreported", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, unreported, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	spend(t, s, unreported.ID, 1, nil)
	codexLane := lane(t, s, p.ID, unreported.ID, "codex", 0, TaskDone)
	spend(t, s, codexLane.ID, 1, nil)
	spend(t, s, codexLane.ID, 2, nil)

	fresh := newTask(p.ID, "fresh", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, fresh, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	freshLane := lane(t, s, p.ID, fresh.ID, "queued", 0, TaskQueued)

	for name, c := range map[string]struct{ id, root int64 }{
		"every row NULL": {codexLane.ID, unreported.ID},
		"no step runs":   {freshLane.ID, fresh.ID},
	} {
		gotRoot, cost, err := s.TreeCost(ctx, c.id)
		if err != nil {
			t.Fatalf("TreeCost(%s): %v", name, err)
		}
		if gotRoot != c.root {
			t.Errorf("TreeCost(%s) root = %d, want %d", name, gotRoot, c.root)
		}
		if cost.HasCost || cost.CostUSD != 0 {
			t.Errorf("TreeCost(%s) = %+v, want no cost reported", name, cost)
		}
		desc, err := s.DescendantsCost(ctx, c.root)
		if err != nil {
			t.Fatalf("DescendantsCost(%s): %v", name, err)
		}
		if desc.HasCost {
			t.Errorf("DescendantsCost(%s) = %+v, want no cost reported", name, desc)
		}
	}

	// A task that does not exist has no root and no cost, rather than an
	// error: the row may have been deleted between a read and this one.
	gotRoot, cost, err := s.TreeCost(ctx, 99999)
	if err != nil || gotRoot != 0 || cost.HasCost {
		t.Errorf("TreeCost(missing) = %d, %+v, %v; want 0, no cost, nil", gotRoot, cost, err)
	}
}

// TestDescendantsCostExcludesTheTaskItself is §13.2's `children.cost_usd`
// (task 115 decision 5): the descendants at every depth, archived ones
// included, and never the task's own step runs — the tree total is that plus
// the task's own `cost_usd`, so counting it here would count it twice. A
// parent whose lanes reported nothing has no children cost even when the
// parent itself spent.
func TestDescendantsCostExcludesTheTaskItself(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	root := newTask(p.ID, "root", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, root, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	spend(t, s, root.ID, 1, float64Ptr(16))
	mid := lane(t, s, p.ID, root.ID, "mid", 0, TaskAwaitingChildren)
	spend(t, s, mid.ID, 1, float64Ptr(0.5))
	archived := lane(t, s, p.ID, root.ID, "archived", 1, TaskArchived)
	spend(t, s, archived.ID, 1, float64Ptr(0.25))
	deep := lane(t, s, p.ID, mid.ID, "deep", 0, TaskRunning)
	spend(t, s, deep.ID, 1, float64Ptr(2))

	for _, c := range []struct {
		name    string
		id      int64
		want    float64
		hasCost bool
	}{
		{"root", root.ID, 0.5 + 0.25 + 2, true},
		// From the middle of the tree: only what is below it, not its
		// siblings and not the root above it.
		{"mid", mid.ID, 2, true},
		// A leaf has no descendants, however much it spent itself.
		{"leaf", deep.ID, 0, false},
	} {
		got, err := s.DescendantsCost(ctx, c.id)
		if err != nil {
			t.Fatalf("DescendantsCost(%s): %v", c.name, err)
		}
		if got.HasCost != c.hasCost || got.CostUSD != c.want {
			t.Errorf("DescendantsCost(%s) = %+v, want %v (HasCost %v)", c.name, got, c.want, c.hasCost)
		}
	}

	// The parent spent, its lanes reported nothing: unreported, not $0.00.
	solo := newTask(p.ID, "solo", TaskAwaitingChildren)
	if err := s.CreateTask(ctx, solo, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	spend(t, s, solo.ID, 1, float64Ptr(1))
	quiet := lane(t, s, p.ID, solo.ID, "cursor", 0, TaskDone)
	spend(t, s, quiet.ID, 1, nil)
	if got, err := s.DescendantsCost(ctx, solo.ID); err != nil || got.HasCost {
		t.Errorf("DescendantsCost(solo) = %+v, %v; want no cost reported", got, err)
	}
}
