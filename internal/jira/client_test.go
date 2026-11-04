package jira

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/fakejira"
)

// testToken is deliberately distinctive: every error string a test sees is
// grepped for it (and for the Authorization payload built from it).
const (
	testUser  = "bot@example.com"
	testToken = "tok-ATATT3xFfGF0-never-in-an-error"
)

// errorLog collects every error string the tests produce, for the redaction
// check in TestNoErrorCarriesTheToken.
var errorLog struct {
	sync.Mutex
	lines []string
}

func record(err error) error {
	if err != nil {
		errorLog.Lock()
		errorLog.lines = append(errorLog.lines, err.Error())
		errorLog.Unlock()
	}
	return err
}

type fakeLeg struct {
	t      *testing.T
	store  *fakejira.Store
	srv    *httptest.Server
	client *Client
	calls  atomic.Int32
	// queries records each request's raw query, by path.
	mu      sync.Mutex
	queries map[string][]url.Values
}

func newFake(t *testing.T) *fakeLeg {
	t.Helper()
	dir := t.TempDir()
	f := &fakeLeg{t: t, queries: map[string][]url.Values{}}
	f.store = &fakejira.Store{
		Path:         filepath.Join(dir, "corpus.json"),
		ScenarioFile: filepath.Join(dir, "scenario"),
		RequestLog:   filepath.Join(dir, "requests.log"),
		User:         testUser,
		Token:        testToken,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries[r.URL.Path] = append(f.queries[r.URL.Path], r.URL.Query())
		f.mu.Unlock()
		f.store.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.client = New(f.srv.URL+"/", f.srv.Client(), func() (string, string, error) {
		f.calls.Add(1)
		return testUser, testToken, nil
	})
	return f
}

func (f *fakeLeg) scenario(s string) {
	f.t.Helper()
	if err := os.WriteFile(f.store.ScenarioFile, []byte(s+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeLeg) count(method, path string) int {
	f.t.Helper()
	n, err := fakejira.CountRequests(f.store.RequestLog, method, path, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fakeLeg) seed(n int) []string {
	f.t.Helper()
	var ids []string
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := f.store.Update(func(c *fakejira.Corpus) error {
		ids = c.SeedIssues("VIN", n, base)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	return ids
}

func asError(err error, e **Error) bool { return errors.As(err, e) }

func TestNewNormalizesSite(t *testing.T) {
	for in, want := range map[string]string{
		"acme.atlassian.net":                  "https://acme.atlassian.net",
		"https://acme.atlassian.net/":         "https://acme.atlassian.net",
		"\thttps://acme.atlassian.net//\n":    "https://acme.atlassian.net",
		"http://127.0.0.1:8080/":              "http://127.0.0.1:8080",
		"https://u:secret@acme.atlassian.net": "https://acme.atlassian.net",
	} {
		c := New(in, nil, nil)
		if c.Site() != want {
			t.Errorf("New(%q).Site() = %q, want %q", in, c.Site(), want)
		}
		if c.HTTP == nil || c.HTTP.Timeout <= 0 {
			t.Errorf("New(%q): no default timeout", in)
		}
	}
	if got := New("acme.atlassian.net", nil, nil).BrowseURL("VIN-7"); got != "https://acme.atlassian.net/browse/VIN-7" {
		t.Errorf("BrowseURL = %q", got)
	}
}

func TestMetadataAgainstFake(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	si, err := f.client.ServerInfo(ctx)
	if record(err) != nil || si.DeploymentType != "Cloud" || si.BaseURL != f.srv.URL {
		t.Errorf("serverInfo %+v %v", si, err)
	}
	me, err := f.client.Myself(ctx)
	if record(err) != nil || me.AccountID != "557058:fake-vincent" || me.DisplayName != "Vincent Bot" {
		t.Errorf("myself %+v %v", me, err)
	}
	p, err := f.client.Project(ctx, "VIN")
	if record(err) != nil || p != (Project{ID: "10000", Key: "VIN", Name: "Vincent"}) {
		t.Errorf("project %+v %v", p, err)
	}
	_, err = f.client.Project(ctx, "NOPE")
	var e *Error
	if record(err) == nil || !asError(err, &e) || e.Reason != ReasonNotFound || e.Status != 404 || !strings.Contains(err.Error(), "NOPE") {
		t.Errorf("unknown project: %v", err)
	}
	if n := f.calls.Load(); n != 4 {
		t.Errorf("Creds called %d times for 4 requests; it must be called on every one", n)
	}
}

// TestSearchPagesPast500: more than 500 issues are read in pages of 100 by
// nextPageToken, Truncated reports a maxPages stop, and nothing ever hits the
// removed /rest/api/3/search.
func TestSearchPagesPast500(t *testing.T) {
	f := newFake(t)
	ids := f.seed(550)
	ctx := context.Background()
	jql := InitialJQL("10000", "", "")

	res, err := f.client.Search(ctx, jql, 3)
	if record(err) != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Issues) != 300 {
		t.Fatalf("maxPages 3: %d issues, truncated %v", len(res.Issues), res.Truncated)
	}
	// Resuming is the caller's JQL cursor, never a page token.
	rest, err := f.client.Search(ctx, InitialJQL("10000", "", res.Issues[len(res.Issues)-1].ID), 10)
	if record(err) != nil {
		t.Fatal(err)
	}
	if rest.Truncated || len(rest.Issues) != 552-300 {
		t.Fatalf("resume: %d issues, truncated %v", len(rest.Issues), rest.Truncated)
	}
	all, err := f.client.Search(ctx, jql, 0)
	if record(err) != nil || !all.Truncated || len(all.Issues) != 100 {
		t.Fatalf("maxPages 0 is one page: %d, %v, %v", len(all.Issues), all.Truncated, err)
	}
	full, err := f.client.Search(ctx, jql, 100)
	if record(err) != nil || full.Truncated || len(full.Issues) != 552 {
		t.Fatalf("full: %d, %v, %v", len(full.Issues), full.Truncated, err)
	}
	if last := full.Issues[len(full.Issues)-1]; last.ID != ids[len(ids)-1] || last.Fields.Summary == "" || last.Fields.Status == nil {
		t.Fatalf("last issue %+v", last)
	}

	if n := f.count("GET", "rest/api/3/search"); n != 0 {
		t.Fatalf("%d requests hit the removed /rest/api/3/search", n)
	}
	if n := f.count("GET", "rest/api/3/search/jql"); n != 3+3+1+6 {
		t.Errorf("%d search/jql requests, want 13", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, q := range f.queries["/rest/api/3/search/jql"] {
		if q.Get("fields") != strings.Join(IssueFieldNames, ",") || q.Get("expand") != "renderedFields" || q.Get("maxResults") != "100" {
			t.Fatalf("search query %v", q)
		}
	}
}

func TestIncrementalJQLAgainstFake(t *testing.T) {
	f := newFake(t)
	// VIN-2 was updated 2026-08-02T10:00:00.123 and VIN-3 on the 3rd: a mark
	// two minutes after VIN-2's edit still reads it, through the Overlap.
	since := time.Date(2026, 8, 2, 10, 2, 0, 0, time.UTC)
	res, err := f.client.Search(context.Background(), IncrementalJQL("10000", "", since), 5)
	if record(err) != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 2 || res.Issues[0].Key != "VIN-2" || res.Issues[1].Key != "VIN-3" {
		t.Fatalf("incremental: %+v", res.Issues)
	}
	all, err := f.client.Search(context.Background(), IncrementalJQL("10000", "", time.Time{}), 5)
	if record(err) != nil || len(all.Issues) != 3 {
		t.Fatalf("zero since: %d issues (%v)", len(all.Issues), err)
	}
}

func TestGetIssueAndBulkFetch(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	is, err := f.client.GetIssue(ctx, "VIN-1")
	if record(err) != nil {
		t.Fatal(err)
	}
	if is.ID != "10001" || is.RenderedFields.Description != "<p>Mirror the board.</p>" || is.Fields.Comment == nil || is.Fields.Comment.Total != 2 {
		t.Fatalf("issue %+v", is)
	}
	if err := f.store.Delete("VIN-2"); err != nil {
		t.Fatal(err)
	}
	_, err = f.client.GetIssue(ctx, "VIN-2")
	if record(err) == nil || ReasonOf(err) != ReasonNotFound {
		t.Fatalf("deleted issue: %v", err)
	}
	got, err := f.client.BulkFetch(ctx, []string{"10001", "10002", "10003", "99999"})
	if record(err) != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "VIN-1" || got[1].Key != "VIN-3" || got[1].Fields.Resolution == nil {
		t.Fatalf("bulkfetch %+v", got)
	}
	if got, err := f.client.BulkFetch(ctx, nil); err != nil || got != nil {
		t.Errorf("empty bulkfetch: %v %v", got, err)
	}
	before := f.count("POST", "rest/api/3/issue/bulkfetch")
	_, err = f.client.BulkFetch(ctx, make([]string, BulkFetchMax+1))
	if record(err) == nil || ReasonOf(err) != ReasonBadRequest {
		t.Fatalf("101 ids: %v", err)
	}
	if after := f.count("POST", "rest/api/3/issue/bulkfetch"); after != before {
		t.Errorf("101 ids sent a request")
	}
}

func TestCommentsEveryPageWithVisibility(t *testing.T) {
	f := newFake(t)
	for range 130 {
		if _, err := f.store.AddComment("VIN-1", []byte(`{"type":"doc","version":1,"content":[]}`), "<p>x</p>", nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.client.Comments(context.Background(), "10001")
	if record(err) != nil {
		t.Fatal(err)
	}
	if len(got) != 132 {
		t.Fatalf("%d comments, want 132", len(got))
	}
	if got[0].ID != "20001" || got[0].RenderedBody != "<p>First.</p>" || got[0].Visibility != nil {
		t.Errorf("first %+v", got[0])
	}
	if v := got[1].Visibility; v == nil || v.Type != "role" || v.Value != "Administrators" {
		t.Errorf("restricted comment %+v", got[1])
	}
	if n := f.count("GET", "rest/api/3/issue/10001/comment"); n != 2 {
		t.Errorf("%d comment pages, want 2", n)
	}
	f.mu.Lock()
	q := f.queries["/rest/api/3/issue/10001/comment"][0]
	f.mu.Unlock()
	if q.Get("orderBy") != "created" || q.Get("expand") != "renderedBody" {
		t.Errorf("comment query %v", q)
	}
}

func TestScenarioErrors(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	f.client.now = func() time.Time { return now }
	ctx := context.Background()

	f.scenario(fakejira.ScenarioUnauthorized)
	_, err := f.client.Myself(ctx)
	if record(err) == nil || ReasonOf(err) != ReasonUnauthorized {
		t.Errorf("unauthorized: %v", err)
	}
	if n := f.count("GET", "rest/api/3/myself"); n != 1 {
		t.Errorf("a 401 was retried: %d requests", n)
	}

	f.scenario(fakejira.ScenarioRateLimited)
	_, err = f.client.Search(ctx, InitialJQL("10000", "", ""), 3)
	var e *Error
	if record(err) == nil || !asError(err, &e) || e.Reason != ReasonRateLimited || !e.RetryAt.Equal(now.Add(2*time.Second)) {
		t.Errorf("rate limited: %v", err)
	}

	f.scenario(fakejira.ScenarioUnreachable)
	_, err = f.client.ServerInfo(ctx)
	if record(err) == nil || ReasonOf(err) != ReasonUnreachable {
		t.Errorf("unreachable: %v", err)
	}
	if strings.Contains(err.Error(), "?") {
		t.Errorf("unreachable error carries a query: %v", err)
	}
}

// TestStatusMapping covers the answers the fake has no scenario for.
func TestStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		header map[string]string
		body   string
		reason string
		text   string
	}{
		{403, map[string]string{"X-Seraph-LoginReason": "AUTHENTICATED_FAILED,AUTHENTICATION_DENIED"}, `{"errorMessages":[]}`, ReasonForbidden, "CAPTCHA"},
		{403, nil, `{"errorMessages":["You do not have permission."]}`, ReasonForbidden, "permission"},
		{400, nil, `{"errorMessages":["Error in the JQL Query: bad"],"errors":{}}`, ReasonBadRequest, "JQL"},
		{429, map[string]string{"X-RateLimit-Reset": "2026-11-04T08:05:00Z"}, `{}`, ReasonRateLimited, "retry at 2026-11-04T08:05:00Z"},
		{503, nil, "<html>down</html>", ReasonUnreachable, "down"},
		{200, nil, "not json", ReasonBadResponse, "decode"},
	} {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			for k, v := range tc.header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		c := New(srv.URL, srv.Client(), staticCreds(testUser, testToken))
		_, err := c.Myself(context.Background())
		srv.Close()
		var e *Error
		if record(err) == nil || !asError(err, &e) || e.Reason != tc.reason || !strings.Contains(err.Error(), tc.text) {
			t.Errorf("%d: %v, want %s containing %q", tc.status, err, tc.reason, tc.text)
		}
		if hits.Load() != 1 {
			t.Errorf("%d: %d requests; the client never retries", tc.status, hits.Load())
		}
	}
}

func TestNoCredentialSendsNothing(t *testing.T) {
	f := newFake(t)
	want := &Error{Op: "credentials", Reason: ReasonNoCredential, Err: errors.New("environment variable JIRA_TOKEN is not set")}
	f.client.Creds = func() (string, string, error) { return "", "", want }
	_, err := f.client.Myself(context.Background())
	if got := (*Error)(nil); record(err) == nil || !errors.As(err, &got) || got != want {
		t.Fatalf("err %v, want the Creds error unchanged", err)
	}
	f.client.Creds = func() (string, string, error) { return "", "", errors.New("keychain locked") }
	_, err = f.client.Myself(context.Background())
	if record(err) == nil || ReasonOf(err) != ReasonNoCredential {
		t.Fatalf("other Creds failure: %v", err)
	}
	f.client.Creds = nil
	if _, err := f.client.Myself(context.Background()); record(err) == nil || ReasonOf(err) != ReasonNoCredential {
		t.Fatalf("nil Creds: %v", err)
	}
	if reqs, err := fakejira.Requests(f.store.RequestLog); err != nil || len(reqs) != 0 {
		t.Fatalf("requests sent without a credential: %v %v", reqs, err)
	}
}

// leakyTransport fails every round trip with an error quoting the request's
// Authorization header and the token, the worst a transport could do.
type leakyTransport struct{}

func (leakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed for " + r.Header.Get("Authorization") + " token=" + testToken)
}

func TestTransportErrorIsRedacted(t *testing.T) {
	c := New("https://"+testUser+":"+testToken+"@acme.atlassian.net", &http.Client{Transport: leakyTransport{}}, staticCreds(testUser, testToken))
	_, err := c.GetIssue(context.Background(), "VIN-1")
	if record(err) == nil || ReasonOf(err) != ReasonUnreachable {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("expected a redaction marker: %v", err)
	}
	// Even unwrapped, the cause carries no secret.
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), testToken) {
			t.Fatalf("unwrapped error leaks the token: %v", e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New("https://acme.atlassian.net", nil, staticCreds(testUser, testToken)).Myself(ctx)
	if record(err) == nil || !errors.Is(err, context.Canceled) || ReasonOf(err) != ReasonUnreachable {
		t.Fatalf("cancelled: %v", err)
	}
}

// TestZZNoErrorCarriesTheToken greps every error string the other tests
// recorded. Go runs tests in source order within a file; it is also run on
// its own errors so it never passes vacuously.
func TestZZNoErrorCarriesTheToken(t *testing.T) {
	TestScenarioErrors(t)
	TestStatusMapping(t)
	TestTransportErrorIsRedacted(t)
	payload := base64.StdEncoding.EncodeToString([]byte(testUser + ":" + testToken))
	errorLog.Lock()
	defer errorLog.Unlock()
	if len(errorLog.lines) < 10 {
		t.Fatalf("only %d error strings recorded", len(errorLog.lines))
	}
	for _, line := range errorLog.lines {
		if strings.Contains(line, testToken) || strings.Contains(line, payload) || strings.Contains(line, "Basic ") {
			t.Errorf("error string leaks a credential: %s", line)
		}
	}
}

// TestIsALeaf: the client imports the standard library and
// internal/jira/jiramap (and so jiramap's own internal/issuestate) only —
// never internal/issuesource, internal/store or internal/config.
func TestIsALeaf(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if !strings.HasPrefix(dep, "github.com/lezli01/vincent/") {
			if first, _, _ := strings.Cut(dep, "/"); strings.Contains(first, ".") {
				t.Errorf("internal/jira depends on third-party %s", dep)
			}
			continue
		}
		switch {
		case strings.HasSuffix(dep, "/internal/jira"),
			strings.HasSuffix(dep, "/internal/jira/jiramap"),
			strings.HasSuffix(dep, "/internal/issuestate"):
		default:
			t.Errorf("internal/jira depends on %s; it may import only internal/jira/jiramap", dep)
		}
	}
}
