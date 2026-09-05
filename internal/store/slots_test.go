package store

import (
	"testing"
)

// TestSlotCountsCountsLanesAndAwaitingInput is §11's definition of a slot as
// an assertion, and the regression guard for issue #324: a slot is
// taskstate.HoldsSlot over *every* task row, not `running` over root tasks.
//
// The parked parent is the load-bearing case. `awaiting_children` holds no
// slot — the parent releases it before its lanes need one, which is what
// makes §7.6's deadlock-freedom argument hold — so a fix that made the header
// honest by counting parents too would break fan-out. It must count for
// nothing here.
func TestSlotCountsCountsLanesAndAwaitingInput(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")

	mk := func(pid int64, title string, state TaskState, parent *int64) *Task {
		task := newTask(pid, title, state)
		if parent != nil {
			task.ParentTaskID = parent
			task.ParentStepIndex = intPtr(0)
			task.LaneID = title
		}
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask(%s): %v", title, err)
		}
		return task
	}

	parent := mk(p1.ID, "parent", TaskAwaitingChildren, nil)
	mk(p1.ID, "lane-running", TaskRunning, &parent.ID)
	mk(p1.ID, "lane-done", TaskDone, &parent.ID)
	mk(p1.ID, "root-asking", TaskAwaitingInput, nil)
	mk(p2.ID, "root-running", TaskRunning, nil)
	mk(p2.ID, "root-queued", TaskQueued, nil)

	got, err := s.SlotCounts(ctx)
	if err != nil {
		t.Fatalf("SlotCounts: %v", err)
	}
	want := SlotCounts{Total: 3, Lanes: 1, AwaitingInput: 1}
	if got != want {
		t.Errorf("SlotCounts = %+v, want %+v (the awaiting_children parent holds no slot)", got, want)
	}
	// The reporting query and the admission counter must never disagree
	// about what a slot is; both read slotStates, and this proves it.
	if n, err := s.CountSlotHolders(ctx); err != nil || n != want.Total {
		t.Errorf("CountSlotHolders = %d, %v; want %d, nil", n, err, want.Total)
	}

	byProject, err := s.SlotCountsByProject(ctx)
	if err != nil {
		t.Fatalf("SlotCountsByProject: %v", err)
	}
	if byProject[p1.ID] != 2 {
		t.Errorf("slots[p1] = %d, want 2 (a running lane and a root awaiting input)", byProject[p1.ID])
	}
	if byProject[p2.ID] != 1 {
		t.Errorf("slots[p2] = %d, want 1 (queued holds nothing)", byProject[p2.ID])
	}
}

// TestSlotCountsOnAnEmptyTable is the COALESCE: SUM over zero rows is NULL in
// SQLite, and a fresh install must report zeros rather than a scan error.
func TestSlotCountsOnAnEmptyTable(t *testing.T) {
	s := openTest(t)
	got, err := s.SlotCounts(t.Context())
	if err != nil {
		t.Fatalf("SlotCounts: %v", err)
	}
	if (got != SlotCounts{}) {
		t.Errorf("SlotCounts = %+v, want zeros", got)
	}
	byProject, err := s.SlotCountsByProject(t.Context())
	if err != nil {
		t.Fatalf("SlotCountsByProject: %v", err)
	}
	if len(byProject) != 0 {
		t.Errorf("SlotCountsByProject = %v, want empty", byProject)
	}
}
