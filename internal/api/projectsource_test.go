package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			store.IssueSource{Provider: "jira", JiraSite: "https://example.atlassian.net", JiraProjectKey: "ENG"},
		},
		{
			"trailing slash", jiraReq("https://example.atlassian.net/", "ENG_2", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "https://example.atlassian.net", JiraProjectKey: "ENG_2"},
		},
		{
			"http 127.0.0.1", jiraReq("http://127.0.0.1:8080", "A", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "http://127.0.0.1:8080", JiraProjectKey: "A"},
		},
		{
			"http localhost", jiraReq("http://localhost:9", "A", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "http://localhost:9", JiraProjectKey: "A"},
		},
		{
			"http [::1]", jiraReq("http://[::1]:9", "A", "", ""),
			store.IssueSource{Provider: "jira", JiraSite: "http://[::1]:9", JiraProjectKey: "A"},
		},
		{
			"jql and mapping", jiraReq("https://x.example", "A", "  labels = backend  ",
				`{ "overrides": {"reopen": "Back to do"}, "duplicate_resolutions": ["Dup"] }`),
			store.IssueSource{
				Provider: "jira", JiraSite: "https://x.example", JiraProjectKey: "A", JiraJQL: "labels = backend",
				JiraMappingJSON: `{"overrides":{"reopen":"Back to do"},"duplicate_resolutions":["Dup"]}`,
			},
		},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, msg := validateIssueSource(c.req)
			if msg != "" {
				t.Fatalf("refused: %s", msg)
			}
			if got != c.want {
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
		{"no key", "issue_source.jira.project_key is required", jiraReq("https://x.example", "", "", "")},
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
	src = decodeProject(t, out)["issue_source"].(map[string]any)
	jira, _ := src["jira"].(map[string]any)
	if src["provider"] != "jira" || src["effective"] != "jira" || jira == nil ||
		jira["site"] != "https://acme.atlassian.net" || jira["project_key"] != "ENG" || jira["jql"] != "labels = x" {
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
	if err != nil || stored.IssueSource != (store.IssueSource{}) {
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
