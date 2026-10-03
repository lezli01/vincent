package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// Task 130.4: the issue prefill from a vincent issue.

func parseTestWorkflow(t *testing.T, src string) *workflow.Workflow {
	t.Helper()
	wf, verrs, err := workflow.Parse([]byte(src), workflow.Options{})
	if err != nil || len(verrs) > 0 {
		t.Fatalf("parse: %v %v", err, verrs)
	}
	return wf
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
