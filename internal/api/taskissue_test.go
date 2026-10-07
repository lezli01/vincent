package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// Task 130.7: creating a task from a vincent issue — `issue_id` on POST
// /v1/tasks, the GET /v1/issues/{id}?workflow= preview, the `?issue_id=`
// list filter, the task DTO's `issue` and derived `github_issue`, and
// `Closes #N`.

const vincentIssueWorkflow = "fix-vincent-issue"

// newVincentIssueHarness is the GitHub harness with the issue workflow
// registered. The GitHub client is the fake `gh`, which records every call:
// an `issue_id` create must make none.
func newVincentIssueHarness(t *testing.T) *githubHarness {
	t.Helper()
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, vincentIssueWorkflow, vincentIssueWorkflowYAML)
	h.reg.ReloadGlobal()
	return h
}

func (h *githubHarness) localIssue(t *testing.T, projectID int64) *store.Issue {
	t.Helper()
	iss, err := h.store.CreateIssue(t.Context(), store.NewIssue{
		ProjectID: projectID, Title: "Lock file leaks", Body: "Seen.", Kind: "bug",
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return iss
}

func (h *githubHarness) importedIssue(t *testing.T, repo string, number int) *store.Issue {
	t.Helper()
	iss, _, err := h.store.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: fmt.Sprintf("I_%s_%d", repo, number),
		Repo: repo, Number: number, URL: fmt.Sprintf("https://github.com/%s/issues/%d", repo, number),
		RemoteJSON: `{"state":"OPEN","assignees":["hubot"]}`,
		Title:      "Select an issue", Body: "upstream", State: issuestate.Open, Labels: []string{"enhancement"},
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	return iss
}

func (h *githubHarness) createTask(t *testing.T, body map[string]any) (int, taskResponse, []byte) {
	t.Helper()
	if _, ok := body["project_id"]; !ok {
		body["project_id"] = h.projectID
	}
	resp, out := h.doJSON(t, http.MethodPost, "/v1/tasks", body)
	var tr taskResponse
	if resp.StatusCode == http.StatusCreated {
		if err := json.Unmarshal(out, &tr); err != nil {
			t.Fatalf("task body: %v (%s)", err, out)
		}
	}
	return resp.StatusCode, tr, out
}

func (h *githubHarness) preview(t *testing.T, issueID int64, wf string) issueBody {
	t.Helper()
	resp, out := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d?workflow=%s", issueID, wf), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d: %s", resp.StatusCode, out)
	}
	var b issueBody
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatalf("issue body: %v (%s)", err, out)
	}
	return b
}

// assertPreviewIsStored: the preview's prefill is exactly what the create
// stored — task 035 decision 2's one prefill, kept for vincent issues.
func assertPreviewIsStored(t *testing.T, h *githubHarness, issueID, taskID int64) {
	t.Helper()
	stored, err := h.store.GetTask(t.Context(), taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	got := h.preview(t, issueID, vincentIssueWorkflow).Prefill
	if got == nil {
		t.Fatal("the preview carries no prefill")
	}
	want := githubPrefill{Title: stored.Title, Description: stored.Description, Fields: stored.Fields}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("preview prefill =\n %s\nstored task =\n %s", gotJSON, wantJSON)
	}
}

func TestCreateTaskFromALocalIssue(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.localIssue(t, h.projectID)

	code, tr, out := h.createTask(t, map[string]any{"workflow": vincentIssueWorkflow, "issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, out)
	}
	if tr.Title != "Lock file leaks" || tr.Description != "Seen." {
		t.Errorf("title/description = %q / %q, want the issue's", tr.Title, tr.Description)
	}
	if tr.Fields["issue"] != fmt.Sprint(iss.ID) || tr.Fields["kind"] != "bug" {
		t.Errorf("fields = %v, want issue and kind prefilled", tr.Fields)
	}
	if tr.Issue == nil || tr.Issue.ID != iss.ID || tr.Issue.Title != iss.Title || tr.Issue.State != "open" || tr.Issue.Source != nil {
		t.Errorf("issue = %+v, want the local issue with no source", tr.Issue)
	}
	if tr.GitHubIssue != nil {
		t.Errorf("a local issue derived github_issue %+v", tr.GitHubIssue)
	}
	if len(tr.Warnings) != 0 {
		t.Errorf("warnings = %v for an open issue", tr.Warnings)
	}
	stored, err := h.store.GetTask(t.Context(), tr.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.IssueID == nil || *stored.IssueID != iss.ID || stored.Issue == nil || stored.Issue.ID != iss.ID {
		t.Errorf("stored issue_id %v / snapshot %+v, want issue %d", stored.IssueID, stored.Issue, iss.ID)
	}
	if stored.GitHubIssue != nil {
		t.Errorf("an issue_id create wrote github_issue_json %+v", stored.GitHubIssue)
	}
	assertPreviewIsStored(t, h, iss.ID, tr.ID)
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("an issue_id create called gh:\n%s", calls)
	}
}

func TestCreateTaskFromAnImportedIssue(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.importedIssue(t, "octo/repo", 200)

	code, tr, out := h.createTask(t, map[string]any{"workflow": vincentIssueWorkflow, "issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, out)
	}
	if tr.Fields["github_issue"] != "200" || tr.Fields["assignee"] != "hubot" {
		t.Errorf("fields = %v, want the remote's number and assignee", tr.Fields)
	}
	// github_issue is derived from the snapshot, so `.github_issue.number`
	// keeps working (task 130 decision 7).
	if gi := tr.GitHubIssue; gi == nil || gi.Number != 200 || gi.Repo != "octo/repo" || gi.Title != "Select an issue" {
		t.Errorf("github_issue = %+v, want #200 of octo/repo", tr.GitHubIssue)
	}
	if src := tr.Issue; src == nil || src.Source == nil || src.Source.Number != 200 || src.Source.Provider != "github" {
		t.Errorf("issue = %+v, want its GitHub source", tr.Issue)
	}
	stored, err := h.store.GetTask(t.Context(), tr.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.GitHubIssue != nil {
		t.Errorf("an issue_id create wrote github_issue_json %+v", stored.GitHubIssue)
	}
	if stored.Issue == nil || stored.Issue.Remote == nil || stored.Issue.Remote.Number != 200 {
		t.Errorf("snapshot = %+v, want the remote reference", stored.Issue)
	}
	assertPreviewIsStored(t, h, iss.ID, tr.ID)
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("an imported issue_id create called gh:\n%s", calls)
	}
}

func TestCreateTaskFromAnIssueExplicitValuesWin(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.localIssue(t, h.projectID)

	code, tr, out := h.createTask(t, map[string]any{
		"workflow": vincentIssueWorkflow, "issue_id": iss.ID,
		"title": "mine", "description": "", "fields": map[string]string{"kind": "chore"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, out)
	}
	if tr.Title != "mine" || tr.Description != "" || tr.Fields["kind"] != "chore" {
		t.Errorf("task = %q %q %v, want every explicit value kept", tr.Title, tr.Description, tr.Fields)
	}
	if tr.Fields["issue"] != fmt.Sprint(iss.ID) {
		t.Errorf("fields = %v, want the unset issue field prefilled", tr.Fields)
	}
	// A blank title is "you decide", not an explicit value.
	code, tr, out = h.createTask(t, map[string]any{"workflow": vincentIssueWorkflow, "issue_id": iss.ID, "title": "  "})
	if code != http.StatusCreated || tr.Title != iss.Title {
		t.Errorf("blank title = %d %q (%s), want the issue's", code, tr.Title, out)
	}
}

func TestCreateTaskFromAnIssueRefusals(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.localIssue(t, h.projectID)
	other := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	foreign := h.localIssue(t, int64(other["id"].(float64)))

	for name, body := range map[string]map[string]any{
		"with github_pull": {"issue_id": iss.ID, "github_pull": 1},
		"unknown":          {"issue_id": iss.ID + 1000},
		"cross-project":    {"issue_id": foreign.ID},
		"non-positive":     {"issue_id": 0},
	} {
		body["workflow"] = vincentIssueWorkflow
		code, _, out := h.createTask(t, body)
		if code != http.StatusBadRequest || !strings.Contains(string(out), "issue_id") {
			t.Errorf("%s = %d: %s, want a 400 naming issue_id", name, code, out)
		}
	}
	tasks, err := h.store.ListTasks(t.Context(), store.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("refused creates left %d task(s)", len(tasks))
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("a refused create called gh:\n%s", calls)
	}
}

func TestCreateTaskFromAClosedIssueWarns(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.localIssue(t, h.projectID)
	if _, err := h.store.TransitionIssue(t.Context(), iss.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatalf("close: %v", err)
	}
	code, tr, out := h.createTask(t, map[string]any{"workflow": vincentIssueWorkflow, "issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, out)
	}
	if !slices.ContainsFunc(tr.Warnings, func(w string) bool { return strings.Contains(w, "is closed") }) {
		t.Errorf("warnings = %v, want the closed-issue warning", tr.Warnings)
	}
	if tr.Issue == nil || tr.Issue.State != "closed" {
		t.Errorf("issue = %+v, want closed", tr.Issue)
	}
}

// TestTaskIssueFallsBackToTheSnapshot (task 130 decision 6): deleting the
// issue clears the link, and the DTO renders the snapshot instead.
func TestTaskIssueFallsBackToTheSnapshot(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.importedIssue(t, "octo/repo", 200)
	code, tr, out := h.createTask(t, map[string]any{"workflow": vincentIssueWorkflow, "issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, out)
	}
	// While linked, the live issue: a retitle shows.
	title := "Retitled"
	if _, err := h.store.UpdateIssue(t.Context(), iss.ID, iss.Version, store.IssuePatch{Title: &title}, issuestate.Sync); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
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
	if got := get(); got.Issue == nil || got.Issue.Title != title {
		t.Errorf("linked issue = %+v, want the live title", got.Issue)
	}
	// The task is the issue's main task, which blocks the delete until it
	// settles (task 134.12).
	if _, _, err := h.store.TransitionTask(t.Context(), tr.ID, store.TaskState(tr.State), store.TaskAborted, store.TaskChange{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if err := h.store.DeleteIssue(t.Context(), iss.ID, issuestate.Human); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	got := get()
	if got.Issue == nil || got.Issue.ID != iss.ID || got.Issue.Title != "Select an issue" {
		t.Errorf("after delete issue = %+v, want the snapshot", got.Issue)
	}
	if got.GitHubIssue == nil || got.GitHubIssue.Number != 200 {
		t.Errorf("after delete github_issue = %+v, still derived from the snapshot", got.GitHubIssue)
	}
}

// TestTaskListByIssueExcludesLanes: `?issue_id=` lists the issue's root
// tasks, and the issue's tasks.count counts the same roots — lanes inherit
// the link (decision 5) but are never the issue's work.
func TestTaskListByIssueExcludesLanes(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.localIssue(t, h.projectID)
	code, root, out := h.createTask(t, map[string]any{"workflow": vincentIssueWorkflow, "issue_id": iss.ID})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, out)
	}
	if code, _, out := h.createTask(t, map[string]any{"title": "unrelated"}); code != http.StatusCreated {
		t.Fatalf("unrelated create = %d: %s", code, out)
	}
	parent, err := h.store.GetTask(t.Context(), root.ID)
	if err != nil {
		t.Fatal(err)
	}
	lane := &store.Task{
		ProjectID: h.projectID, Title: "lane", WorkflowName: "w", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "lane-a", State: store.TaskQueued,
		ParentTaskID: &root.ID, LaneID: "a", IssueID: parent.IssueID, Issue: parent.Issue.Clone(),
	}
	if err := h.store.CreateTask(t.Context(), lane, nil); err != nil {
		t.Fatalf("CreateTask lane: %v", err)
	}

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks?issue_id=%d", iss.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", resp.StatusCode, body)
	}
	var rows []listTaskResponse
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != root.ID {
		t.Fatalf("?issue_id= listed %d row(s), want only root task %d", len(rows), root.ID)
	}
	if rows[0].Issue == nil || rows[0].Issue.ID != iss.ID {
		t.Errorf("list row issue = %+v", rows[0].Issue)
	}
	if got := h.preview(t, iss.ID, vincentIssueWorkflow); got.Tasks.Count != 1 || got.TaskCount != 1 {
		t.Errorf("issue tasks.count = %d, task_count = %d, want 1 root", got.Tasks.Count, got.TaskCount)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/tasks?issue_id=x", nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
}

func TestIssuePreviewNeedsAKnownWorkflow(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.localIssue(t, h.projectID)
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), `"prefill"`) {
		t.Errorf("no workflow = %d %s, want no prefill", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d?workflow=nope", iss.ID), nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
}

// TestTaskCreateDigestWithoutIssueIDIsUnchanged pins task 040's digest of a
// body that does not name issue_id to the value recorded before the field
// existed: `omitempty` is what keeps a key an older daemon recorded
// replaying — and, since task 130.11 removed `github_issue` from the request,
// taskCreateDigest's retained nil slot for it.
func TestTaskCreateDigestWithoutIssueIDIsUnchanged(t *testing.T) {
	wf := "fix"
	req := &taskCreateRequest{
		ProjectID: 1, Title: "t", Workflow: &wf, Fields: map[string]string{"a": "b"},
	}
	got, err := idempotencyDigest(req.digestShape())
	if err != nil {
		t.Fatal(err)
	}
	if want := "104b43080e3b95ed62d004709c466cbfd3b9a99ee4e989b2f0afb79dd874aacc"; got != want {
		t.Errorf("digest = %s, want the recorded %s", got, want)
	}
}

// TestTaskCreateDigestShapeMatchesTheRequest holds taskCreateDigest to
// taskCreateRequest: the same JSON names in the same order, with exactly the
// removed `github_issue` slot added before `github_pull`. A field added to
// the request and not to the digest would let two different creates share a
// key; one added out of order would move every recorded digest.
func TestTaskCreateDigestShapeMatchesTheRequest(t *testing.T) {
	names := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			out = append(out, rt.Field(i).Tag.Get("json"))
		}
		return out
	}
	want := []string{}
	for _, n := range names(taskCreateRequest{}) {
		if n == "github_pull" {
			want = append(want, "github_issue")
		}
		want = append(want, n)
	}
	if got := names(taskCreateDigest{}); !reflect.DeepEqual(got, want) {
		t.Errorf("digest fields = %v\nwant %v", got, want)
	}
	// And every value is carried across.
	wf, pull, id, yes, cost := "w", 3, int64(4), true, 1.5
	req := taskCreateRequest{
		ProjectID: 1, Workflow: &wf, Title: "t", Description: &wf, Fields: map[string]string{"a": "b"},
		BaseBranch: &wf, BranchName: &wf, ExistingBranch: &yes, Priority: &pull, Agent: &wf, Model: &wf,
		Effort: &wf, GitHubPull: &pull, IssueID: &id, Paused: &yes, Restricted: &yes, MaxTaskCostUSD: &cost,
		MergeBack: &mergeBackBody{OnConflict: "agent"},
	}
	shape := reflect.ValueOf(*req.digestShape())
	src := reflect.ValueOf(req)
	for i := range src.NumField() {
		name := src.Type().Field(i).Name
		if !reflect.DeepEqual(src.Field(i).Interface(), shape.FieldByName(name).Interface()) {
			t.Errorf("digestShape drops %s", name)
		}
	}
	if shape.FieldByName("GitHubIssue").Interface() != (*int)(nil) {
		t.Error("the retained github_issue slot is not nil")
	}
}

// TestIssueCreateDigestIsTakenBeforeThePrefill: retitling the issue between
// two identical sends cannot turn the second into a reused-key 409.
func TestIssueCreateDigestIsTakenBeforeThePrefill(t *testing.T) {
	h := newVincentIssueHarness(t)
	iss := h.localIssue(t, h.projectID)
	body := map[string]any{"project_id": h.projectID, "workflow": vincentIssueWorkflow, "issue_id": iss.ID}
	post := func() (int, taskResponse, []byte) {
		resp, out := h.postTaskKey(t, body, "k-130.7")
		var tr taskResponse
		_ = json.Unmarshal(out, &tr)
		return resp.StatusCode, tr, out
	}
	code, first, out := post()
	if code != http.StatusCreated {
		t.Fatalf("first = %d: %s", code, out)
	}
	title := "Retitled"
	if _, err := h.store.UpdateIssue(t.Context(), iss.ID, iss.Version, store.IssuePatch{Title: &title}, issuestate.Human); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	code, replay, out := post()
	if code != http.StatusCreated || replay.ID != first.ID {
		t.Errorf("replay = %d id %d (%s), want 201 with task %d", code, replay.ID, out, first.ID)
	}
}

func TestCompareURLClosesTheImportedIssue(t *testing.T) {
	s := &Server{}
	repo := github.Repo{Owner: "octo", Name: "repo"}
	base := store.Task{Title: "t", BaseBranch: "main", BranchName: "b"}
	imported := func(r string) *store.IssueSnapshot {
		return &store.IssueSnapshot{ID: 1, Title: "x", State: "open", Remote: &store.IssueSnapshotRemote{
			Provider: "github", Repo: r, Number: 200,
		}}
	}
	for _, c := range []struct {
		name   string
		mutate func(*store.Task)
		closes bool
	}{
		{"imported same-repo", func(t *store.Task) { t.Issue = imported("octo/repo") }, true},
		{"imported cross-repo", func(t *store.Task) { t.Issue = imported("other/repo") }, false},
		{"local", func(t *store.Task) { t.Issue = &store.IssueSnapshot{ID: 1, Title: "x", State: "open"} }, false},
		{"legacy", func(t *store.Task) { t.GitHubIssue = &github.Issue{Repo: "octo/repo", Number: 200} }, true},
		{"none", func(*store.Task) {}, false},
	} {
		task := base
		c.mutate(&task)
		got := s.compareURLFor(repo, &task)
		if has := strings.Contains(got, "Closes+%23200"); has != c.closes {
			t.Errorf("%s: compare URL %q, closes = %v, want %v", c.name, got, has, c.closes)
		}
	}
}

// TestTaskGitHubIssueKeepsLegacyRows: a legacy row's github_issue is its
// own snapshot, untouched by the derivation.
func TestTaskGitHubIssueKeepsLegacyRows(t *testing.T) {
	legacy := &github.Issue{Repo: "octo/repo", Number: 7, Title: "old"}
	if got := taskGitHubIssue(&store.Task{GitHubIssue: legacy}); !reflect.DeepEqual(got, legacy) {
		t.Errorf("legacy github_issue = %+v", got)
	}
}
