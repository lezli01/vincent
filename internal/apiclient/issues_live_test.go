package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// newIssuesClient wires the client to the real issue handlers over a real
// store with one project (task 130.3).
func newIssuesClient(t *testing.T) (*apiclient.Client, *store.Store, int64) {
	t.Helper()
	st, err := storetest.Open(filepath.Join(t.TempDir(), "issues.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, testToken), st, p.ID
}

// TestIssuesOverTheWire round-trips every issue route through the client.
func TestIssuesOverTheWire(t *testing.T) {
	t.Parallel()
	c, st, pid := newIssuesClient(t)
	ctx := t.Context()

	req := apiclient.CreateIssueRequest{ProjectID: pid, Title: "Crash", Body: "trace", Labels: []string{"bug"}, Kind: "bug", Priority: 2}
	iss, err := c.CreateIssue(ctx, req, "key-1")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if iss.ID == 0 || iss.State != "open" || iss.Body != "trace" || iss.Author == "" || iss.Version != 1 ||
		!slices.Equal(iss.AvailableActions, []string{"close"}) || len(iss.Editable) != 5 || iss.Source != nil {
		t.Errorf("created = %+v", iss)
	}
	replay, err := c.CreateIssue(ctx, req, "key-1")
	if err != nil || replay.ID != iss.ID {
		t.Errorf("replay = %d, %v, want issue %d", replay.ID, err, iss.ID)
	}

	list, err := c.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: pid, States: []string{"open"}, Labels: []string{"bug"}, Query: "Cra"})
	if err != nil || len(list) != 1 || list[0].ID != iss.ID || list[0].Body != "" {
		t.Errorf("ListIssues = %+v, %v", list, err)
	}

	title := "Crash on start"
	patched, err := c.PatchIssue(ctx, iss.ID, apiclient.IssuePatch{Version: iss.Version, Title: &title, AddLabels: []string{"ui"}})
	if err != nil || patched.Version != 2 || !slices.Equal(patched.Labels, []string{"bug", "ui"}) {
		t.Fatalf("PatchIssue = %+v, %v", patched, err)
	}
	_, err = c.PatchIssue(ctx, iss.ID, apiclient.IssuePatch{Version: iss.Version, Title: &title})
	reason, cur, ok := apiclient.IssueConflict(err)
	if !ok || reason != apiclient.IssueReasonChanged || cur == nil || cur.Version != 2 || cur.Title != title {
		t.Errorf("stale patch = %q %+v %v (%v)", reason, cur, ok, err)
	}

	other, err := c.CreateIssue(ctx, apiclient.CreateIssueRequest{ProjectID: pid, Title: "other"}, "")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := c.CloseIssue(ctx, iss.ID, apiclient.CloseIssueRequest{Reason: "duplicate", DuplicateOf: &other.ID})
	if err != nil || closed.State != "closed" || closed.DuplicateOf == nil || *closed.DuplicateOf != other.ID {
		t.Errorf("CloseIssue = %+v, %v", closed, err)
	}
	_, err = c.CloseIssue(ctx, iss.ID, apiclient.CloseIssueRequest{})
	var e *apiclient.Error
	if !errors.As(err, &e) || e.Status != http.StatusConflict || e.Details["state"] != "closed" {
		t.Errorf("second close = %v", err)
	}
	reopened, err := c.ReopenIssue(ctx, iss.ID)
	if err != nil || reopened.State != "open" || reopened.DuplicateOf != nil {
		t.Errorf("ReopenIssue = %+v, %v", reopened, err)
	}

	got, err := c.GetIssue(ctx, iss.ID, "")
	if err != nil || got.Title != title || got.Tasks.ActiveIDs == nil {
		t.Errorf("GetIssue = %+v, %v", got, err)
	}

	labels, err := c.ListIssueLabels(ctx, pid)
	if err != nil || len(labels) != 2 || labels[0].Name != "bug" || labels[0].IssueCount != 1 || labels[0].Source != "local" {
		t.Errorf("ListIssueLabels = %+v, %v", labels, err)
	}

	if err := c.DeleteIssue(ctx, iss.ID); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	if _, err := c.GetIssue(ctx, iss.ID, ""); !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Errorf("get after delete = %v", err)
	}

	// A mirrored edit is its own typed refusal.
	imported, _, err := st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: pid, Provider: "github", RemoteKey: "I_1", Repo: "o/r", Number: 3, Title: "gh", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	body := "mine"
	_, err = c.PatchIssue(ctx, imported.ID, apiclient.IssuePatch{Version: imported.Version, Body: &body})
	if reason, _, ok := apiclient.IssueConflict(err); !ok || reason != apiclient.IssueReasonMirrored {
		t.Errorf("mirrored patch = %v", err)
	}
	gh, err := c.GetIssue(ctx, imported.ID, "")
	if err != nil || gh.Source == nil || gh.Source.Repo != "o/r" || gh.Source.Number != 3 {
		t.Errorf("imported source = %+v, %v", gh.Source, err)
	}

	// RemoteNumber reaches the lookup (task 130.11), and the daemon's
	// project_id requirement comes back as a 400.
	byNumber, err := c.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: pid, RemoteNumber: 3})
	if err != nil || len(byNumber) != 1 || byNumber[0].ID != imported.ID {
		t.Errorf("ListIssues RemoteNumber 3 = %+v, %v", byNumber, err)
	}
	if _, err := c.ListIssues(ctx, apiclient.IssueListOptions{RemoteNumber: 3}); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Errorf("RemoteNumber without ProjectID = %v, want a 400", err)
	}
}

// TestIssueConflictCarriesALargeIssue: the current issue a stale PATCH's 409
// carries can hold a 64 KiB body, which must survive the client's error read.
func TestIssueConflictCarriesALargeIssue(t *testing.T) {
	t.Parallel()
	c, _, pid := newIssuesClient(t)
	ctx := t.Context()
	iss, err := c.CreateIssue(ctx, apiclient.CreateIssueRequest{ProjectID: pid, Title: "big", Body: strings.Repeat(`"`, 64<<10)}, "")
	if err != nil {
		t.Fatal(err)
	}
	kind := "bug"
	if _, err := c.PatchIssue(ctx, iss.ID, apiclient.IssuePatch{Version: iss.Version, Kind: &kind}); err != nil {
		t.Fatal(err)
	}
	_, err = c.PatchIssue(ctx, iss.ID, apiclient.IssuePatch{Version: iss.Version, Kind: &kind})
	reason, cur, ok := apiclient.IssueConflict(err)
	if !ok || reason != apiclient.IssueReasonChanged || cur == nil || len(cur.Body) != 64<<10 {
		t.Errorf("conflict = %q, ok %v, current body %d bytes (%v)", reason, ok, func() int {
			if cur == nil {
				return -1
			}
			return len(cur.Body)
		}(), err)
	}
}

// TestIssueLanesOverTheWire: lane, attention, the lane filter and the
// project stats' lanes survive the trip through the client (task 134.4).
func TestIssueLanesOverTheWire(t *testing.T) {
	t.Parallel()
	c, st, pid := newIssuesClient(t)
	ctx := t.Context()
	idle, err := c.CreateIssue(ctx, apiclient.CreateIssueRequest{ProjectID: pid, Title: "idle"}, "")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	asking, err := c.CreateIssue(ctx, apiclient.CreateIssueRequest{ProjectID: pid, Title: "asking"}, "")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	task := &store.Task{
		ProjectID: pid, Title: "asking", WorkflowName: "w", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b-asking", State: store.TaskAwaitingInput, IssueID: &asking.ID,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := c.GetIssue(ctx, asking.ID, "")
	if err != nil || got.Lane != "in_progress" || !got.Attention {
		t.Errorf("GetIssue = lane %q attention %v, %v; want in_progress true", got.Lane, got.Attention, err)
	}
	list, err := c.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: pid, Lanes: []string{"open"}})
	if err != nil || len(list) != 1 || list[0].ID != idle.ID || list[0].Lane != "open" || list[0].Attention {
		t.Errorf("ListIssues(lane=open) = %+v, %v", list, err)
	}
	if _, err := c.ListIssues(ctx, apiclient.IssueListOptions{Lanes: []string{"closed"}}); err == nil {
		t.Error("ListIssues(lane=closed) succeeded, want a 400")
	}

	projects, err := c.ListProjects(ctx, apiclient.WithStats())
	if err != nil || len(projects) != 1 || projects[0].Stats == nil {
		t.Fatalf("ListProjects(WithStats) = %+v, %v", projects, err)
	}
	if l := projects[0].Stats.Issues.Lanes; l.Open != 1 || l.InProgress != 1 || l.HandOff != 0 || l.Done != 0 {
		t.Errorf("stats lanes = %+v, want open 1 in_progress 1", l)
	}
}
