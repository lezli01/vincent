package apiclient_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestIssueCommentsOverTheWire round-trips the thread's routes (task
// 130.16): add and list on a local issue, the 400 for an empty body, and the
// 409 a mirrored issue answers.
func TestIssueCommentsOverTheWire(t *testing.T) {
	t.Parallel()
	c, st, pid := newIssuesClient(t)
	ctx := t.Context()
	iss, err := c.CreateIssue(ctx, apiclient.CreateIssueRequest{ProjectID: pid, Title: "local"}, "")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	empty, err := c.ListIssueComments(ctx, iss.ID)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty thread = %+v, %v", empty, err)
	}
	first, err := c.AddIssueComment(ctx, iss.ID, "first")
	if err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if first.ID == 0 || first.IssueID != iss.ID || first.Body != "first" || first.Author != iss.Author ||
		first.Remote || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Errorf("added = %+v", first)
	}
	second, err := c.AddIssueComment(ctx, iss.ID, "second")
	if err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	thread, err := c.ListIssueComments(ctx, iss.ID)
	if err != nil || len(thread) != 2 || thread[0].ID != first.ID || thread[1].ID != second.ID {
		t.Errorf("thread = %+v, %v", thread, err)
	}

	var e *apiclient.Error
	if _, err := c.AddIssueComment(ctx, iss.ID, "  "); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Errorf("empty body = %v, want 400", err)
	}
	if _, err := c.ListIssueComments(ctx, 9999); !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Errorf("unknown issue = %v, want 404", err)
	}

	imported, _, err := st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: pid, Provider: "github", RemoteKey: "I_1", Repo: "o/r", Number: 1,
		Title: "From GitHub", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	if _, err := st.AddIssueComment(ctx, imported.ID, "octocat", "upstream", "IC_1", issuestate.Sync); err != nil {
		t.Fatalf("AddIssueComment (sync): %v", err)
	}
	_, err = c.AddIssueComment(ctx, imported.ID, "mine")
	if reason, _, ok := apiclient.IssueConflict(err); !ok || reason != apiclient.IssueReasonMirrored {
		t.Errorf("comment on a mirrored issue = %v, want 409 %s", err, apiclient.IssueReasonMirrored)
	}
	mirrored, err := c.ListIssueComments(ctx, imported.ID)
	if err != nil || len(mirrored) != 1 || !mirrored[0].Remote || mirrored[0].RemoteKey != "IC_1" || mirrored[0].Author != "octocat" {
		t.Errorf("mirrored thread = %+v, %v", mirrored, err)
	}
}
