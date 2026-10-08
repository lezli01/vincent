package store

import "testing"

// TestIssueRowCountsSideAndMergeBackTasks is task 134.16 decision 1: the
// issue row counts its live side tasks and its pending merge-backs in its
// own query — unsettled and unarchived only — and the list and the detail
// agree.
func TestIssueRowCountsSideAndMergeBackTasks(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	main := newMainTask(p.ID, is.ID, "main")
	mustCreate(t, s, main)

	counts := func(wantSide, wantMerge int) {
		t.Helper()
		iss, err := s.GetIssue(ctx, is.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		listed, err := s.ListIssues(ctx, IssueFilter{IDs: []int64{is.ID}})
		if err != nil || len(listed) != 1 {
			t.Fatalf("ListIssues = %v, %v", listed, err)
		}
		for name, got := range map[string]*Issue{"detail": iss, "list": listed[0]} {
			if got.SideActive != wantSide || got.MergeBacksPending != wantMerge {
				t.Errorf("%s: side_active %d, merge_backs_pending %d; want %d, %d",
					name, got.SideActive, got.MergeBacksPending, wantSide, wantMerge)
			}
		}
	}
	newSide := func(title string) *Task {
		side := newTask(p.ID, title, TaskQueued)
		side.IssueID, side.IssueWorktree, side.MergeOnConflict = &is.ID, IssueWorktreeSide, MergeOnConflictBlock
		mustCreate(t, s, side)
		return side
	}

	counts(0, 0)
	side := newSide("side")
	other := newSide("other")
	counts(2, 0)

	// The side task finishing inserts one queued merge-back: one side task
	// left live, one merge pending.
	moveTask(t, s, side, TaskRunning)
	mb := &Task{Title: "Merge", WorkflowName: "__merge_back", WorkflowSnapshot: "name: x"}
	if _, _, err := s.TransitionTask(ctx, side.ID, TaskRunning, TaskDone, TaskChange{MergeBack: mb}); err != nil {
		t.Fatalf("transition side → done: %v", err)
	}
	counts(1, 1)

	// Aborting the other side task and settling the merge-back leave nothing.
	moveTask(t, s, other, TaskAborted)
	all, err := s.ListTasks(ctx, TaskFilter{ProjectID: p.ID})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	for i := range all {
		if all[i].MergeSourceTaskID != nil {
			moveTask(t, s, &all[i], TaskRunning, TaskDone)
		}
	}
	counts(0, 0)
}
