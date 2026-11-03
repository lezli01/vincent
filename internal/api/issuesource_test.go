package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 142.8: the daemon renders an imported issue's reference — key, ref,
// short_ref and provider_name — on the issue DTO and on a task's `issue`,
// so a client prints it rather than building one from provider parts.

// getIssueBody is GET /v1/issues/{id}, no preview.
func getIssueBody(t *testing.T, h *githubHarness, id int64) issueBody {
	t.Helper()
	resp, out := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get issue = %d: %s", resp.StatusCode, out)
	}
	var b issueBody
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatalf("issue body: %v (%s)", err, out)
	}
	return b
}

// wantGitHubSource is the rendered half of octo/repo#200.
func wantGitHubSource(t *testing.T, what string, src *issueSourceBody) {
	t.Helper()
	if src == nil {
		t.Fatalf("%s: no source", what)
	}
	if src.Provider != "github" || src.Repo != "octo/repo" || src.Number != 200 ||
		src.URL != "https://github.com/octo/repo/issues/200" {
		t.Errorf("%s: existing fields changed: %+v", what, src)
	}
	if src.Key != "200" || src.Ref != "octo/repo#200" || src.ShortRef != "gh#200" || src.ProviderName != "GitHub" {
		t.Errorf("%s: rendered = key %q ref %q short_ref %q provider_name %q, want 200, octo/repo#200, gh#200, GitHub",
			what, src.Key, src.Ref, src.ShortRef, src.ProviderName)
	}
}

func TestIssueSourceIsRenderedEverywhere(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.importedIssue(t, "octo/repo", 200)

	resp, out := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues?project_id=%d", h.projectID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", resp.StatusCode, out)
	}
	var list []issueRowBody
	if err := json.Unmarshal(out, &list); err != nil || len(list) != 1 {
		t.Fatalf("list body: %v (%s)", err, out)
	}
	wantGitHubSource(t, "GET /v1/issues", list[0].Source)
	for _, key := range []string{`"ref":"octo/repo#200"`, `"short_ref":"gh#200"`, `"provider_name":"GitHub"`, `"key":"200"`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("the list's wire is missing %s: %s", key, out)
		}
	}
	wantGitHubSource(t, "GET /v1/issues/{id}", getIssueBody(t, h, iss.ID).Source)

	code, tr, out := h.createTask(t, map[string]any{"workflow": vincentIssueWorkflow, "issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, out)
	}
	get := func() taskResponse {
		resp, out := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", tr.ID), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get = %d: %s", resp.StatusCode, out)
		}
		var got taskResponse
		_ = json.Unmarshal(out, &got)
		return got
	}
	if got := get(); got.Issue != nil {
		wantGitHubSource(t, "task issue (live)", got.Issue.Source)
	}
	if _, _, err := h.store.TransitionTask(t.Context(), tr.ID, store.TaskState(tr.State), store.TaskAborted, store.TaskChange{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if err := h.store.DeleteIssue(t.Context(), iss.ID, issuestate.Human); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	got := get()
	if got.Issue == nil {
		t.Fatal("a deleted issue's task lost its issue")
	}
	wantGitHubSource(t, "task issue (snapshot)", got.Issue.Source)
}

// A snapshot taken before keys were stored carries none, and still renders:
// GitHub's key is its number.
func TestOldSnapshotStillRendersItsReference(t *testing.T) {
	var snap store.IssueSnapshot
	raw := `{"id":3,"title":"t","state":"open","remote":{"provider":"github","repo":"octo/repo","number":200,"url":"https://github.com/octo/repo/issues/200"}}`
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	wantGitHubSource(t, "old snapshot", snapshotIssueResponse(&snap).Source)
}

// A Jira issue's display key lives in its stored payload: the issue DTO
// reads it at read time, and a task's snapshot keeps it once the issue is
// gone.
func TestJiraIssueSourceRendersItsKey(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss, _, err := h.store.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
		ProjectID: h.projectID, Provider: "jira", RemoteKey: "10012",
		URL:        "https://example.atlassian.net/browse/PROJ-12",
		RemoteJSON: `{"key":"PROJ-12","state":"To Do"}`,
		Title:      "From Jira", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	want := func(what string, src *issueSourceBody) {
		t.Helper()
		if src == nil || src.Provider != "jira" || src.Number != 0 || src.Key != "PROJ-12" ||
			src.Ref != "PROJ-12" || src.ShortRef != "PROJ-12" || src.ProviderName != "Jira" {
			t.Errorf("%s: source = %+v, want PROJ-12 from Jira", what, src)
		}
	}
	want("GET /v1/issues/{id}", getIssueBody(t, h, iss.ID).Source)

	cur, err := h.store.GetIssue(t.Context(), iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap := store.NewIssueSnapshot(cur, time.Now())
	if snap.Remote == nil || snap.Remote.Key != "PROJ-12" {
		t.Fatalf("snapshot remote = %+v, want key PROJ-12", snap.Remote)
	}
	want("task issue (snapshot)", snapshotIssueResponse(snap).Source)
	want("task issue (live)", liveIssueResponse(cur).Source)
}

// The forge_write_needs_human prose names the issue's provider (task 142
// decision 11.3).
func TestForgeWriteRefusalNamesTheProvider(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss := h.imported(t, "I_1", 1)
	resp, out := h.do(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", iss.ID), map[string]any{}, headerTaskMarker, "12")
	if d := conflict(t, resp, out); detailString(t, d, "reason") != issueReasonForgeWrite {
		t.Fatalf("agent close = %s, want %s", out, issueReasonForgeWrite)
	}
	if !strings.Contains(string(out), "may write to GitHub") {
		t.Errorf("the refusal does not name GitHub: %s", out)
	}
}
