package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// The fixtures are hand-assembled from Atlassian's documented REST v3
// response shapes (task 142 decision 3: #899's real-site captures are still
// open), named cloud-docs-<date>-<endpoint>.json. internal/issuesource reads
// them too; keep the names stable.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cloud-docs-2026-11-04-"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fixtureServer answers each path with a fixture, through the real client.
func fixtureServer(t *testing.T, routes map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.Error(w, `{"errorMessages":["no fixture"]}`, http.StatusNotFound)
			return
		}
		if r.URL.Path == pathSearchJQL && r.URL.Query().Get("nextPageToken") != "" {
			name = "search-jql-last"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, name))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, srv.Client(), staticCreds("a@example.com", "fixture-token"))
}

func staticCreds(user, token string) func() (string, string, error) {
	return func() (string, string, error) { return user, token, nil }
}

func TestFixtureSearchPages(t *testing.T) {
	c := fixtureServer(t, map[string]string{"GET " + pathSearchJQL: "search-jql-page"})
	res, err := c.Search(context.Background(), "project = 10000", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated || len(res.Issues) != 3 {
		t.Fatalf("issues %d, truncated %v", len(res.Issues), res.Truncated)
	}
	for _, tc := range []struct {
		i              int
		key, category  string
		resolution     string
		updated        time.Time
		renderedPrefix string
	}{
		{0, "EX-1", jiramap.CategoryNew, "", time.Date(2026, 10, 20, 9, 15, 30, 123e6, time.UTC), "<p>Body.</p>"},
		{1, "EX-2", jiramap.CategoryIndeterminate, "", time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC), "<p>In <b>progress</b>.</p>"},
		{2, "EX-3", jiramap.CategoryDone, "Done", time.Date(2026, 10, 22, 11, 30, 0, 0, time.UTC), "<p>Body.</p>"},
	} {
		is := res.Issues[tc.i]
		if is.Key != tc.key || is.Fields.Status == nil || is.Fields.Status.StatusCategory.Key != tc.category {
			t.Errorf("%d: key %s status %+v", tc.i, is.Key, is.Fields.Status)
		}
		if got := ""; is.Fields.Resolution != nil {
			got = is.Fields.Resolution.Name
			if got != tc.resolution {
				t.Errorf("%s: resolution %q", is.Key, got)
			}
		} else if tc.resolution != "" {
			t.Errorf("%s: no resolution", is.Key)
		}
		if !is.Fields.UpdatedAt().Equal(tc.updated) {
			t.Errorf("%s: updated %v, want %v", is.Key, is.Fields.UpdatedAt(), tc.updated)
		}
		if is.RenderedFields.Description != tc.renderedPrefix {
			t.Errorf("%s: rendered %q", is.Key, is.RenderedFields.Description)
		}
		if is.Fields.Project.ID != "10000" || is.Fields.Project.Key != "EX" {
			t.Errorf("%s: project %+v", is.Key, is.Fields.Project)
		}
	}
}

func TestFixtureIssueEmbedsJiramapFields(t *testing.T) {
	c := fixtureServer(t, map[string]string{"GET " + pathIssue("EX-1"): "issue"})
	is, err := c.GetIssue(context.Background(), "EX-1")
	if err != nil {
		t.Fatal(err)
	}
	f := is.Fields
	if is.ID != "10001" || f.Summary != "First open issue" || is.Self == "" {
		t.Fatalf("issue %+v", is)
	}
	if f.Comment == nil || f.Comment.Total != 2 {
		t.Errorf("comment summary %+v", f.Comment)
	}
	if f.IssueType == nil || f.IssueType.Name != "Bug" || f.Priority == nil || f.Priority.Name != "Medium" {
		t.Errorf("type %+v priority %+v", f.IssueType, f.Priority)
	}
	if f.Parent == nil || f.Parent.Key != "EX-0" {
		t.Errorf("parent %+v", f.Parent)
	}
	if len(f.IssueLinks) != 1 || f.IssueLinks[0].OutwardIssue == nil || f.IssueLinks[0].OutwardIssue.Key != "EX-4" {
		t.Errorf("links %+v", f.IssueLinks)
	}
	if len(f.Labels) != 2 || len(f.Components) != 1 || f.Assignee == nil || f.Reporter == nil {
		t.Errorf("labels %v components %v assignee %v reporter %v", f.Labels, f.Components, f.Assignee, f.Reporter)
	}
	// The embedded Fields is what jiramap consumes, unconverted.
	if st, _, _ := jiramap.FromJira(f.Fields, jiramap.Config{}); st != issuestate.Open {
		t.Errorf("FromJira = %v", st)
	}
}

func TestFixtureBulkFetchMissingID(t *testing.T) {
	c := fixtureServer(t, map[string]string{"POST " + pathBulkFetch: "bulkfetch"})
	got, err := c.BulkFetch(context.Background(), []string{"10001", "10003", "99999"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "10001" || got[1].ID != "10003" {
		t.Fatalf("bulkfetch %+v", got)
	}
}

func TestFixtureComments(t *testing.T) {
	c := fixtureServer(t, map[string]string{"GET " + pathComments("10001"): "comments"})
	got, err := c.Comments(context.Background(), "10001")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d comments", len(got))
	}
	if got[0].Visibility != nil || got[2].Visibility != nil {
		t.Errorf("public comments carry visibility")
	}
	if v := got[1].Visibility; v == nil || v.Type != "role" || v.Value != "Administrators" {
		t.Errorf("restricted comment visibility %+v", v)
	}
	if got[1].Author.AccountID != "5b10a2844c20165700ede21g" || got[1].Author.DisplayName != "Emma Richards" {
		t.Errorf("author %+v", got[1].Author)
	}
	if !got[1].Updated.Equal(time.Date(2026, 10, 3, 10, 0, 0, 500e6, time.UTC)) || !got[1].Created.Equal(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("times %v %v", got[1].Created, got[1].Updated)
	}
	if got[2].RenderedBody != "<p>Edited <em>later</em>.</p>" {
		t.Errorf("rendered %q", got[2].RenderedBody)
	}
	var raw map[string]any
	if err := json.Unmarshal(got[1].Raw, &raw); err != nil || raw["id"] != "10001" {
		t.Errorf("raw %s (%v)", got[1].Raw, err)
	}
}

func TestFixtureMetadata(t *testing.T) {
	c := fixtureServer(t, map[string]string{
		"GET " + pathMyself:        "myself",
		"GET " + pathServerInfo:    "serverinfo",
		"GET " + pathProject("EX"): "project",
	})
	ctx := context.Background()
	me, err := c.Myself(ctx)
	if err != nil || me != (Myself{AccountID: "5b10a2844c20165700ede21g", DisplayName: "Mia Krystof"}) {
		t.Errorf("myself %+v %v", me, err)
	}
	si, err := c.ServerInfo(ctx)
	want := ServerInfo{BaseURL: "https://your-domain.atlassian.net", Version: "1001.0.0-SNAPSHOT", DeploymentType: "Cloud", ServerTitle: "Your Jira"}
	if err != nil || si != want {
		t.Errorf("serverInfo %+v %v", si, err)
	}
	p, err := c.Project(ctx, "EX")
	if err != nil || p != (Project{ID: "10000", Key: "EX", Name: "Example"}) {
		t.Errorf("project %+v %v", p, err)
	}
}

func TestFixture429Headers(t *testing.T) {
	var fx struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	}
	if err := json.Unmarshal(fixture(t, "429-headers"), &fx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		drop []string
		want time.Time
	}{
		{"retry-after wins", nil, now.Add(17 * time.Second)},
		{"reset fallback", []string{"Retry-After"}, time.Date(2026, 11, 4, 8, 1, 0, 0, time.UTC)},
		{"neither", []string{"Retry-After", "X-RateLimit-Reset"}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range fx.Headers {
					w.Header().Set(k, v)
				}
				for _, k := range tc.drop {
					w.Header().Del(k)
				}
				w.WriteHeader(fx.Status)
				_, _ = w.Write(fx.Body)
			}))
			defer srv.Close()
			c := New(srv.URL, srv.Client(), staticCreds("a@example.com", "fixture-token"))
			c.now = func() time.Time { return now }
			_, err := c.Myself(context.Background())
			var e *Error
			if ReasonOf(err) != ReasonRateLimited || !asError(err, &e) || e.Status != 429 {
				t.Fatalf("err %v", err)
			}
			if !e.RetryAt.Equal(tc.want) {
				t.Fatalf("RetryAt %v, want %v", e.RetryAt, tc.want)
			}
		})
	}
}
