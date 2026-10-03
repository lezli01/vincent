package daemon

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Backfill placeholders against the importer (task 130 decision 21.3): the
// backfill of task 035 snapshots leaves remotes keyed "legacy:{repo}#{n}"
// and never synced, and the importer adopts each one by number the first
// time GitHub reports the issue — by listing or by the daily sweep.

// placeholder writes a backfill-shaped remote straight into the database,
// the way migration 0040 leaves it: imported issue, legacy key, synced_at
// NULL. A second connection writes the NULLs; the store has no API for a
// row it never writes itself.
func (f *reconcileFixture) placeholder(t *testing.T, repo string, number int, state issuestate.State) *store.Issue {
	t.Helper()
	key := fmt.Sprintf("%s%s#%d", store.LegacyRemoteKeyPrefix, repo, number)
	in := store.RemoteIssue{
		ProjectID: f.project.ID, Provider: issueProvider, RemoteKey: key, Repo: repo, Number: number,
		URL: fmt.Sprintf("https://github.com/%s/issues/%d", repo, number), Title: "snapshot title",
		State: state,
	}
	if state == issuestate.Closed {
		in.CloseReason = issuestate.Completed
	}
	iss, _, err := f.store.UpsertRemoteIssue(t.Context(), in, issuestate.Sync)
	if err != nil {
		t.Fatalf("seed placeholder: %v", err)
	}
	p := filepath.ToSlash(f.store.Path())
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	db, err := sql.Open("sqlite", "file://"+(&url.URL{Path: p}).EscapedPath()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `
		UPDATE issue_remotes SET synced_at = NULL, remote_json = NULL, remote_updated_at = NULL
		WHERE remote_key = ?`, key); err != nil {
		t.Fatalf("clear synced_at: %v", err)
	}
	return iss
}

func (f *reconcileFixture) issue(t *testing.T, id int64) *store.Issue {
	t.Helper()
	iss, err := f.store.GetIssue(t.Context(), id)
	if err != nil {
		t.Fatalf("get issue %d: %v", id, err)
	}
	return iss
}

func (f *reconcileFixture) noWriteBack(t *testing.T, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		if ws, err := f.store.IssueWrites(t.Context(), id); err != nil || len(ws) != 0 {
			t.Errorf("issue %d: adoption queued write-back %+v, %v", id, ws, err)
		}
	}
}

func TestIssueSyncAdoptsBackfillPlaceholdersOnFirstSync(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	// #1 closed locally, open on GitHub; #2 open locally, closed on GitHub;
	// #3 open on both.
	newCorpus(t, issueRow(1, "open", "bug"), issueRow(2, "closed"), issueRow(3, "open"))
	one := f.placeholder(t, "octo/repo", 1, issuestate.Closed)
	two := f.placeholder(t, "octo/repo", 2, issuestate.Open)
	three := f.placeholder(t, "octo/repo", 3, issuestate.Open)

	// One tick: the open listing adopts #1 and #3, and the first sweep —
	// due at once — probes #2, which the open listing lacked.
	f.reconciler().Tick(t.Context())
	if st := f.syncState(t); !st.OK || !st.ImportComplete {
		t.Fatalf("sync: %+v", st)
	}
	if got := len(f.issues(t)); got != 3 {
		t.Fatalf("%d issues after the first sync, want the 3 placeholders and no duplicates", got)
	}
	for n, c := range map[int]struct {
		id    int64
		state issuestate.State
	}{1: {one.ID, issuestate.Open}, 2: {two.ID, issuestate.Closed}, 3: {three.ID, issuestate.Open}} {
		is := f.issue(t, c.id)
		if is.Remote.RemoteKey != fmt.Sprintf("I_node%d", n) || is.Remote.SyncedAt == nil {
			t.Errorf("#%d not re-keyed: %+v", n, is.Remote)
		}
		if is.State != c.state || is.Title != fmt.Sprintf("issue %d", n) {
			t.Errorf("#%d = %s %q, want GitHub's %s %q", n, is.State, is.Title, c.state, fmt.Sprintf("issue %d", n))
		}
	}
	if is := f.issue(t, one.ID); len(is.Labels) != 1 || is.Labels[0] != "bug" {
		t.Errorf("#1 labels = %v, want GitHub's [bug]", is.Labels)
	}
	f.noWriteBack(t, one.ID, two.ID, three.ID)
}

func TestIssueSyncNeverMatchesARealRemoteByNumber(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"), issueRow(2, "open"))
	// A real node id that GitHub no longer reports at #1, never synced, and
	// a placeholder of another repository at #2.
	stale, _, err := f.store.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
		ProjectID: f.project.ID, Provider: issueProvider, RemoteKey: "I_stale", Repo: "octo/repo", Number: 1,
		Title: "stale", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := f.placeholder(t, "octo/elsewhere", 2, issuestate.Open)

	f.reconciler().Tick(t.Context())
	if is := f.issue(t, stale.ID); is.Remote.RemoteKey != "I_stale" || is.Title != "stale" {
		t.Errorf("a real node id row was matched by number: %+v / %+v", is, is.Remote)
	}
	if is := f.issue(t, elsewhere.ID); is.Remote.SyncedAt != nil || is.Remote.Status != "" ||
		!strings.HasPrefix(is.Remote.RemoteKey, store.LegacyRemoteKeyPrefix) {
		t.Errorf("another repository's placeholder was matched or probed: %+v", is.Remote)
	}
	list, err := f.store.ListIssues(t.Context(), store.IssueFilter{ProjectID: f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Errorf("%d issues, want 4: the two seeded rows kept and #1 and #2 imported beside them", len(list))
	}
}

func TestIssueSyncDailySweepAdoptsAndClosesABackfilledIssue(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	// #5 closed on GitHub before the watermark, so no incremental listing
	// will ever report it: only the sweep can.
	newCorpus(t, issueRow(5, "closed"), issueRow(9, "open"))
	r := f.reconciler()
	r.Tick(t.Context())
	if st := f.syncState(t); !st.ImportComplete || st.LastFullScanAt == nil {
		t.Fatalf("first sync: %+v", st)
	}
	backfilled := f.placeholder(t, "octo/repo", 5, issuestate.Open)

	r.Tick(t.Context()) // incremental, not a day yet: nothing finds #5
	if is := f.issue(t, backfilled.ID); is.Remote.SyncedAt != nil {
		t.Fatalf("adopted before the sweep: %+v", is.Remote)
	}

	r.now = func() time.Time { return time.Now().Add(fullScanEvery + time.Hour) }
	r.Tick(t.Context())
	is := f.issue(t, backfilled.ID)
	if is.Remote.RemoteKey != "I_node5" || is.Remote.SyncedAt == nil || is.Remote.Status != "" {
		t.Errorf("the sweep did not re-key the placeholder: %+v", is.Remote)
	}
	if is.State != issuestate.Closed || is.CloseReason != issuestate.Completed {
		t.Errorf("backfilled #5 = %s/%s, want closed/completed", is.State, is.CloseReason)
	}
	if got := len(f.issues(t)); got != 2 {
		t.Errorf("%d issues, want 2: the sweep must not import #5 beside its placeholder", got)
	}
	f.noWriteBack(t, backfilled.ID)
}
