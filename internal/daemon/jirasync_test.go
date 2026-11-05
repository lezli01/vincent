package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// The Jira source end to end (#908, task 142.12): the IssueSyncer's Jira
// pass, through issuesource.Jira and the real jira.Client, against the fake
// Jira Cloud site served over an httptest TLS server — the site is https
// (#902), and the test's client trusts the test certificate.

// jiraE2EToken is the fake site's API token; no log line, sync row or error
// text may ever carry it.
const jiraE2EToken = "s3cr3t-jira-e2e-token-7f3a"

type jiraE2E struct {
	t        *testing.T
	store    *store.Store
	project  store.Project
	fake     *fakejira.Store
	ts       *httptest.Server
	sync     *IssueSyncer
	cfg      config.Config
	logs     *syncBuffer
	scenario string

	mu       sync.Mutex
	clock    time.Time
	env      []string
	override func(w http.ResponseWriter, r *http.Request) bool
}

// syncBuffer is a bytes.Buffer safe for the logger and the test to share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newJiraE2E serves corpus (the fake's built-in one when nil) and binds a
// fresh project to its VIN project.
func newJiraE2E(t *testing.T, edit func(c *fakejira.Corpus)) *jiraE2E {
	t.Helper()
	dir := t.TempDir()
	f := &jiraE2E{
		t:        t,
		scenario: filepath.Join(dir, "scenario"),
		logs:     &syncBuffer{},
		clock:    time.Now().UTC().Truncate(time.Millisecond),
	}
	f.env = []string{"PATH=/bin", "JIRA_USER=" + fakejira.DefaultUser, "JIRA_API_TOKEN=" + jiraE2EToken}
	f.fake = &fakejira.Store{
		Path:         filepath.Join(dir, "corpus.json"),
		ScenarioFile: f.scenario,
		RequestLog:   filepath.Join(dir, "requests.log"),
		Token:        jiraE2EToken,
		Now:          f.now,
	}
	c := fakejira.Builtin()
	if edit != nil {
		edit(&c)
	}
	if err := f.fake.Save(c); err != nil {
		t.Fatal(err)
	}
	f.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		o := f.override
		f.mu.Unlock()
		if o != nil && o(w, r) {
			return
		}
		f.fake.ServeHTTP(w, r)
	}))
	t.Cleanup(f.ts.Close)

	st, err := storetest.Open(filepath.Join(dir, "vincent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.store = st
	f.project = store.Project{Name: "jira", Path: dir, DefaultBranch: "master"}
	if err := st.CreateProject(t.Context(), &f.project); err != nil {
		t.Fatal(err)
	}
	f.bind("VIN")

	f.cfg = config.Default()
	cfg := func() config.Config { return f.cfg }
	logger := slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.sync = wireJiraSource(NewIssueSyncer(st, cfg, nil, nil, logger), cfg, f.environ, f.ts.Client())
	f.sync.environ = f.environ
	f.sync.now = f.now
	return f
}

func (f *jiraE2E) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *jiraE2E) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func (f *jiraE2E) environ() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.env)
}

func (f *jiraE2E) setOverride(o func(w http.ResponseWriter, r *http.Request) bool) {
	f.mu.Lock()
	f.override = o
	f.mu.Unlock()
}

func (f *jiraE2E) setScenario(s string) {
	f.t.Helper()
	if err := os.WriteFile(f.scenario, []byte(s+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *jiraE2E) bind(key string) {
	f.t.Helper()
	f.project.IssueSource = store.IssueSource{
		Provider: store.IssueProviderJira, JiraSite: f.ts.URL, JiraProjectKey: key,
	}
	if err := f.store.UpdateProject(f.t.Context(), &f.project); err != nil {
		f.t.Fatal(err)
	}
}

func (f *jiraE2E) tick() { f.sync.JiraTick(f.t.Context()) }

// sweep is a tick far enough on that the open-set sweep is due.
func (f *jiraE2E) sweep() {
	f.advance(fullScanEvery + time.Minute)
	f.tick()
}

func (f *jiraE2E) row() *store.IssueSyncState {
	f.t.Helper()
	st, err := f.store.GetIssueSyncState(f.t.Context(), f.project.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// issues is the project's Jira issues by remote key (the Jira issue id).
func (f *jiraE2E) issues() map[string]*store.Issue {
	f.t.Helper()
	list, err := f.store.ListIssues(f.t.Context(), store.IssueFilter{ProjectID: f.project.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]*store.Issue{}
	for _, iss := range list {
		if iss.Remote != nil && iss.Remote.Provider == issueprovider.Jira {
			out[iss.Remote.RemoteKey] = iss
		}
	}
	return out
}

func (f *jiraE2E) issue(id string) *store.Issue {
	f.t.Helper()
	iss := f.issues()[id]
	if iss == nil {
		f.t.Fatalf("issue %s not imported", id)
	}
	full, err := f.store.GetIssue(f.t.Context(), iss.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return full
}

func (f *jiraE2E) comments(id string) []string {
	f.t.Helper()
	list, err := f.store.ListIssueComments(f.t.Context(), f.issue(id).ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, c := range list {
		out = append(out, c.RemoteKey+":"+strings.TrimSpace(c.Body))
	}
	return out
}

func (f *jiraE2E) maxEvent() int64 {
	f.t.Helper()
	id, err := f.store.MaxEventID(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *jiraE2E) requests() []fakejira.LoggedRequest {
	f.t.Helper()
	reqs, err := fakejira.Requests(f.fake.RequestLog)
	if err != nil {
		f.t.Fatal(err)
	}
	return reqs
}

func (f *jiraE2E) wantOK() {
	f.t.Helper()
	if st := f.row(); !st.OK || st.Reason != "" {
		f.t.Fatalf("sync row = ok %v reason %q, want ok", st.OK, st.Reason)
	}
}

// TestJiraSyncImportsPagesAndResumes is proof (1): with the fake holding
// more than 500 open issues, a tick imports at most 500 and the next
// resumes where it stopped; the sync row records the binding, the project
// id and a completed import.
func TestJiraSyncImportsPagesAndResumes(t *testing.T) {
	var seeded []string
	f := newJiraE2E(t, func(c *fakejira.Corpus) {
		seeded = c.SeedIssues("VIN", 600, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	})
	open := 2 + len(seeded) // VIN-1 and VIN-2 are open; VIN-3 is done
	f.tick()
	if n := len(f.issues()); n != 500 {
		t.Fatalf("first tick imported %d issues, want 500", n)
	}
	if st := f.row(); st.ImportComplete || st.ImportCursor == "" {
		t.Fatalf("after the first tick: complete %v cursor %q, want a cursor", st.ImportComplete, st.ImportCursor)
	}
	f.advance(time.Minute)
	f.tick()
	if n := len(f.issues()); n != open {
		t.Fatalf("second tick: %d issues, want %d", n, open)
	}
	st := f.row()
	if !st.OK || !st.ImportComplete || st.ImportCursor != "" || st.Repo != "VIN" || st.RemoteProjectID != "10000" ||
		st.Provider != issueprovider.Jira {
		t.Fatalf("sync row = %+v", st)
	}
	iss := f.issue("10001")
	r := iss.Remote
	if r.Repo != "VIN" || r.Number != 1 || r.URL != f.ts.URL+"/browse/VIN-1" || store.RemotePayloadKey(r.RemoteJSON) != "VIN-1" {
		t.Errorf("VIN-1 remote = %+v", r)
	}
	if iss.Title != "Sync Jira issues into vincent" || strings.TrimSpace(iss.Body) != "Mirror the board." ||
		iss.Kind != "story" || !slices.Equal(iss.Labels, []string{"sync"}) || iss.State != issuestate.Open {
		t.Errorf("VIN-1 = %q %q kind %q labels %v state %s", iss.Title, iss.Body, iss.Kind, iss.Labels, iss.State)
	}
	for _, req := range f.requests() {
		if req.Path == "/rest/api/3/search" {
			t.Fatalf("the retired search endpoint was called: %+v", req)
		}
	}
	if n, _ := fakejira.CountRequests(f.fake.RequestLog, http.MethodGet, "/rest/api/3/search/jql", http.StatusOK); n == 0 {
		t.Error("search/jql never called")
	}
}

// TestJiraSyncEnabledAfterWiring is review F2 on #943: the source is wired
// whatever the config says when the daemon starts, so Jira switched on by a
// later reload imports rather than recording no_client until a restart.
func TestJiraSyncEnabledAfterWiring(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.cfg.Jira.Enabled = false
	cfg := func() config.Config { return f.cfg }
	f.sync = wireJiraSource(NewIssueSyncer(f.store, cfg, nil, nil, slog.New(slog.DiscardHandler)), cfg, f.environ, f.ts.Client())
	f.sync.environ = f.environ
	f.sync.now = f.now

	f.cfg.Jira.Enabled = true
	f.tick()
	f.wantOK()
	if n := len(f.issues()); n == 0 {
		t.Fatal("nothing imported once Jira was switched on")
	}
}

// TestJiraSyncIncrementalChanges is proofs (2), (3), (4), (7) and (11): an
// incremental tick picks up a title, body, label, state and comment change;
// an overlap re-read writes nothing; a close in Jira closes the issue as
// sync with the mapped reason; restricted comments never mirror and a
// comment deleted in Jira stays here; and only search/jql is ever asked.
func TestJiraSyncIncrementalChanges(t *testing.T) {
	f := newJiraE2E(t, nil)
	ctx := t.Context()
	f.tick()
	f.wantOK()
	if got := f.comments("10001"); !slices.Equal(got, []string{"20001:First."}) {
		t.Fatalf("VIN-1 comments = %v, want only the unrestricted one", got)
	}

	// (3) An overlap re-read of unchanged issues writes nothing.
	f.advance(time.Minute)
	before := f.maxEvent()
	f.tick()
	f.wantOK()
	if after := f.maxEvent(); after != before {
		evs, _ := f.store.ListEvents(ctx, store.EventFilter{AfterID: before})
		t.Fatalf("an unchanged re-read appended events: %+v", evs)
	}

	// (2) Title, body, labels and a comment.
	f.advance(time.Minute)
	if err := f.fake.Update(func(c *fakejira.Corpus) error {
		for i := range c.Issues {
			if c.Issues[i].ID == "10001" {
				c.Issues[i].Summary = "Sync Jira issues, renamed"
				c.Issues[i].DescriptionHTML = "<p>A <strong>new</strong> body.</p>"
				c.Issues[i].Labels = []string{"sync", "jira"}
				c.Issues[i].Updated = f.now().Format(fakejira.TimeLayout)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	doc := json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Second."}]}]}`)
	added, err := f.fake.AddComment("10001", doc, "<p>Second.</p>", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.fake.AddComment("10001", doc, "<p>Secret.</p>", &fakejira.Visibility{Type: "group", Value: "admins"}); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.tick()
	f.wantOK()
	iss := f.issue("10001")
	if iss.Title != "Sync Jira issues, renamed" || strings.TrimSpace(iss.Body) != "A **new** body." ||
		!slices.Equal(iss.Labels, []string{"jira", "sync"}) {
		t.Errorf("after the edit: %q %q %v", iss.Title, iss.Body, iss.Labels)
	}
	if got := f.comments("10001"); !slices.Equal(got, []string{"20001:First.", added + ":Second."}) {
		t.Errorf("comments = %v", got)
	}

	// (7) A comment deleted in Jira is kept here.
	if err := f.fake.RemoveComment("20001"); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	if err := f.fake.SetUpdated("10001", f.now()); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.tick()
	f.wantOK()
	if got := f.comments("10001"); !slices.Equal(got, []string{"20001:First.", added + ":Second."}) {
		t.Errorf("after a remote delete, comments = %v, want both kept", got)
	}

	// (4) Closing in Jira (done category, Won't Do) closes as sync, not planned.
	since := f.maxEvent()
	f.advance(time.Minute)
	if err := f.fake.Transition("VIN-2", "31", map[string]any{"resolution": map[string]any{"name": "Won't Do"}}); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.tick()
	f.wantOK()
	closed := f.issue("10002")
	if closed.State != issuestate.Closed || closed.CloseReason != issuestate.NotPlanned {
		t.Fatalf("VIN-2 = %s %s, want closed not_planned", closed.State, closed.CloseReason)
	}
	// An import announces one issue.updated naming what moved, "state"
	// among it — the event a `type: issues` trigger reads.
	evs, err := f.store.ListEvents(ctx, store.EventFilter{
		AfterID: since, Types: []string{store.EventIssueUpdated, store.EventIssueStateChanged},
	})
	if err != nil || len(evs) != 1 {
		t.Fatalf("issue events = %+v, %v; want one", evs, err)
	}
	var body struct {
		ID int64  `json:"id"`
		By string `json:"by"`
	}
	if err := json.Unmarshal(evs[0].Payload, &body); err != nil || body.By != string(issuestate.Sync) ||
		body.ID != closed.ID || !strings.Contains(string(evs[0].Payload), `"state"`) {
		t.Errorf("issue event %s payload = %s, want issue %d by sync naming state", evs[0].Type, evs[0].Payload, closed.ID)
	}

	// (11) Only search/jql, never the retired search.
	for _, req := range f.requests() {
		if req.Path == "/rest/api/3/search" {
			t.Fatalf("the retired search endpoint was called: %+v", req)
		}
	}
}

// TestJiraSyncMovedAndMissing is proofs (5) and (6): a moved issue is
// marked moved with its row rewritten to its new key and browse URL;
// deleted and forbidden (archived) issues become missing, never deleted,
// and return to live when they reappear.
func TestJiraSyncMovedAndMissing(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.tick()
	f.wantOK()
	if len(f.issues()) != 2 {
		t.Fatalf("imported %d, want 2", len(f.issues()))
	}
	if err := f.fake.Move("VIN-1", "OPS", "OPS-7"); err != nil {
		t.Fatal(err)
	}
	if err := f.fake.Delete("VIN-2"); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	f.wantOK()
	moved := f.issue("10001").Remote
	if moved.Status != store.RemoteStatusMoved || moved.Repo != "OPS" || moved.Number != 7 ||
		moved.URL != f.ts.URL+"/browse/OPS-7" {
		t.Errorf("moved remote = %+v", moved)
	}
	gone := f.issue("10002")
	if gone.Remote.Status != store.RemoteStatusMissing {
		t.Errorf("deleted VIN-2 remote status = %q, want missing", gone.Remote.Status)
	}

	// Reappearance returns it to live.
	if err := f.fake.Update(func(c *fakejira.Corpus) error {
		for i := range c.Issues {
			if c.Issues[i].ID == "10002" {
				c.Issues[i].Fake = nil
				c.Issues[i].Updated = f.now().Format(fakejira.TimeLayout)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.tick()
	if r := f.issue("10002").Remote; r.Status != store.RemoteStatusLive {
		t.Errorf("reappeared VIN-2 remote status = %q, want live", r.Status)
	}

	// Forbidden (archived, or a lost grant) is missing too.
	if err := f.fake.Forbid("VIN-2"); err != nil {
		t.Fatal(err)
	}
	f.sweep()
	f.wantOK()
	if r := f.issue("10002").Remote; r.Status != store.RemoteStatusMissing {
		t.Errorf("forbidden VIN-2 remote status = %q, want missing", r.Status)
	}
}

// TestJiraSyncFailureReasons is proofs (8), (9) and (10): each failure
// gives its own sync-row reason, a 429 waits until Retry-After, a missing
// variable is no_credential, a changed project id is binding_changed, and
// the token is in no log line, sync row or error text.
func TestJiraSyncFailureReasons(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.tick()
	f.wantOK()

	reason := func(name string) string {
		t.Helper()
		f.advance(time.Minute)
		f.tick()
		st := f.row()
		if st.OK {
			t.Fatalf("%s: sync row ok, want a failure", name)
		}
		if strings.Contains(fmt.Sprintf("%+v", *st), jiraE2EToken) {
			t.Fatalf("%s: the token is in the sync row", name)
		}
		return st.Reason
	}

	f.setScenario(fakejira.ScenarioUnauthorized)
	if got := reason("401"); got != issuesource.ReasonUnauthorized {
		t.Errorf("401 reason = %q", got)
	}
	f.setScenario("success")
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/rest/api/3/project/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errorMessages":["You do not have permission."],"errors":{}}`))
			return true
		}
		return false
	})
	if got := reason("403"); got != issuesource.ReasonForbidden {
		t.Errorf("403 reason = %q", got)
	}
	f.setOverride(nil)
	f.setScenario(fakejira.ScenarioUnreachable)
	if got := reason("unreachable"); got != issuesource.ReasonUnreachable {
		t.Errorf("unreachable reason = %q", got)
	}

	// 429: rate limited until Retry-After, and no call before then.
	// The client stamps Retry-After on the real clock, so the syncer's
	// clock is brought back to it first.
	f.mu.Lock()
	f.clock = time.Now().UTC()
	f.mu.Unlock()
	asked := time.Now()
	f.setScenario(fakejira.ScenarioRateLimited)
	if got := reason("429"); got != issuesource.ReasonRateLimited {
		t.Errorf("429 reason = %q", got)
	}
	until := f.row().RateLimitedUntil
	if until == nil || !until.After(asked) {
		t.Fatalf("rate limited until %v, want after %v (Retry-After)", until, asked)
	}
	f.setScenario("success")
	calls := len(f.requests())
	f.mu.Lock()
	f.clock = until.Add(-time.Millisecond)
	f.mu.Unlock()
	f.tick()
	if n := len(f.requests()); n != calls {
		t.Errorf("a tick before Retry-After made %d calls", n-calls)
	}
	if st := f.row(); st.Reason != issuesource.ReasonRateLimited {
		t.Errorf("before the reset the row says %q", st.Reason)
	}
	f.mu.Lock()
	f.clock = until.Add(time.Second)
	f.mu.Unlock()
	f.tick()
	f.wantOK()

	// A missing variable is no_credential, never a crash.
	f.mu.Lock()
	f.env = []string{"PATH=/bin", "JIRA_USER=" + fakejira.DefaultUser}
	f.mu.Unlock()
	if got := reason("no token"); got != issuesource.ReasonNoCredential {
		t.Errorf("missing variable reason = %q", got)
	}
	f.mu.Lock()
	f.env = []string{"PATH=/bin", "JIRA_USER=" + fakejira.DefaultUser, "JIRA_API_TOKEN=" + jiraE2EToken}
	f.mu.Unlock()
	f.advance(time.Minute)
	f.tick()
	f.wantOK()

	// (9) The project key now resolves to another project id.
	if err := f.fake.Update(func(c *fakejira.Corpus) error {
		for i := range c.Projects {
			if c.Projects[i].Key == "VIN" {
				c.Projects[i].ID = "10099"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := reason("changed id"); got != issuesource.ReasonBindingChanged {
		t.Errorf("changed project id reason = %q", got)
	}

	// 404 on the project: a binding to a key the site does not have. A
	// rebind is a new binding, so the row starts afresh.
	if err := f.store.DeleteIssueSyncState(t.Context(), f.project.ID, issueprovider.Jira); err != nil {
		t.Fatal(err)
	}
	f.bind("NOPE")
	if got := reason("404"); got != issuesource.ReasonNotFound {
		t.Errorf("404 reason = %q", got)
	}

	logs := f.logs.String()
	if !strings.Contains(logs, "issue sync failed") {
		t.Fatalf("no failure was logged; the grep proves nothing:\n%s", logs)
	}
	if strings.Contains(logs, jiraE2EToken) {
		t.Errorf("the token is in the log:\n%s", logs)
	}
	for _, req := range f.requests() {
		if req.Path == "/rest/api/3/search" {
			t.Fatalf("the retired search endpoint was called: %+v", req)
		}
	}
}

// TestJiraSyncOutboxRowsStillWaitNoClient is proof (14): a human close of
// an imported Jira issue enqueues a write the outbox holds as no_client —
// the daemon never gives the outbox the Jira source until #909.
func TestJiraSyncOutboxRowsStillWaitNoClient(t *testing.T) {
	f := newJiraE2E(t, nil)
	ctx := t.Context()
	f.tick()
	f.wantOK()
	iss := f.issue("10001")
	if _, err := f.store.TransitionIssue(ctx, iss.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	outbox := NewIssueOutbox(f.store, func() config.Config { return f.cfg }, nil,
		slog.New(slog.NewTextHandler(f.logs, nil)))
	calls := len(f.requests())
	outbox.Drain(ctx)
	writes, err := f.store.IssueWrites(ctx, iss.ID)
	if err != nil || len(writes) != 1 {
		t.Fatalf("writes = %+v, %v; want one", writes, err)
	}
	if w := writes[0]; w.Status != store.OutboxPending || w.LastReason != outboxReasonNoClient {
		t.Errorf("outbox row = %+v, want pending %s", w, outboxReasonNoClient)
	}
	if n := len(f.requests()); n != calls {
		t.Errorf("the outbox made %d calls to Jira", n-calls)
	}
}
