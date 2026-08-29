package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// The §13.2 pull-request endpoints (task 052), against the real handlers over
// httptest with the daemon's GitHub client pointed at cmd/fakegh — so these
// run on all three platforms and none of them touches the network.

func (h *githubHarness) pulls(t *testing.T) (*http.Response, []byte) {
	t.Helper()
	return h.doJSON(t, http.MethodGet,
		"/v1/projects/"+strconv.FormatInt(h.projectID, 10)+"/github/pulls", nil)
}

// seedTask inserts a task on branch, so a listing has something to match.
func (h *githubHarness) seedTask(t *testing.T, branch string) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: "a task", WorkflowName: "adhoc",
		BaseBranch: "main", BranchName: branch, State: store.TaskQueued,
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func (h *githubHarness) taskPull(t *testing.T, id int64) githubTaskPullResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet,
		"/v1/tasks/"+strconv.FormatInt(id, 10)+"/github/pull", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("task pull: %d %s", resp.StatusCode, body)
	}
	var out githubTaskPullResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("task pull body: %v (%s)", err, body)
	}
	return out
}

// TestGitHubPullsListing: the listing is open-only, newest first, and marks
// the rows a stored link claims.
func TestGitHubPullsListing(t *testing.T) {
	t.Setenv("FAKEGH_PR_BRANCH", "vincent/1-a-task")
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.seedTask(t, "vincent/1-a-task")
	if _, err := h.store.SetTaskGitHubPull(t.Context(), task.ID,
		store.LinkPull("octo/repo", 412, github.SourceAuto, task.CreatedAt)); err != nil {
		t.Fatalf("link: %v", err)
	}
	resp, body := h.pulls(t)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pulls: %d %s", resp.StatusCode, body)
	}
	var out []githubPullResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("pulls body: %v (%s)", err, body)
	}
	if len(out) != 2 {
		t.Fatalf("listing has %d rows, want the 2 open ones", len(out))
	}
	// The merged pull request in the corpus is not in the listing: an
	// open-only listing that carried it would disagree with `gh pr list`.
	for _, row := range out {
		if row.Merged {
			t.Errorf("a merged pull request appeared in an open-only listing: #%d", row.Number)
		}
	}
	if out[0].Number != 412 || out[1].Number != 401 {
		t.Fatalf("order = %d, %d; want newest first", out[0].Number, out[1].Number)
	}
	if out[0].TaskID == nil || *out[0].TaskID != task.ID {
		t.Errorf("row 412 task_id = %v, want %d", out[0].TaskID, task.ID)
	}
	if out[0].LinkSource != github.SourceAuto {
		t.Errorf("link_source = %q, want auto", out[0].LinkSource)
	}
	if out[1].TaskID != nil {
		t.Errorf("row 401 claims task %v, but nothing links it", out[1].TaskID)
	}
}

// TestGitHubPullsGateMakesNoCall is the acceptance criterion, asserted 035's
// way: a disabled integration makes no `gh` invocation on this route either.
func TestGitHubPullsGateMakesNoCall(t *testing.T) {
	off := func() config.Config {
		c := config.Default()
		c.GitHub.Enabled = false
		return c
	}
	h := newGitHubHarness(t, off, ghOrigin)
	resp, body := h.pulls(t)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pulls: %d %s, want 409", resp.StatusCode, body)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Fatalf("a disabled integration invoked gh:\n%s", calls)
	}
}

// TestGitHubPullsNonGitHubProjectMakesNoCall: the gate stops at the first
// "no", so an origin that is not github.com is never probed.
func TestGitHubPullsNonGitHubProjectMakesNoCall(t *testing.T) {
	h := newGitHubHarness(t, nil, "https://gitlab.com/octo/repo.git")
	resp, body := h.pulls(t)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pulls: %d %s, want 409", resp.StatusCode, body)
	}
	var envelope struct {
		Error struct {
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("error body: %v (%s)", err, body)
	}
	if envelope.Error.Details["reason"] != github.ReasonNotGitHub {
		t.Errorf("reason = %q, want %q", envelope.Error.Details["reason"], github.ReasonNotGitHub)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Fatalf("a non-GitHub project invoked gh:\n%s", calls)
	}
}

// TestTaskPullServesTheMergedCase is what the durable link exists for: the
// open-only listing cannot answer for #377, and the task still names it with
// its real state.
func TestTaskPullServesTheMergedCase(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.seedTask(t, "vincent/3-ship-the-thing")
	if _, err := h.store.SetTaskGitHubPull(t.Context(), task.ID,
		store.LinkPull("octo/repo", 377, github.SourceAuto, task.CreatedAt)); err != nil {
		t.Fatalf("link: %v", err)
	}
	got := h.taskPull(t, task.ID)
	if !got.Linked || got.Number != 377 {
		t.Fatalf("task pull = %+v, want a link to #377", got)
	}
	if got.Pull == nil {
		t.Fatal("the linked pull request was not fetched: a merged one has to come from GetPull")
	}
	if !got.Pull.Merged || got.Pull.State != github.StateClosed {
		t.Errorf("pull = %+v, want merged and closed", got.Pull)
	}
	if got.Pull.FetchedAt.IsZero() {
		t.Error("the live fetch carries no FetchedAt")
	}
	if got.CompareURL != "" {
		t.Error("a linked task was offered a compare URL: there is nothing to open")
	}
}

// TestTaskPullOffersACompareURL: an unlinked task gets GitHub's own
// "open a pull request" page, prefilled — and building it makes no call.
func TestTaskPullOffersACompareURL(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	task := &store.Task{
		ProjectID: h.projectID, Title: "a task", WorkflowName: "adhoc",
		Description: "Do the thing.",
		BaseBranch:  "main", BranchName: "vincent/7-unlinked", State: store.TaskQueued,
		GitHubIssue: &github.Issue{Repo: "octo/repo", Number: 231, Title: "an issue"},
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}
	got := h.taskPull(t, task.ID)
	if got.Linked {
		t.Fatalf("task pull = %+v, want unlinked", got)
	}
	if got.CompareURL == "" {
		t.Fatal("no compare URL was offered for an unlinked task with a branch")
	}
	// The capability probe runs, as it does on every GitHub route; nothing
	// beyond it does. Producing the URL is string construction, so no `gh pr`
	// invocation may appear in the log — asserted, not asserted by
	// inspection.
	if calls := h.ghCalls(t); strings.Contains(calls, "pr ") {
		t.Errorf("building a compare URL invoked gh pr:\n%s", calls)
	}
	u, err := url.Parse(got.CompareURL)
	if err != nil {
		t.Fatalf("compare URL does not parse: %v", err)
	}
	if u.Path != "/octo/repo/compare/main...vincent/7-unlinked" {
		t.Errorf("path = %q, want base...head", u.Path)
	}
	if body := u.Query().Get("body"); body != "Do the thing.\n\nCloses #231" {
		t.Errorf("body = %q, want the description plus Closes #231", body)
	}
	if title := u.Query().Get("title"); title != task.Title {
		t.Errorf("title = %q, want the task's title", title)
	}
}

// TestTaskPullLinkAndUnlink: a human's link wins over the reconciler's, and a
// human's unlink is *stored* rather than cleared, so the next tick can read
// the refusal.
func TestTaskPullLinkAndUnlink(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.seedTask(t, "vincent/7-unlinked")
	path := "/v1/tasks/" + strconv.FormatInt(task.ID, 10) + "/github/pull"

	resp, body := h.doJSON(t, http.MethodPost, path, map[string]any{"number": 412})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link: %d %s", resp.StatusCode, body)
	}
	var linked taskResponse
	if err := json.Unmarshal(body, &linked); err != nil {
		t.Fatalf("link body: %v (%s)", err, body)
	}
	if linked.GitHubPull == nil || linked.GitHubPull.Number != 412 ||
		linked.GitHubPull.Source != github.SourceHuman {
		t.Fatalf("github_pull = %+v, want a human link to #412", linked.GitHubPull)
	}
	if linked.GitHubPull.Repo != "octo/repo" {
		t.Errorf("repo = %q, want the identity derived from origin", linked.GitHubPull.Repo)
	}

	resp, body = h.doJSON(t, http.MethodDelete, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlink: %d %s", resp.StatusCode, body)
	}
	var unlinked taskResponse
	if err := json.Unmarshal(body, &unlinked); err != nil {
		t.Fatalf("unlink body: %v (%s)", err, body)
	}
	if unlinked.GitHubPull == nil {
		t.Fatal("the unlink cleared the column: the refusal is not recoverable")
	}
	if !unlinked.GitHubPull.Suppressed || unlinked.GitHubPull.Number != 412 {
		t.Errorf("github_pull = %+v, want a suppressed record of #412", unlinked.GitHubPull)
	}
}

// TestTaskPullLinkRejectsANonNumber keeps the §13.1 envelope's shape on the
// one route that takes a body.
func TestTaskPullLinkRejectsANonNumber(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.seedTask(t, "vincent/7-unlinked")
	path := "/v1/tasks/" + strconv.FormatInt(task.ID, 10) + "/github/pull"
	resp, body := h.doJSON(t, http.MethodPost, path, map[string]any{"number": 0})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("link: %d %s, want 400", resp.StatusCode, body)
	}
}

// TestTaskPullRendersWhenGitHubIsOff: a workspace still gets its stored link
// and a named reason when the integration is disabled — refusing the whole
// row would take a fact vincent owns away from a client that can render it.
func TestTaskPullRendersWhenGitHubIsOff(t *testing.T) {
	off := func() config.Config {
		c := config.Default()
		c.GitHub.Enabled = false
		return c
	}
	h := newGitHubHarness(t, off, ghOrigin)
	task := h.seedTask(t, "vincent/1-a-task")
	if _, err := h.store.SetTaskGitHubPull(t.Context(), task.ID,
		store.LinkPull("octo/repo", 412, github.SourceAuto, task.CreatedAt)); err != nil {
		t.Fatalf("link: %v", err)
	}
	got := h.taskPull(t, task.ID)
	if !got.Linked || got.Number != 412 {
		t.Fatalf("task pull = %+v, want the stored link", got)
	}
	if got.Reason != github.ReasonDisabled {
		t.Errorf("reason = %q, want %q", got.Reason, github.ReasonDisabled)
	}
	if got.Pull != nil {
		t.Error("a disabled integration fetched the pull request anyway")
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Fatalf("a disabled integration invoked gh:\n%s", calls)
	}
}
