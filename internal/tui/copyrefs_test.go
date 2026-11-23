package tui

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

func init() {
	panelKeyProbes[ctxTasks]["ctrl+y"] = func(t *testing.T) {
		b := taskBoardFixture()
		_, cmd := b.update(registryKey(t, "ctrl+y"))
		if msg, ok := drain(cmd).(openCopyPickerMsg); !ok || len(msg.items) == 0 || msg.items[0].label != "id" {
			t.Fatalf("ctrl+y on the board produced %#v", drain(cmd))
		}
	}
	panelKeyProbes[ctxTaskOverview]["ctrl+y"] = func(t *testing.T) {
		v := cardView(t, apiclient.TaskDetail{Task: apiclient.Task{ID: 4, State: stateDone, BranchName: "feat/4-x", StepTotal: 1}})
		_, cmd := v.update(registryKey(t, "ctrl+y"))
		if msg, ok := drain(cmd).(openCopyPickerMsg); !ok || !hasCopy(msg.items, "branch", "feat/4-x") {
			t.Fatalf("ctrl+y on the Overview produced %#v", drain(cmd))
		}
	}
	panelKeyProbes[ctxIssues]["ctrl+y"] = func(t *testing.T) {
		v := issuesFixture()
		_, cmd := v.update(registryKey(t, "ctrl+y"))
		if msg, ok := drain(cmd).(openCopyPickerMsg); !ok || len(msg.items) == 0 || msg.items[0].label != "id" {
			t.Fatalf("ctrl+y on the issues board produced %#v", drain(cmd))
		}
	}
}

func hasCopy(items []copyItem, label, text string) bool {
	for _, it := range items {
		if it.label == label && it.text == text {
			return true
		}
	}
	return false
}

// #1076: a task offers its id, branch, worktree path and pull request; an
// issue its id, title, reference and page.
func TestCopyRowsForTasksAndIssues(t *testing.T) {
	wt := "/data/worktrees/task-4"
	task := apiclient.Task{
		ID: 4, BranchName: "feat/4-x", WorktreePath: &wt,
		GitHubPull: &apiclient.GitHubPullLink{Repo: "octo/api", Number: 412},
	}
	items := taskCopyItems(task)
	for label, text := range map[string]string{
		"id": "4", "branch": "feat/4-x", "worktree path": wt, "pull request": "https://github.com/octo/api/pull/412",
	} {
		if !hasCopy(items, label, text) {
			t.Errorf("task copy rows miss %s %q: %+v", label, text, items)
		}
	}
	iss := apiclient.Issue{
		ID: 18, Title: "Push twice",
		Source: &apiclient.IssueSource{Ref: "MOB-7", URL: "https://example.atlassian.net/browse/MOB-7"},
	}
	items = issueCopyItems(iss)
	for label, text := range map[string]string{
		"id": "18", "title": "Push twice", "reference": "MOB-7", "url": "https://example.atlassian.net/browse/MOB-7",
	} {
		if !hasCopy(items, label, text) {
			t.Errorf("issue copy rows miss %s %q: %+v", label, text, items)
		}
	}
}
