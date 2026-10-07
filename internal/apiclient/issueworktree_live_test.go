package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestIssueWorktreeOverTheWire pins the task 134.10 client fields against the
// real handlers: CreateTaskRequest.MergeBack, Task.IssueWorktree and
// Task.MergeBack, TaskDetail.MainWorktreeOccupantTaskID, and
// Issue.MainWorktree on both the detail and the list.
func TestIssueWorktreeOverTheWire(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: "Share a branch"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	main, err := h.client.CreateTask(ctx, apiclient.CreateTaskRequest{ProjectID: h.projectID, IssueID: &iss.ID})
	if err != nil {
		t.Fatalf("CreateTask main: %v", err)
	}
	if main.IssueWorktree == nil || *main.IssueWorktree != "main" || main.MergeBack != nil {
		t.Fatalf("main task = %v / %+v", main.IssueWorktree, main.MergeBack)
	}
	if _, _, err := h.store.TransitionTask(ctx, main.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}

	next, err := h.client.CreateTask(ctx, apiclient.CreateTaskRequest{ProjectID: h.projectID, IssueID: &iss.ID, Title: "next"})
	if err != nil {
		t.Fatalf("CreateTask next: %v", err)
	}
	if next.MainWorktreeOccupantTaskID == nil || *next.MainWorktreeOccupantTaskID != main.ID {
		t.Errorf("hint = %v, want task %d", next.MainWorktreeOccupantTaskID, main.ID)
	}

	side, err := h.client.CreateTask(ctx, apiclient.CreateTaskRequest{
		ProjectID: h.projectID, IssueID: &iss.ID, Title: "side",
		MergeBack: &apiclient.MergeBack{OnConflict: "agent"},
	})
	if err != nil {
		t.Fatalf("CreateTask side: %v", err)
	}
	if side.IssueWorktree == nil || *side.IssueWorktree != "side" || side.MergeBack == nil || side.MergeBack.OnConflict != "agent" {
		t.Errorf("side task = %v / %+v", side.IssueWorktree, side.MergeBack)
	}

	got, err := h.client.GetIssue(ctx, iss.ID, "")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if mw := got.MainWorktree; mw == nil || mw.Branch != main.BranchName || mw.OccupantTaskID == nil || *mw.OccupantTaskID != main.ID {
		t.Errorf("Issue.MainWorktree = %+v, want %q held by %d", got.MainWorktree, main.BranchName, main.ID)
	}
	rows, err := h.client.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: h.projectID})
	if err != nil || len(rows) != 1 || rows[0].MainWorktree == nil || rows[0].MainWorktree.Branch != main.BranchName {
		t.Errorf("ListIssues = %+v, %v", rows, err)
	}
}
