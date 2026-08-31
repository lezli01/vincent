package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// The pull-requests takeover against the **real** API handlers and a real
// daemon-side GitHub client pointed at cmd/fakegh — which is what keeps the
// client and server wire types from drifting.

// pullsView is the takeover as the root holds it.
func pullsView(t *testing.T, h *newTaskLiveHarness) *pullRequestsView {
	t.Helper()
	v, ok := h.m.views[viewPullRequests].(*pullRequestsView)
	if !ok {
		t.Fatalf("view %d is %T, want *pullRequestsView", viewPullRequests, h.m.views[viewPullRequests])
	}
	return v
}

// A project with a github.com origin and a working credential puts the nav
// row on the palette and fills the view; one without does neither.
func TestPullRequestsListsEveryAvailableProject(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})
	v := pullsView(t, h)
	h.p.until(10*time.Second, "the pull-request listing", func() bool {
		return v.loaded && len(v.rows()) > 0
	})

	out := v.render(160, 30)
	// Open only: the merged row is in fakegh's corpus and must not be here,
	// which is the listing's own contract.
	if !strings.Contains(out, "Add a thing") {
		t.Errorf("the open pull request is not listed:\n%s", out)
	}
	if strings.Contains(out, "already merged") {
		t.Errorf("the merged pull request leaked into an open-only listing:\n%s", out)
	}
	if !strings.Contains(out, "draft") {
		t.Errorf("the draft's folded status word is missing:\n%s", out)
	}
}

// A reconciler tick that links a pull request re-renders an open view with
// no keypress: the root broadcasts to every view, active or not.
func TestPullRequestsRefreshesOnAReconcilerTick(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})
	v := pullsView(t, h)
	h.p.until(10*time.Second, "the pull-request listing", func() bool {
		return v.loaded && len(v.rows()) > 0
	})
	if strings.Contains(v.render(160, 30), "task #") {
		t.Fatal("a row is claimed before anything linked one")
	}

	task := &store.Task{
		ProjectID: h.projectID, Title: "Add a thing", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/1-add-a-thing",
		State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// What the daemon's reconciler writes on a head-branch match. The store
	// publishes task.github_pull_changed, which is the note the view acts on.
	if _, err := h.st.SetTaskGitHubPull(context.Background(), task.ID, &github.PullLink{
		Repo: "octo/repo", Number: 412, Source: "auto", LinkedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}

	h.p.until(10*time.Second, "the linked row to appear with no keypress", func() bool {
		return strings.Contains(v.render(160, 30), "(auto)")
	})
}

// `P` on the takeover: the picker of tasks that could have a pull request and
// do not, against the real handlers (task 069).
//
// This screen has no task rows and is not given any — its question is "what is
// open across everything I run", and a task with no pull request is not an
// open pull request. So the offer is a picker, and choosing a task navigates
// to that task's workspace with the form up: the offer to create still lives
// in the workspace (052 decision 6), widened rather than reversed.
func TestPullRequestsCreatePickerOffersOnlyEligibleTasks(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})
	v := pullsView(t, h)
	h.p.until(10*time.Second, "the pull-request listing", func() bool {
		return v.loaded && len(v.rows()) > 0
	})

	// Eligible: a branch, and no pull request claiming it.
	eligible := &store.Task{
		ProjectID: h.projectID, Title: "Not yet on GitHub", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/7-not-yet", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), eligible, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Not eligible: fakegh's open pull request #412 has this head branch, so
	// the listing claims it.
	claimed := &store.Task{
		ProjectID: h.projectID, Title: "Already has one", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/1-add-a-thing", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), claimed, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.st.SetTaskGitHubPull(context.Background(), claimed.ID, &github.PullLink{
		Repo: "octo/repo", Number: 412, Source: "auto", LinkedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}
	h.p.until(10*time.Second, "the claim to reach the view", func() bool {
		return strings.Contains(v.render(160, 30), "(auto)")
	})

	v.updateKey(keyPress("P"))
	if v.picker == nil {
		t.Fatal("P did not open a picker")
	}
	out := strings.Join(v.picker.renderBody(), "\n")
	if !strings.Contains(out, "Not yet on GitHub") {
		t.Errorf("the eligible task is missing from the picker:\n%s", out)
	}
	if strings.Contains(out, "Already has one") {
		t.Errorf("a task that already has a pull request is offered:\n%s", out)
	}
	// Choosing it hands the intent to the workspace rather than opening a
	// second copy of the form here.
	sel, ok := drain(v.openTaskWithPRForm(eligible.ID)).(selectTaskMsg)
	if !ok || sel.id != eligible.ID || !sel.openPR {
		t.Fatalf("choosing a task produced %#v", sel)
	}
}
