package issuesource

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestJiraResolutionOverriddenFromCaptures is task 142.22 (#953) on the
// 2026-11 captures: a not_planned close sent through the company-managed
// Done screen (resolution offered) gets the captured 204 and is settled
// done, as task 142.13 decision 2 settles every success without a
// read-back. The read that follows is the captured company-managed Done
// issue — resolution Done, the simplified workflow's post-function's value —
// which FromJira maps to completed; importing it adopts completed by sync
// and re-marks the write conflict/resolution_overridden.
func TestJiraResolutionOverriddenFromCaptures(t *testing.T) {
	ctx := context.Background()
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "")

	read, err := src.GetIssue(ctx, b, store.IssueRemote{RemoteKey: "10008"})
	if err != nil || read.Remote.State != issuestate.Closed || read.Remote.CloseReason != issuestate.Completed {
		t.Fatalf("captured VCA-5 = %+v, %v; want closed/completed", read.Remote, err)
	}

	st, err := storetest.Open(filepath.Join(t.TempDir(), "vincent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "vca", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	// Imported while still open, before the close.
	before := read.Remote
	before.ProjectID = p.ID
	before.State, before.CloseReason = issuestate.Open, ""
	iss, _, err := st.UpsertRemoteIssue(ctx, before, issuestate.Sync)
	if err != nil || iss == nil {
		t.Fatalf("import = %+v, %v", iss, err)
	}
	if _, err := st.TransitionIssue(ctx, iss.ID, issuestate.Close, issuestate.NotPlanned, nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	rows, err := st.PendingIssueWrites(ctx, time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending writes = %+v, %v", rows, err)
	}
	row := rows[0]

	// The write: the chooser picks Done with Won't Do, and the captured
	// 204 is a success.
	cur, err := st.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.SetIssueState(ctx, b, *cur.Remote, row.Desired); err != nil {
		t.Fatalf("SetIssueState = %v, want the captured 204 read as success", err)
	}
	site.mu.Lock()
	posts := append([]string(nil), site.transitions...)
	site.mu.Unlock()
	if len(posts) != 1 || !strings.Contains(posts[0], `"id":"41"`) || !strings.Contains(posts[0], "Won't Do") {
		t.Fatalf("transition POSTs = %q, want one Done with Won't Do", posts)
	}
	if ok, err := st.SettleIssueWrite(ctx, row.ID, store.OutboxDone, "", &row.Desired); !ok || err != nil {
		t.Fatalf("settle = %v, %v", ok, err)
	}

	// The read-back: the captured Done issue, confirmed by id.
	after := read.Remote
	after.ProjectID = p.ID
	got, _, err := st.UpsertRemoteIssue(ctx, after, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != issuestate.Closed || got.CloseReason != issuestate.Completed {
		t.Errorf("issue = %s/%s, want Jira's closed/completed", got.State, got.CloseReason)
	}
	if got.Sync == nil || got.Sync.ID != row.ID || got.Sync.Status != store.OutboxConflict ||
		got.Sync.LastReason != store.OutboxReasonResolutionOverridden {
		t.Errorf("write = %+v, want conflict/%s", got.Sync, store.OutboxReasonResolutionOverridden)
	}
}
