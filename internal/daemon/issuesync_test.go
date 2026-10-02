package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The issue importer (task 130.8), driven tick by tick against cmd/fakegh
// serving a corpus file the test edits between ticks — the same shape a
// running daemon sees GitHub change in. Every claim about network traffic
// is read off the fake's argv log, which records the If-None-Match header
// on the `gh api` command line.

type corpus struct {
	t    *testing.T
	path string
	rows []map[string]any
}

// issueBase is the corpus's clock: issue n is updated n minutes after it.
var issueBase = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func issueRow(n int, state string, labels ...string) map[string]any {
	ls := []any{}
	for _, l := range labels {
		ls = append(ls, map[string]any{"name": l})
	}
	stamp := issueBase.Add(time.Duration(n) * time.Minute).Format(time.RFC3339)
	row := map[string]any{
		"id":             1000000 + n,
		"node_id":        fmt.Sprintf("I_node%d", n),
		"number":         n,
		"title":          fmt.Sprintf("issue %d", n),
		"body":           fmt.Sprintf("body of %d", n),
		"state":          state,
		"state_reason":   nil,
		"closed_at":      nil,
		"created_at":     stamp,
		"updated_at":     stamp,
		"labels":         ls,
		"assignees":      []any{map[string]any{"login": "hubot"}},
		"user":           map[string]any{"login": "octocat"},
		"milestone":      nil,
		"url":            fmt.Sprintf("https://api.github.com/repos/octo/repo/issues/%d", n),
		"repository_url": "https://api.github.com/repos/octo/repo",
		"html_url":       fmt.Sprintf("https://github.com/octo/repo/issues/%d", n),
	}
	if state == "closed" {
		row["state_reason"] = "completed"
		row["closed_at"] = stamp
	}
	return row
}

// newCorpus points fakegh at a corpus file holding rows.
func newCorpus(t *testing.T, rows ...map[string]any) *corpus {
	t.Helper()
	c := &corpus{t: t, path: filepath.Join(t.TempDir(), "issues.json"), rows: rows}
	t.Setenv("FAKEGH_ISSUES_FILE", c.path)
	c.save()
	return c
}

func (c *corpus) save() {
	c.t.Helper()
	b, err := json.Marshal(c.rows)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(c.path, b, 0o600); err != nil {
		c.t.Fatal(err)
	}
}

// edit changes issue n and moves its updated_at forward, as GitHub does.
func (c *corpus) edit(n int, at time.Time, fn func(row map[string]any)) {
	c.t.Helper()
	for _, row := range c.rows {
		if row["number"] == n {
			fn(row)
			row["updated_at"] = at.UTC().Format(time.RFC3339)
			c.save()
			return
		}
	}
	c.t.Fatalf("corpus has no issue %d", n)
}

// apiCalls is the `gh api` lines of the argv log: the issue sync's
// requests, without the credential probe and the pull request listing.
func (f *reconcileFixture) apiCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(strings.ReplaceAll(f.ghCalls(t), "\r", "")), "\n") {
		if strings.HasPrefix(line, "api ") {
			out = append(out, line)
		}
	}
	return out
}

func (f *reconcileFixture) syncState(t *testing.T) *store.IssueSyncState {
	t.Helper()
	st, err := f.store.GetIssueSyncState(t.Context(), f.project.ID)
	if err != nil {
		t.Fatalf("sync state: %v", err)
	}
	return st
}

func (f *reconcileFixture) issues(t *testing.T) map[int]*store.Issue {
	t.Helper()
	list, err := f.store.ListIssues(t.Context(), store.IssueFilter{ProjectID: f.project.ID})
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	out := map[int]*store.Issue{}
	for _, is := range list {
		if is.Remote == nil {
			t.Fatalf("issue %d has no remote", is.ID)
		}
		out[is.Remote.Number] = is
	}
	return out
}

func (f *reconcileFixture) lastEventID(t *testing.T) int64 {
	t.Helper()
	id, err := f.store.MaxEventID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *reconcileFixture) eventsAfter(t *testing.T, after int64, types ...string) []store.Event {
	t.Helper()
	evs, err := f.store.ListEvents(t.Context(), store.EventFilter{AfterID: after, Types: types})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestIssueSyncInitialImportIsOpenOnlyCappedAndResumed(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	var rows []map[string]any
	for n := 1; n <= 520; n++ {
		rows = append(rows, issueRow(n, "open"))
	}
	closed := issueRow(600, "closed")
	pull := issueRow(601, "open")
	pull["pull_request"] = map[string]any{"url": "https://api.github.com/repos/octo/repo/pulls/601"}
	newCorpus(t, append(rows, closed, pull)...)
	r := f.reconciler()

	r.Tick(t.Context())
	calls := f.apiCalls(t)
	if len(calls) != importPageCap {
		t.Fatalf("first pass made %d requests, want the cap %d:\n%s", len(calls), importPageCap, strings.Join(calls, "\n"))
	}
	if !strings.Contains(calls[0], "state=open") {
		t.Errorf("the initial import is not open-only: %s", calls[0])
	}
	st := f.syncState(t)
	if st.ImportComplete || !st.OK || st.Repo != "octo/repo" {
		t.Fatalf("after a capped pass: %+v", st)
	}
	if got := len(f.issues(t)); got != 500 {
		t.Fatalf("first pass imported %d issues, want 500", got)
	}

	r.Tick(t.Context())
	st = f.syncState(t)
	if !st.ImportComplete || !st.OK {
		t.Fatalf("the second pass did not finish the import: %+v", st)
	}
	got := f.issues(t)
	if len(got) != 520 {
		t.Fatalf("imported %d issues, want 520", len(got))
	}
	if got[600] != nil || got[601] != nil {
		t.Error("a closed issue or a pull request was imported")
	}
	if is := got[7]; is.Title != "issue 7" || is.Author != "octocat" || is.Remote.RemoteKey != "I_node7" ||
		is.Remote.Repo != "octo/repo" || is.Remote.URL != "https://github.com/octo/repo/issues/7" {
		t.Errorf("issue 7 mapped wrong: %+v / %+v", is, is.Remote)
	}
}

func TestIssueSyncIdleRepoCostsOne304PerTick(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"), issueRow(2, "open"))
	r := f.reconciler()
	r.Tick(t.Context()) // import, and the first sweep
	r.Tick(t.Context()) // the incremental question, answered 200 with its ETag
	before, events := len(f.apiCalls(t)), f.lastEventID(t)
	for range 2 {
		r.Tick(t.Context())
	}
	calls := f.apiCalls(t)[before:]
	if len(calls) != 2 {
		t.Fatalf("two idle ticks made %d requests, want 2:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	st := f.syncState(t)
	for _, c := range calls {
		if st.ETag == "" || !strings.Contains(c, "If-None-Match: "+st.ETag) {
			t.Errorf("an idle tick was not conditional on the stored ETag %q: %s", st.ETag, c)
		}
	}
	if calls[0] != calls[1] {
		t.Errorf("the question moved between idle ticks:\n%s\n%s", calls[0], calls[1])
	}
	if evs := f.eventsAfter(t, events); len(evs) != 0 {
		t.Errorf("idle ticks appended events: %+v", evs)
	}
}

func TestIssueSyncRefreshes(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	c := newCorpus(t, issueRow(1, "open", "bug", "area/api"), issueRow(2, "open"))
	r := f.reconciler()
	r.Tick(t.Context())
	is := f.issues(t)[1]
	kind, prio := "chore", 2
	if _, err := f.store.UpdateIssue(t.Context(), is.ID, is.Version,
		store.IssuePatch{Kind: &kind, Priority: &prio}, issuestate.Human); err != nil {
		t.Fatalf("set kind and priority: %v", err)
	}

	// Re-syncing unchanged issues announces nothing.
	events := f.lastEventID(t)
	r.Tick(t.Context())
	if evs := f.eventsAfter(t, events); len(evs) != 0 {
		t.Fatalf("an unchanged re-sync appended events: %+v", evs)
	}

	later := issueBase.Add(time.Hour)
	c.edit(1, later, func(row map[string]any) {
		row["labels"] = []any{map[string]any{"name": "enhancement"}}
	})
	c.edit(2, later, func(row map[string]any) {
		row["state"], row["state_reason"], row["closed_at"] = "closed", "not_planned", later.Format(time.RFC3339)
	})
	r.Tick(t.Context())
	got := f.issues(t)
	if !slices.Equal(got[1].Labels, []string{"enhancement"}) {
		t.Errorf("labels = %v, want the GitHub set [enhancement]", got[1].Labels)
	}
	if got[1].Kind != kind || got[1].Priority != prio {
		t.Errorf("a refresh overwrote local fields: kind %q priority %d", got[1].Kind, got[1].Priority)
	}
	if got[2].State != issuestate.Closed || got[2].CloseReason != issuestate.NotPlanned {
		t.Errorf("issue 2 = %s/%s, want closed/not_planned within one tick", got[2].State, got[2].CloseReason)
	}
}

func TestIssueSyncSkipsUnknownClosedAndDeletedIssues(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	c := newCorpus(t, issueRow(1, "open"), issueRow(2, "open"))
	r := f.reconciler()
	r.Tick(t.Context())
	if err := f.store.DeleteIssue(t.Context(), f.issues(t)[1].ID, issuestate.Human); err != nil {
		t.Fatalf("delete: %v", err)
	}
	later := issueBase.Add(time.Hour)
	c.edit(1, later, func(row map[string]any) { row["title"] = "edited after the delete" })
	c.rows = append(c.rows, issueRow(3, "closed"))
	c.edit(3, later, func(map[string]any) {})
	r.Tick(t.Context())
	got := f.issues(t)
	if got[1] != nil {
		t.Error("a locally deleted issue was imported again")
	}
	if got[3] != nil {
		t.Error("a closed issue nothing here knew was imported")
	}
	if !f.syncState(t).OK {
		t.Errorf("sync failed: %+v", f.syncState(t))
	}
}

func TestIssueSyncSweepMarksMovedAndMissing(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	c := newCorpus(t, issueRow(1, "open"), issueRow(2, "open"), issueRow(3, "open"))
	r := f.reconciler()
	r.Tick(t.Context())

	c.edit(1, issueBase.Add(time.Hour), func(row map[string]any) {
		row["_fake"] = map[string]any{"transferred_to": "octo/other#12"}
	})
	c.edit(2, issueBase.Add(time.Hour), func(row map[string]any) {
		row["_fake"] = map[string]any{"deleted": true}
	})
	// Not yet a day: no sweep, nothing marked.
	r.Tick(t.Context())
	if got := f.issues(t); got[1].Remote.Status != "" || got[2].Remote.Status != "" {
		t.Fatalf("swept before a day passed: %q %q", got[1].Remote.Status, got[2].Remote.Status)
	}
	r.now = func() time.Time { return time.Now().Add(fullScanEvery + time.Hour) }
	r.Tick(t.Context())
	got := f.issues(t)
	if len(got) != 3 {
		t.Fatalf("the sweep removed issues: %d left", len(got))
	}
	if got[1].Remote.Status != store.RemoteStatusMoved || !strings.Contains(got[1].Remote.RemoteJSON, "octo/other/issues/12") {
		t.Errorf("transferred issue: status %q json %s", got[1].Remote.Status, got[1].Remote.RemoteJSON)
	}
	if got[2].Remote.Status != store.RemoteStatusMissing {
		t.Errorf("deleted issue: status %q, want missing", got[2].Remote.Status)
	}
	if got[3].Remote.Status != "" || got[1].State != issuestate.Open {
		t.Errorf("the sweep touched what it should not: %+v %+v", got[3].Remote, got[1])
	}
}

func TestIssueSyncMakesNoCallWhenSwitchedOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(f *reconcileFixture)
	}{
		{"disabled", func(f *reconcileFixture) { f.cfg.GitHub.Enabled = false }},
		{"poll 0", func(f *reconcileFixture) { f.cfg.GitHub.PollInterval = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "https://github.com/octo/repo.git")
			newCorpus(t, issueRow(1, "open"))
			tc.set(f)
			r := f.reconciler()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { r.Run(ctx); close(done) }()
			r.RequestSync(f.project.ID)
			time.Sleep(300 * time.Millisecond)
			cancel()
			<-done
			if calls := f.ghCalls(t); calls != "" {
				t.Fatalf("a switched-off sync invoked gh:\n%s", calls)
			}
			if _, err := f.store.GetIssueSyncState(t.Context(), f.project.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("a switched-off sync wrote a row: %v", err)
			}
		})
	}
}

func TestIssueSyncGatesRecordWhy(t *testing.T) {
	f := newFixture(t, "https://gitlab.com/octo/repo.git")
	r := f.reconciler()
	events := f.lastEventID(t)
	r.Tick(t.Context())
	r.Tick(t.Context())
	if st := f.syncState(t); st.OK || st.Reason != syncReasonNotGitHub {
		t.Errorf("non-GitHub project: %+v", st)
	}
	if evs := f.eventsAfter(t, events, store.EventIssueSyncChanged); len(evs) != 1 {
		t.Errorf("two failing ticks announced %d sync changes, want 1", len(evs))
	}
	if calls := f.ghCalls(t); calls != "" {
		t.Errorf("a non-GitHub project invoked gh:\n%s", calls)
	}

	nc := newFixture(t, "https://github.com/octo/repo.git")
	nc.client = nil
	nc.reconciler().Tick(t.Context())
	if st := nc.syncState(t); st.OK || st.Reason != syncReasonNoClient {
		t.Errorf("no client: %+v", st)
	}
}

func TestIssueSyncStopsOnARepointedOrigin(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"))
	r := f.reconciler()
	r.Tick(t.Context())
	testrepo.Run(t, f.project.Path, "remote", "set-url", "origin", "https://github.com/octo/other.git")
	before := len(f.apiCalls(t))
	r.Tick(t.Context())
	if calls := f.apiCalls(t)[before:]; len(calls) != 0 {
		t.Errorf("a re-pointed origin was listed: %v", calls)
	}
	st := f.syncState(t)
	if st.OK || st.Reason != syncReasonOriginChanged || st.Repo != "octo/repo" {
		t.Errorf("re-pointed origin: %+v", st)
	}
	if is := f.issues(t)[1]; is.Remote.Repo != "octo/repo" || is.Remote.RemoteKey != "I_node1" {
		t.Errorf("an issue was re-keyed: %+v", is.Remote)
	}
}

func TestIssueSyncChangedFiresOnTransitionsOnly(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"))
	r := f.reconciler()
	events := f.lastEventID(t)
	r.Tick(t.Context()) // ok, and a first ok is no transition
	t.Setenv("FAKEGH_SCENARIO", "unreachable")
	r.Tick(t.Context())
	r.Tick(t.Context())
	if st := f.syncState(t); st.OK || st.Reason != "unreachable" {
		t.Errorf("unreachable: %+v", st)
	}
	t.Setenv("FAKEGH_SCENARIO", "")
	r.Tick(t.Context())
	evs := f.eventsAfter(t, events, store.EventIssueSyncChanged)
	var oks []bool
	for _, ev := range evs {
		var body struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(ev.Payload, &body); err != nil {
			t.Fatal(err)
		}
		oks = append(oks, body.OK)
	}
	if !slices.Equal(oks, []bool{false, true}) {
		t.Errorf("sync_changed ok values = %v, want [false true]", oks)
	}
}

func TestIssueSyncBacksOffUntilTheRateLimitResets(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"))
	r := f.reconciler()
	t.Setenv("FAKEGH_SCENARIO", "rate-limited")
	r.Tick(t.Context())
	st := f.syncState(t)
	if st.OK || st.Reason != "rate_limited" || st.RateLimitedUntil == nil || !st.RateLimitedUntil.After(time.Now()) {
		t.Fatalf("rate limited: %+v", st)
	}
	t.Setenv("FAKEGH_SCENARIO", "")
	before := len(f.apiCalls(t))
	r.Tick(t.Context())
	if calls := f.apiCalls(t)[before:]; len(calls) != 0 {
		t.Fatalf("a rate-limited project was polled before its reset: %v", calls)
	}
	reset := *st.RateLimitedUntil
	r.now = func() time.Time { return reset.Add(time.Second) }
	r.Tick(t.Context())
	if st := f.syncState(t); !st.OK || st.RateLimitedUntil != nil || len(f.issues(t)) != 1 {
		t.Errorf("after the reset: %+v", st)
	}
}

func TestIssueSyncNowWakesTheLoop(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	f.cfg.GitHub.PollInterval = config.Duration(time.Hour) // only a wake syncs again
	c := newCorpus(t, issueRow(1, "open"))
	r := f.reconciler()
	f.store.OnIssueSyncRequested(r.RequestSync)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, func() bool { return len(f.issues(t)) == 1 })
	c.rows = append(c.rows, issueRow(2, "open"))
	c.edit(2, issueBase.Add(time.Hour), func(map[string]any) {})
	if err := f.store.RequestIssueSync(t.Context(), f.project.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	waitFor(t, func() bool { return len(f.issues(t)) == 2 })
	waitFor(t, func() bool { return f.syncState(t).RequestedAt == nil })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
