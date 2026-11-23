package tui

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1075: the task filter matches what the card prints.
func TestTaskFilterMatchesWhatTheCardPrints(t *testing.T) {
	tasks := []apiclient.Task{
		{
			ID: 1, Title: "one", Workflow: "verify-build", StepName: "soak", BranchName: "vincent/1-one",
			GitHubPull: &apiclient.GitHubPullLink{Number: 412},
		},
		{ID: 2, Title: "two", Workflow: "docs-refresh"},
	}
	for _, q := range []string{"verify", "soak", "vincent/1", "#412", "pr 412"} {
		if got := filterTasks(tasks, q); len(got) != 1 || got[0].ID != 1 {
			t.Errorf("filter %q matched %v", q, got)
		}
	}
}

// #1075: the issue filter matches the source reference, author and
// priority, and `#N` is the id alone.
func TestIssueFilterMatchesSourceAuthorPriority(t *testing.T) {
	jira := apiclient.Issue{
		ID: 18, Title: "Push twice", Author: "Tomas Berg", Priority: 1,
		Source: &apiclient.IssueSource{Ref: "MOB-7", ShortRef: "MOB-7", Key: "MOB-7"},
	}
	gh := apiclient.Issue{ID: 5, Title: "Dark mode", Source: &apiclient.IssueSource{Ref: "octo/web#41", ShortRef: "web#41"}}
	for q, want := range map[string]*apiclient.Issue{"mob-7": &jira, "tomas": &jira, "urgent": &jira, "web#41": &gh} {
		if !issueMatches(*want, q) {
			t.Errorf("%q does not match #%d", q, want.ID)
		}
	}
	if issueMatches(gh, "#4") {
		t.Error("#4 matched web#41")
	}
}

// #1075: z cycles each board's order.
func TestSortOrders(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	tasks := []apiclient.Task{
		{ID: 1, Title: "b", State: stateRunning, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: 2, Title: "a", State: stateRunning, CreatedAt: now},
	}
	sortBoardTasks(tasks, "newest")
	if tasks[0].ID != 2 {
		t.Errorf("newest first put #%d first", tasks[0].ID)
	}
	sortBoardTasks(tasks, "title")
	if tasks[0].Title != "a" {
		t.Errorf("by title put %q first", tasks[0].Title)
	}
	issues := []apiclient.Issue{{ID: 1, Priority: 0}, {ID: 2, Priority: 3}, {ID: 3, Priority: 1}}
	sortIssues(issues, "priority")
	if issues[0].ID != 3 || issues[2].ID != 1 {
		t.Errorf("by priority = %v", []int64{issues[0].ID, issues[1].ID, issues[2].ID})
	}
	if nextSort(boardSorts, "title") != "" {
		t.Error("the order does not wrap back to the board's own")
	}
}
