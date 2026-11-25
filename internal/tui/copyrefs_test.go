package tui

import (
	"strings"
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
	panelKeyProbes[ctxPullRequests]["ctrl+y"] = func(t *testing.T) {
		v := pullRequestsFixture(testPull(41, "Add checks"))
		_, cmd := v.update(registryKey(t, "ctrl+y"))
		if msg, ok := drain(cmd).(openCopyPickerMsg); !ok || len(msg.items) == 0 || msg.items[0].label != "url" {
			t.Fatalf("ctrl+y on the pull requests board produced %#v", drain(cmd))
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
	items := taskCopyItems(task, nil)
	for label, text := range map[string]string{
		"id": "4", "branch": "feat/4-x", "worktree path": wt, "pull request": "octo/api#412",
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

// #1156: the pull request's URL is the daemon's record of it, never one
// built as github.com/… — which is wrong on GitHub Enterprise — and a pull
// request a person unlinked offers nothing.
func TestCopyRowsPullRequestURLFromRecord(t *testing.T) {
	link := &apiclient.GitHubPullLink{Repo: "octo/api", Number: 412}
	task := apiclient.Task{ID: 4, BranchName: "feat/4-x", GitHubPull: link}
	ghe := &apiclient.GitHubPullRequest{Repo: "octo/api", Number: 412, URL: "https://ghe.example.com/octo/api/pull/412"}

	items := taskCopyItems(task, ghe)
	if !hasCopy(items, "pull request URL", ghe.URL) {
		t.Errorf("copy rows miss the record's URL: %+v", items)
	}
	for _, it := range items {
		if strings.Contains(it.text, "https://github.com/") {
			t.Errorf("copy row %q builds a github.com URL: %q", it.label, it.text)
		}
	}

	// Without a record (the board) there is no URL to offer, only the
	// reference the link names.
	for _, it := range taskCopyItems(task, nil) {
		if it.label == "pull request URL" {
			t.Errorf("a task without a fetched record offers a URL: %+v", it)
		}
	}

	// Unlinked by a person: the link row stays, suppressed, and is not a
	// pull request of this task's any more.
	unlinked := task
	unlinked.GitHubPull = &apiclient.GitHubPullLink{Repo: "octo/api", Number: 412, Suppressed: true}
	for _, it := range taskCopyItems(unlinked, ghe) {
		if strings.HasPrefix(it.label, "pull request") {
			t.Errorf("an unlinked pull request offers %q %q", it.label, it.text)
		}
	}
}

// #1156: the workspace copies the URL its fetched pull request carries, on
// every tab, the Output tab's picker included.
func TestWorkspaceCopiesFetchedPullURL(t *testing.T) {
	v := cardView(t, apiclient.TaskDetail{Task: apiclient.Task{
		ID: 4, State: stateDone, BranchName: "feat/4-x", StepTotal: 1,
		GitHubPull: &apiclient.GitHubPullLink{Repo: "octo/api", Number: 41},
	}})
	pull := linkedPull()
	pull.Pull.URL = "https://ghe.example.com/octo/api/pull/41"
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: pull})
	for _, tab := range []taskViewTab{taskTabOverview, taskTabOutput} {
		v.tab = tab
		_, cmd := v.update(registryKey(t, "ctrl+y"))
		msg, ok := drain(cmd).(openCopyPickerMsg)
		if !ok || !hasCopy(msg.items, "pull request URL", pull.Pull.URL) {
			t.Errorf("tab %v: ctrl+y offers %#v, want the record's URL", tab, drain(cmd))
		}
	}
}

// #1156: the pull requests board copies the selected pull request's URL,
// reference and head branch, as every other board copies its row's.
func TestPullRequestsBoardCopies(t *testing.T) {
	v := pullRequestsFixture(testPull(41, "Add checks", func(p *apiclient.GitHubPullRequest) {
		p.URL = "https://ghe.example.com/octo/api/pull/41"
	}))
	_, cmd := v.update(registryKey(t, "ctrl+y"))
	msg, ok := drain(cmd).(openCopyPickerMsg)
	if !ok {
		t.Fatalf("ctrl+y on the pull requests board produced %#v", drain(cmd))
	}
	for label, text := range map[string]string{
		"url": "https://ghe.example.com/octo/api/pull/41", "reference": "octo/api#41",
		"branch": "vincent/41-branch", "title": "Add checks",
	} {
		if !hasCopy(msg.items, label, text) {
			t.Errorf("pull request copy rows miss %s %q: %+v", label, text, msg.items)
		}
	}
}

// #1156: copy is hinted wherever it is a key — the footer names it, at a
// priority after the surface's own actions, so a narrow footer drops it first.
func TestCopyKeyIsHinted(t *testing.T) {
	for _, b := range bindings {
		if b.key == copyPickKey && !b.noPalette && b.hint == "" {
			t.Errorf("%s: ctrl+y has no footer hint", b.context)
		}
	}
}
