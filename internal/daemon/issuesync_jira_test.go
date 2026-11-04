package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The importer's Jira rules (task 142.12, #908) against stub sources whose
// Provider is jira, driven through the Jira pass: an id-ordered initial
// import resumed by cursor, the sticky project id, the bulk sweep that marks
// absent issues missing and never deletes, and a moved issue's row
// following it. The Jira source itself is another unit's; these stubs hold
// the importer to the seam it implements.

const (
	jiraSite = "acme.atlassian.net"
	// jiraStubPageSize is the stub's page: importPageCap pages are 500.
	jiraStubPageSize = 100
)

// jiraStubIssue is one issue on the stub: id is its stable remote key,
// project and number its key ("ENG-7").
type jiraStubIssue struct {
	id      int
	project string
	number  int
	title   string
	open    bool
	updated time.Time
	hidden  bool // left out of every listing
	absent  bool // deleted or unreadable: no read finds it
}

type jiraFail struct{ f issuesource.Failure }

func (e *jiraFail) Error() string { return "jira stub: " + e.f.Reason }

// jiraStub is a source shaped like Jira's: id-ordered open listing paged
// by pageSize and resumed by After, an update-ordered incremental listing,
// and single reads that answer a moved issue with the issue where it went.
// It has no bulk read and no binding resolution; jiraFull adds both.
type jiraStub struct {
	byID     map[int]*jiraStubIssue
	pageSize int
	calls    []string
	afters   []string // every After a listing was given
	listErr  error
}

func newJiraStub(issues ...*jiraStubIssue) *jiraStub {
	s := &jiraStub{byID: map[int]*jiraStubIssue{}, pageSize: jiraStubPageSize}
	for _, is := range issues {
		s.add(is)
	}
	return s
}

func (s *jiraStub) add(is *jiraStubIssue) {
	if is.project == "" {
		is.project = "ENG"
	}
	if is.title == "" {
		is.title = fmt.Sprintf("issue %d", is.id)
	}
	s.byID[is.id] = is
}

var (
	_ issuesource.Source          = (*jiraStub)(nil)
	_ issuesource.BulkGetter      = (*jiraFull)(nil)
	_ issuesource.BindingResolver = (*jiraFull)(nil)
)

func (*jiraStub) Provider() string { return issueprovider.Jira }

func (*jiraStub) Bind(context.Context, store.Project) (issuesource.Binding, bool) {
	return issuesource.NewBinding(jiraSite+"/ENG", nil), true
}

func (*jiraStub) Locate(rem store.IssueRemote) (issuesource.Binding, bool) {
	return issuesource.NewBinding(rem.Repo, nil), rem.RemoteKey != ""
}

func browseURL(project string, number int) string {
	return fmt.Sprintf("https://%s/browse/%s-%d", jiraSite, project, number)
}

func (*jiraStub) mapped(is *jiraStubIssue) issuesource.Issue {
	updated := is.updated
	ri := store.RemoteIssue{
		Provider: issueprovider.Jira, RemoteKey: strconv.Itoa(is.id), Repo: jiraSite + "/" + is.project,
		Number: is.number, URL: browseURL(is.project, is.number), RemoteJSON: "{}",
		Title: is.title, State: issuestate.Open, RemoteUpdatedAt: &updated,
	}
	v := store.IssueStateValue{State: string(issuestate.Open)}
	if !is.open {
		ri.State, ri.CloseReason = issuestate.Closed, issuestate.Completed
		v = store.IssueStateValue{State: string(issuestate.Closed), Reason: string(issuestate.Completed)}
	}
	return issuesource.Issue{Remote: ri, StateValue: v}
}

func (s *jiraStub) ListIssues(_ context.Context, _ issuesource.Binding, opts issuesource.ListOptions) (issuesource.Page, error) {
	s.calls = append(s.calls, "list")
	s.afters = append(s.afters, opts.After)
	if s.listErr != nil {
		return issuesource.Page{}, s.listErr
	}
	var rows []*jiraStubIssue
	for _, is := range s.byID {
		if is.hidden || is.absent || is.project != "ENG" {
			continue
		}
		if opts.OpenOnly {
			after, _ := strconv.Atoi(opts.After)
			if !is.open || is.id <= after {
				continue
			}
		} else if is.updated.Before(opts.Since) {
			continue
		}
		rows = append(rows, is)
	}
	var page issuesource.Page
	if !opts.OpenOnly {
		sort.Slice(rows, func(i, j int) bool { return rows[i].updated.Before(rows[j].updated) })
		for _, is := range rows {
			page.Issues = append(page.Issues, s.mapped(is))
		}
		return page, nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	limit := 5 * s.pageSize
	if opts.PageCap > 0 {
		limit = opts.PageCap * s.pageSize
	}
	if len(rows) > limit {
		rows = rows[:limit]
		page.Truncated = true
		page.ResumeAfter = strconv.Itoa(rows[len(rows)-1].id)
	}
	for _, is := range rows {
		page.Issues = append(page.Issues, s.mapped(is))
	}
	return page, nil
}

// read is one issue as a read finds it: the issue, and the moved error
// when it left ENG.
func (s *jiraStub) read(key string) (*jiraStubIssue, error) {
	id, _ := strconv.Atoi(key)
	is, ok := s.byID[id]
	if !ok || is.absent {
		return nil, &jiraFail{issuesource.Failure{Reason: issuesource.ReasonNotFound}}
	}
	if is.project != "ENG" {
		return is, &jiraFail{issuesource.Failure{
			Reason: issuesource.ReasonMoved, Location: browseURL(is.project, is.number),
		}}
	}
	return is, nil
}

func (s *jiraStub) GetIssue(_ context.Context, _ issuesource.Binding, rem store.IssueRemote) (issuesource.Issue, error) {
	s.calls = append(s.calls, "get "+rem.RemoteKey)
	is, err := s.read(rem.RemoteKey)
	if is == nil {
		return issuesource.Issue{}, err
	}
	return s.mapped(is), err
}

func (s *jiraStub) ListComments(context.Context, issuesource.Binding, *issuesource.Issue) ([]issuesource.Comment, error) {
	s.calls = append(s.calls, "comments")
	return nil, nil
}

func (s *jiraStub) SetIssueState(context.Context, issuesource.Binding, store.IssueRemote, store.IssueStateValue) error {
	s.calls = append(s.calls, "set")
	return errors.New("jira stub: no write-back")
}

func (s *jiraStub) CreateComment(context.Context, issuesource.Binding, store.IssueRemote, string) (string, error) {
	s.calls = append(s.calls, "post")
	return "", errors.New("jira stub: no write-back")
}

func (*jiraStub) Classify(err error) issuesource.Failure {
	if err == nil {
		return issuesource.Failure{}
	}
	var jf *jiraFail
	if errors.As(err, &jf) {
		return jf.f
	}
	return issuesource.Failure{Reason: issuesource.ReasonUnreachable}
}

// jiraFull is jiraStub with Jira's bulk read and binding resolution.
type jiraFull struct {
	*jiraStub
	projectID  string
	resolveErr error
	batches    []int
}

func (s *jiraFull) ResolveBinding(_ context.Context, b issuesource.Binding) (issuesource.Binding, string, error) {
	s.calls = append(s.calls, "resolve")
	if s.resolveErr != nil {
		return issuesource.Binding{}, "", s.resolveErr
	}
	return issuesource.NewBinding(b.String(), s.projectID), s.projectID, nil
}

func (s *jiraFull) GetIssues(_ context.Context, _ issuesource.Binding, rems []store.IssueRemote) (map[string]issuesource.BulkIssue, error) {
	s.calls = append(s.calls, "bulk")
	s.batches = append(s.batches, len(rems))
	out := map[string]issuesource.BulkIssue{}
	for _, rem := range rems {
		is, err := s.read(rem.RemoteKey)
		if is == nil {
			continue
		}
		bi := issuesource.BulkIssue{Issue: s.mapped(is)}
		if err != nil {
			bi.MovedTo = browseURL(is.project, is.number)
		}
		out[rem.RemoteKey] = bi
	}
	return out, nil
}

// jiraFixture is a project bound to Jira, credentialed, with src wired
// and a settable clock.
type jiraFixture struct {
	*reconcileFixture
	r   *PullReconciler
	now time.Time
}

func newJiraFixture(t *testing.T, src issuesource.Source) *jiraFixture {
	t.Helper()
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t)
	f.bindJira(t)
	jf := &jiraFixture{reconcileFixture: f, r: f.reconciler(), now: issueBase.Add(time.Hour)}
	jf.r.environ = jiraEnv("me", "tok")
	jf.r.WithJiraSource(src)
	jf.r.now = func() time.Time { return jf.now }
	return jf
}

func (f *jiraFixture) tick(t *testing.T) { f.r.JiraTick(t.Context()) }

// jiraIssues is the project's Jira issues by remote key.
func (f *jiraFixture) jiraIssues(t *testing.T) map[string]*store.Issue {
	t.Helper()
	list, err := f.store.ListIssues(t.Context(), store.IssueFilter{ProjectID: f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*store.Issue{}
	for _, iss := range list {
		if iss.Remote != nil && iss.Remote.Provider == issueprovider.Jira {
			out[iss.Remote.RemoteKey] = iss
		}
	}
	return out
}

func manyJiraIssues(n int, updated func(i int) time.Time) []*jiraStubIssue {
	out := make([]*jiraStubIssue, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, &jiraStubIssue{id: 10000 + i, number: i, open: true, updated: updated(i)})
	}
	return out
}

// TestJiraImportResumesByIdCursor: an id-ordered open walk of 600 issues
// imports at most 500 a tick (importPageCap pages of 100) and resumes from
// the cursor, which is cleared when the import completes; the sweep's walk
// resumes by cursor too. An issue read on the first tick and changed before
// the import ended — earlier than the newest issue the import saw — is
// still picked up, because the first incremental bound is the import's
// start; and once read, an overlap re-read of unchanged issues writes
// nothing and announces nothing.
func TestJiraImportResumesByIdCursor(t *testing.T) {
	issues := manyJiraIssues(600, func(i int) time.Time { return issueBase.Add(time.Duration(i) * time.Minute) })
	issues[599].updated = issueBase.Add(11*time.Hour + 45*time.Minute)
	src := newJiraStub(issues...)
	f := newJiraFixture(t, src)
	f.now = issueBase.Add(11 * time.Hour)
	f.tick(t)

	st := f.syncState(t)
	if !st.OK || st.ImportComplete || st.ImportCursor != "10500" || st.Provider != issueprovider.Jira {
		t.Fatalf("after tick 1: %+v", st)
	}
	if st.Since == nil || st.Since.After(f.now) {
		t.Errorf("tick 1 recorded no import start: since %v", st.Since)
	}
	if got := len(f.jiraIssues(t)); got != 500 {
		t.Fatalf("tick 1 imported %d, want 500", got)
	}

	// Changed after it was read, before the import ends.
	src.byID[10001].title, src.byID[10001].updated = "retitled mid-import", issueBase.Add(11*time.Hour+30*time.Minute)
	f.now = issueBase.Add(12 * time.Hour)
	src.afters = nil
	f.tick(t)
	st = f.syncState(t)
	if !st.OK || !st.ImportComplete || st.ImportCursor != "" {
		t.Fatalf("after tick 2: %+v", st)
	}
	if got := len(f.jiraIssues(t)); got != 600 {
		t.Fatalf("tick 2 left %d issues, want 600", got)
	}
	// The import resumed at 10500; the sweep walked 500 and resumed there.
	if want := []string{"10500", "", "10500"}; !slices.Equal(src.afters, want) {
		t.Errorf("listings' After = %q, want %q", src.afters, want)
	}
	if st.Since == nil || !st.Since.Before(issueBase.Add(11*time.Hour+30*time.Minute)) {
		t.Errorf("first incremental bound %v is after an issue changed mid-import", st.Since)
	}
	if st.LastFullScanAt == nil {
		t.Errorf("the sweep did not run once the import completed")
	}

	f.now = issueBase.Add(12*time.Hour + 10*time.Minute)
	f.tick(t)
	if got := f.jiraIssues(t)["10001"]; got.Title != "retitled mid-import" {
		t.Errorf("the issue changed mid-import was skipped: %q", got.Title)
	}
	st = f.syncState(t)
	if want := issueBase.Add(11*time.Hour + 43*time.Minute); st.Since == nil || !st.Since.Equal(want) {
		t.Errorf("bound after a full incremental = %v, want the watermark's %v", st.Since, want)
	}

	// Nothing moved: the overlap re-reads the newest issue and writes
	// nothing.
	before := f.lastEventID(t)
	f.now = f.now.Add(10 * time.Minute)
	src.calls = nil
	f.tick(t)
	if !slices.Equal(src.calls, []string{"list"}) {
		t.Errorf("idle tick calls = %v", src.calls)
	}
	if evs := f.eventsAfter(t, before); len(evs) != 0 {
		t.Errorf("an overlap re-read announced %d events: %+v", len(evs), evs)
	}
}

// TestJiraBindingChangedStops: the first pass records the project id the
// binding resolves to; a later pass resolving another stops with
// binding_changed before listing, and no pass after it makes a call.
func TestJiraBindingChangedStops(t *testing.T) {
	src := &jiraFull{jiraStub: newJiraStub(&jiraStubIssue{id: 10001, number: 1, open: true, updated: issueBase}), projectID: "10042"}
	f := newJiraFixture(t, src)
	f.tick(t)
	st := f.syncState(t)
	if !st.OK || st.RemoteProjectID != "10042" {
		t.Fatalf("after the first pass: %+v", st)
	}
	if src.calls[0] != "resolve" {
		t.Errorf("first call = %v, want resolve", src.calls)
	}

	f.now = f.now.Add(time.Minute)
	f.tick(t) // same id: carries on
	if st := f.syncState(t); !st.OK {
		t.Fatalf("same id: %+v", st)
	}

	src.projectID = "10077"
	src.calls = nil
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	st = f.syncState(t)
	if st.OK || st.Reason != issuesource.ReasonBindingChanged || st.RemoteProjectID != "10042" {
		t.Errorf("changed id: %+v", st)
	}
	if !slices.Equal(src.calls, []string{"resolve"}) {
		t.Errorf("calls after the change = %v, want resolve alone", src.calls)
	}
	src.calls = nil
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	if len(src.calls) != 0 {
		t.Errorf("a binding_changed row made calls: %v", src.calls)
	}
	if st := f.syncState(t); st.Reason != issuesource.ReasonBindingChanged {
		t.Errorf("row after a stopped pass: %+v", st)
	}

	// A rebind drops the row and with it the recorded id.
	f.project.IssueSource.JiraProjectKey = "OPS"
	rebind := &store.Rebind{OldProvider: store.IssueProviderJira}
	if err := f.store.UpdateProjectRebind(t.Context(), &f.project, rebind); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	if st := f.syncState(t); !st.OK || st.RemoteProjectID != "10077" {
		t.Errorf("after a rebind: %+v", st)
	}
}

// TestJiraPassFailuresStop: a failing resolution or listing stops the
// pass with its reason and makes no further call — a 404 on the bound
// project is not_found, 401 unauthorized, 403 forbidden — and a 429 waits
// until its reset before calling again.
func TestJiraPassFailuresStop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve bool // fail the resolution rather than the listing
		reason  string
		want    []string
	}{
		{"project 404", true, issuesource.ReasonNotFound, []string{"resolve"}},
		{"resolve 401", true, issuesource.ReasonUnauthorized, []string{"resolve"}},
		{"list 401", false, issuesource.ReasonUnauthorized, []string{"list"}},
		{"list 403", false, issuesource.ReasonForbidden, []string{"list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newJiraStub(&jiraStubIssue{id: 10001, number: 1, open: true, updated: issueBase})
			var src issuesource.Source = stub
			fail := &jiraFail{issuesource.Failure{Reason: tc.reason}}
			if tc.resolve {
				src = &jiraFull{jiraStub: stub, projectID: "10042", resolveErr: fail}
			} else {
				stub.listErr = fail
			}
			f := newJiraFixture(t, src)
			f.tick(t)
			st := f.syncState(t)
			if st.OK || st.Reason != tc.reason || st.RateLimitedUntil != nil || st.RemoteProjectID != "" {
				t.Errorf("row = %+v, want reason %s", st, tc.reason)
			}
			if !slices.Equal(stub.calls, tc.want) {
				t.Errorf("calls = %v, want %v", stub.calls, tc.want)
			}
			if len(f.jiraIssues(t)) != 0 {
				t.Errorf("a failed pass imported issues")
			}
		})
	}

	t.Run("429", func(t *testing.T) {
		stub := newJiraStub(&jiraStubIssue{id: 10001, number: 1, open: true, updated: issueBase})
		f := newJiraFixture(t, stub)
		reset := f.now.Add(30 * time.Second)
		stub.listErr = &jiraFail{issuesource.Failure{Reason: issuesource.ReasonRateLimited, ResetAt: reset}}
		f.tick(t)
		st := f.syncState(t)
		if st.Reason != issuesource.ReasonRateLimited || st.RateLimitedUntil == nil || !st.RateLimitedUntil.Equal(reset) {
			t.Fatalf("row = %+v, want rate_limited until %v", st, reset)
		}
		stub.listErr, stub.calls = nil, nil
		f.now = reset.Add(-time.Second)
		f.tick(t)
		if len(stub.calls) != 0 {
			t.Errorf("called before the reset: %v", stub.calls)
		}
		f.now = reset.Add(time.Second)
		f.tick(t)
		if st := f.syncState(t); !st.OK || st.RateLimitedUntil != nil || len(stub.calls) == 0 {
			t.Errorf("after the reset: %+v, calls %v", st, stub.calls)
		}
	})
}

// TestJiraBulkSweep: the sweep reads the open issues the listing lacked
// BulkGetMax at a time, never one by one: a closed one is closed, a moved
// one's row follows it, and an absent one — deleted or unreadable alike —
// is marked missing and never deleted. Each comes back to live when a
// listing at the binding returns it again.
func TestJiraBulkSweep(t *testing.T) {
	issues := manyJiraIssues(205, func(i int) time.Time { return issueBase.Add(time.Duration(i) * time.Second) })
	src := &jiraFull{jiraStub: newJiraStub(issues...), projectID: "10042"}
	f := newJiraFixture(t, src)
	f.tick(t)
	if got := len(f.jiraIssues(t)); got != 205 {
		t.Fatalf("imported %d, want 205", got)
	}

	for _, is := range issues {
		is.hidden = true
	}
	src.byID[10001].open = false
	src.byID[10002].project, src.byID[10002].number = "OPS", 31
	src.byID[10003].absent = true
	src.calls, src.batches = nil, nil
	f.now = f.now.Add(fullScanEvery + time.Hour)
	f.tick(t)

	if st := f.syncState(t); !st.OK {
		t.Fatalf("after the sweep: %+v", st)
	}
	if !slices.Equal(src.batches, []int{100, 100, 5}) {
		t.Errorf("bulk batches = %v, want 100, 100, 5", src.batches)
	}
	for _, c := range src.calls {
		if strings.HasPrefix(c, "get ") {
			t.Errorf("the sweep probed one by one: %q", c)
		}
	}
	got := f.jiraIssues(t)
	if len(got) != 205 {
		t.Errorf("the sweep deleted issues: %d left", len(got))
	}
	if is := got["10001"]; is.State != issuestate.Closed || is.Remote.Status != store.RemoteStatusLive {
		t.Errorf("closed: %+v / %+v", is, is.Remote)
	}
	moved := got["10002"].Remote
	if moved.Status != store.RemoteStatusMoved || moved.Repo != jiraSite+"/OPS" || moved.Number != 31 ||
		moved.URL != browseURL("OPS", 31) || !strings.Contains(moved.RemoteJSON, browseURL("OPS", 31)) {
		t.Errorf("moved: %+v", moved)
	}
	if r := got["10003"].Remote; r.Status != store.RemoteStatusMissing {
		t.Errorf("absent: %+v", r)
	}
	if r := got["10004"].Remote; r.Status != store.RemoteStatusLive {
		t.Errorf("still open, unlisted: %+v", r)
	}

	// Both return to the binding and are listed again: live.
	src.byID[10002].project, src.byID[10002].number, src.byID[10002].hidden = "ENG", 2, false
	src.byID[10003].absent, src.byID[10003].hidden = false, false
	src.byID[10002].updated = f.now
	src.byID[10003].updated = f.now
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	got = f.jiraIssues(t)
	for _, key := range []string{"10002", "10003"} {
		if r := got[key].Remote; r.Status != store.RemoteStatusLive || r.Repo != jiraSite+"/ENG" {
			t.Errorf("%s back at the binding: %+v", key, r)
		}
	}
	if r := got["10002"].Remote; r.Number != 2 || r.URL != browseURL("ENG", 2) {
		t.Errorf("10002 not rewritten back: %+v", r)
	}
}

// TestJiraMovedProbeRewrites: without a bulk read, the sweep's single read
// of a moved issue answers it where it went, and the row is rewritten to
// that project, number and URL and marked moved there.
func TestJiraMovedProbeRewrites(t *testing.T) {
	src := newJiraStub(
		&jiraStubIssue{id: 10001, number: 1, open: true, updated: issueBase},
		&jiraStubIssue{id: 10002, number: 2, open: true, updated: issueBase},
	)
	f := newJiraFixture(t, src)
	f.tick(t)
	src.byID[10001].hidden = true
	src.byID[10001].project, src.byID[10001].number = "OPS", 9
	src.byID[10002].absent = true
	f.now = f.now.Add(fullScanEvery + time.Hour)
	src.calls = nil
	f.tick(t)
	if !slices.Contains(src.calls, "get 10001") || !slices.Contains(src.calls, "get 10002") {
		t.Errorf("calls = %v", src.calls)
	}
	got := f.jiraIssues(t)
	r := got["10001"].Remote
	if r.Status != store.RemoteStatusMoved || r.Repo != jiraSite+"/OPS" || r.Number != 9 || r.URL != browseURL("OPS", 9) {
		t.Errorf("moved: %+v", r)
	}
	if r := got["10002"].Remote; r.Status != store.RemoteStatusMissing {
		t.Errorf("absent: %+v", r)
	}
}

// TestJiraImportKeepsAPendingLocalClose: the write-pending hold applies to
// a Jira row as to a GitHub one — a human close waiting in the outbox is
// not reverted by an import that still reports the issue open.
func TestJiraImportKeepsAPendingLocalClose(t *testing.T) {
	src := newJiraStub(&jiraStubIssue{id: 10001, number: 1, open: true, updated: issueBase})
	f := newJiraFixture(t, src)
	f.tick(t)
	iss := f.jiraIssues(t)["10001"]
	if _, err := f.store.TransitionIssue(t.Context(), iss.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	src.byID[10001].updated = f.now
	src.byID[10001].title = "retitled"
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	got := f.jiraIssues(t)["10001"]
	if got.State != issuestate.Closed || got.Title != "retitled" {
		t.Errorf("after an import = %+v; want the local close kept and the title taken", got)
	}
}
