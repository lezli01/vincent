package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// Creating a task **from** a pull request (§13.2, task 064), against the real
// handlers with the daemon's GitHub client pointed at cmd/fakegh.

// createFromPull posts a create request naming a pull request and returns the
// decoded 201.
func (h *githubHarness) createFromPull(t *testing.T, body map[string]any) (*http.Response, []byte) {
	t.Helper()
	body["project_id"] = h.projectID
	return h.doJSON(t, http.MethodPost, "/v1/tasks", body)
}

// TestCreateTaskFromPullPrefillsAndNamesTheBranch is the core of the feature:
// one prefill implementation, and the head branch as the task's branch.
func TestCreateTaskFromPullPrefillsAndNamesTheBranch(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.createFromPull(t, map[string]any{"github_pull": 412})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var out taskResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("create body: %v (%s)", err, body)
	}
	if out.Title != "#412 Add a thing" {
		t.Errorf("title = %q, want the pull request's title with its number", out.Title)
	}
	if !strings.Contains(out.Description, "GitHub pull request #412") {
		t.Errorf("description = %q, want the body plus the link line", out.Description)
	}
	// The branch is the head branch, not vincent/{id}-{slug} — the point of
	// the whole feature (decision 1).
	if out.BranchName != "vincent/1-add-a-thing" {
		t.Errorf("branch = %q, want the pull request's head branch", out.BranchName)
	}
	// And the link is there the instant the task exists, as `human`, with the
	// flag that says the branch came from the pull request (decisions 7, 8).
	task, err := h.store.GetTask(t.Context(), out.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.GitHubPull == nil {
		t.Fatal("no link was written at creation")
	}
	if task.GitHubPull.Source != github.SourceHuman {
		t.Errorf("link source = %q, want human so the reconciler will not overwrite it", task.GitHubPull.Source)
	}
	if !task.GitHubPull.FromPull() {
		t.Error("the link does not record that the branch came from the pull request")
	}
	if task.GitHubPull.Fork {
		t.Error("#412 is a same-repository pull request, not a fork")
	}
}

// The three surfaces produce the same stored task. The TUI's path is the
// listing's computed prefill applied to a create call, which is what the form
// does; the CLI's is the bare number.
func TestCreateTaskFromPullIsOnePrefillImplementation(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.doJSON(t, http.MethodGet,
		"/v1/projects/"+strconv.FormatInt(h.projectID, 10)+"/github/pulls?state=all&workflow=adhoc", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listing: %d %s", resp.StatusCode, body)
	}
	var rows []githubPullResponse
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("listing body: %v (%s)", err, body)
	}
	var previewed *githubPrefill
	for _, row := range rows {
		if row.Number == 412 {
			previewed = row.Prefill
		}
	}
	if previewed == nil {
		t.Fatal("the listing computed no prefill for #412")
	}
	// The bare-number path (the CLI flag and the API).
	_, bare := h.createFromPull(t, map[string]any{"github_pull": 412})
	var fromNumber taskResponse
	if err := json.Unmarshal(bare, &fromNumber); err != nil {
		t.Fatalf("bare create: %v (%s)", err, bare)
	}
	if fromNumber.Title != previewed.Title || fromNumber.Description != previewed.Description {
		t.Errorf("the create call and the preview disagree:\n create = %q / %q\n preview = %q / %q",
			fromNumber.Title, fromNumber.Description, previewed.Title, previewed.Description)
	}
}

// The declared `pull` field is filled with the number, which is how a `run:`
// step reads it out of §8.5's environment — there is no `.Pull` to render.
func TestCreateTaskFromPullFillsTheDeclaredPullField(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, "pr-work", `name: pr-work
description: act on a pull request
fields:
  - name: pull
    type: integer
steps:
  - {id: note, type: command, run: "exit 0"}
`)
	h.reg.ReloadGlobal()
	_, body := h.createFromPull(t, map[string]any{"github_pull": 412, "workflow": "pr-work"})
	var out taskResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("create body: %v (%s)", err, body)
	}
	if out.Fields["pull"] != "412" {
		t.Errorf("fields = %v, want pull=412", out.Fields)
	}
}

// A fork is detectable, runs, and says so on the task at creation rather than
// when a delivery step fails (decision 5).
func TestCreateTaskFromForkPullRecordsTheFork(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	_, body := h.createFromPull(t, map[string]any{"github_pull": 355})
	var out taskResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("create body: %v (%s)", err, body)
	}
	task, err := h.store.GetTask(t.Context(), out.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.GitHubPull == nil || !task.GitHubPull.Fork {
		t.Fatalf("link = %+v, want the fork flag set", task.GitHubPull)
	}
	if task.BranchName != "typo-fix" {
		t.Errorf("branch = %q, want the fork's head branch", task.BranchName)
	}
}

// A closed or merged pull request is selectable; the default listing is still
// open-only (decision 9).
func TestGitHubPullsStateParameter(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	count := func(query string) int {
		t.Helper()
		resp, body := h.doJSON(t, http.MethodGet,
			"/v1/projects/"+strconv.FormatInt(h.projectID, 10)+"/github/pulls"+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("listing%s: %d %s", query, resp.StatusCode, body)
		}
		var rows []githubPullResponse
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("listing body: %v (%s)", err, body)
		}
		return len(rows)
	}
	open, all := count(""), count("?state=all")
	if all <= open {
		t.Errorf("state=all returned %d rows and the default %d; all must reach the merged one", all, open)
	}
	// And the default is still open: the merged row must not be in it.
	resp, body := h.pulls(t)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default listing: %d %s", resp.StatusCode, body)
	}
	var rows []githubPullResponse
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("default listing body: %v (%s)", err, body)
	}
	for _, row := range rows {
		if row.Merged {
			t.Errorf("the default listing carried a merged pull request: #%d", row.Number)
		}
	}
}

// branch_override on a pull-request task's retry is refused (decision 10).
func TestRetryBranchOverrideRefusedOnAPullTask(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	task := h.seedTask(t, "vincent/1-a-task")
	link := store.LinkPull("octo/repo", 412, github.SourceHuman, task.CreatedAt)
	link.Branch = true
	if _, err := h.store.SetTaskGitHubPull(t.Context(), task.ID, link); err != nil {
		t.Fatalf("link: %v", err)
	}
	resp, body := h.doJSON(t, http.MethodPost,
		"/v1/tasks/"+strconv.FormatInt(task.ID, 10)+"/retry",
		map[string]any{"branch_override": "somewhere/else"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "head branch") {
		t.Errorf("body = %s, want the refusal to say why", body)
	}
	// And the branch is untouched.
	after, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if after.BranchName != "vincent/1-a-task" {
		t.Errorf("branch = %q, want it unchanged", after.BranchName)
	}
}

// The regression guard: a task created without `github_pull` is unchanged in
// every respect, including making no GitHub call at all.
func TestCreateTaskWithoutPullMakesNoGitHubCall(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks",
		map[string]any{"project_id": h.projectID, "title": "an ordinary task"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var out taskResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("create body: %v (%s)", err, body)
	}
	if !strings.HasPrefix(out.BranchName, "vincent/") {
		t.Errorf("branch = %q, want the built-in vincent/{id}-{slug} name", out.BranchName)
	}
	task, err := h.store.GetTask(t.Context(), out.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.GitHubPull != nil {
		t.Errorf("link = %+v, want none", task.GitHubPull)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("gh was invoked:\n%s", calls)
	}
}

// Naming both an issue and a pull request is refused: two prefills would
// fight over the same title and description.
func TestCreateTaskRefusesBothIssueAndPull(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.createFromPull(t, map[string]any{"github_issue": 231, "github_pull": 412})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create: %d %s, want 400", resp.StatusCode, body)
	}
}
