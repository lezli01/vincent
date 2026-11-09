package issuesource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/jira/jiramap"
	"github.com/lezli01/vincent/internal/store"
)

// The Jira source's mapping and calls against the real Jira Cloud REST v3
// answers 142.3 captured (internal/jira/testdata/jira-cloud-2026-11; task
// 142.21, #954), replayed with their status lines and recorded headers over
// an httptest TLS server. The one documented-shape file kept beside them,
// cloud-docs-2026-11-04-issue.json, is read only by
// TestJiraAssigneeStaysInRemoteJSON: no captured issue has an assignee. The
// end-to-end proof against the fake Jira is the daemon's
// (internal/daemon/jirasync_test.go).

var jiraCaptureDir = filepath.Join("..", "jira", "testdata", "jira-cloud-2026-11")

// jiraCapture is one recorded answer.
type jiraCapture struct {
	status int
	header http.Header
	body   []byte
}

// loadJiraCapture reads <name>.json or <name>.txt with <name>.headers.txt.
func loadJiraCapture(t *testing.T, name string) jiraCapture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(jiraCaptureDir, name+".headers.txt"))
	if err != nil {
		t.Fatal(err)
	}
	c := jiraCapture{header: http.Header{}}
	for i, line := range strings.Split(strings.ReplaceAll(string(raw), "\r", ""), "\n") {
		if i == 0 {
			f := strings.Fields(line)
			if len(f) < 2 {
				t.Fatalf("%s: status line %q", name, line)
			}
			if c.status, err = strconv.Atoi(f[1]); err != nil {
				t.Fatalf("%s: status line %q", name, line)
			}
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			c.header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	for _, ext := range []string{".json", ".txt"} {
		if c.body, err = os.ReadFile(filepath.Join(jiraCaptureDir, name+ext)); err == nil {
			return c
		}
	}
	t.Fatalf("capture %s has no body file", name)
	return c
}

func (c jiraCapture) replay(w http.ResponseWriter) {
	for k, vs := range c.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(c.status)
	_, _ = w.Write(c.body)
}

// nextPageToken is a captured search page's token, "" on the last.
func nextPageToken(t *testing.T, name string) string {
	t.Helper()
	var p struct {
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.Unmarshal(loadJiraCapture(t, name).body, &p); err != nil {
		t.Fatal(err)
	}
	return p.NextPageToken
}

// jiraSite replays the captures and records every search's JQL.
type jiraSite struct {
	t  *testing.T
	ts *httptest.Server
	// pages routes a search by the nextPageToken the previous page answered.
	pages map[string]string

	mu   sync.Mutex
	jqls []string
	// search names the capture that answers a search; nil is the three-page
	// walk.
	search func(jql string) string
	// status, when set, answers every request with it: the captured answer
	// for 401 and 429, a bare envelope otherwise.
	status int
}

func newJiraSite(t *testing.T) *jiraSite {
	t.Helper()
	s := &jiraSite{t: t, pages: map[string]string{
		"":                                   "search_jql_page1",
		nextPageToken(t, "search_jql_page1"): "search_jql_page2_middle",
		nextPageToken(t, "search_jql_page2_middle"): "search_jql_page3_last",
	}}
	s.ts = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *jiraSite) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	status, search := s.status, s.search
	s.mu.Unlock()
	reply := func(name string) { loadJiraCapture(s.t, name).replay(w) }
	switch status {
	case 0:
	case http.StatusUnauthorized:
		reply("myself_401_invalid_token")
		return
	case http.StatusTooManyRequests:
		reply("rate_limited_429")
		return
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"errorMessages":["no"],"errors":{}}`))
		return
	}
	switch p := r.URL.Path; p {
	case "/rest/api/3/project/VCA":
		reply("project_company_managed")
	case "/rest/api/3/search/jql":
		jql := r.URL.Query().Get("jql")
		s.mu.Lock()
		s.jqls = append(s.jqls, jql)
		s.mu.Unlock()
		if search != nil {
			reply(search(jql))
			return
		}
		name, ok := s.pages[r.URL.Query().Get("nextPageToken")]
		if !ok {
			s.t.Errorf("search with an unknown page token")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply(name)
	case "/rest/api/3/issue/bulkfetch":
		reply("bulkfetch_deleted_and_moved")
	case "/rest/api/3/issue/10005/comment":
		reply("comments_company_managed")
	case "/rest/api/3/issue/10113/comment":
		reply("comments_team_managed")
	case "/rest/api/3/issue/10004":
		reply("issue_rendered_rich")
	case "/rest/api/3/issue/10006":
		reply("issue_moved_by_id")
	case "/rest/api/3/issue/10008":
		reply("issue_done_company_managed")
	default:
		reply("issue_deleted_404")
	}
}

func (s *jiraSite) source(creds func() (string, string, error)) *Jira {
	if creds == nil {
		creds = func() (string, string, error) { return "me@example.com", "tok", nil }
	}
	return NewJira(func(site string) *jira.Client { return jira.New(site, s.ts.Client(), creds) })
}

func (s *jiraSite) project(jql string) store.Project {
	return store.Project{ID: 1, IssueSource: store.IssueSource{
		Provider: store.IssueProviderJira, JiraSite: s.ts.URL, JiraProjectKey: "VCA", JiraJQL: jql,
	}}
}

// bound binds and resolves the captured company-managed project, VCA.
func (s *jiraSite) bound(src *Jira, jql string) Binding {
	s.t.Helper()
	b, ok := src.Bind(context.Background(), s.project(jql))
	if !ok || b.String() != "VCA" {
		s.t.Fatalf("Bind = %q, %v", b.String(), ok)
	}
	resolved, id, err := src.ResolveBinding(context.Background(), b)
	if err != nil || id != "10001" || resolved.String() != "VCA" {
		s.t.Fatalf("ResolveBinding = %q, %q, %v", resolved.String(), id, err)
	}
	return resolved
}

func TestJiraBindNeedsAJiraBinding(t *testing.T) {
	src := NewJira(func(site string) *jira.Client { return jira.New(site, nil, nil) })
	for _, p := range []store.Project{
		{},
		{IssueSource: store.IssueSource{Provider: store.IssueProviderGitHub}},
		{IssueSource: store.IssueSource{Provider: store.IssueProviderJira, JiraSite: "acme.atlassian.net"}},
		{IssueSource: store.IssueSource{Provider: store.IssueProviderJira, JiraProjectKey: "EX"}},
	} {
		if _, ok := src.Bind(context.Background(), p); ok {
			t.Errorf("Bind(%+v) = ok", p.IssueSource)
		}
	}
	if src.Provider() != issueprovider.Jira {
		t.Errorf("Provider = %q", src.Provider())
	}
}

func TestJiraIssueMapping(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "")
	page, err := src.ListIssues(context.Background(), b, ListOptions{OpenOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Truncated || len(page.Issues) != 103 {
		t.Fatalf("listing = %d issues, truncated %v; want the three captured pages", len(page.Issues), page.Truncated)
	}
	byKey := map[string]Issue{}
	for _, is := range page.Issues {
		byKey[is.Remote.RemoteKey] = is
	}
	for _, tc := range []struct {
		id, key, body    string
		number, comments int
		state            issuestate.State
		reason           issuestate.Reason
		labels           []string
	}{
		{id: "10004", key: "VCA-1", number: 1, body: "# Rich fixture heading", state: issuestate.Open, labels: []string{"batch-0", "fixture"}},
		{id: "10005", key: "VCA-2", number: 2, body: "Seeded fixture issue number 2.", comments: 3, state: issuestate.Open, labels: []string{"batch-0", "fixture"}},
		{id: "10008", key: "VCA-5", number: 5, body: "Seeded fixture issue number 5.", state: issuestate.Closed, reason: issuestate.Completed, labels: []string{"batch-0", "fixture"}},
		{id: "10108", key: "VCA-105", number: 105, body: "Seeded fixture issue number 105.", state: issuestate.Open, labels: []string{"batch-2", "fixture"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			is, ok := byKey[tc.id]
			if !ok {
				t.Fatalf("issue %s not listed", tc.id)
			}
			r := is.Remote
			if r.Provider != issueprovider.Jira || r.RemoteKey != tc.id || r.Repo != "VCA" || r.Number != tc.number ||
				r.URL != site.ts.URL+"/browse/"+tc.key {
				t.Errorf("identity = %s %s %s %d %s", r.Provider, r.RemoteKey, r.Repo, r.Number, r.URL)
			}
			if strings.Contains(r.URL, "/rest/api/") {
				t.Errorf("URL is the API's self, not the browse page: %s", r.URL)
			}
			if got := strings.TrimSpace(r.Body); !strings.HasPrefix(got, tc.body) {
				t.Errorf("body = %q, want it to open %q", got, tc.body)
			}
			if r.Kind != "task" || r.State != tc.state || r.CloseReason != tc.reason || !slices.Equal(r.Labels, tc.labels) {
				t.Errorf("kind %q state %s reason %q labels %v", r.Kind, r.State, r.CloseReason, r.Labels)
			}
			if r.Author != "Fixture User" || r.Title == "" || r.RemoteUpdatedAt == nil {
				t.Errorf("author %q title %q updated %v", r.Author, r.Title, r.RemoteUpdatedAt)
			}
			if is.Comments != tc.comments {
				t.Errorf("comments = %d, want %d", is.Comments, tc.comments)
			}
			if store.RemotePayloadKey(r.RemoteJSON) != tc.key {
				t.Errorf("remote_json key = %q", store.RemotePayloadKey(r.RemoteJSON))
			}
			want := store.IssueStateValue{State: string(tc.state), Reason: string(tc.reason)}
			if is.StateValue != want {
				t.Errorf("state value = %+v, want %+v", is.StateValue, want)
			}
		})
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	const jql = "project = 10001 AND statusCategory != Done ORDER BY id ASC"
	if !slices.Equal(site.jqls, []string{jql, jql, jql}) {
		t.Errorf("searches = %q", site.jqls)
	}
}

// TestJiraAssigneeStaysInRemoteJSON: the assignee is in remote_json and in
// no other column (decision 5 of #908's scope). No captured issue has an
// assignee, so this maps the kept documented-shape issue directly.
func TestJiraAssigneeStaysInRemoteJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "jira", "testdata", "cloud-docs-2026-11-04-issue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc jira.Issue
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Fields.Assignee == nil {
		t.Fatal("the documented-shape issue lost its assignee")
	}
	is := mapJiraIssue(&doc, "https://example.atlassian.net/browse/"+doc.Key, nil, jiramap.Config{})
	var rec map[string]any
	if err := json.Unmarshal([]byte(is.Remote.RemoteJSON), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["assignee"] == nil || rec["assignee_account_id"] == nil {
		t.Errorf("remote_json = %s, want the assignee kept", is.Remote.RemoteJSON)
	}
	if strings.Contains(is.Remote.Author, doc.Fields.Assignee.AccountID) {
		t.Errorf("an account id leaked into the author: %q", is.Remote.Author)
	}
}

func TestJiraListingCursors(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "labels = fixture")

	page, err := src.ListIssues(context.Background(), b, ListOptions{OpenOnly: true, After: "10000", PageCap: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Page 1's last issue is VCA-42, id 10045.
	if !page.Truncated || len(page.Issues) != 40 || page.ResumeAfter != "10045" || !page.ResumeSince.IsZero() {
		t.Errorf("open page: %d issues, truncated %v after %q since %v", len(page.Issues), page.Truncated, page.ResumeAfter, page.ResumeSince)
	}
	// An incremental walk resumes by id from its own bound, never by time:
	// the resumed listing asks from the first one's since, not the later
	// one it is given, and after the last id read (review F1).
	since := time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC)
	page, err = src.ListIssues(context.Background(), b, ListOptions{Since: since, After: "ignored", PageCap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || !page.ResumeSince.IsZero() || page.ResumeAfter == "" {
		t.Fatalf("incremental page: truncated %v after %q since %v", page.Truncated, page.ResumeAfter, page.ResumeSince)
	}
	last := page.Issues[len(page.Issues)-1].Remote.RemoteKey
	if want := strconv.FormatInt(since.UnixMilli(), 10) + ":" + last; last != "10045" || page.ResumeAfter != want {
		t.Errorf("incremental cursor %q, want %q", page.ResumeAfter, want)
	}
	if _, err := src.ListIssues(context.Background(), b, ListOptions{Since: since.Add(time.Hour), After: page.ResumeAfter, PageCap: 1}); err != nil {
		t.Fatal(err)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	want := []string{
		"project = 10001 AND statusCategory != Done AND (labels = fixture) AND id > 10000 ORDER BY id ASC",
		jira.IncrementalJQL("10001", "labels = fixture", since, ""),
		jira.IncrementalJQL("10001", "labels = fixture", since, last),
	}
	if !slices.Equal(site.jqls, want) {
		t.Errorf("searches = %q, want %q", site.jqls, want)
	}
}

// TestJiraCommentsDropRestricted: a role-restricted comment is dropped on
// both project types — Administrators on company-managed VCA-2, Member on
// team-managed VTM-2.
func TestJiraCommentsDropRestricted(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "")
	for _, tc := range []struct {
		id   string
		want []string
	}{
		{"10005", []string{"10000 Fixture User First public comment, edited.", "10001 Fixture User Third public comment."}},
		{"10113", []string{"10004 Fixture User Public team-managed comment."}},
	} {
		thread, err := src.ListComments(context.Background(), b, &Issue{Remote: store.RemoteIssue{RemoteKey: tc.id}})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range thread {
			got = append(got, c.Remote.RemoteKey+" "+c.Remote.Author+" "+strings.TrimSpace(c.Remote.Body))
			if c.Remote.CreatedAt.IsZero() || c.Remote.UpdatedAt.IsZero() {
				t.Errorf("comment %s has no times", c.Remote.RemoteKey)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("issue %s thread = %q, want %q (the restricted comment dropped)", tc.id, got, tc.want)
		}
	}
}

// TestJiraMoved: VCA-3 (id 10006) was moved to VCB-4 on the captured site.
// Read by id it is returned mapped where it is, with an error Classify reads
// as moved; a bulk read reports it with MovedTo, and drops a deleted id
// silently.
func TestJiraMoved(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "")

	is, err := src.GetIssue(context.Background(), b, store.IssueRemote{RemoteKey: "10006"})
	f := src.Classify(err)
	if f.Reason != ReasonMoved || f.Location != site.ts.URL+"/browse/VCB-4" || is.Remote.Repo != "VCB" || is.Remote.Number != 4 || is.Remote.RemoteKey != "10006" {
		t.Errorf("moved GetIssue = %+v, %+v", is.Remote, f)
	}
	// 10007 is deleted: bulkfetch leaves it out with no issueErrors.
	got, err := src.GetIssues(context.Background(), b, []store.IssueRemote{{RemoteKey: "10007"}, {RemoteKey: "10006"}, {RemoteKey: "10004"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["10006"].MovedTo != site.ts.URL+"/browse/VCB-4" || got["10004"].MovedTo != "" ||
		got["10004"].Issue.Remote.Repo != "VCA" {
		t.Errorf("bulk = %+v", got)
	}
	if _, ok := got["10007"]; ok {
		t.Error("a deleted id was reported")
	}
	// The deleted issue read alone is a 404, which is not_found: Jira answers
	// deleted and unreadable alike, so it is never gone (decision 6).
	_, err = src.GetIssue(context.Background(), b, store.IssueRemote{RemoteKey: "10007"})
	if f := src.Classify(err); f.Reason != ReasonNotFound || !strings.Contains(err.Error(), "Issue does not exist or you do not have permission to see it.") {
		t.Errorf("a 404 classifies as %q (%v), want not_found (never gone)", f.Reason, err)
	}
	// Still at the binding and closed there.
	is, err = src.GetIssue(context.Background(), b, store.IssueRemote{RemoteKey: "10008"})
	if err != nil || is.Remote.State != issuestate.Closed || is.Remote.CloseReason != issuestate.Completed {
		t.Errorf("VCA-5 = %+v, %v", is.Remote, err)
	}
}

// TestJiraBulkDropsJQLExcluded: with extra JQL on the binding, a bulk read
// leaves out what the JQL no longer matches, so the importer marks it
// missing. A moved issue is reported moved without being filtered.
func TestJiraBulkDropsJQLExcluded(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "labels = sync")
	site.mu.Lock()
	site.search = func(string) string { return "search_jql_page3_last" } // VCA-1 not among them
	site.mu.Unlock()
	got, err := src.GetIssues(context.Background(), b, []store.IssueRemote{{RemoteKey: "10004"}, {RemoteKey: "10006"}, {RemoteKey: "10007"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["10004"]; ok || len(got) != 1 || got["10006"].MovedTo == "" {
		t.Errorf("bulk = %v, want only the moved 10006", got)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if last := site.jqls[len(site.jqls)-1]; last != "project = 10001 AND (labels = sync) AND id in (10004) ORDER BY id ASC" {
		t.Errorf("filter JQL = %q", last)
	}
}

func TestJiraClassify(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, ReasonUnauthorized},
		{http.StatusForbidden, ReasonForbidden},
		{http.StatusNotFound, ReasonNotFound},
		{http.StatusTooManyRequests, ReasonRateLimited},
	} {
		site := newJiraSite(t)
		src := site.source(nil)
		b, _ := src.Bind(context.Background(), site.project(""))
		site.status = tc.status
		start := time.Now()
		_, _, err := src.ResolveBinding(context.Background(), b)
		f := src.Classify(err)
		if f.Reason != tc.want {
			t.Errorf("%d: reason %q, want %q", tc.status, f.Reason, tc.want)
		}
		// The captured 429 says retry-after: 1.
		if tc.status == http.StatusTooManyRequests && f.ResetAt.Before(start.Add(time.Second)) {
			t.Errorf("429: ResetAt %v, want at least a second after %v", f.ResetAt, start)
		}
	}
	site := newJiraSite(t)
	src := site.source(func() (string, string, error) {
		return jira.EnvCredentials([]string{"JIRA_USER=me"}, "JIRA_USER", "JIRA_API_TOKEN")
	})
	b, _ := src.Bind(context.Background(), site.project(""))
	if _, _, err := src.ResolveBinding(context.Background(), b); src.Classify(err).Reason != ReasonNoCredential {
		t.Errorf("a missing variable classifies as %q", src.Classify(err).Reason)
	}
	unreachable := NewJira(func(string) *jira.Client {
		return jira.New("https://127.0.0.1:1", nil, func() (string, string, error) { return "u", "t", nil })
	})
	b, _ = unreachable.Bind(context.Background(), site.project(""))
	if _, _, err := unreachable.ResolveBinding(context.Background(), b); unreachable.Classify(err).Reason != ReasonUnreachable {
		t.Errorf("an unreachable site classifies as %q", unreachable.Classify(err).Reason)
	}
	if f := src.Classify(nil); f != (Failure{}) {
		t.Errorf("Classify(nil) = %+v", f)
	}
}

func TestJiraLocate(t *testing.T) {
	src := NewJira(nil)
	if _, ok := src.Locate(store.IssueRemote{RemoteKey: "10001", Repo: "EX"}); !ok {
		t.Error("Locate refused an addressable row")
	}
	if _, ok := src.Locate(store.IssueRemote{Repo: "EX"}); ok {
		t.Error("Locate accepted a row with no remote key")
	}
}
