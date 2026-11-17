package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

func jiraReq(site, key, jql, mapping string) projectSourceRequest {
	j := &projectSourceJira{Site: site, ProjectKey: key, JQL: jql}
	if mapping != "" {
		j.Mapping = json.RawMessage(mapping)
	}
	return projectSourceRequest{Provider: "jira", Jira: j}
}

// jiraList is a binding on https://x.example naming keys in its projects list, edit applied to
// its Jira half when non-nil.
func jiraList(keys ...string) func(edit func(*projectSourceJira)) projectSourceRequest {
	return func(edit func(*projectSourceJira)) projectSourceRequest {
		j := &projectSourceJira{Site: "https://x.example"}
		for _, k := range keys {
			j.Projects = append(j.Projects, projectSourceJiraProject{Key: k})
		}
		if edit != nil {
			edit(j)
		}
		return projectSourceRequest{Provider: "jira", Jira: j}
	}
}

// manyKeys is n distinct project keys.
func manyKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("K%d", i)
	}
	return out
}

// TestValidateIssueSource is task 142.6 decision 2, table-driven.
func TestValidateIssueSource(t *testing.T) {
	ok := []struct {
		name string
		req  projectSourceRequest
		want store.IssueSource
	}{
		{"derive", projectSourceRequest{Provider: "derive"}, store.IssueSource{}},
		{"none", projectSourceRequest{Provider: "none"}, store.IssueSource{Provider: "none"}},
		{"github", projectSourceRequest{Provider: "github"}, store.IssueSource{Provider: "github"}},
		{
			"https", jiraReq("https://Example.Atlassian.NET", "ENG", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "https://example.atlassian.net", JiraProjects: []store.JiraProject{{Key: "ENG"}}},
		},
		{
			"trailing slash", jiraReq("https://example.atlassian.net/", "ENG_2", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "https://example.atlassian.net", JiraProjects: []store.JiraProject{{Key: "ENG_2"}}},
		},
		{
			"http 127.0.0.1", jiraReq("http://127.0.0.1:8080", "A", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "http://127.0.0.1:8080", JiraProjects: []store.JiraProject{{Key: "A"}}},
		},
		{
			"http localhost", jiraReq("http://localhost:9", "A", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "http://localhost:9", JiraProjects: []store.JiraProject{{Key: "A"}}},
		},
		{
			"http [::1]", jiraReq("http://[::1]:9", "A", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "http://[::1]:9", JiraProjects: []store.JiraProject{{Key: "A"}}},
		},
		{
			"jql and mapping", jiraReq("https://x.example", "A", "  labels = backend  ",
				`{ "overrides": {"reopen": "Back to do"}, "duplicate_resolutions": ["Dup"] }`),
			store.IssueSource{
				Provider: "jira", JiraSite: "https://x.example", JiraProjects: []store.JiraProject{{Key: "A", JQL: "labels = backend"}},
				JiraMappingJSON: `{"overrides":{"reopen":"Back to do"},"duplicate_resolutions":["Dup"]}`,
			},
		},
	}
	ok = append(ok, []struct {
		name string
		req  projectSourceRequest
		want store.IssueSource
	}{
		{
			"several projects", jiraList(" ENG ", "OPS")(func(j *projectSourceJira) { j.Projects[1].JQL = " type = Bug " }),
			store.IssueSource{Provider: "jira", JiraSite: "https://x.example", JiraProjects: []store.JiraProject{{Key: "ENG"}, {Key: "OPS", JQL: "type = Bug"}}},
		},
		{
			"as many as allowed", jiraList(manyKeys(maxJiraProjects)...)(nil),
			store.IssueSource{Provider: "jira", JiraSite: "https://x.example", JiraProjects: func() []store.JiraProject {
				var ps []store.JiraProject
				for _, k := range manyKeys(maxJiraProjects) {
					ps = append(ps, store.JiraProject{Key: k})
				}
				return ps
			}()},
		},
	}...)
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, msg := validateIssueSource(c.req)
			if msg != "" {
				t.Fatalf("refused: %s", msg)
			}
			if !got.Equal(c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}

	bad := []struct {
		name, needle string
		req          projectSourceRequest
	}{
		{"unknown provider", "issue_source.provider", projectSourceRequest{Provider: "gitlab"}},
		{"jira without jira", "issue_source.jira is required", projectSourceRequest{Provider: "jira"}},
		{
			"jira fields on github", "only allowed with provider jira",
			projectSourceRequest{Provider: "github", Jira: &projectSourceJira{Site: "https://x"}},
		},
		{"no site", "issue_source.jira.site is required", jiraReq("", "ENG", "", "")},
		{"no key", "issue_source.jira.projects is required", jiraReq("https://x.example", "", "", "")},
		{"jql without key", "issue_source.jira.project_key is required", jiraReq("https://x.example", "", "labels = a", "")},
		{"both spellings", "not both", jiraList("A", "B")(func(j *projectSourceJira) { j.ProjectKey = "C" })},
		{"duplicate key", "projects[2].key \"A\" is already projects[0]", jiraList("A", "B", "A")(nil)},
		{"bad listed key", "issue_source.jira.projects[1].key \"b\" is not a Jira project key", jiraList("A", "b")(nil)},
		{"empty listed key", "issue_source.jira.projects[0].key is required", jiraList("")(nil)},
		{
			"bad listed jql", "issue_source.jira.projects[1].jql must not contain ORDER BY",
			jiraList("A", "B")(func(j *projectSourceJira) { j.Projects[1].JQL = "x ORDER BY id" }),
		},
		{"too many", "at most 50", jiraList(manyKeys(maxJiraProjects + 1)...)(nil)},
		{"http non-loopback", "https://", jiraReq("http://example.atlassian.net", "A", "", "")},
		{"userinfo", "user or password", jiraReq("https://me:tok@example.atlassian.net", "A", "", "")},
		{"path", "no path", jiraReq("https://example.atlassian.net/jira", "A", "", "")},
		{"query", "query", jiraReq("https://example.atlassian.net/?a=b", "A", "", "")},
		{"empty query", "query", jiraReq("https://example.atlassian.net?", "A", "", "")},
		{"fragment", "fragment", jiraReq("https://example.atlassian.net#x", "A", "", "")},
		{"no scheme", "absolute https://", jiraReq("example.atlassian.net", "A", "", "")},
		{"ftp", "absolute https://", jiraReq("ftp://example.atlassian.net", "A", "", "")},
		{"lowercase key", "not a Jira project key", jiraReq("https://x.example", "eng", "", "")},
		{"digit-first key", "not a Jira project key", jiraReq("https://x.example", "1ENG", "", "")},
		{"dash key", "not a Jira project key", jiraReq("https://x.example", "EN-G", "", "")},
		{"long key", "longer than", jiraReq("https://x.example", "A"+strings.Repeat("B", maxJiraProjectKey), "", "")},
		{"order by", "ORDER BY", jiraReq("https://x.example", "A", "labels = x order  by created", "")},
		{"newline", "single line", jiraReq("https://x.example", "A", "labels = x\nOR 1=1", "")},
		{"long jql", "longer than", jiraReq("https://x.example", "A", strings.Repeat("a", maxJiraJQL+1), "")},
		{"malformed mapping", "issue_source.jira.mapping", jiraReq("https://x.example", "A", "", `{"overrides":`)},
		{"unknown mapping field", "issue_source.jira.mapping", jiraReq("https://x.example", "A", "", `{"reopen":"x"}`)},
		{"array mapping", "issue_source.jira.mapping", jiraReq("https://x.example", "A", "", `["x"]`)},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			got, msg := validateIssueSource(c.req)
			if msg == "" {
				t.Fatalf("accepted as %+v", got)
			}
			if !strings.Contains(msg, c.needle) {
				t.Errorf("message %q does not name %q", msg, c.needle)
			}
		})
	}
}

// TestEveryRegisteredProviderIsBindable holds the binding's validator to the
// provider registry (review F2): a provider added to internal/issueprovider
// must also become bindable here, or this fails rather than the provider
// being silently refused at bind time.
func TestEveryRegisteredProviderIsBindable(t *testing.T) {
	for _, id := range issueprovider.All() {
		req := projectSourceRequest{Provider: id}
		if id == issueprovider.Jira {
			req = jiraReq("https://x.example", "A", "", "")
		}
		got, msg := validateIssueSource(req)
		if msg != "" {
			t.Errorf("registered provider %q refused: %s", id, msg)
			continue
		}
		if got.Provider != id {
			t.Errorf("provider %q stored as %q", id, got.Provider)
		}
	}
}

// TestProjectIssueSourceOverAPI binds and unbinds over the routes: the DTO
// carries the stored binding and the daemon-derived effective provider, a
// refusal is a 400 validation_failed, and null resets to derive.
func TestProjectIssueSourceOverAPI(t *testing.T) {
	repo := testrepo.Init(t, "main")
	// A repository file naming a site is never read: the binding is daemon
	// state only (task 142 decision 5.2).
	if err := os.MkdirAll(filepath.Join(repo, ".vincent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".vincent", "issue-source.yaml"),
		[]byte("provider: jira\nsite: https://evil.example\nproject_key: PWN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newProjectHarness(t)
	p := h.mustCreate(t, map[string]any{"path": repo})
	id := int64(p["id"].(float64))
	src := p["issue_source"].(map[string]any)
	if src["provider"] != "derive" || src["effective"] != "none" || src["jira"] != nil {
		t.Fatalf("new project issue_source = %v, want derive/none with no jira", src)
	}

	path := "/v1/projects/" + itoa(id)
	resp, out := h.doJSON(t, http.MethodPatch, path, map[string]any{
		"issue_source": map[string]any{"provider": "jira", "jira": map[string]any{
			"site": "https://Acme.atlassian.net/", "project_key": "ENG", "jql": "labels = x",
			"mapping": map[string]any{"overrides": map[string]any{"reopen": "Again"}},
		}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bind jira: %d %s", resp.StatusCode, out)
	}
	// The single-project spelling is read back as the list (task 146).
	src = decodeProject(t, out)["issue_source"].(map[string]any)
	jira, _ := src["jira"].(map[string]any)
	projects, _ := jira["projects"].([]any)
	if src["provider"] != "jira" || src["effective"] != "jira" || jira == nil ||
		jira["site"] != "https://acme.atlassian.net" || len(projects) != 1 ||
		fmt.Sprint(projects[0]) != "map[jql:labels = x key:ENG]" || jira["project_key"] != nil {
		t.Fatalf("bound issue_source = %v", src)
	}
	if m, _ := jira["mapping"].(map[string]any); m == nil {
		t.Errorf("mapping = %v, want the override object", jira["mapping"])
	}

	// The sync status carries the bound site.
	resp, out = h.doJSON(t, http.MethodGet, path+"/issues/sync", nil)
	if resp.StatusCode != http.StatusOK || decodeProject(t, out)["site"] != "https://acme.atlassian.net" {
		t.Errorf("sync status = %d %s, want the bound site", resp.StatusCode, out)
	}

	for name, body := range map[string]any{
		"http site":     map[string]any{"provider": "jira", "jira": map[string]any{"site": "http://acme.example", "project_key": "ENG"}},
		"unknown field": map[string]any{"provider": "none", "token": "x"},
		"bad provider":  map[string]any{"provider": "gitlab"},
	} {
		resp, out = h.doJSON(t, http.MethodPatch, path, map[string]any{"issue_source": body})
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
		if !strings.Contains(string(out), "issue_source") {
			t.Errorf("%s: message %s does not name issue_source", name, out)
		}
	}
	resp, out = h.doJSON(t, http.MethodPost, "/v1/projects", map[string]any{
		"path": testrepo.Init(t, "main"), "issue_source": map[string]any{"provider": "jira"},
	})
	wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)

	resp, out = h.doJSON(t, http.MethodPatch, path, map[string]any{"issue_source": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unbind: %d %s", resp.StatusCode, out)
	}
	src = decodeProject(t, out)["issue_source"].(map[string]any)
	if src["provider"] != "derive" || src["jira"] != nil {
		t.Errorf("after null issue_source = %v, want derive", src)
	}
	stored, err := h.store.GetProject(t.Context(), id)
	if err != nil || !stored.IssueSource.Equal(store.IssueSource{}) {
		t.Errorf("stored binding = %+v, %v; want cleared", stored.IssueSource, err)
	}
}

// TestProjectRebindOverAPI: switching a GitHub-derived project to none marks
// its imported remotes missing and drops the sync row; a JQL-only edit on a
// jira project is not a rebind.
func TestProjectRebindOverAPI(t *testing.T) {
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	h := newProjectHarness(t)
	p := h.mustCreate(t, map[string]any{"path": repo})
	id := int64(p["id"].(float64))
	if src := p["issue_source"].(map[string]any); src["effective"] != "github" {
		t.Fatalf("github origin effective = %v", src["effective"])
	}
	ctx := t.Context()
	iss, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: id, Provider: "github", RemoteKey: "I_1", Repo: "o/r", Number: 1, Title: "t", State: "open",
	}, "sync")
	if err != nil {
		t.Fatal(err)
	}
	put := func() {
		t.Helper()
		if err := h.store.PutIssueSyncState(ctx, store.IssueSyncState{ProjectID: id, Provider: "github", Repo: "o/r", OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	put()
	path := "/v1/projects/" + itoa(id)

	// Pinning github explicitly keeps the effective provider: no rebind.
	resp, out := h.doJSON(t, http.MethodPatch, path, map[string]any{"issue_source": map[string]any{"provider": "github"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pin github: %d %s", resp.StatusCode, out)
	}
	if _, err := h.store.GetIssueSyncState(ctx, id); err != nil {
		t.Fatalf("pinning the same provider dropped the sync row: %v", err)
	}

	resp, out = h.doJSON(t, http.MethodPatch, path, map[string]any{"issue_source": map[string]any{"provider": "none"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bind none: %d %s", resp.StatusCode, out)
	}
	if _, err := h.store.GetIssueSyncState(ctx, id); err == nil {
		t.Error("rebind kept the sync row")
	}
	got, err := h.store.GetIssue(ctx, iss.ID)
	if err != nil || got.Remote == nil || got.Remote.Status != store.RemoteStatusMissing {
		t.Fatalf("remote after rebind = %+v, %v; want missing", got, err)
	}

	jira := func(jql string) map[string]any {
		return map[string]any{"issue_source": map[string]any{"provider": "jira", "jira": map[string]any{
			"site": "https://acme.atlassian.net", "project_key": "ENG", "jql": jql,
		}}}
	}
	if resp, out = h.doJSON(t, http.MethodPatch, path, jira("")); resp.StatusCode != http.StatusOK {
		t.Fatalf("bind jira: %d %s", resp.StatusCode, out)
	}
	put()
	if resp, out = h.doJSON(t, http.MethodPatch, path, jira("labels = x")); resp.StatusCode != http.StatusOK {
		t.Fatalf("narrow jql: %d %s", resp.StatusCode, out)
	}
	if _, err := h.store.GetIssueSyncState(ctx, id); err != nil {
		t.Errorf("a JQL-only edit rebound the project: %v", err)
	}

	// Several projects (task 146 decision 4): adding one rebinds — it needs
	// an import of its own — but keeps the issues of the one still named;
	// reordering them or editing one's JQL does not.
	eng, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: id, Provider: "jira", RemoteKey: "100", Repo: "ENG", Number: 1, Title: "e", State: "open",
	}, "sync")
	if err != nil {
		t.Fatal(err)
	}
	list := func(projects ...map[string]any) map[string]any {
		return map[string]any{"issue_source": map[string]any{"provider": "jira", "jira": map[string]any{
			"site": "https://acme.atlassian.net", "projects": projects,
		}}}
	}
	engP, opsP := map[string]any{"key": "ENG"}, map[string]any{"key": "OPS", "jql": "type = Bug"}
	if resp, out = h.doJSON(t, http.MethodPatch, path, list(engP, opsP)); resp.StatusCode != http.StatusOK {
		t.Fatalf("add a project: %d %s", resp.StatusCode, out)
	}
	if _, err := h.store.GetIssueSyncState(ctx, id); err == nil {
		t.Error("adding a project kept the sync row")
	}
	if got, err := h.store.GetIssue(ctx, eng.ID); err != nil || got.Remote == nil || got.Remote.Status != store.RemoteStatusLive {
		t.Errorf("a still-bound project's issue after the rebind = %+v, %v; want live", got, err)
	}
	put()
	opsP["jql"] = "type = Task"
	if resp, out = h.doJSON(t, http.MethodPatch, path, list(opsP, engP)); resp.StatusCode != http.StatusOK {
		t.Fatalf("reorder: %d %s", resp.StatusCode, out)
	}
	if _, err := h.store.GetIssueSyncState(ctx, id); err != nil {
		t.Errorf("reordering the projects rebound the project: %v", err)
	}
	if resp, out = h.doJSON(t, http.MethodPatch, path, list(opsP)); resp.StatusCode != http.StatusOK {
		t.Fatalf("drop a project: %d %s", resp.StatusCode, out)
	}
	if got, err := h.store.GetIssue(ctx, eng.ID); err != nil || got.Remote == nil || got.Remote.Status != store.RemoteStatusMissing {
		t.Errorf("a dropped project's issue = %+v, %v; want missing", got, err)
	}
}

// TestProjectListingReusesOrigin is review F2: a listing resolves a derived
// project's effective provider from an origin read within originTTL rather
// than running git for every project on every refresh, and reads origin
// afresh once that has passed.
func TestProjectListingReusesOrigin(t *testing.T) {
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	h := newProjectHarness(t)
	now := time.Now()
	h.srv.origins.now = func() time.Time { return now }
	h.mustCreate(t, map[string]any{"path": repo})
	effective := func() any {
		t.Helper()
		resp, out := h.doJSON(t, http.MethodGet, "/v1/projects", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list: %d %s", resp.StatusCode, out)
		}
		var list []map[string]any
		if err := json.Unmarshal(out, &list); err != nil || len(list) != 1 {
			t.Fatalf("list = %s, %v", out, err)
		}
		return list[0]["issue_source"].(map[string]any)["effective"]
	}
	if got := effective(); got != "github" {
		t.Fatalf("effective = %v, want github", got)
	}
	// Origin moves off GitHub; within the TTL the listing runs no git, so
	// it still reports what it read.
	testrepo.Run(t, repo, "remote", "set-url", "origin", "https://example.com/o/r.git")
	if got := effective(); got != "github" {
		t.Errorf("within the TTL effective = %v, want the cached github (git was run again)", got)
	}
	now = now.Add(originTTL)
	if got := effective(); got != "none" {
		t.Errorf("after the TTL effective = %v, want none read afresh", got)
	}
}

// TestProjectRepointRebinds is review F3: re-pointing a derived project's
// path is a rebind exactly when it changes the effective provider. A path
// whose origin is not GitHub abandons the imported remotes and drops the
// sync row; a path whose origin is another github.com repository is not a
// rebind, so both are kept and the importer's origin_changed gate (task 130
// decision 15.5) is what answers it.
func TestProjectRepointRebinds(t *testing.T) {
	withOrigin := func(url string) string {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "remote", "add", "origin", url)
		return repo
	}
	for _, tc := range []struct {
		name, origin, effective string
		rebind                  bool
	}{
		{"off github", "https://example.com/o/r.git", "none", true},
		{"another github repository", "https://github.com/o/other.git", "github", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newProjectHarness(t)
			p := h.mustCreate(t, map[string]any{"path": withOrigin("https://github.com/o/r.git")})
			id := int64(p["id"].(float64))
			ctx := t.Context()
			iss, _, err := h.store.UpsertRemoteIssue(ctx, store.RemoteIssue{
				ProjectID: id, Provider: "github", RemoteKey: "I_1", Repo: "o/r", Number: 1, Title: "t", State: "open",
			}, "sync")
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.PutIssueSyncState(ctx, store.IssueSyncState{ProjectID: id, Provider: "github", Repo: "o/r", OK: true}); err != nil {
				t.Fatal(err)
			}
			resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+itoa(id), map[string]any{"path": withOrigin(tc.origin)})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("re-point: %d %s", resp.StatusCode, out)
			}
			if got := decodeProject(t, out)["issue_source"].(map[string]any)["effective"]; got != tc.effective {
				t.Errorf("effective after re-point = %v, want %s", got, tc.effective)
			}
			st, err := h.store.GetIssueSyncState(ctx, id)
			got, gerr := h.store.GetIssue(ctx, iss.ID)
			if gerr != nil || got.Remote == nil {
				t.Fatalf("issue after re-point = %+v, %v", got, gerr)
			}
			if tc.rebind {
				if !errors.Is(err, store.ErrNotFound) {
					t.Errorf("sync row after a rebinding re-point = %+v, %v; want ErrNotFound", st, err)
				}
				if got.Remote.Status != store.RemoteStatusMissing {
					t.Errorf("remote after a rebinding re-point = %q, want missing", got.Remote.Status)
				}
				return
			}
			if err != nil || st.Repo != "o/r" {
				t.Errorf("sync row after a same-provider re-point = %+v, %v; want kept with repo o/r", st, err)
			}
			if got.Remote.Status != store.RemoteStatusLive {
				t.Errorf("remote after a same-provider re-point = %q, want live", got.Remote.Status)
			}
		})
	}
}
