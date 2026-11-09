package jira

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// The client against real Jira Cloud REST v3 answers (task 142.21, #954).
// testdata/jira-cloud-2026-11/ holds the captures 142.3 (#899) took from a
// throwaway Cloud site: one <endpoint>_<case>.json (or .txt, for a body
// that is not JSON) and a .headers.txt per call, the status line and an
// allow-list of headers. The account, email, display name, avatar hash,
// cloud id and site are scrubbed (the site is example-site.atlassian.net);
// nothing else is edited. "Facts verified (142.3)" in
// docs/tasks/142-jira-issue-sync.md says what each one showed.
//
// One documented-shape file stays: testdata/cloud-docs-2026-11-04-issue.json,
// hand-assembled from Atlassian's REST v3 reference before the captures
// existed (task 142 decision 3). No captured issue has a parent, an issue
// link, a component or an assignee, so it alone serves the assertions on
// those fields. internal/issuesource reads both; keep the names stable.

// captureDir is where the 142.3 captures live.
var captureDir = filepath.Join("testdata", "jira-cloud-2026-11")

// capture is one recorded answer.
type capture struct {
	status int
	header http.Header
	body   []byte
}

// loadCapture reads <name>.json or <name>.txt with <name>.headers.txt.
func loadCapture(t *testing.T, name string) capture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(captureDir, name+".headers.txt"))
	if err != nil {
		t.Fatal(err)
	}
	c := capture{header: http.Header{}}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for first := true; sc.Scan(); first = false {
		line := strings.TrimRight(sc.Text(), "\r")
		if first {
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
		body, err := os.ReadFile(filepath.Join(captureDir, name+ext))
		if err == nil {
			c.body = body
			return c
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	t.Fatalf("capture %s has no body file", name)
	return c
}

// replay writes the capture: its recorded headers less drop, its status
// line, its body.
func (c capture) replay(w http.ResponseWriter, drop ...string) {
	for k, vs := range c.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	for _, k := range drop {
		w.Header().Del(k)
	}
	w.WriteHeader(c.status)
	_, _ = w.Write(c.body)
}

// docShape reads the one kept documented-shape file (see the top comment).
func docShape(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cloud-docs-2026-11-04-issue.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// replaySite answers each request with the capture route names, through the
// real client, and records every request it saw.
type replaySite struct {
	client *Client
	mu     sync.Mutex
	seen   []*http.Request
	bodies []string
}

func (s *replaySite) requests() ([]*http.Request, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.seen...), append([]string(nil), s.bodies...)
}

// route names the capture a request is answered with; "" is a test failure.
type route func(r *http.Request) string

func newReplaySite(t *testing.T, rt route) *replaySite {
	t.Helper()
	s := &replaySite{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, r)
		s.bodies = append(s.bodies, string(raw))
		s.mu.Unlock()
		name := rt(r)
		if name == "" {
			t.Errorf("no capture for %s %s", r.Method, r.URL)
			http.Error(w, `{"errorMessages":["no capture"]}`, http.StatusNotFound)
			return
		}
		loadCapture(t, name).replay(w)
	}))
	t.Cleanup(srv.Close)
	s.client = New(srv.URL, srv.Client(), staticCreds(captureUser, captureToken))
	return s
}

// byPath routes "METHOD path" to a capture name.
func byPath(m map[string]string) route {
	return func(r *http.Request) string { return m[r.Method+" "+r.URL.Path] }
}

const (
	captureUser  = "fixture.user@example.com"
	captureToken = "capture-token-never-sent-anywhere"
)

func staticCreds(user, token string) func() (string, string, error) {
	return func() (string, string, error) { return user, token, nil }
}

// searchPages routes search/jql through the three captured pages by the
// nextPageToken the previous page answered: none is page 1, page 1's token
// page 2, page 2's token page 3. Any other token is a test failure.
func searchPages(t *testing.T) route {
	t.Helper()
	next := func(name string) string {
		var p struct {
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(loadCapture(t, name).body, &p); err != nil || p.NextPageToken == "" {
			t.Fatalf("%s: nextPageToken %q (%v)", name, p.NextPageToken, err)
		}
		return p.NextPageToken
	}
	pages := map[string]string{
		"":                              "search_jql_page1",
		next("search_jql_page1"):        "search_jql_page2_middle",
		next("search_jql_page2_middle"): "search_jql_page3_last",
	}
	return func(r *http.Request) string {
		if r.Method != http.MethodGet || r.URL.Path != pathSearchJQL {
			return ""
		}
		return pages[r.URL.Query().Get("nextPageToken")]
	}
}

func TestCaptureSearchPages(t *testing.T) {
	// The pages themselves: 40 + 40 + 23, the last isLast with no token, and
	// no page carrying a total (142.3 fact 4).
	for _, tc := range []struct {
		name   string
		n      int
		isLast bool
	}{
		{"search_jql_page1", 40, false},
		{"search_jql_page2_middle", 40, false},
		{"search_jql_page3_last", 23, true},
	} {
		var p map[string]json.RawMessage
		if err := json.Unmarshal(loadCapture(t, tc.name).body, &p); err != nil {
			t.Fatal(err)
		}
		var issues []json.RawMessage
		var isLast bool
		_ = json.Unmarshal(p["issues"], &issues)
		_ = json.Unmarshal(p["isLast"], &isLast)
		_, hasToken := p["nextPageToken"]
		_, hasTotal := p["total"]
		if len(issues) != tc.n || isLast != tc.isLast || hasToken == tc.isLast || hasTotal {
			t.Errorf("%s: %d issues, isLast %v, token %v, total %v", tc.name, len(issues), isLast, hasToken, hasTotal)
		}
	}

	site := newReplaySite(t, searchPages(t))
	res, err := site.client.Search(context.Background(), "project = 10001", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated || len(res.Issues) != 103 {
		t.Fatalf("issues %d, truncated %v", len(res.Issues), res.Truncated)
	}
	reqs, _ := site.requests()
	if len(reqs) != 3 || reqs[0].URL.Query().Get("nextPageToken") != "" {
		t.Errorf("%d requests", len(reqs))
	}
	for i, key := range map[int]string{0: "VCA-1", 39: "VCA-42", 40: "VCA-43", 79: "VCA-82", 80: "VCA-83", 102: "VCA-105"} {
		if res.Issues[i].Key != key {
			t.Errorf("issue %d = %s, want %s", i, res.Issues[i].Key, key)
		}
	}
	var done []string
	for i := range res.Issues {
		is := &res.Issues[i]
		f := is.Fields
		if f.Project.ID != "10001" || f.Project.Key != "VCA" || !strings.HasPrefix(is.Key, "VCA-") {
			t.Errorf("%s: project %+v", is.Key, f.Project)
		}
		if f.Status == nil {
			t.Fatalf("%s: no status", is.Key)
		}
		switch f.Status.StatusCategory.Key {
		case jiramap.CategoryDone:
			done = append(done, is.Key)
			if f.Resolution == nil || f.Resolution.Name != "Done" {
				t.Errorf("%s: done with resolution %+v", is.Key, f.Resolution)
			}
		case jiramap.CategoryNew:
			if f.Resolution != nil {
				t.Errorf("%s: open with resolution %+v", is.Key, f.Resolution)
			}
		default:
			t.Errorf("%s: category %q", is.Key, f.Status.StatusCategory.Key)
		}
		if f.UpdatedAt().IsZero() {
			t.Errorf("%s: updated %q does not parse", is.Key, f.Updated)
		}
		if is.RenderedFields.Description == "" {
			t.Errorf("%s: no rendered description", is.Key)
		}
	}
	if len(done) != 1 || done[0] != "VCA-5" {
		t.Errorf("done issues %v, want VCA-5 alone", done)
	}
	// +0100 offset and milliseconds, as Cloud answers them.
	if want := time.Date(2026, 11, 8, 20, 32, 36, 776e6, time.UTC); !res.Issues[0].Fields.UpdatedAt().Equal(want) {
		t.Errorf("VCA-1 updated %v, want %v", res.Issues[0].Fields.UpdatedAt(), want)
	}

	// maxPages stops the walk with a page outstanding.
	site = newReplaySite(t, searchPages(t))
	res, err = site.client.Search(context.Background(), "project = 10001", 2)
	if err != nil || !res.Truncated || len(res.Issues) != 80 {
		t.Errorf("two pages: %d issues, truncated %v, %v", len(res.Issues), res.Truncated, err)
	}
}

func TestCaptureIssueRendered(t *testing.T) {
	site := newReplaySite(t, byPath(map[string]string{"GET " + pathIssue("VCA-1"): "issue_rendered_rich"}))
	is, err := site.client.GetIssue(context.Background(), "VCA-1")
	if err != nil {
		t.Fatal(err)
	}
	f := is.Fields
	if is.ID != "10004" || is.Key != "VCA-1" || f.Summary != "Rich fixture issue" ||
		is.Self != "https://example-site.atlassian.net/rest/api/3/issue/10004" {
		t.Fatalf("issue %s %s %q %s", is.ID, is.Key, f.Summary, is.Self)
	}
	if f.Project.ID != "10001" || f.Project.Key != "VCA" {
		t.Errorf("project %+v", f.Project)
	}
	if f.IssueType == nil || f.IssueType.Name != "Task" || f.Priority == nil || f.Priority.Name != "Medium" {
		t.Errorf("type %+v priority %+v", f.IssueType, f.Priority)
	}
	if len(f.Labels) != 2 || f.Labels[0] != "batch-0" || f.Labels[1] != "fixture" {
		t.Errorf("labels %v", f.Labels)
	}
	if f.Reporter == nil || f.Reporter.DisplayName != "Fixture User" || f.Assignee != nil {
		t.Errorf("reporter %+v assignee %+v", f.Reporter, f.Assignee)
	}
	if f.Comment == nil || f.Comment.Total != 0 {
		t.Errorf("comment summary %+v", f.Comment)
	}
	if !strings.HasPrefix(is.RenderedFields.Description, "<h1>") {
		t.Errorf("rendered %q", is.RenderedFields.Description)
	}
	if st, _, _ := jiramap.FromJira(f.Fields, jiramap.Config{}); st != issuestate.Open {
		t.Errorf("FromJira = %v", st)
	}
	reqs, _ := site.requests()
	if q := reqs[0].URL.Query(); q.Get("expand") != expandRenderedFields || q.Get("fields") != strings.Join(IssueFieldNames, ",") {
		t.Errorf("query %v", q)
	}
}

// TestDocShapeIssueEmbedsJiramapFields reads the kept documented-shape file:
// no capture has a parent, a link, a component or an assignee.
func TestDocShapeIssueEmbedsJiramapFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(docShape(t))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, srv.Client(), staticCreds(captureUser, captureToken))
	is, err := c.GetIssue(context.Background(), "EX-1")
	if err != nil {
		t.Fatal(err)
	}
	f := is.Fields
	if f.Parent == nil || f.Parent.Key != "EX-0" {
		t.Errorf("parent %+v", f.Parent)
	}
	if len(f.IssueLinks) != 1 || f.IssueLinks[0].OutwardIssue == nil || f.IssueLinks[0].OutwardIssue.Key != "EX-4" {
		t.Errorf("links %+v", f.IssueLinks)
	}
	if len(f.Components) != 1 || f.Assignee == nil || f.Assignee.AccountID == "" {
		t.Errorf("components %v assignee %v", f.Components, f.Assignee)
	}
	// The embedded Fields is what jiramap consumes, unconverted.
	if st, _, _ := jiramap.FromJira(f.Fields, jiramap.Config{}); st != issuestate.Open {
		t.Errorf("FromJira = %v", st)
	}
}

// TestCaptureDoneIssuesMapClosed: a Done issue carries resolution Done on
// both project types. The team-managed one (VTM-2) contradicts #896's
// "team-managed projects have no resolution" finding (142.3); the
// null-resolution fallback stays covered by jiramap's own unit tests.
func TestCaptureDoneIssuesMapClosed(t *testing.T) {
	for _, tc := range []struct{ capture, key, projectID string }{
		{"issue_done_company_managed", "VCA-5", "10001"},
		{"issue_done_team_managed", "VTM-2", "10003"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			site := newReplaySite(t, byPath(map[string]string{"GET " + pathIssue(tc.key): tc.capture}))
			is, err := site.client.GetIssue(context.Background(), tc.key)
			if err != nil {
				t.Fatal(err)
			}
			f := is.Fields
			if is.Key != tc.key || f.Project.ID != tc.projectID {
				t.Errorf("issue %s in %+v", is.Key, f.Project)
			}
			if f.Resolution == nil || f.Resolution.Name != "Done" {
				t.Errorf("resolution %+v", f.Resolution)
			}
			st, reason, _ := jiramap.FromJira(f.Fields, jiramap.Config{})
			if st != issuestate.Closed || reason != issuestate.Completed {
				t.Errorf("FromJira = %v %q", st, reason)
			}
		})
	}
}

// TestCaptureMovedIssue: VCA-3 moved to VCB-4 keeps its id, and the old key
// resolves to the new issue (142.3 fact 1).
func TestCaptureMovedIssue(t *testing.T) {
	site := newReplaySite(t, byPath(map[string]string{
		"GET " + pathIssue("10006"): "issue_moved_by_id",
		"GET " + pathIssue("VCA-3"): "issue_moved_by_old_key",
	}))
	for _, ask := range []string{"10006", "VCA-3"} {
		is, err := site.client.GetIssue(context.Background(), ask)
		if err != nil {
			t.Fatal(err)
		}
		if is.ID != "10006" || is.Key != "VCB-4" || is.Fields.Project.ID != "10002" || is.Fields.Project.Key != "VCB" {
			t.Errorf("%s: %s %s in %+v", ask, is.ID, is.Key, is.Fields.Project)
		}
	}
}

func TestCaptureDeletedIssue404(t *testing.T) {
	site := newReplaySite(t, byPath(map[string]string{"GET " + pathIssue("10007"): "issue_deleted_404"}))
	_, err := site.client.GetIssue(context.Background(), "10007")
	var e *Error
	if ReasonOf(err) != ReasonNotFound || !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Fatalf("err %v", err)
	}
	if e.Err == nil || e.Err.Error() != "Issue does not exist or you do not have permission to see it." {
		t.Errorf("text %v", e.Err)
	}
}

// TestCaptureBulkFetchDeletedAndMoved: a deleted id is dropped silently with
// no issueErrors, and the moved issue asked for by id and by old key comes
// back once, under its new key.
func TestCaptureBulkFetchDeletedAndMoved(t *testing.T) {
	var ans struct {
		IssueErrors []json.RawMessage `json:"issueErrors"`
	}
	if err := json.Unmarshal(loadCapture(t, "bulkfetch_deleted_and_moved").body, &ans); err != nil || ans.IssueErrors == nil || len(ans.IssueErrors) != 0 {
		t.Fatalf("issueErrors %v (%v)", ans.IssueErrors, err)
	}
	site := newReplaySite(t, byPath(map[string]string{"POST " + pathBulkFetch: "bulkfetch_deleted_and_moved"}))
	ask := []string{"10007", "10006", "VCA-3", "VCA-1"}
	got, err := site.client.BulkFetch(context.Background(), ask)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "10004" || got[0].Key != "VCA-1" || got[1].ID != "10006" || got[1].Key != "VCB-4" {
		t.Fatalf("bulkfetch %d issues: %+v", len(got), got)
	}
	_, bodies := site.requests()
	var req bulkFetchRequest
	if err := json.Unmarshal([]byte(bodies[0]), &req); err != nil || strings.Join(req.IssueIDsOrKeys, ",") != strings.Join(ask, ",") {
		t.Errorf("request %s (%v)", bodies[0], err)
	}
}

func TestCaptureComments(t *testing.T) {
	const account = "557058:00000000-0000-4000-8000-000000000001"
	site := newReplaySite(t, byPath(map[string]string{
		"GET " + pathComments("10005"): "comments_company_managed",
		"GET " + pathComments("10113"): "comments_team_managed",
	}))
	got, err := site.client.Comments(context.Background(), "10005")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "10000" || got[1].ID != "10001" || got[2].ID != "10002" {
		t.Fatalf("%d comments %+v", len(got), got)
	}
	if got[0].Visibility != nil || got[1].Visibility != nil {
		t.Errorf("public comments carry visibility")
	}
	if v := got[2].Visibility; v == nil || v.Type != "role" || v.Value != "Administrators" {
		t.Errorf("restricted comment visibility %+v", v)
	}
	// A read answers both value and identifier (142.3); Raw keeps both.
	var raw struct {
		Visibility map[string]string `json:"visibility"`
	}
	if err := json.Unmarshal(got[2].Raw, &raw); err != nil || raw.Visibility["value"] != "Administrators" || raw.Visibility["identifier"] != "Administrators" {
		t.Errorf("raw visibility %v (%v)", raw.Visibility, err)
	}
	if got[0].Author.AccountID != account || got[0].Author.DisplayName != "Fixture User" {
		t.Errorf("author %+v", got[0].Author)
	}
	if !got[0].Created.Equal(time.Date(2026, 11, 8, 20, 32, 56, 408e6, time.UTC)) || !got[0].Updated.Equal(time.Date(2026, 11, 8, 20, 33, 2, 215e6, time.UTC)) {
		t.Errorf("times %v %v", got[0].Created, got[0].Updated)
	}
	if got[0].RenderedBody != "<p>First public comment, edited.</p>" {
		t.Errorf("rendered %q", got[0].RenderedBody)
	}

	got, err = site.client.Comments(context.Background(), "10113")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Visibility == nil || got[0].Visibility.Value != "Member" || got[1].Visibility != nil {
		t.Errorf("team-managed thread %+v", got)
	}
}

// TestCaptureCommentsPaged follows startAt across the thread: 2 of 3, then
// the last one from startAt 2.
func TestCaptureCommentsPaged(t *testing.T) {
	site := newReplaySite(t, func(r *http.Request) string {
		if r.URL.Path != pathComments("10005") {
			return ""
		}
		switch r.URL.Query().Get("startAt") {
		case "0":
			return "comments_paged_first"
		case "2":
			return "comments_paged_last"
		}
		return ""
	})
	got, err := site.client.Comments(context.Background(), "10005")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "10000,10001,10002" {
		t.Errorf("ids %v", ids)
	}
	if reqs, _ := site.requests(); len(reqs) != 2 {
		t.Errorf("%d requests, want 2", len(reqs))
	}
}

func TestCaptureMetadata(t *testing.T) {
	site := newReplaySite(t, byPath(map[string]string{
		"GET " + pathMyself:         "myself",
		"GET " + pathServerInfo:     "serverinfo",
		"GET " + pathProject("VCA"): "project_company_managed",
		"GET " + pathProject("VTM"): "project_team_managed",
	}))
	c, ctx := site.client, context.Background()
	me, err := c.Myself(ctx)
	if err != nil || me != (Myself{AccountID: "557058:00000000-0000-4000-8000-000000000001", DisplayName: "Fixture User"}) {
		t.Errorf("myself %+v %v", me, err)
	}
	si, err := c.ServerInfo(ctx)
	want := ServerInfo{BaseURL: "https://example-site.atlassian.net", Version: "1001.0.0-SNAPSHOT", DeploymentType: "Cloud", ServerTitle: "Jira"}
	if err != nil || si != want {
		t.Errorf("serverInfo %+v %v", si, err)
	}
	for key, want := range map[string]Project{
		"VCA": {ID: "10001", Key: "VCA", Name: "Vincent Company A"},
		"VTM": {ID: "10003", Key: "VTM", Name: "Vincent Team"},
	} {
		if p, err := c.Project(ctx, key); err != nil || p != want {
			t.Errorf("project %s: %+v %v", key, p, err)
		}
	}
}

// TestCaptureTransitions: with no transition screen every transition's
// fields is empty; with one, Done lists resolution.
func TestCaptureTransitions(t *testing.T) {
	for _, tc := range []struct {
		capture    string
		resolution bool
	}{
		{"transitions_company_managed_from_backlog", false},
		{"transitions_company_managed_from_done", false},
		{"transitions_company_managed_from_backlog_with_screen", true},
		{"transitions_company_managed_from_done_with_screen", true},
		{"transitions_team_managed_from_todo", false},
		{"transitions_team_managed_from_done", false},
	} {
		t.Run(tc.capture, func(t *testing.T) {
			site := newReplaySite(t, byPath(map[string]string{"GET " + pathTransitions("10004"): tc.capture}))
			ts, err := site.client.Transitions(context.Background(), "10004")
			if err != nil {
				t.Fatal(err)
			}
			if len(ts) != 4 {
				t.Fatalf("%d transitions", len(ts))
			}
			for _, tr := range ts {
				done := tr.To.StatusCategory.Key == jiramap.CategoryDone
				if done != (tr.ID == "41" && tr.Name == "Done") {
					t.Errorf("transition %s %q to %q", tr.ID, tr.Name, tr.To.StatusCategory.Key)
				}
				_, hasRes := tr.Fields["resolution"]
				if hasRes != (done && tc.resolution) {
					t.Errorf("transition %s %q fields %v", tr.ID, tr.Name, tr.Fields)
				}
				if !done && len(tr.Fields) != 0 {
					t.Errorf("transition %s %q fields %v", tr.ID, tr.Name, tr.Fields)
				}
			}
			if reqs, _ := site.requests(); reqs[0].URL.Query().Get("expand") != expandTransFields {
				t.Errorf("query %v", reqs[0].URL.Query())
			}
		})
	}
}

func TestCaptureTransitionPost(t *testing.T) {
	for _, tc := range []struct {
		capture, resolution, text string
	}{
		{"transition_post_204", "", ""},
		{"transition_post_204_screen_without_resolution", "", ""},
		{"transition_post_204_screen_with_resolution", "Won't Do", ""},
		// A validator's refusal: its message, empty errors.
		{"transition_post_400_missing_required_field", "", "A due date is required to finish this issue."},
		// A resolution with no screen to set it on: the errors map, flattened.
		{"transition_post_400_resolution_not_on_screen", "Done", "resolution: Field 'resolution' cannot be set. It is not on the appropriate screen, or unknown."},
	} {
		t.Run(tc.capture, func(t *testing.T) {
			site := newReplaySite(t, byPath(map[string]string{"POST " + pathTransitions("10004"): tc.capture}))
			err := site.client.DoTransition(context.Background(), "10004", "41", tc.resolution)
			if tc.text == "" {
				if err != nil {
					t.Fatalf("err %v", err)
				}
				return
			}
			var e *Error
			if ReasonOf(err) != ReasonBadRequest || !errors.As(err, &e) || e.Status != http.StatusBadRequest {
				t.Fatalf("err %v", err)
			}
			if e.Err == nil || e.Err.Error() != tc.text {
				t.Errorf("text %v, want %q", e.Err, tc.text)
			}
		})
	}
}

func TestCaptureAddComment(t *testing.T) {
	site := newReplaySite(t, byPath(map[string]string{"POST " + pathComments("10005"): "comment_post_201"}))
	id, err := site.client.AddComment(context.Background(), "10005", json.RawMessage(`{"type":"doc","version":1,"content":[]}`))
	if err != nil || id != "10000" {
		t.Errorf("AddComment = %q, %v", id, err)
	}
}

// TestCapture401PlainText: a bad or revoked token draws a plain-text body
// under content-type application/json, so errorText's first-line fallback
// carries it. The two answers are identical: they cannot be told apart.
func TestCapture401PlainText(t *testing.T) {
	var texts []string
	for _, name := range []string{"myself_401_invalid_token", "myself_401_revoked_token"} {
		t.Run(name, func(t *testing.T) {
			if ct := loadCapture(t, name).header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("content-type %q", ct)
			}
			site := newReplaySite(t, byPath(map[string]string{"GET " + pathMyself: name}))
			_, err := site.client.Myself(context.Background())
			var e *Error
			if ReasonOf(err) != ReasonUnauthorized || !errors.As(err, &e) || e.Status != http.StatusUnauthorized || e.Err == nil {
				t.Fatalf("err %v", err)
			}
			if e.Err.Error() != "Client must be authenticated to access this resource." {
				t.Errorf("text %q", e.Err.Error())
			}
			if strings.Contains(err.Error(), captureToken) {
				t.Errorf("the token reached the error: %v", err)
			}
			texts = append(texts, err.Error())
		})
	}
	if len(texts) == 2 && texts[0] != texts[1] {
		t.Errorf("invalid and revoked differ: %q, %q", texts[0], texts[1])
	}
}

// TestCapture429 replays the real burst-limit answer: retry-after: 1 wins,
// x-ratelimit-reset (minute precision) is the fallback, and with neither
// RetryAt is zero. ratelimit-reason is recorded; the client does not read it.
func TestCapture429(t *testing.T) {
	c429 := loadCapture(t, "rate_limited_429")
	if c429.header.Get("Ratelimit-Reason") != "jira-burst-based" {
		t.Errorf("ratelimit-reason %q", c429.header.Get("Ratelimit-Reason"))
	}
	now := time.Date(2026, 11, 8, 20, 36, 10, 0, time.UTC)
	for _, tc := range []struct {
		name string
		drop []string
		want time.Time
	}{
		{"retry-after wins", nil, now.Add(time.Second)},
		{"reset fallback", []string{"Retry-After"}, time.Date(2026, 11, 8, 20, 37, 0, 0, time.UTC)},
		{"neither", []string{"Retry-After", "X-RateLimit-Reset"}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				c429.replay(w, tc.drop...)
			}))
			defer srv.Close()
			c := New(srv.URL, srv.Client(), staticCreds(captureUser, captureToken))
			c.now = func() time.Time { return now }
			_, err := c.Myself(context.Background())
			var e *Error
			if ReasonOf(err) != ReasonRateLimited || !errors.As(err, &e) || e.Status != http.StatusTooManyRequests {
				t.Fatalf("err %v", err)
			}
			if !e.RetryAt.Equal(tc.want) {
				t.Errorf("RetryAt %v, want %v", e.RetryAt, tc.want)
			}
			if e.Err == nil || e.Err.Error() != "The request has been rate-limited. Please try again later." {
				t.Errorf("text %v", e.Err)
			}
		})
	}
}
