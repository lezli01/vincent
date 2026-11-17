package issuesource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/store"
)

// multiSite is the fake Jira with VIN and OPS populated and a third project
// to move issues out of the binding into. Search queries carrying a
// project's own JQL — outside the fake's subset — are answered by excluded:
// every listed id but the ones it names.
type multiSite struct {
	ts       *httptest.Server
	fake     *fakejira.Store
	mu       sync.Mutex
	jqls     []string
	excluded map[string]bool
}

func newMultiSite(t *testing.T) *multiSite {
	t.Helper()
	dir := t.TempDir()
	m := &multiSite{
		fake:     &fakejira.Store{Path: filepath.Join(dir, "corpus.json"), Token: "tok"},
		excluded: map[string]bool{},
	}
	c := fakejira.Builtin()
	c.SeedIssues("OPS", 2, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	c.Projects = append(c.Projects, fakejira.Project{ID: "10002", Key: "WEB", Name: "Web", Statuses: c.Projects[1].Statuses})
	if err := m.fake.Save(c); err != nil {
		t.Fatal(err)
	}
	m.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jql := r.URL.Query().Get("jql")
		if jql != "" {
			m.mu.Lock()
			m.jqls = append(m.jqls, jql)
			m.mu.Unlock()
		}
		if strings.Contains(jql, " id in (") {
			m.answerFilter(w, jql)
			return
		}
		m.fake.ServeHTTP(w, r)
	}))
	t.Cleanup(m.ts.Close)
	return m
}

// answerFilter answers dropExcluded's `... AND id in (a, b) ...` with the
// ids not excluded.
func (m *multiSite) answerFilter(w http.ResponseWriter, jql string) {
	_, list, _ := strings.Cut(jql, " id in (")
	list, _, _ = strings.Cut(list, ")")
	var issues []string
	m.mu.Lock()
	for _, id := range strings.Split(list, ", ") {
		if !m.excluded[id] {
			issues = append(issues, `{"id":"`+id+`","key":"X-`+id+`","fields":{}}`)
		}
	}
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"issues":[` + strings.Join(issues, ",") + `],"isLast":true}`))
}

func (m *multiSite) source() *Jira {
	creds := func() (string, string, error) { return fakejira.DefaultUser, "tok", nil }
	return NewJira(func(site string) *jira.Client { return jira.New(site, m.ts.Client(), creds) })
}

func (m *multiSite) project(ps ...store.JiraProject) store.Project {
	return store.Project{ID: 1, IssueSource: store.IssueSource{
		Provider: store.IssueProviderJira, JiraSite: m.ts.URL, JiraProjects: ps,
	}}
}

// Several projects bind as one binding (task 146): one resolution per key,
// a sticky id naming each, one listing over all of them, and each project's
// JQL judging only its own issues.
func TestJiraSeveralProjects(t *testing.T) {
	m := newMultiSite(t)
	src := m.source()
	ctx := context.Background()
	project := m.project(store.JiraProject{Key: "VIN", JQL: "labels = sync"}, store.JiraProject{Key: "OPS"})
	b, ok := src.Bind(ctx, project)
	if !ok || b.String() != "VIN,OPS" {
		t.Fatalf("Bind = %q, %v", b.String(), ok)
	}
	b, id, err := src.ResolveBinding(ctx, b)
	if err != nil || id != "OPS:10001,VIN:10000" {
		t.Fatalf("ResolveBinding = %q, %v", id, err)
	}

	m.mu.Lock()
	m.jqls = nil
	m.mu.Unlock()
	// The listing is one query over both projects; the fake cannot answer
	// VIN's JQL, so only the query's shape is checked here.
	_, _ = src.ListIssues(ctx, b, ListOptions{OpenOnly: true})
	m.mu.Lock()
	if want := "((project = 10000 AND (labels = sync)) OR project = 10001) AND statusCategory != Done ORDER BY id ASC"; len(m.jqls) == 0 || m.jqls[0] != want {
		t.Errorf("listing JQL = %q, want %q", m.jqls, want)
	}
	m.jqls = nil
	m.excluded["10001"] = true // VIN-1 no longer matches VIN's JQL
	m.mu.Unlock()

	// VIN-2 leaves the binding for WEB; VIN-1 falls out of VIN's JQL; the
	// OPS issues have no JQL to fall out of.
	if err := m.fake.Move("VIN-2", "WEB", "WEB-1"); err != nil {
		t.Fatal(err)
	}
	rems := []store.IssueRemote{{RemoteKey: "10001"}, {RemoteKey: "10002"}, {RemoteKey: "10004"}, {RemoteKey: "10005"}}
	got, err := src.GetIssues(ctx, b, rems)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["10001"]; ok {
		t.Errorf("VIN-1 excluded by VIN's JQL was reported: %+v", got["10001"])
	}
	if got["10002"].MovedTo == "" {
		t.Errorf("VIN-2 moved out of the binding = %+v, want MovedTo", got["10002"])
	}
	for _, id := range []string{"10004", "10005"} {
		if bi, ok := got[id]; !ok || bi.MovedTo != "" || bi.Issue.Remote.Repo != "OPS" {
			t.Errorf("OPS issue %s = %+v, %v; want kept in OPS", id, bi, ok)
		}
	}
	m.mu.Lock()
	filters := slices.DeleteFunc(slices.Clone(m.jqls), func(q string) bool { return !strings.Contains(q, " id in (") })
	m.mu.Unlock()
	if want := []string{"project = 10000 AND (labels = sync) AND id in (10001) ORDER BY id ASC"}; !slices.Equal(filters, want) {
		t.Errorf("filter JQL = %q, want only VIN's, over VIN's ids: %q", filters, want)
	}

	// A row is written through the binding while it names its project.
	for repo, want := range map[string]bool{"VIN": true, "ops": true, "WEB": false} {
		if _, ok := src.LocateIn(ctx, project, store.IssueRemote{RemoteKey: "1", Repo: repo}); ok != want {
			t.Errorf("LocateIn(%s) = %v, want %v", repo, ok, want)
		}
	}
}
