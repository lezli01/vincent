package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github"
)

// The `github_issue_json` column (migration 0014, task 035 decision 3). It is
// a snapshot: written once at creation, read back verbatim, and never
// refreshed — which is what makes `.Issue` renderable offline and a run
// reproducible.

func linkedIssue() *github.Issue {
	return &github.Issue{
		Repo:            "lezli01/vincent",
		Number:          200,
		Title:           "Select a GitHub issue when creating a task",
		Body:            "### Problem\n\nSomething is wrong.",
		URL:             "https://github.com/lezli01/vincent/issues/200",
		State:           github.StateOpen,
		Labels:          []string{"enhancement", "area/api"},
		Author:          "lezli01",
		Assignee:        "hubot",
		Milestone:       "v0.2.0",
		MilestoneNumber: 4,
		CreatedAt:       time.Date(2026, 8, 26, 19, 21, 29, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 8, 26, 19, 30, 0, 0, time.UTC),
		FetchedAt:       time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC),
	}
}

func TestTaskGitHubIssueRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "linked")

	in := newTask(p.ID, "from-an-issue", TaskQueued)
	in.GitHubIssue = linkedIssue()
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	out, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.GitHubIssue == nil {
		t.Fatal("the stored task carries no issue")
	}
	if !reflect.DeepEqual(*out.GitHubIssue, *linkedIssue()) {
		t.Errorf("issue round-tripped as\n %+v\nwant\n %+v", *out.GitHubIssue, *linkedIssue())
	}
	// Labels survive as a list, which is the whole reason the column is JSON
	// rather than a joined string (decision 3).
	if len(out.GitHubIssue.Labels) != 2 {
		t.Errorf("labels = %v, want both entries as a list", out.GitHubIssue.Labels)
	}
}

// TestTaskWithoutAnIssueStoresNull: NULL means "no linked issue", and it is
// what every task created before this feature and every task created without
// naming an issue carries.
func TestTaskWithoutAnIssueStoresNull(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "unlinked")

	in := newTask(p.ID, "plain", TaskQueued)
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	out, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.GitHubIssue != nil {
		t.Errorf("an unlinked task read back with an issue: %+v", out.GitHubIssue)
	}
	var raw any
	if err := s.db.QueryRowContext(ctx,
		`SELECT github_issue_json FROM tasks WHERE id = ?`, in.ID).Scan(&raw); err != nil {
		t.Fatalf("read column: %v", err)
	}
	if raw != nil {
		t.Errorf("github_issue_json = %v, want SQL NULL", raw)
	}
}

// TestTaskIssueSurvivesATransition: the snapshot is not part of any
// transition's write set, so moving a task through the §6 lifecycle must not
// quietly drop it.
func TestTaskIssueSurvivesATransition(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "transitions")

	in := newTask(p.ID, "from-an-issue", TaskQueued)
	in.GitHubIssue = linkedIssue()
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	out, _, err := s.TransitionTask(ctx, in.ID, TaskQueued, TaskRunning, TaskChange{})
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if out.GitHubIssue == nil || out.GitHubIssue.Number != 200 {
		t.Errorf("the issue did not survive a transition: %+v", out.GitHubIssue)
	}
}
