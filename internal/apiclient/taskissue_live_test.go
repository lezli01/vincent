package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestTaskFromIssueOverTheWire pins the task 130.7 client fields against the
// real handlers: CreateTaskRequest.IssueID, Task.Issue, the IssueID list
// filter and GetIssue's prefill preview.
func TestTaskFromIssueOverTheWire(t *testing.T) {
	h := newCreateHarness(t)
	ctx := t.Context()
	iss, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "I_9", Repo: "octo/repo", Number: 9,
		URL: "https://github.com/octo/repo/issues/9", Title: "Wire it", Body: "body", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}

	preview, err := h.client.GetIssue(ctx, iss.ID, "two-step")
	if err != nil || preview.Prefill == nil {
		t.Fatalf("GetIssue preview = %+v, %v", preview.Prefill, err)
	}
	plain, err := h.client.GetIssue(ctx, iss.ID, "")
	if err != nil || plain.Prefill != nil {
		t.Errorf("GetIssue without a workflow = %+v, %v, want no prefill", plain.Prefill, err)
	}

	wf := "two-step"
	created, err := h.client.CreateTask(ctx, apiclient.CreateTaskRequest{
		ProjectID: h.projectID, Workflow: &wf, IssueID: &iss.ID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.Title != preview.Prefill.Title || created.Description != preview.Prefill.Description {
		t.Errorf("created %q / %q, want the previewed %q / %q",
			created.Title, created.Description, preview.Prefill.Title, preview.Prefill.Description)
	}
	if ti := created.Issue; ti == nil || ti.ID != iss.ID || ti.State != "open" || ti.Source == nil || ti.Source.Number != 9 {
		t.Errorf("Task.Issue = %+v", created.Issue)
	}
	if created.GitHubIssue == nil || created.GitHubIssue.Number != 9 {
		t.Errorf("derived GitHubIssue = %+v", created.GitHubIssue)
	}

	if _, err := h.client.CreateTask(ctx, apiclient.CreateTaskRequest{ProjectID: h.projectID, Title: "other"}); err != nil {
		t.Fatalf("CreateTask other: %v", err)
	}
	rows, err := h.client.ListTasks(ctx, apiclient.ListTasksOptions{IssueID: iss.ID})
	if err != nil || len(rows) != 1 || rows[0].ID != created.ID || rows[0].Issue == nil {
		t.Errorf("ListTasks(IssueID) = %+v, %v, want only task %d", rows, err, created.ID)
	}
}
