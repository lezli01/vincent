package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

func remoteIn(projectID int64, key string, number int) RemoteIssue {
	return RemoteIssue{
		ProjectID: projectID, Provider: "github", RemoteKey: key, Repo: "o/r", Number: number,
		URL: "https://github.com/o/r/issues/1", RemoteJSON: `{"n":1}`, Title: "remote " + key,
		State: issuestate.Open,
	}
}

func mustUpsertRemote(t *testing.T, s *Store, in RemoteIssue) *Issue {
	t.Helper()
	iss, _, err := s.UpsertRemoteIssue(t.Context(), in, issuestate.Sync)
	if err != nil || iss == nil {
		t.Fatalf("UpsertRemoteIssue(%s) = %+v, %v", in.RemoteKey, iss, err)
	}
	return iss
}

func TestIssueSyncStateRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")

	if _, err := s.GetIssueSyncState(ctx, p1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no row = %v, want ErrNotFound", err)
	}
	if err := s.PutIssueSyncState(ctx, IssueSyncState{ProjectID: 999, Provider: "github", OK: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}

	at := func(h int) *time.Time {
		v := time.Date(2026, 9, 1, h, 0, 0, 123456789, time.UTC)
		return &v
	}
	want := IssueSyncState{
		ProjectID: p2.ID, Provider: "github", Repo: "o/r", Watermark: at(1), Since: at(2), ETag: `W/"e"`,
		LastAttemptAt: at(3), LastOKAt: at(4), OK: true, ImportComplete: true,
		LastFullScanAt: at(5), RateLimitedUntil: at(6), RequestedAt: at(7),
	}
	if err := s.PutIssueSyncState(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssueSyncState(ctx, IssueSyncState{ProjectID: p1.ID, Provider: "github", OK: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssueSyncState(ctx, p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", *got, want)
	}
	all, err := s.ListIssueSyncStates(ctx)
	if err != nil || len(all) != 2 || all[0].ProjectID != p1.ID || all[1].ProjectID != p2.ID {
		t.Errorf("list = %+v, %v", all, err)
	}
	if all[0].Watermark != nil || all[0].RequestedAt != nil || all[0].ImportComplete {
		t.Errorf("sparse row = %+v", all[0])
	}

	// The row goes with its project.
	if err := s.DeleteProjectCascade(ctx, p2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetIssueSyncState(ctx, p2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after project delete = %v, want ErrNotFound", err)
	}
}

func TestIssueSyncChangedOnlyOnTransitions(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	put := func(ok bool, reason string) {
		t.Helper()
		if err := s.PutIssueSyncState(ctx, IssueSyncState{ProjectID: p.ID, Provider: "github", OK: ok, Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int { return len(issueEvents(t, s, EventIssueSyncChanged)) }

	put(true, "") // a first ok write is no transition
	if n := count(); n != 0 {
		t.Fatalf("first ok write = %d events", n)
	}
	put(true, "") // identical rewrite
	if n := count(); n != 0 {
		t.Fatalf("identical rewrite = %d events", n)
	}
	put(false, "auth") // ok -> failing
	put(false, "rate_limited")
	if n := count(); n != 1 {
		t.Fatalf("ok->fail->fail = %d events, want 1", n)
	}
	put(true, "") // failing -> ok
	evs := issueEvents(t, s, EventIssueSyncChanged)
	if len(evs) != 2 {
		t.Fatalf("fail->ok = %d events, want 2", len(evs))
	}
	if m := payloadOf(t, evs[0]); m["project_id"] != float64(p.ID) || m["ok"] != false || m["reason"] != "auth" {
		t.Errorf("failing payload = %v", m)
	}
	if m := payloadOf(t, evs[1]); m["ok"] != true || m["reason"] != nil {
		t.Errorf("recovered payload = %v", m)
	}
	if evs[0].ProjectID == nil || *evs[0].ProjectID != p.ID || evs[0].TaskID != nil {
		t.Errorf("event scope = project %v task %v", evs[0].ProjectID, evs[0].TaskID)
	}

	// A project with no row that first fails announces it.
	q := testProject(t, s, "p2")
	if err := s.PutIssueSyncState(ctx, IssueSyncState{ProjectID: q.ID, Provider: "github", Reason: "gh"}); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3 {
		t.Errorf("first failing write = %d events total, want 3", n)
	}
}

func TestRequestIssueSync(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	var got atomic.Int64
	s.OnIssueSyncRequested(func(id int64) {
		// The store must hold nothing the callback could need.
		if _, err := s.GetIssueSyncState(t.Context(), id); err != nil {
			t.Errorf("read from callback: %v", err)
		}
		got.Store(id)
	})

	if err := s.RequestIssueSync(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}
	if got.Load() != 0 {
		t.Errorf("callback ran for a refused request")
	}
	before := time.Now().Add(-time.Second)
	if err := s.RequestIssueSync(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got.Load() != p.ID {
		t.Errorf("callback got %d, want %d", got.Load(), p.ID)
	}
	st, err := s.GetIssueSyncState(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.RequestedAt == nil || st.RequestedAt.Before(before) || !st.OK || st.Provider != "github" {
		t.Errorf("state = %+v", st)
	}
	if n := len(issueEvents(t, s, EventIssueSyncChanged)); n != 0 {
		t.Errorf("request wrote %d sync events", n)
	}

	// A request on an existing row stamps only requested_at.
	st.Repo, st.OK, st.Reason, st.RequestedAt = "o/r", false, "auth", nil
	if err := s.PutIssueSyncState(ctx, *st); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestIssueSync(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetIssueSyncState(ctx, p.ID)
	if again.RequestedAt == nil || again.OK || again.Reason != "auth" || again.Repo != "o/r" {
		t.Errorf("after second request = %+v", again)
	}
}

// TestPutIssueSyncStateKeepsALaterRequest is the regression test for an
// attempt's write erasing a "sync now" recorded while the attempt ran: the
// importer read the row, the request landed, and the attempt's write of
// requested_at = nil dropped it, so the wake found nothing to serve.
func TestPutIssueSyncStateKeepsALaterRequest(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	attempt := time.Now().Add(-time.Minute).UTC()
	st := IssueSyncState{ProjectID: p.ID, Provider: "github", OK: true, LastAttemptAt: &attempt}
	if err := s.RequestIssueSync(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssueSyncState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssueSyncState(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestedAt == nil {
		t.Fatalf("a request made during the attempt was cleared: %+v", got)
	}

	// An attempt that started after the request serves it, and clears it.
	later := time.Now().Add(time.Minute).UTC()
	st.LastAttemptAt = &later
	if err := s.PutIssueSyncState(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetIssueSyncState(ctx, p.ID); got.RequestedAt != nil {
		t.Errorf("a served request survived: %+v", got)
	}
}

// TestRefreshKeepsLocalKindAndPriority is the regression test for a refresh
// overwriting kind: kind and priority are vincent's (task 130.8).
func TestRefreshKeepsLocalKindAndPriority(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	in := remoteIn(p.ID, "I_k", 1)
	in.Kind = "bug"
	iss := mustUpsertRemote(t, s, in)
	if iss.Kind != "bug" {
		t.Fatalf("created kind = %q, want bug", iss.Kind)
	}
	kind, prio := "feature", 1
	if _, err := s.UpdateIssue(ctx, iss.ID, iss.Version, IssuePatch{Kind: &kind, Priority: &prio}, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	updates := len(issueEvents(t, s, EventIssueUpdated))

	in.Kind = "chore"
	same := mustUpsertRemote(t, s, in)
	if same.Kind != "feature" || same.Priority != 1 {
		t.Errorf("after kind-only refresh: kind %q priority %d, want feature 1", same.Kind, same.Priority)
	}
	if n := len(issueEvents(t, s, EventIssueUpdated)); n != updates {
		t.Errorf("kind-only refresh wrote %d issue.updated", n-updates)
	}
	in.Title = "retitled"
	moved := mustUpsertRemote(t, s, in)
	if moved.Kind != "feature" || moved.Priority != 1 || moved.Title != "retitled" {
		t.Errorf("after title refresh = %+v", moved)
	}
}

func TestSetIssueRemoteStatus(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	in := remoteIn(p.ID, "I_m", 1)
	iss := mustUpsertRemote(t, s, in)
	if iss.Remote.Status != RemoteStatusLive {
		t.Fatalf("new remote status = %q", iss.Remote.Status)
	}
	updates := func() []Event { return issueEvents(t, s, EventIssueUpdated) }

	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "nope", RemoteStatusMissing, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key = %v, want ErrNotFound", err)
	}
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_m", "gone", ""); err == nil {
		t.Errorf("unknown status accepted")
	}
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_m", RemoteStatusLive, ""); err != nil || len(updates()) != 0 {
		t.Errorf("unchanged live = %v, %d events", err, len(updates()))
	}

	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_m", RemoteStatusMoved, "other/repo#4"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatalf("issue after moved: %v", err)
	}
	if got.Remote == nil || got.Remote.Status != RemoteStatusMoved || got.Remote.SyncedAt == nil {
		t.Errorf("remote after moved = %+v", got.Remote)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(got.Remote.RemoteJSON), &raw); err != nil || raw["moved_to"] != "other/repo#4" || raw["n"] != float64(1) {
		t.Errorf("remote_json = %s (%v)", got.Remote.RemoteJSON, err)
	}
	evs := updates()
	if len(evs) != 1 {
		t.Fatalf("issue.updated = %d, want 1", len(evs))
	}
	if m := payloadOf(t, evs[0]); !reflect.DeepEqual(m["changed"], []any{"remote_status"}) || m["by"] != "sync" || m["id"] != float64(iss.ID) {
		t.Errorf("payload = %v", m)
	}
	// Unchanged: nothing more.
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_m", RemoteStatusMoved, "other/repo#4"); err != nil || len(updates()) != 1 {
		t.Errorf("repeat moved = %v, %d events", err, len(updates()))
	}
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_m", RemoteStatusMissing, ""); err != nil || len(updates()) != 2 {
		t.Errorf("moved -> missing = %v, %d events", err, len(updates()))
	}
	if got, err := s.GetIssue(ctx, iss.ID); err != nil || got.Remote.Status != RemoteStatusMissing || got.State != issuestate.Open {
		t.Errorf("issue after missing = %+v, %v", got, err)
	}

	// A refresh matching the key again resets the status to live.
	again := mustUpsertRemote(t, s, in)
	if again.Remote.Status != RemoteStatusLive {
		t.Errorf("status after refresh = %q, want live", again.Remote.Status)
	}
	if m := payloadOf(t, updates()[2]); !reflect.DeepEqual(m["changed"], []any{"remote"}) {
		t.Errorf("reset payload = %v", m)
	}

	// A tombstone is never touched and stays known.
	if err := s.DeleteIssue(ctx, iss.ID, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	before := len(issueEvents(t, s, allIssueEventTypes()...))
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_m", RemoteStatusMissing, ""); err != nil {
		t.Errorf("tombstone = %v", err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT remote_status FROM issue_remotes WHERE remote_key = 'I_m'`).Scan(&status); err != nil || status != "" {
		t.Errorf("tombstone status = %q, %v", status, err)
	}
	if after := len(issueEvents(t, s, allIssueEventTypes()...)); after != before {
		t.Errorf("tombstone write appended %d events", after-before)
	}
	if known, err := s.RemoteIssueKnown(ctx, p.ID, "github", "I_m"); err != nil || !known {
		t.Errorf("tombstone known = %v, %v", known, err)
	}
	if got, created, err := s.UpsertRemoteIssue(ctx, in, issuestate.Sync); got != nil || created || err != nil {
		t.Errorf("tombstone re-import = %+v, %v, %v", got, created, err)
	}
	if known, err := s.RemoteIssueKnown(ctx, p.ID, "github", "I_never"); err != nil || known {
		t.Errorf("unseen key known = %v, %v", known, err)
	}
}

func TestListOpenRemoteIssues(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	q := testProject(t, s, "p2")
	open := mustUpsertRemote(t, s, remoteIn(p.ID, "I_open", 1))
	closedIn := remoteIn(p.ID, "I_closed", 2)
	closedIn.State, closedIn.CloseReason = issuestate.Closed, issuestate.Completed
	mustUpsertRemote(t, s, closedIn)
	gone := mustUpsertRemote(t, s, remoteIn(p.ID, "I_gone", 3))
	mustUpsertRemote(t, s, remoteIn(p.ID, "I_moved", 4))
	mustUpsertRemote(t, s, remoteIn(p.ID, "I_missing", 5))
	mustUpsertRemote(t, s, remoteIn(q.ID, "I_other", 6))
	mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "local"})
	if err := s.DeleteIssue(ctx, gone.ID, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_moved", RemoteStatusMoved, "x/y#1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_missing", RemoteStatusMissing, ""); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListOpenRemoteIssues(ctx, p.ID, "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RemoteKey != "I_open" || list[0].IssueID == nil || *list[0].IssueID != open.ID ||
		list[0].SyncedAt == nil || list[0].Number != 1 || list[0].Status != RemoteStatusLive {
		t.Errorf("open remotes = %+v", list)
	}
	if other, err := s.ListOpenRemoteIssues(ctx, p.ID, "gitlab"); err != nil || len(other) != 0 {
		t.Errorf("other provider = %+v, %v", other, err)
	}
}

// TestImportedIssueIDs: a live link maps its key to the issue; a moved or
// missing remote, a tombstone, another project and an unknown key do not.
func TestImportedIssueIDs(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	other := testProject(t, s, "p2")
	live := mustUpsertRemote(t, s, remoteIn(p.ID, "I_live", 1))
	mustUpsertRemote(t, s, remoteIn(p.ID, "I_moved", 2))
	mustUpsertRemote(t, s, remoteIn(other.ID, "I_other", 3))
	gone := mustUpsertRemote(t, s, remoteIn(p.ID, "I_gone", 4))
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_moved", RemoteStatusMoved, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIssue(ctx, gone.ID, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	got, err := s.ImportedIssueIDs(ctx, p.ID, "github", []string{"I_live", "I_moved", "I_other", "I_gone", "I_none"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int64{"I_live": live.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("ImportedIssueIDs = %v, want %v", got, want)
	}
	if got, err := s.ImportedIssueIDs(ctx, p.ID, "github", nil); err != nil || len(got) != 0 {
		t.Errorf("no keys = %v, %v", got, err)
	}
}
