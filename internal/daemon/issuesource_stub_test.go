package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The issue-provider seam (task 142.4) against a source that is not GitHub:
// its remote keys are "PROJ-n", it has no issue numbers, no repository-wide
// comment listing and no placeholders. Whatever the importer and the drain
// do for it they do through issuesource.Source alone.

const stubProvider = "stub"

type stubIssue struct {
	key      string
	open     bool
	updated  time.Time
	hidden   bool  // left out of every listing, as a lagging or lost one is
	getErr   error // what GetIssue answers instead of the issue
	comments []string
}

type stubError struct{ reason string }

func (e *stubError) Error() string { return "stub: " + e.reason }

type stubSource struct {
	issues map[string]*stubIssue
	calls  []string
	posted map[string][]string
	states map[string]store.IssueStateValue
}

var _ issuesource.Source = (*stubSource)(nil)

func newStubSource(issues ...*stubIssue) *stubSource {
	s := &stubSource{issues: map[string]*stubIssue{}, posted: map[string][]string{}, states: map[string]store.IssueStateValue{}}
	for _, is := range issues {
		s.issues[is.key] = is
	}
	return s
}

func (*stubSource) Provider() string { return stubProvider }

func (*stubSource) Bind(context.Context, store.Project) (issuesource.Binding, bool) {
	return issuesource.NewBinding("PROJ", nil), true
}

func (*stubSource) Locate(rem store.IssueRemote) (issuesource.Binding, bool) {
	return issuesource.NewBinding("PROJ", nil), rem.RemoteKey != ""
}

func (s *stubSource) mapped(is *stubIssue) issuesource.Issue {
	updated := is.updated
	ri := store.RemoteIssue{
		Provider: stubProvider, RemoteKey: is.key, Repo: "PROJ",
		URL: "https://stub.example/browse/" + is.key, RemoteJSON: "{}",
		Title: "stub " + is.key, State: issuestate.Open, RemoteUpdatedAt: &updated,
	}
	v := store.IssueStateValue{State: string(issuestate.Open)}
	if !is.open {
		ri.State, ri.CloseReason = issuestate.Closed, issuestate.Completed
		v = store.IssueStateValue{State: string(issuestate.Closed), Reason: string(issuestate.Completed)}
	}
	return issuesource.Issue{Remote: ri, StateValue: v, Comments: len(is.comments)}
}

func (s *stubSource) ListIssues(_ context.Context, _ issuesource.Binding, opts issuesource.ListOptions) (issuesource.Page, error) {
	s.calls = append(s.calls, "list")
	var rows []*stubIssue
	for _, is := range s.issues {
		if is.hidden || (opts.OpenOnly && !is.open) || is.updated.Before(opts.Since) {
			continue
		}
		rows = append(rows, is)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].updated.Before(rows[j].updated) })
	var page issuesource.Page
	for _, is := range rows {
		page.Issues = append(page.Issues, s.mapped(is))
	}
	return page, nil
}

func (s *stubSource) GetIssue(_ context.Context, _ issuesource.Binding, rem store.IssueRemote) (issuesource.Issue, error) {
	s.calls = append(s.calls, "get "+rem.RemoteKey)
	is, ok := s.issues[rem.RemoteKey]
	if !ok {
		return issuesource.Issue{}, &stubError{issuesource.ReasonNotFound}
	}
	if is.getErr != nil {
		return issuesource.Issue{}, is.getErr
	}
	return s.mapped(is), nil
}

func (s *stubSource) ListComments(_ context.Context, _ issuesource.Binding, is *issuesource.Issue) ([]issuesource.Comment, error) {
	s.calls = append(s.calls, "comments "+is.Remote.RemoteKey)
	var out []issuesource.Comment
	for i, body := range s.issues[is.Remote.RemoteKey].comments {
		at := issueBase.Add(time.Duration(i) * time.Second)
		out = append(out, issuesource.Comment{Remote: store.RemoteIssueComment{
			RemoteKey: fmt.Sprintf("%s-c%d", is.Remote.RemoteKey, i), Author: "stubber", Body: body,
			CreatedAt: at, UpdatedAt: at,
		}})
	}
	return out, nil
}

func (s *stubSource) SetIssueState(_ context.Context, _ issuesource.Binding, rem store.IssueRemote, v store.IssueStateValue) error {
	s.calls = append(s.calls, "set "+rem.RemoteKey)
	s.states[rem.RemoteKey] = v
	s.issues[rem.RemoteKey].open = v.State == string(issuestate.Open)
	return nil
}

func (s *stubSource) CreateComment(_ context.Context, _ issuesource.Binding, rem store.IssueRemote, body string) (string, error) {
	s.calls = append(s.calls, "post "+rem.RemoteKey)
	s.posted[rem.RemoteKey] = append(s.posted[rem.RemoteKey], body)
	return fmt.Sprintf("%s-posted%d", rem.RemoteKey, len(s.posted[rem.RemoteKey])), nil
}

func (*stubSource) Classify(err error) issuesource.Failure {
	if err == nil {
		return issuesource.Failure{}
	}
	var se *stubError
	if errors.As(err, &se) {
		return issuesource.Failure{Reason: se.reason}
	}
	return issuesource.Failure{Reason: issuesource.ReasonUnreachable}
}

// stubIssues is the fixture project's stub-provider issues by remote key.
func (f *reconcileFixture) stubIssues(t *testing.T) map[string]*store.Issue {
	t.Helper()
	list, err := f.store.ListIssues(t.Context(), store.IssueFilter{ProjectID: f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*store.Issue{}
	for _, iss := range list {
		if iss.Remote != nil && iss.Remote.Provider == stubProvider {
			out[iss.Remote.RemoteKey] = iss
		}
	}
	return out
}

func (f *reconcileFixture) commentBodies(t *testing.T, issueID int64) []string {
	t.Helper()
	cs, err := f.store.ListIssueComments(t.Context(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, c.Body)
	}
	return out
}

// TestStubSourceImportsProbesAndSweeps: an issue with no number and a
// provider other than GitHub is imported, its thread attached by remote key,
// and the sweep probes what the listing lost — a closed one closed, a gone
// one deleted, an unreadable one missing.
func TestStubSourceImportsProbesAndSweeps(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t) // GitHub has nothing; only the stub's issues are under test
	src := newStubSource(
		&stubIssue{key: "PROJ-1", open: true, updated: issueBase.Add(time.Minute), comments: []string{"first", "second"}},
		&stubIssue{key: "PROJ-2", open: true, updated: issueBase.Add(2 * time.Minute)},
		&stubIssue{key: "PROJ-3", open: true, updated: issueBase.Add(3 * time.Minute)},
		&stubIssue{key: "PROJ-4", open: true, updated: issueBase.Add(4 * time.Minute)},
	)
	r := f.reconciler()
	r.issueSrc = src
	now := issueBase.Add(time.Hour)
	r.now = func() time.Time { return now }
	r.Tick(t.Context())

	st := f.syncState(t)
	if !st.OK || st.Provider != stubProvider || st.Repo != "PROJ" || !st.ImportComplete {
		t.Fatalf("sync state after the first tick: %+v", st)
	}
	got := f.stubIssues(t)
	if len(got) != 4 {
		t.Fatalf("imported %d stub issues, want 4: %v", len(got), got)
	}
	if rem := got["PROJ-1"].Remote; rem.Number != 0 || rem.Repo != "PROJ" {
		t.Errorf("PROJ-1's remote: %+v", rem)
	}
	if bodies := f.commentBodies(t, got["PROJ-1"].ID); !slices.Equal(bodies, []string{"first", "second"}) {
		t.Errorf("PROJ-1's thread = %q, want first, second", bodies)
	}

	// A comment lands on PROJ-1, which moves its update time; PROJ-2 closes
	// but the listing lags; PROJ-3 is deleted; PROJ-4 can no longer be read.
	src.issues["PROJ-1"].comments = append(src.issues["PROJ-1"].comments, "third")
	src.issues["PROJ-1"].updated = issueBase.Add(10 * time.Minute)
	src.issues["PROJ-2"].open, src.issues["PROJ-2"].hidden = false, true
	src.issues["PROJ-3"].hidden, src.issues["PROJ-3"].getErr = true, &stubError{issuesource.ReasonGone}
	src.issues["PROJ-4"].hidden, src.issues["PROJ-4"].getErr = true, &stubError{issuesource.ReasonNotFound}
	now = now.Add(fullScanEvery + time.Hour) // the sweep is due
	src.calls = nil
	r.Tick(t.Context())

	if st := f.syncState(t); !st.OK {
		t.Fatalf("sync state after the sweep: %+v", st)
	}
	got = f.stubIssues(t)
	if bodies := f.commentBodies(t, got["PROJ-1"].ID); !slices.Equal(bodies, []string{"first", "second", "third"}) {
		t.Errorf("PROJ-1's thread = %q, want first, second, third", bodies)
	}
	if is := got["PROJ-2"]; is == nil || is.State != issuestate.Closed {
		t.Errorf("PROJ-2, closed on the provider: %+v", is)
	}
	if is, ok := got["PROJ-3"]; ok {
		t.Errorf("PROJ-3, gone on the provider, is still here: %+v", is)
	}
	if known, err := f.store.RemoteIssueKnown(t.Context(), f.project.ID, stubProvider, "PROJ-3"); err != nil || !known {
		t.Errorf("PROJ-3 left no tombstone: %v, %v", known, err)
	}
	if is := got["PROJ-4"]; is == nil || is.Remote.Status != store.RemoteStatusMissing {
		t.Errorf("PROJ-4, unreadable: %+v", is)
	}
	for _, want := range []string{"get PROJ-2", "get PROJ-3", "get PROJ-4", "comments PROJ-1"} {
		if !slices.Contains(src.calls, want) {
			t.Errorf("the second tick never made %q: %v", want, src.calls)
		}
	}
}

// outboxRow inserts a pending write the way the store's enqueue would. The
// store enqueues only for GitHub remotes today (#901), so a row for another
// provider can only be written here.
func outboxRow(t *testing.T, st *store.Store, issueID int64, op string, desired, base store.IssueStateValue, commentID *int64) {
	t.Helper()
	db, err := sql.Open("sqlite", st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	dj, _ := json.Marshal(desired)
	bj, _ := json.Marshal(base)
	stamp := time.Now().Add(-time.Minute).UTC().Format(store.TimeFormat)
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO issue_sync_outbox (issue_id, op, desired_json, base_json, status, next_attempt_at,
			origin, comment_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', ?, 'human', ?, ?, ?)`,
		issueID, op, string(dj), string(bj), stamp, commentID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

// TestOutboxDispatchesEachRowToItsProvider: a GitHub row and two stub rows
// (a state write and a comment) drain through their own sources, paced as
// one writer; a row whose provider has no source waits and is not failed.
func TestOutboxDispatchesEachRowToItsProvider(t *testing.T) {
	f := newOutboxFixture(t, issueRow(1, "open"))
	ctx := t.Context()
	src := newStubSource(
		&stubIssue{key: "PROJ-1", open: true, updated: issueBase},
		&stubIssue{key: "PROJ-2", open: true, updated: issueBase},
	)
	other := newStubSource(&stubIssue{key: "ELSE-1", open: true, updated: issueBase})
	for key, s := range map[string]*stubSource{"PROJ-1": src, "PROJ-2": src, "ELSE-1": other} {
		provider := stubProvider
		if s == other {
			provider = "elsewhere"
		}
		is := s.mapped(s.issues[key])
		ri := is.Remote
		ri.ProjectID, ri.Provider = f.project.ID, provider
		if _, _, err := f.store.UpsertRemoteIssue(ctx, ri, issuestate.Sync); err != nil {
			t.Fatal(err)
		}
	}
	stubs := f.stubIssues(t)
	open := store.IssueStateValue{State: string(issuestate.Open)}
	closed := store.IssueStateValue{State: string(issuestate.Closed), Reason: string(issuestate.NotPlanned)}
	outboxRow(t, f.store, stubs["PROJ-1"].ID, store.OutboxOpSetState, closed, open, nil)
	c, err := f.store.AddIssueComment(ctx, stubs["PROJ-2"].ID, "me", "handed off", "", issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	outboxRow(t, f.store, stubs["PROJ-2"].ID, store.OutboxOpAddComment, store.IssueStateValue{}, store.IssueStateValue{}, &c.ID)
	elsewhere := f.elsewhereIssue(t)
	outboxRow(t, f.store, elsewhere, store.OutboxOpSetState, closed, open, nil)
	f.close(t, 1, issuestate.NotPlanned) // the GitHub row

	f.outbox.sources = func(provider string) (issuesource.Source, string) {
		if provider == stubProvider {
			return src, ""
		}
		return f.outbox.githubOnly(provider)
	}
	next := f.outbox.Drain(ctx)

	if p := f.patches(t); len(p) != 1 {
		t.Errorf("GitHub got %d PATCHes, want 1:\n%s", len(p), strings.Join(f.apiCalls(t), "\n"))
	}
	if !slices.Equal(src.calls, []string{"get PROJ-1", "set PROJ-1", "post PROJ-2"}) {
		t.Errorf("stub calls = %v", src.calls)
	}
	if v := src.states["PROJ-1"]; !v.Same(closed) {
		t.Errorf("PROJ-1 written as %+v, want %+v", v, closed)
	}
	if got, err := f.store.GetIssueComment(ctx, c.ID); err != nil || got.RemoteKey != "PROJ-2-posted1" {
		t.Errorf("posted comment's key: %+v, %v", got, err)
	}
	// Three writes, one writer: every write after the first waited out the
	// pacing gap, whichever provider it went to.
	if len(f.sleeps) != 2 {
		t.Errorf("pacing waits = %v, want 2 for three writes", f.sleeps)
	}
	if len(other.calls) != 0 {
		t.Errorf("a provider with no source was called: %v", other.calls)
	}
	writes, err := f.store.IssueWrites(ctx, elsewhere)
	if err != nil || len(writes) != 1 {
		t.Fatalf("elsewhere's writes: %+v, %v", writes, err)
	}
	if w := writes[0]; w.Status != store.OutboxPending || w.LastReason != outboxReasonNoClient {
		t.Errorf("elsewhere's row: %+v, want pending, %s", w, outboxReasonNoClient)
	}
	if !next.IsZero() {
		t.Errorf("Drain's next = %v, want zero: only a held row is pending", next)
	}
}

func (f *outboxFixture) elsewhereIssue(t *testing.T) int64 {
	t.Helper()
	list, err := f.store.ListIssues(t.Context(), store.IssueFilter{ProjectID: f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, iss := range list {
		if iss.Remote != nil && iss.Remote.Provider == "elsewhere" {
			return iss.ID
		}
	}
	t.Fatal("no elsewhere issue")
	return 0
}

// TestIssueSyncAndOutboxNameNoGitHubClient holds the seam (task 142.4):
// the importer and the drain reach GitHub only through issuesource.
func TestIssueSyncAndOutboxNameNoGitHubClient(t *testing.T) {
	banned := regexp.MustCompile(`\bgithub\.(Client|Repo|Reason\w*|Error|Issue\b|IssueComment|ListSinceOptions)`)
	for _, name := range []string{"issuesync.go", "issueoutbox.go"} {
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		if m := banned.FindAllString(string(b), -1); len(m) > 0 {
			t.Errorf("%s names %v; it must go through issuesource", name, m)
		}
	}
}
