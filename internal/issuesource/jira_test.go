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
	"github.com/lezli01/vincent/internal/store"
)

// The Jira source's mapping and calls, against the client unit's captured
// Cloud documentation fixtures (internal/jira/testdata) served over an
// httptest TLS server. The end-to-end proof against the fake Jira is the
// daemon's (internal/daemon/jirasync_test.go).

func jiraFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "jira", "testdata", "cloud-docs-2026-11-04-"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// jiraSite serves the fixtures and records every search's JQL.
type jiraSite struct {
	t  *testing.T
	ts *httptest.Server

	mu   sync.Mutex
	jqls []string
	// search answers a search; nil is the two-page fixture walk.
	search func(jql string) []byte
	// status, when set, answers every request with it.
	status int
}

func newJiraSite(t *testing.T) *jiraSite {
	t.Helper()
	s := &jiraSite{t: t}
	s.ts = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *jiraSite) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	status, search := s.status, s.search
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "17")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"errorMessages":["no"],"errors":{}}`))
		return
	}
	p := r.URL.Path
	switch p {
	case "/rest/api/3/project/EX":
		_, _ = w.Write(jiraFixture(s.t, "project"))
	case "/rest/api/3/search/jql":
		jql := r.URL.Query().Get("jql")
		s.mu.Lock()
		s.jqls = append(s.jqls, jql)
		s.mu.Unlock()
		switch {
		case search != nil:
			_, _ = w.Write(search(jql))
		case r.URL.Query().Get("nextPageToken") == "":
			_, _ = w.Write(jiraFixture(s.t, "search-jql-page"))
		default:
			_, _ = w.Write(jiraFixture(s.t, "search-jql-last"))
		}
	case "/rest/api/3/issue/bulkfetch":
		_, _ = w.Write(jiraFixture(s.t, "bulkfetch"))
	case "/rest/api/3/issue/10001/comment":
		_, _ = w.Write(jiraFixture(s.t, "comments"))
	case "/rest/api/3/issue/10001":
		_, _ = w.Write(jiraFixture(s.t, "issue"))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorMessages":["Issue does not exist or you do not have permission to see it."],"errors":{}}`))
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
		Provider: store.IssueProviderJira, JiraSite: s.ts.URL, JiraProjectKey: "EX", JiraJQL: jql,
	}}
}

// bound binds and resolves the fixture project.
func (s *jiraSite) bound(src *Jira, jql string) Binding {
	s.t.Helper()
	b, ok := src.Bind(context.Background(), s.project(jql))
	if !ok || b.String() != "EX" {
		s.t.Fatalf("Bind = %q, %v", b.String(), ok)
	}
	resolved, id, err := src.ResolveBinding(context.Background(), b)
	if err != nil || id != "10000" || resolved.String() != "EX" {
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
	if page.Truncated || len(page.Issues) != 3 {
		t.Fatalf("listing = %d issues, truncated %v; want the two fixture pages", len(page.Issues), page.Truncated)
	}
	byKey := map[string]Issue{}
	for _, is := range page.Issues {
		byKey[is.Remote.RemoteKey] = is
	}
	for _, tc := range []struct {
		id, key, body, kind string
		number, comments    int
		state               issuestate.State
		reason              issuestate.Reason
		labels              []string
	}{
		{id: "10001", key: "EX-1", number: 1, body: "Body.", kind: "bug", comments: 2, state: issuestate.Open, labels: []string{"sync", "p1"}},
		{id: "10002", key: "EX-2", number: 2, body: "In **progress**.", kind: "bug", state: issuestate.Open, labels: []string{"sync", "p1"}},
		{id: "10003", key: "EX-3", number: 3, body: "Body.", kind: "bug", state: issuestate.Closed, reason: issuestate.Completed},
	} {
		t.Run(tc.key, func(t *testing.T) {
			is, ok := byKey[tc.id]
			if !ok {
				t.Fatalf("issue %s not listed", tc.id)
			}
			r := is.Remote
			if r.Provider != issueprovider.Jira || r.RemoteKey != tc.id || r.Repo != "EX" || r.Number != tc.number ||
				r.URL != site.ts.URL+"/browse/"+tc.key {
				t.Errorf("identity = %s %s %s %d %s", r.Provider, r.RemoteKey, r.Repo, r.Number, r.URL)
			}
			if strings.Contains(r.URL, "/rest/api/") {
				t.Errorf("URL is the API's self, not the browse page: %s", r.URL)
			}
			if got := strings.TrimSpace(r.Body); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			if r.Kind != tc.kind || r.State != tc.state || r.CloseReason != tc.reason || !slices.Equal(r.Labels, tc.labels) {
				t.Errorf("kind %q state %s reason %q labels %v", r.Kind, r.State, r.CloseReason, r.Labels)
			}
			if r.Author != "Emma Richards" || r.Title == "" || r.RemoteUpdatedAt == nil {
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
	if len(site.jqls) != 2 || site.jqls[0] != "project = 10000 AND statusCategory != Done ORDER BY id ASC" {
		t.Errorf("searches = %q", site.jqls)
	}
}

// TestJiraAssigneeStaysInRemoteJSON: the assignee is in remote_json and in
// no other column (decision 5 of #908's scope).
func TestJiraAssigneeStaysInRemoteJSON(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "")
	is, err := src.GetIssue(context.Background(), b, store.IssueRemote{RemoteKey: "10001"})
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(is.Remote.RemoteJSON), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["assignee"] == nil || rec["assignee_account_id"] == nil {
		t.Errorf("remote_json = %s, want the assignee kept", is.Remote.RemoteJSON)
	}
	if strings.Contains(is.Remote.Author, "5b10ac8d") {
		t.Errorf("an account id leaked into the author: %q", is.Remote.Author)
	}
}

func TestJiraListingCursors(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "labels = sync")

	page, err := src.ListIssues(context.Background(), b, ListOptions{OpenOnly: true, After: "10000", PageCap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || page.ResumeAfter != "10002" || !page.ResumeSince.IsZero() {
		t.Errorf("open page: truncated %v after %q since %v", page.Truncated, page.ResumeAfter, page.ResumeSince)
	}
	// An incremental walk resumes by id from its own bound, never by time:
	// the resumed listing asks from the first one's since, not the later
	// one it is given, and after the last id read (review F1).
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	page, err = src.ListIssues(context.Background(), b, ListOptions{Since: since, After: "ignored", PageCap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || !page.ResumeSince.IsZero() || page.ResumeAfter == "" {
		t.Fatalf("incremental page: truncated %v after %q since %v", page.Truncated, page.ResumeAfter, page.ResumeSince)
	}
	last := page.Issues[len(page.Issues)-1].Remote.RemoteKey
	if want := strconv.FormatInt(since.UnixMilli(), 10) + ":" + last; page.ResumeAfter != want {
		t.Errorf("incremental cursor %q, want %q", page.ResumeAfter, want)
	}
	if _, err := src.ListIssues(context.Background(), b, ListOptions{Since: since.Add(time.Hour), After: page.ResumeAfter, PageCap: 1}); err != nil {
		t.Fatal(err)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	want := []string{
		"project = 10000 AND statusCategory != Done AND (labels = sync) AND id > 10000 ORDER BY id ASC",
		jira.IncrementalJQL("10000", "labels = sync", since, ""),
		jira.IncrementalJQL("10000", "labels = sync", since, last),
	}
	if !slices.Equal(site.jqls, want) {
		t.Errorf("searches = %q, want %q", site.jqls, want)
	}
}

func TestJiraCommentsDropRestricted(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "")
	thread, err := src.ListComments(context.Background(), b, &Issue{Remote: store.RemoteIssue{RemoteKey: "10001"}})
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
	want := []string{"10000 Emma Richards Lorem ipsum.", "10002 Emma Richards Edited *later*."}
	if !slices.Equal(got, want) {
		t.Errorf("thread = %q, want %q (the Administrators-only comment dropped)", got, want)
	}
}

// TestJiraMoved: an issue whose project is no longer the resolved one is
// returned mapped where it is, with an error Classify reads as moved; bulk
// reads report it with MovedTo.
func TestJiraMoved(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "")
	ref := *jiraRefOf(b)
	ref.projectID = "20000" // the fixtures' project 10000 is now elsewhere
	other := NewBinding("EX", &ref)

	is, err := src.GetIssue(context.Background(), other, store.IssueRemote{RemoteKey: "10001"})
	f := src.Classify(err)
	if f.Reason != ReasonMoved || f.Location != site.ts.URL+"/browse/EX-1" || is.Remote.Repo != "EX" || is.Remote.Number != 1 {
		t.Errorf("moved GetIssue = %+v, %+v", is.Remote, f)
	}
	got, err := src.GetIssues(context.Background(), other, []store.IssueRemote{{RemoteKey: "10001"}, {RemoteKey: "10003"}, {RemoteKey: "10404"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["10001"].MovedTo != site.ts.URL+"/browse/EX-1" || got["10003"].MovedTo == "" {
		t.Errorf("bulk = %+v", got)
	}
	if _, ok := got["10404"]; ok {
		t.Error("an absent id was reported")
	}

	// Still at the binding: no MovedTo.
	got, err = src.GetIssues(context.Background(), b, []store.IssueRemote{{RemoteKey: "10001"}, {RemoteKey: "10003"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["10001"].MovedTo != "" || got["10003"].Issue.Remote.State != issuestate.Closed {
		t.Errorf("bulk at the binding = %+v", got)
	}
	if _, err := src.GetIssue(context.Background(), b, store.IssueRemote{RemoteKey: "10404"}); src.Classify(err).Reason != ReasonNotFound {
		t.Errorf("a 404 classifies as %q, want not_found (never gone)", src.Classify(err).Reason)
	}
}

// TestJiraBulkDropsJQLExcluded: with extra JQL on the binding, a bulk read
// leaves out what the JQL no longer matches, so the importer marks it
// missing.
func TestJiraBulkDropsJQLExcluded(t *testing.T) {
	site := newJiraSite(t)
	src := site.source(nil)
	b := site.bound(src, "labels = sync")
	site.mu.Lock()
	site.search = func(string) []byte { return jiraFixture(t, "search-jql-last") } // only EX-3 matches
	site.mu.Unlock()
	got, err := src.GetIssues(context.Background(), b, []store.IssueRemote{{RemoteKey: "10001"}, {RemoteKey: "10003"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["10001"]; ok || len(got) != 1 {
		t.Errorf("bulk = %v, want only 10003", got)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if last := site.jqls[len(site.jqls)-1]; last != "project = 10000 AND (labels = sync) AND id in (10001, 10003) ORDER BY id ASC" {
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
		_, _, err := src.ResolveBinding(context.Background(), b)
		f := src.Classify(err)
		if f.Reason != tc.want {
			t.Errorf("%d: reason %q, want %q", tc.status, f.Reason, tc.want)
		}
		if tc.status == http.StatusTooManyRequests && !f.ResetAt.After(time.Now()) {
			t.Errorf("429: ResetAt %v, want Retry-After ahead", f.ResetAt)
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
