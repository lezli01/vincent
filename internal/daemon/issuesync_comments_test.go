package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github/fakeissues"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The comment pass and the per-issue backfill (task 130 decision 24,
// 130.16), driven tick by tick against cmd/fakegh the way the issue pass's
// tests are. Requests are counted off the fake's request log, which records
// each one's path and status — so "one comments request, answered 304" is
// read off what the fake answered, not inferred from argv.

// commentBase is the comment corpus's clock: a day after issueBase, so
// every comment postdates the issues it is on.
var commentBase = issueBase.Add(24 * time.Hour)

// commentRow is a REST issue comment by hubot on issue or pull request n.
func commentRow(id int64, n int, body string, at time.Time) map[string]any {
	stamp := at.UTC().Format(time.RFC3339)
	return map[string]any{
		"id":         id,
		"node_id":    fmt.Sprintf("IC_node%d", id),
		"url":        fmt.Sprintf("https://api.github.com/repos/octo/repo/issues/comments/%d", id),
		"html_url":   fmt.Sprintf("https://github.com/octo/repo/issues/%d#issuecomment-%d", n, id),
		"issue_url":  fmt.Sprintf("https://api.github.com/repos/octo/repo/issues/%d", n),
		"body":       body,
		"user":       map[string]any{"login": "hubot"},
		"created_at": stamp,
		"updated_at": stamp,
	}
}

// addComment files a comment row; the fake recomputes the issue's count.
func (c *corpus) addComment(id int64, n int, body string, at time.Time) {
	c.t.Helper()
	c.rows = append(c.rows, commentRow(id, n, body, at))
	c.save()
}

// editComment rewrites comment id's body and moves its updated_at to at.
func (c *corpus) editComment(id int64, body string, at time.Time) {
	c.t.Helper()
	for _, row := range c.rows {
		if row["id"] == id && row["issue_url"] != nil {
			row["body"], row["updated_at"] = body, at.UTC().Format(time.RFC3339)
			c.save()
			return
		}
	}
	c.t.Fatalf("corpus has no comment %d", id)
}

// removeComment deletes comment id, as deleting it on GitHub does.
func (c *corpus) removeComment(id int64) {
	c.t.Helper()
	for i, row := range c.rows {
		if row["id"] == id && row["issue_url"] != nil {
			c.rows = append(c.rows[:i], c.rows[i+1:]...)
			c.save()
			return
		}
	}
	c.t.Fatalf("corpus has no comment %d", id)
}

// requestLog points the fake's request log at a fresh file.
func requestLog(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "requests.log")
	t.Setenv("FAKEGH_REQUESTS_FILE", log)
	return log
}

// countRequests is fakeissues.CountRequests for a GET of path under
// octo/repo; status 0 counts every answer.
func countRequests(t *testing.T, log, path string, status int) int {
	t.Helper()
	n, err := fakeissues.CountRequests(log, "GET", "repos/octo/repo/"+path, status)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

const commentListing = "issues/comments"

func (f *reconcileFixture) comments(t *testing.T, issueID int64) []*store.IssueComment {
	t.Helper()
	list, err := f.store.ListIssueComments(t.Context(), issueID)
	if err != nil {
		t.Fatalf("list comments of issue %d: %v", issueID, err)
	}
	return list
}

// commentEvent is an issue.comment_* payload: ids and an actor, never text.
type commentEvent struct {
	ID        int64  `json:"id"`
	CommentID int64  `json:"comment_id"`
	By        string `json:"by"`
}

func (f *reconcileFixture) commentEvents(t *testing.T, after int64) []store.Event {
	t.Helper()
	return f.eventsAfter(t, after, store.EventIssueCommentAdded, store.EventIssueCommentUpdated)
}

// oneCommentEvent asserts evs is exactly one evType by sync about comment
// commentID on issue issueID, and that it carries no text.
func oneCommentEvent(t *testing.T, evs []store.Event, evType string, issueID, commentID int64) {
	t.Helper()
	if len(evs) != 1 || evs[0].Type != evType {
		t.Fatalf("events = %+v, want exactly one %s", evs, evType)
	}
	var body commentEvent
	if err := json.Unmarshal(evs[0].Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != issueID || body.CommentID != commentID || body.By != string(issuestate.Sync) {
		t.Errorf("%s payload = %s, want issue %d, comment %d, by sync", evType, evs[0].Payload, issueID, commentID)
	}
	var raw map[string]any
	if err := json.Unmarshal(evs[0].Payload, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["body"]; ok {
		t.Errorf("%s carries the comment's text: %s", evType, evs[0].Payload)
	}
}

func TestCommentSyncIdleTickIsOne304(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	log := requestLog(t)
	newCorpus(t, issueRow(1, "open"), issueRow(2, "open"))
	r := f.reconciler()
	r.Tick(t.Context())
	if n := countRequests(t, log, commentListing, 0); n != 1 {
		t.Fatalf("the first tick listed comments %d times, want once", n)
	}
	events := f.lastEventID(t)
	r.Tick(t.Context())
	if n := countRequests(t, log, commentListing, 0); n != 2 {
		t.Fatalf("the idle tick listed comments %d times, want once", n-1)
	}
	if n := countRequests(t, log, commentListing, 304); n != 1 {
		t.Errorf("the idle tick's comment listing was not a 304 (%d 304s)", n)
	}
	if st := f.syncState(t); !st.OK || st.CommentETag == "" {
		t.Errorf("sync state after an idle tick: %+v", st)
	}
	if evs := f.eventsAfter(t, events); len(evs) != 0 {
		t.Errorf("an idle tick appended events: %+v", evs)
	}
}

func TestCommentSyncMirrorsAddedEditedAndKeepsRemoved(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	log := requestLog(t)
	c := newCorpus(t, issueRow(1, "open"), issueRow(2, "open"))
	r := f.reconciler()
	r.Tick(t.Context())
	issue := f.issues(t)[1]

	events := f.lastEventID(t)
	c.addComment(7001, 1, "first", commentBase)
	r.Tick(t.Context())
	got := f.comments(t, issue.ID)
	if len(got) != 1 || got[0].Body != "first" || got[0].Author != "hubot" || got[0].RemoteKey != "7001" {
		t.Fatalf("mirrored thread = %+v, want hubot's one comment keyed 7001", got)
	}
	oneCommentEvent(t, f.commentEvents(t, events), store.EventIssueCommentAdded, issue.ID, got[0].ID)
	if n := countRequests(t, log, "issues/1/comments", 0); n != 0 {
		t.Errorf("a known issue's comment was backfilled (%d per-issue reads)", n)
	}

	// An unchanged re-list writes nothing and announces nothing.
	events = f.lastEventID(t)
	r.Tick(t.Context())
	r.Tick(t.Context())
	if evs := f.eventsAfter(t, events); len(evs) != 0 {
		t.Fatalf("an unchanged re-list appended events: %+v", evs)
	}

	c.editComment(7001, "edited", commentBase.Add(time.Hour))
	r.Tick(t.Context())
	edited := f.comments(t, issue.ID)
	if len(edited) != 1 || edited[0].ID != got[0].ID || edited[0].Body != "edited" {
		t.Fatalf("after the edit: %+v, want comment %d updated in place", edited, got[0].ID)
	}
	oneCommentEvent(t, f.commentEvents(t, events), store.EventIssueCommentUpdated, issue.ID, got[0].ID)

	// A comment deleted on GitHub is never detected (decision 24.1).
	events = f.lastEventID(t)
	c.removeComment(7001)
	r.Tick(t.Context())
	if kept := f.comments(t, issue.ID); len(kept) != 1 || kept[0].Body != "edited" {
		t.Errorf("a comment removed on GitHub was not kept: %+v", kept)
	}
	if evs := f.commentEvents(t, events); len(evs) != 0 {
		t.Errorf("a removed comment announced something: %+v", evs)
	}
}

func TestCommentSyncIgnoresPullAndUnimportedComments(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	log := requestLog(t)
	pull := issueRow(601, "open")
	pull["pull_request"] = map[string]any{"url": "https://api.github.com/repos/octo/repo/pulls/601"}
	c := newCorpus(t, issueRow(1, "open"), issueRow(600, "closed"), pull)
	r := f.reconciler()
	r.Tick(t.Context())
	events := f.lastEventID(t)
	c.addComment(7001, 601, "a pull request conversation comment", commentBase)
	c.addComment(7002, 600, "on an issue never imported", commentBase)
	r.Tick(t.Context())
	if evs := f.commentEvents(t, events); len(evs) != 0 {
		t.Errorf("an unattributable comment was mirrored: %+v", evs)
	}
	got := f.issues(t)
	if len(got) != 1 || len(f.comments(t, got[1].ID)) != 0 {
		t.Errorf("issues %v, issue 1's thread %v: want only #1, with no comments", got, f.comments(t, got[1].ID))
	}
	if st := f.syncState(t); !st.OK || st.CommentWatermark == nil || st.CommentWatermark.Before(commentBase) {
		t.Errorf("the watermark did not pass the ignored comments: %+v", st)
	}
	for _, n := range []int{600, 601} {
		if k := countRequests(t, log, fmt.Sprintf("issues/%d/comments", n), 0); k != 0 {
			t.Errorf("#%d's thread was read %d times", n, k)
		}
	}
}

func TestCommentSyncBackfillsAnIssueImportedLater(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	log := requestLog(t)
	c := newCorpus(t, issueRow(1, "open"), issueRow(5, "closed"), issueRow(6, "closed"),
		commentRow(7001, 1, "on one", commentBase), commentRow(7002, 5, "on five", commentBase))
	r := f.reconciler()

	// The first import of #1 reads its thread once, and the comment pass's
	// re-list of the same comment is silent: one comment_added, not two.
	events := f.lastEventID(t)
	r.Tick(t.Context())
	one := f.issues(t)[1]
	if n := countRequests(t, log, "issues/1/comments", 0); n != 1 {
		t.Fatalf("#1's thread was read %d times on import, want once", n)
	}
	if got := f.comments(t, one.ID); len(got) != 1 || got[0].Body != "on one" {
		t.Fatalf("#1's thread = %+v", got)
	}
	if evs := f.commentEvents(t, events); len(evs) != 1 {
		t.Errorf("#1's one comment announced %d times: %+v", len(evs), evs)
	}
	r.Tick(t.Context())
	if st := f.syncState(t); st.CommentWatermark == nil || st.CommentWatermark.Before(commentBase) {
		t.Fatalf("the comment watermark did not pass #5's comment: %+v", st)
	}

	// #5 and #6 reopen after the watermark passed #5's comment.
	later := commentBase.Add(time.Hour)
	reopen := func(row map[string]any) {
		row["state"], row["state_reason"], row["closed_at"] = "open", "reopened", nil
	}
	c.edit(5, later, reopen)
	c.edit(6, later, reopen)
	r.Tick(t.Context())
	got := f.issues(t)
	if got[5] == nil || got[6] == nil {
		t.Fatalf("#5 and #6 were not imported: %v", got)
	}
	if thread := f.comments(t, got[5].ID); len(thread) != 1 || thread[0].Body != "on five" || thread[0].RemoteKey != "7002" {
		t.Errorf("#5's earlier thread was not backfilled: %+v", thread)
	}
	r.Tick(t.Context())
	if n := countRequests(t, log, "issues/5/comments", 0); n != 1 {
		t.Errorf("#5's thread was read %d times, want exactly once", n)
	}
	if n := countRequests(t, log, "issues/6/comments", 0); n != 0 {
		t.Errorf("#6, with no comments, had its thread read %d times", n)
	}
	if n := countRequests(t, log, "issues/1/comments", 0); n != 1 {
		t.Errorf("#1's thread was read again: %d reads", n)
	}
}

func TestCommentSyncBackfillsAdoptedPlaceholders(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	log := requestLog(t)
	// #1 is adopted by the open listing, #2 — closed on GitHub — by the
	// sweep's probe; #3 has no comments.
	newCorpus(t, issueRow(1, "open"), issueRow(2, "closed"), issueRow(3, "open"),
		commentRow(7001, 1, "on one", commentBase), commentRow(7002, 2, "on two", commentBase))
	one := f.placeholder(t, "octo/repo", 1, issuestate.Open)
	two := f.placeholder(t, "octo/repo", 2, issuestate.Open)
	f.placeholder(t, "octo/repo", 3, issuestate.Open)

	f.reconciler().Tick(t.Context())
	for n, c := range map[int]struct {
		id   int64
		body string
	}{1: {one.ID, "on one"}, 2: {two.ID, "on two"}} {
		if thread := f.comments(t, c.id); len(thread) != 1 || thread[0].Body != c.body {
			t.Errorf("#%d's thread after adoption = %+v, want %q", n, thread, c.body)
		}
		if k := countRequests(t, log, fmt.Sprintf("issues/%d/comments", n), 0); k != 1 {
			t.Errorf("#%d's thread was read %d times, want once", n, k)
		}
	}
	if k := countRequests(t, log, "issues/3/comments", 0); k != 0 {
		t.Errorf("#3, with no comments, had its thread read %d times", k)
	}
}

func TestCommentSyncMakesNoRequestWhenDeferredOrStopped(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	log := requestLog(t)
	newCorpus(t, issueRow(1, "open"))
	r := f.reconciler()

	t.Setenv("FAKEGH_SCENARIO", "rate-limited")
	r.Tick(t.Context())
	t.Setenv("FAKEGH_SCENARIO", "")
	r.Tick(t.Context()) // deferred until the reset
	if n := countRequests(t, log, commentListing, 0); n != 0 {
		t.Fatalf("a rate-limited project listed comments %d times", n)
	}
	reset := *f.syncState(t).RateLimitedUntil
	r.now = func() time.Time { return reset.Add(time.Second) }
	r.Tick(t.Context())
	if n := countRequests(t, log, commentListing, 0); n != 1 {
		t.Fatalf("after the reset comments were listed %d times, want once", n)
	}

	testrepo.Run(t, f.project.Path, "remote", "set-url", "origin", "https://github.com/octo/other.git")
	r.Tick(t.Context())
	if st := f.syncState(t); st.Reason != syncReasonOriginChanged {
		t.Fatalf("re-pointed origin: %+v", st)
	}
	if n := countRequests(t, log, commentListing, 0); n != 1 {
		t.Errorf("a re-pointed origin listed comments (%d listings)", n)
	}
}
