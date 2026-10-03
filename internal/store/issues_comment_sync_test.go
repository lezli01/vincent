package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

func remoteComment(key, body string, updated time.Time) RemoteIssueComment {
	return RemoteIssueComment{
		RemoteKey: key, Author: "octo", Body: body,
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: updated,
	}
}

// TestUpsertRemoteIssueComment (task 130.16, decision 24.4): an insert
// announces comment_added, an edit updates in place and announces
// comment_updated, an unchanged re-list writes and announces nothing — all
// by sync, none carrying text.
func TestUpsertRemoteIssueComment(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))
	commentEvents := func() []Event {
		return issueEvents(t, s, EventIssueCommentAdded, EventIssueCommentUpdated)
	}

	t1 := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	changed, err := s.UpsertRemoteIssueComment(ctx, iss.ID, remoteComment("101", "first", t1))
	if err != nil || !changed {
		t.Fatalf("insert = %v, %v; want true", changed, err)
	}
	list, err := s.ListIssueComments(ctx, iss.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("after insert = %+v, %v", list, err)
	}
	inserted := *list[0]
	if inserted.RemoteKey != "101" || inserted.Body != "first" || inserted.Author != "octo" ||
		!inserted.CreatedAt.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !inserted.UpdatedAt.Equal(t1) {
		t.Errorf("inserted = %+v", inserted)
	}
	evs := commentEvents()
	if len(evs) != 1 || evs[0].Type != EventIssueCommentAdded {
		t.Fatalf("events after insert = %+v", evs)
	}
	wantPayload := map[string]any{"id": float64(iss.ID), "comment_id": float64(inserted.ID), "by": "sync"}
	if got := payloadOf(t, evs[0]); !reflect.DeepEqual(got, wantPayload) {
		t.Errorf("added payload = %v, want %v", got, wantPayload)
	}

	// The same comment again: nothing.
	changed, err = s.UpsertRemoteIssueComment(ctx, iss.ID, remoteComment("101", "first", t1))
	if err != nil || changed {
		t.Fatalf("no-op = %v, %v; want false", changed, err)
	}
	if evs := commentEvents(); len(evs) != 1 {
		t.Errorf("no-op announced: %+v", evs)
	}

	// An edit: same row, new body and updated_at, created_at kept.
	t2 := t1.Add(time.Hour)
	edited := remoteComment("101", "first, edited", t2)
	edited.CreatedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) // ignored on update
	changed, err = s.UpsertRemoteIssueComment(ctx, iss.ID, edited)
	if err != nil || !changed {
		t.Fatalf("edit = %v, %v; want true", changed, err)
	}
	list, _ = s.ListIssueComments(ctx, iss.ID)
	if len(list) != 1 || list[0].ID != inserted.ID || list[0].Body != "first, edited" ||
		!list[0].UpdatedAt.Equal(t2) || !list[0].CreatedAt.Equal(inserted.CreatedAt) {
		t.Errorf("after edit = %+v", list)
	}
	evs = commentEvents()
	if len(evs) != 2 || evs[1].Type != EventIssueCommentUpdated {
		t.Fatalf("events after edit = %+v", evs)
	}
	if got := payloadOf(t, evs[1]); !reflect.DeepEqual(got, wantPayload) {
		t.Errorf("updated payload = %v, want %v", got, wantPayload)
	}

	// Only updated_at moving is still an update; an author change too.
	for _, c := range []RemoteIssueComment{
		remoteComment("101", "first, edited", t2.Add(time.Minute)),
		{RemoteKey: "101", Author: "hubot", Body: "first, edited", UpdatedAt: t2.Add(time.Minute)},
	} {
		if changed, err := s.UpsertRemoteIssueComment(ctx, iss.ID, c); err != nil || !changed {
			t.Errorf("upsert %+v = %v, %v; want true", c, changed, err)
		}
	}
	if evs := commentEvents(); len(evs) != 4 {
		t.Errorf("events = %d, want 4", len(evs))
	}
	for _, e := range commentEvents() {
		if strings.Contains(string(e.Payload), "first") {
			t.Errorf("payload carries text: %s", e.Payload)
		}
	}

	if _, err := s.UpsertRemoteIssueComment(ctx, 9999, remoteComment("1", "x", t1)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown issue = %v, want ErrNotFound", err)
	}
	if _, err := s.UpsertRemoteIssueComment(ctx, iss.ID, remoteComment("", "x", t1)); err == nil {
		t.Error("empty remote key accepted")
	}
}

// TestRemoteCommentKeyIsPerIssue: the unique index is (issue_id, remote_key)
// — one GitHub comment may land under two issues (two projects sharing an
// origin) but never twice under one — and local comments, NULL-keyed, never
// collide with each other or with a mirrored one.
func TestRemoteCommentKeyIsPerIssue(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")
	a := mustUpsertRemote(t, s, remoteIn(p1.ID, "I_1", 1))
	b := mustUpsertRemote(t, s, remoteIn(p2.ID, "I_1", 1))
	t1 := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := s.UpsertRemoteIssueComment(ctx, id, remoteComment("101", "c", t1)); err != nil {
			t.Fatalf("upsert on issue %d: %v", id, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO issue_comments (issue_id, body, remote_key, created_at, updated_at)
		VALUES (?, 'dup', '101', 'x', 'x')`, a.ID); err == nil {
		t.Error("a second row with the same (issue_id, remote_key) was accepted")
	}

	for _, body := range []string{"local one", "local two"} {
		if _, err := s.AddIssueComment(ctx, a.ID, "me", body, "", issuestate.Human); err != nil {
			t.Fatalf("AddIssueComment: %v", err)
		}
	}
	list, err := s.ListIssueComments(ctx, a.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("thread = %+v, %v; want one mirrored and two local", list, err)
	}
	var local int
	for _, c := range list {
		if c.RemoteKey == "" {
			local++
		}
	}
	if local != 2 {
		t.Errorf("local comments = %d, want 2", local)
	}
}

// TestIssueIDByRemoteNumber: scoped by project, provider and repo, and a
// tombstone never answers.
func TestIssueIDByRemoteNumber(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	other := testProject(t, s, "p2")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_7", 7))
	moved := remoteIn(p.ID, "I_8", 7)
	moved.Repo = "o/old"
	old := mustUpsertRemote(t, s, moved)
	mustUpsertRemote(t, s, remoteIn(other.ID, "I_7", 7))

	if id, ok, err := s.IssueIDByRemoteNumber(ctx, p.ID, "github", "o/r", 7); err != nil || !ok || id != iss.ID {
		t.Errorf("o/r#7 = %d, %v, %v; want %d", id, ok, err, iss.ID)
	}
	if id, ok, err := s.IssueIDByRemoteNumber(ctx, p.ID, "github", "o/old", 7); err != nil || !ok || id != old.ID {
		t.Errorf("o/old#7 = %d, %v, %v; want %d", id, ok, err, old.ID)
	}
	for _, q := range []struct {
		provider, repo string
		number         int
	}{{"github", "o/r", 8}, {"github", "o/x", 7}, {"gitlab", "o/r", 7}} {
		if id, ok, err := s.IssueIDByRemoteNumber(ctx, p.ID, q.provider, q.repo, q.number); err != nil || ok {
			t.Errorf("%+v = %d, %v, %v; want none", q, id, ok, err)
		}
	}

	if err := s.DeleteIssue(ctx, iss.ID, issuestate.Human); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	if id, ok, err := s.IssueIDByRemoteNumber(ctx, p.ID, "github", "o/r", 7); err != nil || ok {
		t.Errorf("tombstone answered: %d, %v, %v", id, ok, err)
	}
}

// TestIssueSyncStateCommentFieldsRoundTrip: the comment pass's bookkeeping
// is written and read like the issue pass's, and independently of it.
func TestIssueSyncStateCommentFieldsRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	wm := time.Date(2026, 9, 3, 1, 2, 3, 456, time.UTC)
	since := wm.Add(-time.Minute)
	want := IssueSyncState{
		ProjectID: p.ID, Provider: "github", Repo: "o/r", OK: true,
		CommentWatermark: &wm, CommentSince: &since, CommentETag: `W/"c"`,
	}
	if err := s.PutIssueSyncState(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssueSyncState(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", *got, want)
	}
	// Overwriting clears them like any other field of the row.
	want.CommentWatermark, want.CommentSince, want.CommentETag = nil, nil, ""
	if err := s.PutIssueSyncState(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetIssueSyncState(ctx, p.ID); got.CommentWatermark != nil || got.CommentSince != nil || got.CommentETag != "" {
		t.Errorf("cleared = %+v", got)
	}
}

// TestTaskSnapshotCarriesTheThread (task 130.16, decision 24.5): a task
// created from an issue freezes its whole thread, oldest first; a later
// comment leaves the stored snapshot alone; a fan-out lane keeps its
// parent's clone; an issue with no comments omits the key.
func TestTaskSnapshotCarriesTheThread(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustUpsertRemote(t, s, remoteIn(p.ID, "I_1", 1))
	// The mirrored comment is older than the local one, and inserted second:
	// the thread is ordered by created_at, not insertion.
	if _, err := s.AddIssueComment(ctx, iss.ID, "me", "local reply", "", issuestate.Human); err != nil {
		t.Fatal(err)
	}
	early := remoteComment("101", strings.Repeat("long ", 2000), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := s.UpsertRemoteIssueComment(ctx, iss.ID, early); err != nil {
		t.Fatal(err)
	}
	thread, _ := s.ListIssueComments(ctx, iss.ID)
	want := make([]IssueSnapshotComment, 0, len(thread))
	for _, c := range thread {
		want = append(want, IssueSnapshotComment{Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	if len(want) != 2 || want[0].Author != "octo" {
		t.Fatalf("thread order = %+v", want)
	}

	parent := newTask(p.ID, "from issue", TaskQueued)
	parent.IssueID = &iss.ID
	parent.Issue = NewIssueSnapshot(iss, time.Now())
	if err := s.CreateTask(ctx, parent, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := s.GetTask(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Issue.Comments, want) {
		t.Errorf("snapshot thread =\n %+v\nwant\n %+v", got.Issue.Comments, want)
	}

	// A comment after creation reaches neither the stored snapshot nor a
	// lane, which inherits its parent's clone.
	if _, err := s.AddIssueComment(ctx, iss.ID, "me", "too late", "", issuestate.Human); err != nil {
		t.Fatal(err)
	}
	lane := newTask(p.ID, "lane", TaskQueued)
	lane.ParentTaskID = &parent.ID
	lane.IssueID = &iss.ID
	lane.Issue = got.Issue.Clone()
	if err := s.CreateTask(ctx, lane, nil); err != nil {
		t.Fatalf("CreateTask(lane): %v", err)
	}
	for _, id := range []int64{parent.ID, lane.ID} {
		tk, err := s.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(tk.Issue.Comments, want) {
			t.Errorf("task %d thread = %+v, want the creation-time %+v", id, tk.Issue.Comments, want)
		}
	}

	// No comments: the key is left out of issue_json.
	bare := testIssue(t, s, p.ID, "quiet")
	quiet := newTask(p.ID, "quiet", TaskQueued)
	quiet.IssueID = &bare.ID
	quiet.Issue = NewIssueSnapshot(bare, time.Now())
	if err := s.CreateTask(ctx, quiet, nil); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT issue_json FROM tasks WHERE id = ?`, quiet.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["comments"]; ok {
		t.Errorf("empty thread encoded: %s", raw)
	}
}

func TestIssueSnapshotCloneCopiesComments(t *testing.T) {
	orig := &IssueSnapshot{ID: 1, Comments: []IssueSnapshotComment{{Author: "a", Body: "b"}}}
	c := orig.Clone()
	if !reflect.DeepEqual(c, orig) {
		t.Fatalf("Clone = %+v, want %+v", c, orig)
	}
	c.Comments[0].Body = "mutated"
	if orig.Comments[0].Body == "mutated" {
		t.Error("Clone shares the Comments backing array")
	}
}
