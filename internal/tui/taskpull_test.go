package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// taskPullFixture is the workspace sitting on the Task Details tab with one
// answer from GET /v1/tasks/{id}/github/pull already in.
func taskPullFixture(t *testing.T, pull apiclient.GitHubTaskPull) *taskView {
	t.Helper()
	v := tabbedTaskFixture(t, taskTabDetails)
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: pull})
	return v
}

// The route always answers 200, so all three states render inline and none
// of them is an error.
func TestTaskWorkspacePullSectionRendersEveryState(t *testing.T) {
	linked := taskPullFixture(t, apiclient.GitHubTaskPull{
		Linked: true, Repo: "octo/api", Number: 41, Source: "auto",
		Pull: &apiclient.GitHubPullRequest{
			Repo: "octo/api", Number: 41, Title: "Add a thing", State: "open",
			URL: "https://github.com/octo/api/pull/41", HeadBranch: "vincent/4-add",
		},
	})
	out := strings.Join(linked.pullSectionLines(100), "\n")
	if !strings.Contains(out, "octo/api#41") || !strings.Contains(out, "open") {
		t.Errorf("the linked state does not render its pull request:\n%s", out)
	}

	unusable := taskPullFixture(t, apiclient.GitHubTaskPull{Reason: "not_authenticated"})
	out = strings.Join(unusable.pullSectionLines(100), "\n")
	if !strings.Contains(out, "not_authenticated") {
		t.Errorf("the unusable state does not render its reason:\n%s", out)
	}
	if strings.Contains(out, "could not read") {
		t.Error("an unusable integration rendered as an error")
	}

	offered := taskPullFixture(t, apiclient.GitHubTaskPull{CompareURL: compareURLFixture})
	out = strings.Join(offered.pullSectionLines(100), "\n")
	if !strings.Contains(out, "No pull request is linked") || !strings.Contains(out, "P opens") {
		t.Errorf("the compare-URL offer does not render:\n%s", out)
	}

	// And the section is in the tab's own sidebar order, so it is reachable.
	found := false
	for _, title := range taskDetailSectionOrder {
		if title == "GitHub pull request" {
			found = true
		}
	}
	if !found {
		t.Error("the section is not in taskDetailSectionOrder")
	}
}

// A stored link vincent cannot currently read is still a link: the live half
// is what is missing, and saying which is which is the difference between a
// stale row and a broken one.
func TestTaskWorkspacePullSectionSeparatesTheLinkFromTheLiveState(t *testing.T) {
	v := taskPullFixture(t, apiclient.GitHubTaskPull{
		Linked: true, Repo: "octo/api", Number: 41, Source: "human",
		Reason: "network_unavailable",
	})
	out := strings.Join(v.pullSectionLines(100), "\n")
	if !strings.Contains(out, "octo/api#41") {
		t.Error("the stored link was dropped because its live state could not be read")
	}
	if !strings.Contains(out, "could not read its current state") {
		t.Errorf("nothing says the live state is missing:\n%s", out)
	}
	// The URL is built from the stored link so a merged pull request whose
	// fetch failed still has somewhere to go.
	if got := v.pullWebURL(); got != "https://github.com/octo/api/pull/41" {
		t.Errorf("pullWebURL = %q, want the constructed link", got)
	}
}

// `o` opens the linked pull request; with nothing linked it says so rather
// than opening nothing silently.
func TestTaskWorkspaceOpenPull(t *testing.T) {
	opened := withFakeOpener(t, nil)
	v := taskPullFixture(t, apiclient.GitHubTaskPull{
		Linked: true, Repo: "octo/api", Number: 41,
		Pull: &apiclient.GitHubPullRequest{URL: "https://github.com/octo/api/pull/41"},
	})
	drain(v.openPullCmd())
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/octo/api/pull/41" {
		t.Fatalf("opened %v, want the linked pull request", *opened)
	}

	empty := taskPullFixture(t, apiclient.GitHubTaskPull{})
	if cmd := empty.openPullCmd(); cmd != nil {
		t.Fatal("o opened something with nothing linked")
	}
	if !empty.pullNoteBad || empty.pullNote == "" {
		t.Error("o said nothing with nothing linked")
	}
}

// `P` opens the editor, and only when there is a prefill to edit.
func TestTaskWorkspaceCreatePR(t *testing.T) {
	v := taskPullFixture(t, apiclient.GitHubTaskPull{CompareURL: compareURLFixture})
	v.openCreatePR()
	if v.createPR == nil {
		t.Fatal("P did not open the compare-URL editor")
	}
	if !v.popup {
		t.Error("the editor does not own the keyboard")
	}
	if v.bindingContext() != ctxCreatePR {
		t.Errorf("binding context = %q, want %q", v.bindingContext(), ctxCreatePR)
	}

	linked := taskPullFixture(t, apiclient.GitHubTaskPull{Linked: true, Repo: "octo/api", Number: 41})
	linked.openCreatePR()
	if linked.createPR != nil {
		t.Fatal("P offered to create a second pull request for a linked task")
	}
	if !strings.Contains(linked.pullNote, "already has a pull request") {
		t.Errorf("note = %q, want the reason", linked.pullNote)
	}

	unusable := taskPullFixture(t, apiclient.GitHubTaskPull{Reason: "not_authenticated"})
	unusable.openCreatePR()
	if unusable.createPR != nil {
		t.Fatal("P opened an editor with no compare URL")
	}
	if !strings.Contains(unusable.pullNote, "not_authenticated") {
		t.Errorf("note = %q, want the daemon's reason", unusable.pullNote)
	}
}

// A reconciler tick re-reads the section without a keypress.
func TestTaskWorkspaceRefetchesOnLinkChanged(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabDetails)
	v.detail.client = offlineClient()
	_, cmd := v.update(noteMsg{note: apiclient.EventNote{
		Event: apiclient.Event{Type: eventTaskGitHubPullChanged},
	}})
	if cmd == nil {
		t.Fatal("task.github_pull_changed did not re-read the task's pull request")
	}
}
