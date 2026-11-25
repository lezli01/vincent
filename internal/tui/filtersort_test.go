package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

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

// #1155: the task filter matches the agents the task's attempts ran on and
// its agent override. No card prints them, but which agent worked on a task
// is what someone looking for "the codex one" types.
func TestTaskFilterMatchesAgents(t *testing.T) {
	codex := "codex"
	tasks := []apiclient.Task{
		{ID: 1, Title: "one", Agents: []string{"claude", "cursor"}},
		{ID: 2, Title: "two", AgentOverride: &codex},
		{ID: 3, Title: "three"},
	}
	for q, want := range map[string]int64{"cursor": 1, "claude": 1, "codex": 2} {
		if got := filterTasks(tasks, q); len(got) != 1 || got[0].ID != want {
			t.Errorf("filter %q matched %v, want only #%d", q, got, want)
		}
	}
}

// #1155: the task board's time orders and the card's age read the same
// time. The card prints how long ago the task last changed, so the order is
// by that time and is named for it — the rule the issues board follows.
func TestTaskSortMatchesCardAge(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	tasks := []apiclient.Task{
		{ID: 1, State: stateRunning, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: 2, State: stateRunning, CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now.Add(-time.Minute)},
	}
	if want := []string{"", "updated", "updated_oldest", "title"}; !slices.Equal(boardSorts, want) {
		t.Fatalf("task orders = %v, want %v", boardSorts, want)
	}
	sortBoardTasks(tasks, "updated")
	if tasks[0].ID != 2 {
		t.Errorf("recently updated first put #%d first", tasks[0].ID)
	}
	sortBoardTasks(tasks, "updated_oldest")
	if tasks[0].ID != 1 {
		t.Errorf("least recently updated first put #%d first", tasks[0].ID)
	}
	if got := boardSortLabel("updated_oldest"); got != "least recently updated first" {
		t.Errorf("label = %q", got)
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
		{ID: 1, Title: "b", State: stateRunning, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: 2, Title: "a", State: stateRunning, UpdatedAt: now},
	}
	sortBoardTasks(tasks, "updated")
	if tasks[0].ID != 2 {
		t.Errorf("recently updated first put #%d first", tasks[0].ID)
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

// #1155: the active sort is named while it is in force — on the Tasks
// title beside the filter, on the card board's edge and on the issues
// board's edge — and never pushes out what matters more: the title drops it
// whole before it cuts the filter.
func TestActiveSortIsNamed(t *testing.T) {
	s, _ := newShellFixture(t, task(1, stateRunning), task(2, stateDone))
	if got := s.panelTitle(panelTasks); got != "Tasks" {
		t.Errorf("unsorted title = %q, want %q", got, "Tasks")
	}
	s.board.sortOrder = "updated"
	s.board.filter.SetValue("x")
	if got := s.panelTitle(panelTasks); got != "Tasks — /x — recently updated first" {
		t.Errorf("title = %q, want the filter then the sort", got)
	}
	if got := s.tasksTitle(len("Tasks — /x") + 3); got != "Tasks — /x" {
		t.Errorf("tight title = %q, want the sort dropped whole", got)
	}
	if got := ansi.Strip(s.board.cardSummary()); !strings.Contains(got, "recently updated first") {
		t.Errorf("card edge %q does not name the sort", got)
	}

	v := handoffBoardFixture()
	if got := ansi.Strip(v.boardSummary()); strings.Contains(got, "first") || strings.Contains(got, "by ") {
		t.Errorf("unsorted issues edge %q names a sort", got)
	}
	v.sortOrder = "priority"
	if got := ansi.Strip(v.boardSummary()); !strings.Contains(got, "by priority") {
		t.Errorf("issues edge %q does not name the sort", got)
	}
	v.lastLoad = time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	full := ansi.Strip(v.boardSummary())
	if got, _ := v.boardSummaryFit(len(full) - 1); !strings.Contains(ansi.Strip(got), "by priority") {
		t.Errorf("at %d cells the edge %q lost the sort before the time", len(full)-1, ansi.Strip(got))
	}
}

// #1155: the issues board's time order and the card's age read the same
// time. The card prints how long ago the issue last changed, so the order
// is by that time, most recent first, and is named for it.
func TestIssueSortMatchesCardAge(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	issues := []apiclient.Issue{
		{ID: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: 2, CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now.Add(-time.Minute)},
	}
	if issueSorts[1] != "updated" {
		t.Fatalf("issue orders = %v, want the time order named for the update", issueSorts)
	}
	sortIssues(issues, "updated")
	if issues[0].ID != 2 {
		t.Errorf("recently updated first put #%d first", issues[0].ID)
	}
}
