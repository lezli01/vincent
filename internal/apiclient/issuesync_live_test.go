package apiclient_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestIssueSyncOverTheWire round-trips the issue sync routes and the issue
// source's sync fields through the client against the real handlers
// (task 130.8).
func TestIssueSyncOverTheWire(t *testing.T) {
	t.Parallel()
	c, st, pid := newIssuesClient(t)
	ctx := t.Context()

	got, err := c.IssueSyncStatus(ctx, pid)
	if err != nil || !got.Enabled || got.OK || got.Reason != "pending" || got.Provider != "github" {
		t.Fatalf("IssueSyncStatus before any sync = %+v, %v", got, err)
	}
	got, err = c.SyncIssues(ctx, pid)
	if err != nil || got.Reason != "pending" {
		t.Fatalf("SyncIssues = %+v, %v", got, err)
	}
	if row, err := st.GetIssueSyncState(ctx, pid); err != nil || row.RequestedAt == nil {
		t.Fatalf("sync now left no request: %+v, %v", row, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := st.PutIssueSyncState(ctx, store.IssueSyncState{
		ProjectID: pid, Provider: "github", Repo: "o/r",
		LastAttemptAt: &now, LastOKAt: &now, OK: true, ImportComplete: true,
	}); err != nil {
		t.Fatalf("PutIssueSyncState: %v", err)
	}
	got, err = c.IssueSyncStatus(ctx, pid)
	if err != nil || !got.OK || got.Reason != "" || got.Repo != "o/r" || !got.ImportComplete ||
		got.LastSyncedAt == nil || !got.LastSyncedAt.Equal(now) {
		t.Errorf("IssueSyncStatus after an ok sync = %+v, %v", got, err)
	}

	for name, call := range map[string]func() error{
		"status": func() error { _, err := c.IssueSyncStatus(ctx, 9999); return err },
		"sync":   func() error { _, err := c.SyncIssues(ctx, 9999); return err },
	} {
		var e *apiclient.Error
		if err := call(); !errors.As(err, &e) || e.Status != http.StatusNotFound {
			t.Errorf("%s unknown project = %v, want 404", name, err)
		}
	}

	imported, _, err := st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: pid, Provider: "github", RemoteKey: "I_5", Repo: "o/r", Number: 5,
		URL: "https://github.com/o/r/issues/5", Title: "t", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	if err := st.SetIssueRemoteStatus(ctx, pid, "github", "I_5", store.RemoteStatusMoved, "o/elsewhere#1"); err != nil {
		t.Fatalf("SetIssueRemoteStatus: %v", err)
	}
	iss, err := c.GetIssue(ctx, imported.ID, "")
	if err != nil || iss.Source == nil || iss.Source.LastSyncedAt == nil || iss.Source.Status != "moved" {
		t.Errorf("imported issue source = %+v, %v", iss.Source, err)
	}
}
