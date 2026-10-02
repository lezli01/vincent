package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// issueEvents returns the events of the given type, in id order.
func issueEvents(t *testing.T, s *Store, types ...string) []Event {
	t.Helper()
	evs, err := s.ListEvents(t.Context(), EventFilter{Types: types})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return evs
}

func allIssueEventTypes() []string {
	return []string{
		EventIssueCreated, EventIssueUpdated, EventIssueStateChanged,
		EventIssueLabelsChanged, EventIssueCommentAdded, EventIssueDeleted,
	}
}

func payloadOf(t *testing.T, e Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatalf("payload %s: %v", e.Payload, err)
	}
	return m
}

func mustCreateIssue(t *testing.T, s *Store, in NewIssue) *Issue {
	t.Helper()
	iss, err := s.CreateIssue(t.Context(), in, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return iss
}

// TestMigration0036OnAPopulatedDatabase: 0036 applies over rows written at
// 0035, the existing tasks read back with no issue, and the foreign keys
// the migration adds are consistent.
func TestMigration0036OnAPopulatedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 35)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0035: %v", err)
		}
		defer func() { _ = db.Close() }()
		now := formatTime(time.Now())
		if _, err := db.Exec(`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
			VALUES (1, 'p', '/p', 'main', ?, ?)`, now, now); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, workflow_name, workflow_snapshot,
			base_branch, branch_name, state, created_at, updated_at)
			VALUES (1, 1, 't', 'adhoc', 'steps: []', 'main', 'b', 'done', ?, ?)`, now, now); err != nil {
			t.Fatalf("insert task: %v", err)
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 35 -> 36): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var issueID sql.NullInt64
	var issueJSON sql.NullString
	if err := s.db.QueryRow(`SELECT issue_id, issue_json FROM tasks WHERE id = 1`).Scan(&issueID, &issueJSON); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if issueID.Valid || issueJSON.Valid {
		t.Errorf("legacy task issue_id = %v, issue_json = %v; want both NULL", issueID, issueJSON)
	}
	for _, table := range []string{"issues", "issue_remotes", "labels", "issue_labels", "issue_comments"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing after 0036", table)
		}
	}
	// A real issue on the migrated database, pointed at by the legacy task.
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: 1, Title: "x", Labels: []string{"bug"}})
	if _, err := s.db.Exec(`UPDATE tasks SET issue_id = ? WHERE id = 1`, iss.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Error("PRAGMA foreign_key_check reports a violation after 0036")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAndGetIssue(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{
		ProjectID: p.ID, Title: "Crash on start", Body: "trace", Kind: "bug", Author: "ann",
		Priority: 2, Labels: []string{" zeta ", "Alpha", "alpha", ""},
	})
	if iss.State != issuestate.Open || iss.CloseReason != "" || iss.ClosedAt != nil || iss.Version != 1 {
		t.Errorf("new issue = %+v", iss)
	}
	if iss.Title != "Crash on start" || iss.Body != "trace" || iss.Kind != "bug" || iss.Author != "ann" || iss.Priority != 2 {
		t.Errorf("fields = %+v", iss)
	}
	if want := []string{"Alpha", "zeta"}; !reflect.DeepEqual(iss.Labels, want) {
		t.Errorf("labels = %v, want %v", iss.Labels, want)
	}
	if iss.Remote != nil || iss.Active || iss.TaskCount != 0 {
		t.Errorf("remote/derived = %v %v %d", iss.Remote, iss.Active, iss.TaskCount)
	}
	got, err := s.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, iss) {
		t.Errorf("GetIssue = %+v, want %+v", got, iss)
	}
	if _, err := s.GetIssue(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIssue(999) = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateIssue(ctx, NewIssue{ProjectID: 999, Title: "x"}, issuestate.Human); !errors.Is(err, ErrNotFound) {
		t.Errorf("CreateIssue in a missing project = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateIssue(ctx, NewIssue{ProjectID: p.ID, Title: "x"}, "robot"); err == nil {
		t.Error("CreateIssue by an unknown actor succeeded")
	}

	evs := issueEvents(t, s, allIssueEventTypes()...)
	if len(evs) != 1 || evs[0].Type != EventIssueCreated {
		t.Fatalf("events = %+v, want one issue.created", evs)
	}
	if evs[0].TaskID != nil || evs[0].ProjectID == nil || *evs[0].ProjectID != p.ID {
		t.Errorf("event ids = task %v project %v", evs[0].TaskID, evs[0].ProjectID)
	}
	if m := payloadOf(t, evs[0]); m["id"] != float64(iss.ID) || m["by"] != "human" || len(m) != 2 {
		t.Errorf("payload = %v", m)
	}
}

func TestListIssuesFilters(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")
	a := mustCreateIssue(t, s, NewIssue{ProjectID: p1.ID, Title: "alpha crash", Kind: "bug", Labels: []string{"Bug"}})
	b := mustCreateIssue(t, s, NewIssue{ProjectID: p1.ID, Title: "beta", Body: "100% wrong", Kind: "feature"})
	c := mustCreateIssue(t, s, NewIssue{ProjectID: p2.ID, Title: "gamma", Labels: []string{"bug"}})
	if _, err := s.TransitionIssue(ctx, b.ID, issuestate.Close, "", issuestate.Human); err != nil {
		t.Fatal(err)
	}
	r, _, err := s.UpsertRemoteIssue(ctx, RemoteIssue{
		ProjectID: p1.ID, Provider: "github", RemoteKey: "N_1", Repo: "o/r", Number: 1,
		Title: "remote", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}

	ids := func(f IssueFilter) []int64 {
		t.Helper()
		got, err := s.ListIssues(ctx, f)
		if err != nil {
			t.Fatalf("ListIssues(%+v): %v", f, err)
		}
		var out []int64
		for _, i := range got {
			out = append(out, i.ID)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		f    IssueFilter
		want []int64
	}{
		{"all, newest updated first", IssueFilter{}, []int64{r.ID, b.ID, c.ID, a.ID}},
		{"project", IssueFilter{ProjectID: p1.ID}, []int64{r.ID, b.ID, a.ID}},
		{"open", IssueFilter{States: []issuestate.State{issuestate.Open}}, []int64{r.ID, c.ID, a.ID}},
		{"closed", IssueFilter{States: []issuestate.State{issuestate.Closed}}, []int64{b.ID}},
		{"label, any case", IssueFilter{Label: "BUG"}, []int64{c.ID, a.ID}},
		{"label in project", IssueFilter{ProjectID: p2.ID, Label: "bug"}, []int64{c.ID}},
		{"kind", IssueFilter{Kind: "feature"}, []int64{b.ID}},
		{"source local", IssueFilter{ProjectID: p1.ID, Source: "local"}, []int64{b.ID, a.ID}},
		{"source github", IssueFilter{Source: "github"}, []int64{r.ID}},
		{"text in title", IssueFilter{Text: "crash"}, []int64{a.ID}},
		{"text in body, literal percent", IssueFilter{Text: "100%"}, []int64{b.ID}},
		{"text wildcard is literal", IssueFilter{Text: "_"}, nil},
		{"limit", IssueFilter{Limit: 2}, []int64{r.ID, b.ID}},
	} {
		if got := ids(tc.f); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: ids = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUpdateIssueCompareAndSet(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "t", Body: "b"})

	title, prio, body := "t2", 3, "b"
	up, err := s.UpdateIssue(ctx, iss.ID, iss.Version, IssuePatch{Title: &title, Priority: &prio, Body: &body}, issuestate.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if up.Title != "t2" || up.Priority != 3 || up.Version != 2 || !up.UpdatedAt.After(iss.UpdatedAt) {
		t.Errorf("updated = %+v", up)
	}
	evs := issueEvents(t, s, EventIssueUpdated)
	if len(evs) != 1 {
		t.Fatalf("issue.updated events = %d, want 1", len(evs))
	}
	if m := payloadOf(t, evs[0]); !reflect.DeepEqual(m["changed"], []any{"priority", "title"}) || m["by"] != "agent" {
		t.Errorf("payload = %v", m)
	}

	// The version the caller read is stale now.
	other := "lost"
	if _, err := s.UpdateIssue(ctx, iss.ID, iss.Version, IssuePatch{Title: &other}, issuestate.Human); !errors.Is(err, ErrIssueChanged) {
		t.Errorf("stale update = %v, want ErrIssueChanged", err)
	}
	// An identical or empty patch writes nothing.
	for _, patch := range []IssuePatch{{}, {Title: &title, Body: &body}} {
		same, err := s.UpdateIssue(ctx, iss.ID, up.Version, patch, issuestate.Human)
		if err != nil {
			t.Fatal(err)
		}
		if same.Version != up.Version {
			t.Errorf("no-op patch bumped version to %d", same.Version)
		}
	}
	if n := len(issueEvents(t, s, EventIssueUpdated)); n != 1 {
		t.Errorf("issue.updated events = %d after no-op patches, want 1", n)
	}
	if _, err := s.UpdateIssue(ctx, 999, 1, IssuePatch{Title: &title}, issuestate.Human); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing = %v, want ErrNotFound", err)
	}
}

func TestTransitionIssue(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "t"})
	dup := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "orig"})

	closed, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, "", issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != issuestate.Closed || closed.CloseReason != issuestate.Completed || closed.ClosedAt == nil || closed.Version != 2 {
		t.Errorf("closed = %+v", closed)
	}
	evs := issueEvents(t, s, EventIssueStateChanged)
	if len(evs) != 1 {
		t.Fatalf("state events = %d", len(evs))
	}
	if m := payloadOf(t, evs[0]); m["from"] != "open" || m["to"] != "closed" || m["reason"] != "completed" || m["by"] != "human" {
		t.Errorf("payload = %v", m)
	}

	// A human closing a closed issue is refused, and changes nothing.
	if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, "", issuestate.Human); !errors.Is(err, ErrInvalidIssueAction) {
		t.Errorf("double close = %v, want ErrInvalidIssueAction", err)
	}
	// Sync reporting the same is a no-op: no event, no version bump.
	same, err := s.TransitionIssue(ctx, iss.ID, issuestate.RemoteClosed, "", issuestate.Sync)
	if err != nil {
		t.Fatalf("sync double close: %v", err)
	}
	if same.Version != closed.Version {
		t.Errorf("sync no-op bumped version to %d", same.Version)
	}
	if n := len(issueEvents(t, s, EventIssueStateChanged)); n != 1 {
		t.Errorf("state events = %d after a sync no-op, want 1", n)
	}
	// Remote actions are sync's alone.
	if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.RemoteReopened, "", issuestate.Human); !errors.Is(err, ErrInvalidIssueAction) {
		t.Errorf("human remote_reopened = %v, want ErrInvalidIssueAction", err)
	}

	// Reopen clears the reason, the time and the duplicate pointer.
	if _, err := s.db.Exec(`UPDATE issues SET duplicate_of_issue_id = ? WHERE id = ?`, dup.ID, iss.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.TransitionIssue(ctx, iss.ID, issuestate.Reopen, "", issuestate.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != issuestate.Open || reopened.CloseReason != "" || reopened.ClosedAt != nil || reopened.DuplicateOfIssueID != nil {
		t.Errorf("reopened = %+v", reopened)
	}

	dupClosed, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, issuestate.Duplicate, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	if dupClosed.CloseReason != issuestate.Duplicate {
		t.Errorf("close reason = %s, want duplicate", dupClosed.CloseReason)
	}
	if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Reopen, issuestate.Completed, issuestate.Human); err == nil {
		t.Error("reopen with a reason succeeded")
	}
	if _, err := s.TransitionIssue(ctx, 999, issuestate.Close, "", issuestate.Human); !errors.Is(err, ErrNotFound) {
		t.Errorf("transition missing = %v, want ErrNotFound", err)
	}
}

func TestLabels(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")
	bug, err := s.UpsertLabel(ctx, p.ID, " bug ", "red", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if bug.Name != "bug" || bug.Source != "local" || bug.Color != "red" {
		t.Errorf("label = %+v", bug)
	}
	again, err := s.UpsertLabel(ctx, p.ID, "Bug", "", "broken", "github")
	if err != nil {
		t.Fatalf("UpsertLabel(Bug): %v", err)
	}
	if again.ID != bug.ID || again.Name != "bug" || again.Color != "red" || again.Description != "broken" || again.Source != "local" {
		t.Errorf("Bug = %+v, want the bug row", again)
	}
	other, err := s.UpsertLabel(ctx, p2.ID, "bug", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == bug.ID {
		t.Error("a second project shares the first project's label row")
	}

	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "t", Labels: []string{"BUG"}})
	if want := []string{"bug"}; !reflect.DeepEqual(iss.Labels, want) {
		t.Errorf("labels = %v, want %v", iss.Labels, want)
	}
	set, err := s.SetIssueLabels(ctx, iss.ID, []string{"ux", "Bug", "Docs"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bug", "Docs", "ux"}; !reflect.DeepEqual(set.Labels, want) || set.Version != 2 {
		t.Errorf("labels = %v v%d, want %v v2", set.Labels, set.Version, want)
	}
	// The same set, any order or case, writes nothing.
	if same, err := s.SetIssueLabels(ctx, iss.ID, []string{"DOCS", "ux", "bug"}, issuestate.Human); err != nil || same.Version != 2 {
		t.Errorf("same set = v%d, %v", same.Version, err)
	}
	evs := issueEvents(t, s, EventIssueLabelsChanged)
	if len(evs) != 1 {
		t.Fatalf("labels events = %d, want 1", len(evs))
	}
	if m := payloadOf(t, evs[0]); !reflect.DeepEqual(m["labels"], []any{"bug", "Docs", "ux"}) {
		t.Errorf("payload = %v", m)
	}
	cleared, err := s.SetIssueLabels(ctx, iss.ID, nil, issuestate.Human)
	if err != nil || len(cleared.Labels) != 0 {
		t.Errorf("cleared = %v, %v", cleared.Labels, err)
	}
	labels, err := s.ListLabels(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range labels {
		names = append(names, l.Name)
	}
	if want := []string{"bug", "Docs", "ux"}; !reflect.DeepEqual(names, want) {
		t.Errorf("catalogue = %v, want %v — clearing an issue's labels keeps the catalogue", names, want)
	}
}

func TestIssueComments(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "t"})
	c1, err := s.AddIssueComment(ctx, iss.ID, "ann", "first", "", issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddIssueComment(ctx, iss.ID, "bob", "second", "IC_1", issuestate.Sync); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIssueComments(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != c1.ID || got[0].Body != "first" || got[1].RemoteKey != "IC_1" || got[0].RemoteKey != "" {
		t.Errorf("comments = %+v", got)
	}
	after, err := s.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != iss.Version {
		t.Errorf("a comment bumped the version to %d", after.Version)
	}
	evs := issueEvents(t, s, EventIssueCommentAdded)
	if len(evs) != 2 {
		t.Fatalf("comment events = %d", len(evs))
	}
	if m := payloadOf(t, evs[0]); m["comment_id"] != float64(c1.ID) || m["id"] != float64(iss.ID) || m["body"] != nil {
		t.Errorf("payload = %v", m)
	}
	if _, err := s.AddIssueComment(ctx, 999, "a", "b", "", issuestate.Human); !errors.Is(err, ErrNotFound) {
		t.Errorf("comment on missing = %v, want ErrNotFound", err)
	}
}

func TestUpsertRemoteIssue(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")
	updatedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	in := RemoteIssue{
		ProjectID: p1.ID, Provider: "github", RemoteKey: "I_kw1", Repo: "o/r", Number: 7,
		URL: "https://github.com/o/r/issues/7", RemoteJSON: `{"n":7}`, RemoteUpdatedAt: &updatedAt,
		Title: "remote", Body: "rb", Author: "octo", State: issuestate.Open, Labels: []string{"bug"},
	}
	iss, created, err := s.UpsertRemoteIssue(ctx, in, issuestate.Sync)
	if err != nil || !created {
		t.Fatalf("first upsert = %v, created %v", err, created)
	}
	r := iss.Remote
	if r == nil || r.Provider != "github" || r.RemoteKey != "I_kw1" || r.Number != 7 || r.SyncedAt == nil ||
		r.IssueID == nil || *r.IssueID != iss.ID || r.RemoteUpdatedAt == nil || !r.RemoteUpdatedAt.Equal(updatedAt) {
		t.Errorf("remote = %+v", r)
	}
	if iss.Author != "octo" || !reflect.DeepEqual(iss.Labels, []string{"bug"}) {
		t.Errorf("issue = %+v", iss)
	}
	labels, _ := s.ListLabels(ctx, p1.ID)
	if len(labels) != 1 || labels[0].Source != "github" {
		t.Errorf("labels = %+v, want one github label", labels)
	}

	// An unchanged refresh writes no event and keeps the version.
	same, created, err := s.UpsertRemoteIssue(ctx, in, issuestate.Sync)
	if err != nil || created || same.ID != iss.ID || same.Version != iss.Version {
		t.Errorf("unchanged refresh = %+v, %v, %v", same, created, err)
	}
	// A closed remote moves state and labels in one issue.updated.
	in.State, in.CloseReason, in.Labels = issuestate.Closed, issuestate.NotPlanned, []string{"bug", "wontfix"}
	moved, _, err := s.UpsertRemoteIssue(ctx, in, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	if moved.State != issuestate.Closed || moved.CloseReason != issuestate.NotPlanned || moved.ClosedAt == nil || moved.Version != 2 {
		t.Errorf("moved = %+v", moved)
	}
	evs := issueEvents(t, s, EventIssueUpdated)
	if len(evs) != 1 {
		t.Fatalf("issue.updated = %d, want 1", len(evs))
	}
	if m := payloadOf(t, evs[0]); !reflect.DeepEqual(m["changed"], []any{"close_reason", "labels", "state"}) || m["by"] != "sync" {
		t.Errorf("payload = %v", m)
	}
	if n := len(issueEvents(t, s, EventIssueCreated)); n != 1 {
		t.Errorf("issue.created = %d, want 1", n)
	}

	// The same key in a second project is a second issue.
	in2 := in
	in2.ProjectID = p2.ID
	other, created, err := s.UpsertRemoteIssue(ctx, in2, issuestate.Sync)
	if err != nil || !created || other.ID == iss.ID || other.ProjectID != p2.ID {
		t.Errorf("second project = %+v, %v, %v", other, created, err)
	}

	// Deleting tombstones the remote row; re-upserting it creates nothing.
	if err := s.DeleteIssue(ctx, iss.ID, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	var issueID sql.NullInt64
	if err := s.db.QueryRow(`SELECT issue_id FROM issue_remotes WHERE project_id = ? AND remote_key = 'I_kw1'`, p1.ID).Scan(&issueID); err != nil {
		t.Fatalf("tombstone row: %v", err)
	}
	if issueID.Valid {
		t.Errorf("remote issue_id = %d after delete, want NULL", issueID.Int64)
	}
	before := len(issueEvents(t, s, allIssueEventTypes()...))
	got, created, err := s.UpsertRemoteIssue(ctx, in, issuestate.Sync)
	if err != nil || got != nil || created {
		t.Errorf("tombstoned key = %+v, %v, %v; want nil, false, nil", got, created, err)
	}
	if after := len(issueEvents(t, s, allIssueEventTypes()...)); after != before {
		t.Errorf("tombstoned upsert wrote %d events", after-before)
	}
	list, err := s.ListIssues(ctx, IssueFilter{ProjectID: p1.ID})
	if err != nil || len(list) != 0 {
		t.Errorf("project 1 issues = %d, %v; want none", len(list), err)
	}
	// The second project's copy is untouched by the first's tombstone.
	if _, err := s.GetIssue(ctx, other.ID); err != nil {
		t.Errorf("second project's issue: %v", err)
	}
}

func TestDeleteIssue(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "t", Labels: []string{"bug"}})
	if _, err := s.AddIssueComment(ctx, iss.ID, "a", "b", "", issuestate.Human); err != nil {
		t.Fatal(err)
	}
	task := newTask(p.ID, "work", TaskQueued)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET issue_id = ?, issue_json = '{"t":1}' WHERE id = ?`, iss.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIssue(ctx, iss.ID, issuestate.Agent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetIssue(ctx, iss.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted issue = %v", err)
	}
	var issueID sql.NullInt64
	var snapshot sql.NullString
	if err := s.db.QueryRow(`SELECT issue_id, issue_json FROM tasks WHERE id = ?`, task.ID).Scan(&issueID, &snapshot); err != nil {
		t.Fatal(err)
	}
	if issueID.Valid || snapshot.String != `{"t":1}` {
		t.Errorf("task after delete: issue_id %v, snapshot %v; want NULL and kept", issueID, snapshot)
	}
	for _, table := range []string{"issue_labels", "issue_comments"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s rows = %d after delete", table, n)
		}
	}
	evs := issueEvents(t, s, EventIssueDeleted)
	if len(evs) != 1 {
		t.Fatalf("issue.deleted = %d", len(evs))
	}
	if m := payloadOf(t, evs[0]); m["id"] != float64(iss.ID) || m["by"] != "agent" {
		t.Errorf("payload = %v", m)
	}
	if err := s.DeleteIssue(ctx, iss.ID, issuestate.Human); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

// TestDerivedActivityCountsRootTasksOnly: task_count and active read the
// issue's root tasks, lanes never (decision 5), and active is !Settled.
func TestDerivedActivityCountsRootTasksOnly(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "t"})
	link := func(taskID int64) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE tasks SET issue_id = ? WHERE id = ?`, iss.ID, taskID); err != nil {
			t.Fatal(err)
		}
	}
	done := newTask(p.ID, "done", TaskDone)
	if err := s.CreateTask(ctx, done, nil); err != nil {
		t.Fatal(err)
	}
	link(done.ID)
	// A running lane under the done root must not make the issue active.
	l := lane(t, s, p.ID, done.ID, "a", 0, TaskRunning)
	link(l.ID)
	got, err := s.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskCount != 1 || got.Active {
		t.Errorf("done root + running lane: count %d active %v; want 1 false", got.TaskCount, got.Active)
	}

	blocked := newTask(p.ID, "blocked", TaskBlocked)
	if err := s.CreateTask(ctx, blocked, nil); err != nil {
		t.Fatal(err)
	}
	link(blocked.ID)
	list, err := s.ListIssues(ctx, IssueFilter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListIssues = %d, %v", len(list), err)
	}
	if list[0].TaskCount != 2 || !list[0].Active {
		t.Errorf("+ blocked root: count %d active %v; want 2 true", list[0].TaskCount, list[0].Active)
	}
}

// TestRolledBackIssueWritePublishesNothing: the hook fires post-commit only,
// so a write that fails inside its transaction publishes nothing and leaves
// no row behind.
func TestRolledBackIssueWritePublishesNothing(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	var published []string
	s.SetEventHook(func(e *Event) { published = append(published, e.Type) })

	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "t"})
	if !slices.Equal(published, []string{EventIssueCreated}) {
		t.Fatalf("published = %v", published)
	}
	published = nil

	// The issue insert succeeds and labelling it then fails inside the same
	// transaction; the store validates no label, so a trigger forces it.
	if _, err := s.db.Exec(`CREATE TEMP TRIGGER refuse_labels BEFORE INSERT ON issue_labels
		BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIssue(ctx, NewIssue{ProjectID: p.ID, Title: "rolled back", Labels: []string{"x"}}, issuestate.Human); err == nil {
		t.Fatal("CreateIssue succeeded through the refusing trigger")
	}
	if _, err := s.SetIssueLabels(ctx, iss.ID, []string{"x"}, issuestate.Human); err == nil {
		t.Fatal("SetIssueLabels succeeded through the refusing trigger")
	}
	if len(published) != 0 {
		t.Errorf("rolled-back writes published %v", published)
	}
	list, err := s.ListIssues(ctx, IssueFilter{})
	if err != nil || len(list) != 1 {
		t.Errorf("issues = %d, %v; want only the committed one", len(list), err)
	}
	if n := len(issueEvents(t, s, allIssueEventTypes()...)); n != 1 {
		t.Errorf("event rows = %d, want 1", n)
	}
	if got, _ := s.GetIssue(ctx, iss.ID); got.Version != 1 {
		t.Errorf("version = %d after a rolled-back label write", got.Version)
	}
}
