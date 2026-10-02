package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// Task 130.4: the issue prefill from a vincent issue, and the legacy
// `github_issue` path's widened field mapping.

// numbersWorkflowYAML declares both numbers a workflow can ask for.
const numbersWorkflowYAML = `name: fix-numbers
description: Fix a reported issue by number.
fields:
  - name: issue
    type: integer
  - name: github_issue
    type: integer
steps:
  - {id: approve, type: manual, instructions: review}
`

func parseTestWorkflow(t *testing.T, src string) *workflow.Workflow {
	t.Helper()
	wf, verrs, err := workflow.Parse([]byte(src), workflow.Options{})
	if err != nil || len(verrs) > 0 {
		t.Fatalf("parse: %v %v", err, verrs)
	}
	return wf
}

// TestLegacyIssuePrefillFillsBothNumbers (task 130.4 decision 1): until
// 130.11 removes `github_issue`, it fills a declared `issue` and a declared
// `github_issue` with the GitHub number, so a repo workflow switched to
// `github_issue` keeps working.
func TestLegacyIssuePrefillFillsBothNumbers(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, "fix-numbers", numbersWorkflowYAML)
	h.reg.ReloadGlobal()

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "workflow": "fix-numbers", "github_issue": 200,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var tr taskResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("task body: %v (%s)", err, body)
	}
	if tr.Fields["issue"] != "200" || tr.Fields["github_issue"] != "200" {
		t.Errorf("fields = %v, want issue and github_issue both 200", tr.Fields)
	}
	// The legacy path writes the legacy snapshot only (decision 1).
	stored, err := h.store.GetTask(t.Context(), tr.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.IssueID != nil || stored.Issue != nil {
		t.Errorf("legacy create wrote issue_id %v / issue_json %+v", stored.IssueID, stored.Issue)
	}
}

// TestLegacyIssueOversizedBodyIs400 (decision 4): an imported body larger
// than §13.1's description bound fails the create, never truncated.
func TestLegacyIssueOversizedBodyIs400(t *testing.T) {
	corpus := filepath.Join(t.TempDir(), "issues.json")
	rows := []map[string]any{{
		"id": 7001, "node_id": "I_1", "number": 1, "title": "huge",
		"body":  strings.Repeat("x", maxDescriptionBytes+1),
		"state": "open", "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
		"url":            "https://api.github.com/repos/octo/repo/issues/1",
		"repository_url": "https://api.github.com/repos/octo/repo",
		"html_url":       "https://github.com/octo/repo/issues/1",
	}}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corpus, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKEGH_ISSUES_FILE", corpus)
	h := newGitHubHarness(t, nil, ghOrigin)

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "github_issue": 1,
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	tasks, err := h.store.ListTasks(t.Context(), store.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("an oversized issue created %d task(s)", len(tasks))
	}
}

const vincentIssueWorkflowYAML = `name: fix-vincent-issue
description: Fix a vincent issue.
fields:
  - name: issue
    type: integer
  - name: github_issue
    type: integer
  - name: labels
  - name: assignee
  - name: milestone
  - name: kind
    pattern: '^(bug|chore)$'
  - name: notes
steps:
  - {id: approve, type: manual, instructions: review}
`

func importedIssueSnapshot() *store.IssueSnapshot {
	return &store.IssueSnapshot{
		ID: 13, Title: "Select an issue", Body: "Body.", State: "open", Kind: "feature",
		Labels: []string{"enhancement"},
		Remote: &store.IssueSnapshotRemote{
			Provider: "github", Repo: "octo/repo", Number: 200, URL: "https://github.com/octo/repo/issues/200",
			Assignees: []string{"hubot"}, Milestone: "v0.2.0", MilestoneNumber: 4,
		},
	}
}

// TestVincentIssuePrefill: the library mapping plus the API's half — a
// candidate the declaration rejects (`kind: feature` against a pattern) is
// skipped, `notes` is never invented, and nothing reaches GitHub.
func TestVincentIssuePrefill(t *testing.T) {
	wf := parseTestWorkflow(t, vincentIssueWorkflowYAML)

	got := vincentIssuePrefill(importedIssueSnapshot(), wf)
	want := githubPrefill{
		Title:       "#200 Select an issue",
		Description: "Body.\n\nGitHub issue #200: https://github.com/octo/repo/issues/200",
		Fields: map[string]string{
			"issue": "13", "github_issue": "200", "labels": "enhancement",
			"assignee": "hubot", "milestone": "v0.2.0",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imported prefill =\n %+v\nwant\n %+v", got, want)
	}

	local := &store.IssueSnapshot{ID: 12, Title: "Lock file leaks", Body: "Seen.", State: "open", Kind: "bug"}
	got = vincentIssuePrefill(local, wf)
	want = githubPrefill{
		Title: "Lock file leaks", Description: "Seen.",
		Fields: map[string]string{"issue": "12", "kind": "bug"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("local prefill =\n %+v\nwant\n %+v", got, want)
	}

	if got := vincentIssuePrefill(local, nil); got.Fields != nil || got.Title != "Lock file leaks" {
		t.Errorf("prefill without a workflow = %+v", got)
	}
}

// TestFoldPrefillExplicitWins (task 035 decision 2): presence wins for
// fields, blank/absent for title and description; an oversized result is a
// bound violation, not a truncation.
func TestFoldPrefillExplicitWins(t *testing.T) {
	wf := parseTestWorkflow(t, vincentIssueWorkflowYAML)
	prefill := vincentIssuePrefill(importedIssueSnapshot(), wf)

	mine := "my description"
	req := &taskCreateRequest{
		Title: "My title", Description: &mine,
		Fields: map[string]string{"labels": "", "assignee": "someone-else"},
	}
	if msg := foldPrefill(req, prefill); msg != "" {
		t.Fatalf("fold: %s", msg)
	}
	if req.Title != "My title" || *req.Description != "my description" {
		t.Errorf("explicit text lost: %q / %q", req.Title, *req.Description)
	}
	wantFields := map[string]string{
		"labels": "", "assignee": "someone-else",
		"issue": "13", "github_issue": "200", "milestone": "v0.2.0",
	}
	if !reflect.DeepEqual(req.Fields, wantFields) {
		t.Errorf("fields = %v, want %v", req.Fields, wantFields)
	}

	empty := &taskCreateRequest{Title: "  "}
	if msg := foldPrefill(empty, prefill); msg != "" || empty.Title != prefill.Title ||
		empty.Description == nil || *empty.Description != prefill.Description {
		t.Errorf("blank request = %+v (%q)", empty, msg)
	}

	huge := importedIssueSnapshot()
	huge.Body = strings.Repeat("x", maxDescriptionBytes+1)
	if msg := foldPrefill(&taskCreateRequest{}, vincentIssuePrefill(huge, wf)); msg == "" {
		t.Error("an oversized body folded without a bound violation")
	}
}
