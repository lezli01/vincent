package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
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
	// Admission cuts the main branch; a side task needs it in git (task
	// 134.13).
	testrepo.Run(t, h.repo, "branch", main.BranchName)

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
	if side.BaseBranch != main.BranchName {
		t.Errorf("side base = %q, want the main branch %q", side.BaseBranch, main.BranchName)
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

// TestIssueWorktreeRefusalsOverTheWire pins task 134.12's two 409s to their
// decoders against the real handlers: IssueWorktreeMoved names the task now
// holding the issue's main worktree, IssueHasLiveMainTask the main task that
// keeps the issue from being deleted.
func TestIssueWorktreeRefusalsOverTheWire(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: "Hand it on"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	pred, err := h.client.CreateTask(ctx, apiclient.CreateTaskRequest{ProjectID: h.projectID, IssueID: &iss.ID})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	succ, err := h.client.CreateTask(ctx, apiclient.CreateTaskRequest{ProjectID: h.projectID, IssueID: &iss.ID, Title: "next"})
	if err != nil {
		t.Fatalf("CreateTask next: %v", err)
	}

	err = h.client.DeleteIssue(ctx, iss.ID)
	if id, ok := apiclient.IssueHasLiveMainTask(err); !ok || id != pred.ID {
		t.Errorf("DeleteIssue = %v, want issue_has_live_main_task naming task %d (got %d, %v)", err, pred.ID, id, ok)
	}

	// The refusal is decided before anything touches the directory, so the
	// path only has to match the row.
	dir := t.TempDir()
	if err := h.store.SetTaskProgress(ctx, pred.ID, nil, &dir, nil); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	for _, step := range [][2]store.TaskState{{store.TaskQueued, store.TaskRunning}, {store.TaskRunning, store.TaskDone}} {
		if _, _, err := h.store.TransitionTask(ctx, pred.ID, step[0], step[1], store.TaskChange{}); err != nil {
			t.Fatalf("set %s: %v", step[1], err)
		}
	}
	if err := h.store.TransferIssueWorktree(ctx, pred.ID, succ.ID, dir, "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatalf("TransferIssueWorktree: %v", err)
	}
	_, _, err = h.client.FollowUp(ctx, pred.ID, apiclient.FollowUpInput{Prompt: "more"})
	if id, ok := apiclient.IssueWorktreeMoved(err); !ok || id != succ.ID {
		t.Errorf("FollowUp = %v, want issue_worktree_moved naming task %d (got %d, %v)", err, succ.ID, id, ok)
	}
}
