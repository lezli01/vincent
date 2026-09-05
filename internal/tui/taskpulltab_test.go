package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
)

// linkedPull is a task that has a pull request, which is the condition the
// Pull Request tab exists under.
func linkedPull() apiclient.GitHubTaskPull {
	return apiclient.GitHubTaskPull{
		Linked: true, Repo: "octo/api", Number: 41, Source: "auto",
		Pull: &apiclient.GitHubPullRequest{
			Repo: "octo/api", Number: 41, Title: "Add checks",
			URL: "https://github.com/octo/api/pull/41", State: "open",
			HeadBranch: "vincent/4-add-checks", BaseBranch: "main",
			HeadSHA: "0b1d2e3f4a5b6c7d8e9f", Author: "octocat",
		},
	}
}

func checkRollupFixture() apiclient.GitHubTaskChecks {
	return apiclient.GitHubTaskChecks{
		Linked: true, Repo: "octo/api", Number: 41,
		Ref: "0b1d2e3f4a5b6c7d8e9f", State: "in_progress",
		Runs: []apiclient.GitHubCheckRun{
			{Name: "build", State: "in_progress", URL: "https://github.com/octo/api/actions/runs/77/job/9", RunID: 77},
			{Name: "lint", State: "success", URL: "https://github.com/octo/api/actions/runs/77/job/10", RunID: 77},
			{Name: "netlify/deploy", State: "success", URL: "https://netlify.example/d/1"},
		},
	}
}

// pullTabFixture is a workspace standing on the Pull Request tab with a
// rollup already applied.
func pullTabFixture(t *testing.T) *taskView {
	t.Helper()
	v := tabbedTaskFixture(t, taskTabPull)
	// A client that is never dialled: the probes assert that a key produces a
	// command, and no test in this package reaches the network.
	v.detail.client = apiclient.New("http://127.0.0.1:1", "test")
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	v.applyChecks(taskChecksMsg{taskID: v.detail.taskID, checks: checkRollupFixture()})
	v.width, v.height = 120, 40
	return v
}

// The tab exists only for a linked pull request, and `6` does nothing without
// one — the acceptance criterion the conditional tab is built around.
func TestPullTabPresenceFollowsTheLink(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabDetails)
	if v.pullTabAvailable() {
		t.Fatal("the tab is on offer for a task with no pull request")
	}
	if cmd := v.updateKey(registryKey(t, "7")); cmd != nil || v.tab != taskTabDetails {
		t.Fatalf("7 moved to %v with nothing linked, want no move", v.tab)
	}
	// The unconditional neighbour is what makes that absence free: 6 is Step
	// Details whether or not there is a pull request (issue #323).
	if v.updateKey(registryKey(t, "6")); v.tab != taskTabStepDetails {
		t.Fatalf("6 moved to %v with nothing linked, want Step Details", v.tab)
	}
	v.tab = taskTabDetails
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	if !v.pullTabAvailable() {
		t.Fatal("the tab is absent for a task with a linked pull request")
	}
	v.updateKey(registryKey(t, "7"))
	if v.tab != taskTabPull {
		t.Fatalf("7 moved to %v, want the Pull Request tab", v.tab)
	}
	v.updateKey(registryKey(t, "6"))
	if v.tab != taskTabStepDetails {
		t.Fatalf("6 moved to %v with a pull request linked, want Step Details", v.tab)
	}
}

// `github.enabled: false` hides the tab as it hides the rest of the
// integration, even for a task whose stored link is still there.
func TestPullTabHiddenWhenGitHubDisabled(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabDetails)
	pull := linkedPull()
	pull.Pull, pull.Reason = nil, github.ReasonDisabled
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: pull})
	if v.pullTabAvailable() {
		t.Fatal("the tab is on offer while the integration is disabled")
	}
	if got := v.tabs(); len(got) != 6 {
		t.Fatalf("the strip carries %d tabs, want 6", len(got))
	}
}

// The cycle is the part the modulo arithmetic gets wrong first: tab/⇧tab must
// walk the strip as it stands, not the enum.
func TestPullTabCycleSkipsAnAbsentTab(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabStepDetails)
	v.updateKey(registryKey(t, "tab"))
	if v.tab != taskTabSteps {
		t.Fatalf("tab from Step Details with no pull request landed on %v, want Steps", v.tab)
	}
	v = tabbedTaskFixture(t, taskTabStepDetails)
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	v.updateKey(registryKey(t, "tab"))
	if v.tab != taskTabPull {
		t.Fatalf("tab from Step Details with a pull request landed on %v, want Pull Request", v.tab)
	}
	v.updateKey(registryKey(t, "tab"))
	if v.tab != taskTabSteps {
		t.Fatalf("tab wrapped to %v, want Steps", v.tab)
	}
	v.updateKey(registryKey(t, "["))
	if v.tab != taskTabPull {
		t.Fatalf("[ wrapped to %v, want Pull Request", v.tab)
	}
}

// Every check on the head commit is a row carrying its state.
func TestPullTabRendersEveryCheck(t *testing.T) {
	out := ansi.Strip(pullTabFixture(t).renderPullTab(120, 30))
	for _, want := range []string{
		"octo/api#41", "Add checks", "vincent/4-add-checks",
		"build", "in_progress", "lint", "success", "netlify/deploy",
		"0b1d2e3f4a5b", "actions run 77",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the tab does not render %q:\n%s", want, out)
		}
	}
}

// A running check becomes success or failure without the user re-entering the
// tab: the rollup is replaced by the next fetch, in place.
func TestPullTabChecksUpdateInPlace(t *testing.T) {
	v := pullTabFixture(t)
	done := checkRollupFixture()
	done.Runs[0].State, done.State = "failure", "failure"
	v.applyChecks(taskChecksMsg{taskID: v.detail.taskID, checks: done})
	out := ansi.Strip(v.renderPullTab(120, 30))
	if !strings.Contains(out, "failure") || strings.Contains(out, "in_progress") {
		t.Fatalf("the row did not settle to its conclusion:\n%s", out)
	}
}

// Re-run has no honest meaning off a GitHub Actions run, so the row has to
// carry which one it is — the provenance decision 3 turns on.
func TestPullTabRowsCarryActionsProvenance(t *testing.T) {
	v := pullTabFixture(t)
	runs := v.pullTab.checks.Runs
	if !runs[0].Actions() || runs[0].RunID != 77 {
		t.Fatalf("the Actions row reports run %d, want 77", runs[0].RunID)
	}
	if runs[2].Actions() {
		t.Fatalf("the third-party check reports Actions run %d, want none", runs[2].RunID)
	}
}

// `c` opens the selected check and nothing else; a row with no page of its
// own says so rather than opening the pull request instead.
func TestPullTabOpensTheSelectedCheck(t *testing.T) {
	opened := withFakeOpener(t, nil)
	v := pullTabFixture(t)
	v.updateKey(registryKey(t, "down"))
	cmd := v.updateKey(registryKey(t, "c"))
	if cmd == nil {
		t.Fatal("c opened nothing")
	}
	cmd()
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/octo/api/actions/runs/77/job/10" {
		t.Fatalf("c opened %v, want the selected check's page", *opened)
	}
	v.pullTab.checks.Runs[1].URL = ""
	if cmd := v.updateKey(registryKey(t, "c")); cmd != nil {
		t.Fatal("c opened something for a check with no page")
	}
	if !v.pullTab.noteBad {
		t.Fatal("c said nothing about a check with no page")
	}
}

// Unlinking from the tab takes the tab with it — the one state a conditional
// tab can strand a human on.
func TestPullTabUnlinkLeavesTheTab(t *testing.T) {
	v := pullTabFixture(t)
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: apiclient.GitHubTaskPull{
		Repo: "octo/api", Number: 41, Suppressed: true,
	}})
	if v.tab == taskTabPull {
		t.Fatal("the workspace is still standing on a tab that is no longer on the strip")
	}
	if v.pullTabAvailable() {
		t.Fatal("a suppressed link still offers the tab")
	}
}

// The tab renders when GitHub cannot be reached rather than refusing to: the
// daemon's named reason is what it shows, never a leg's own error text.
func TestPullTabRendersTheDaemonsReason(t *testing.T) {
	v := pullTabFixture(t)
	v.applyChecks(taskChecksMsg{taskID: v.detail.taskID, checks: apiclient.GitHubTaskChecks{
		Linked: true, Repo: "octo/api", Number: 41, Reason: github.ReasonRateLimited,
	}})
	out := ansi.Strip(v.renderPullTab(120, 30))
	if !strings.Contains(out, github.Message(github.ReasonRateLimited)) {
		t.Fatalf("the tab does not render the daemon's reason:\n%s", out)
	}
}

// A tick for a task the human has left, or for a tab they have left, fetches
// nothing.
func TestPullTabTickIsDroppedOffTheTab(t *testing.T) {
	v := pullTabFixture(t)
	v.detail.active = true
	if _, cmd := v.update(taskChecksTickMsg{taskID: v.detail.taskID + 1}); cmd != nil {
		t.Fatal("a tick for another task fetched checks")
	}
	v.tab = taskTabDetails
	if _, cmd := v.update(taskChecksTickMsg{taskID: v.detail.taskID}); cmd != nil {
		t.Fatal("a tick off the tab fetched checks")
	}
}
