package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// testPull is one listing row.
func testPull(number int, title string, opts ...func(*apiclient.GitHubPullRequest)) apiclient.GitHubPullRequest {
	p := apiclient.GitHubPullRequest{
		Repo: "octo/api", Number: number, Title: title, State: "open",
		URL:        "https://github.com/octo/api/pull/" + strconv.Itoa(number),
		HeadBranch: "vincent/" + strconv.Itoa(number) + "-branch",
	}
	for _, opt := range opts {
		opt(&p)
	}
	return p
}

func claimedBy(id int64, source string) func(*apiclient.GitHubPullRequest) {
	return func(p *apiclient.GitHubPullRequest) { p.TaskID, p.LinkSource = &id, source }
}

// pullRequestsFixture is the takeover with one available project and its
// listing already in.
func pullRequestsFixture(pulls ...apiclient.GitHubPullRequest) *pullRequestsView {
	project := testProject(1, "api")
	v := newPullRequestsView()
	v.client = offlineClient()
	v.available = []githubProject{{
		project: project,
		status:  apiclient.GitHubStatus{Enabled: true, Available: true, Repo: "octo/api"},
	}}
	v.groups = []pullGroup{{project: project, pulls: pulls}}
	v.tasks = []apiclient.Task{
		{ID: 7, ProjectID: 1, Title: "add a thing", State: stateRunning, BranchName: "vincent/7-add"},
		{ID: 8, ProjectID: 1, Title: "another thing", State: stateQueued, BranchName: "vincent/8-another"},
		{ID: 9, ProjectID: 2, Title: "somewhere else", State: stateQueued},
	}
	v.loaded = true
	return v
}

// withFakeOpener swaps the platform hand-off for a recorder, so the tests
// assert what would have been opened without launching a browser.
func withFakeOpener(t *testing.T, err error) *[]string {
	t.Helper()
	opened := &[]string{}
	prev := openURL
	openURL = func(_ context.Context, url string) error {
		*opened = append(*opened, url)
		return err
	}
	t.Cleanup(func() { openURL = prev })
	return opened
}

// drain runs a tea.Cmd to its message, in-process: every command these
// tests exercise is either pure or points at offlineClient.
func drain(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// The nav row is withheld while no project answers the §13.2 probe — from
// the palette, the ? overlay and the footer alike. A screen that exists only
// to explain that it has nothing to show is not a view.
func TestPullRequestsNavRowIsWithheldWithoutAGitHubProject(t *testing.T) {
	for _, e := range paletteEntries(ctxTasks, taskActions{}, false, true, false) {
		if e.navTarget == viewPullRequests && e.nav {
			t.Fatal("the palette offers the pull-requests view with no GitHub project")
		}
	}
	if strings.Contains(helpText(ctxTasks, false), "pull requests —") {
		t.Error("the ? overlay names the pull-requests view with no GitHub project")
	}
	if got := len(bindingsFor(ctxTaskDetails)) - len(withoutGitHub(bindingsFor(ctxTaskDetails), false)); got != 2 {
		t.Errorf("withoutGitHub dropped %d task-details rows, want the 2 pull-request keys", got)
	}
	// And with one, it is back.
	found := false
	for _, e := range paletteEntries(ctxTasks, taskActions{}, false, true, true) {
		if e.nav && e.navTarget == viewPullRequests {
			found = true
		}
	}
	if !found {
		t.Error("the palette hides the pull-requests view even with a GitHub project")
	}
}

// Probes that have not answered yet are not "available": the row must not
// flicker into existence and out again while the fan-out lands.
func TestPullRequestsProbeInFlightIsNotAvailable(t *testing.T) {
	m := &root{views: newViews(t.Context())}
	if m.githubAvailable() {
		t.Fatal("the nav row is offered before any probe answered")
	}
	m.Update(githubProbeMsg{projects: []githubProject{
		{project: testProject(1, "api"), status: apiclient.GitHubStatus{Available: false, Reason: "no_remote"}},
	}})
	if m.githubAvailable() {
		t.Fatal("a project that answered no made the nav row appear")
	}
	m.Update(githubProbeMsg{projects: []githubProject{
		{project: testProject(1, "api"), status: apiclient.GitHubStatus{Available: true, Repo: "octo/api"}},
	}})
	if !m.githubAvailable() {
		t.Fatal("a project that answered yes did not make the nav row appear")
	}
}

// A project whose listing failed renders its reason, and the other groups
// still render their rows: each group holds its own error.
func TestPullRequestsFailedGroupDoesNotSinkTheOthers(t *testing.T) {
	v := pullRequestsFixture()
	other := testProject(2, "web")
	v.available = append(v.available, githubProject{
		project: other,
		status:  apiclient.GitHubStatus{Available: true, Repo: "octo/web"},
	})
	v.groups = []pullGroup{
		{project: v.available[0].project, err: "GitHub is not available for this project: not authenticated"},
		{project: other, pulls: []apiclient.GitHubPullRequest{testPull(12, "ship the thing")}},
	}
	out := v.render(120, 24)
	if !strings.Contains(out, "not authenticated") {
		t.Error("the failed group does not carry its reason")
	}
	if !strings.Contains(out, "ship the thing") {
		t.Error("a failed group hid the healthy group's rows")
	}
}

// The listing's 409 arrives as an apiclient.Error; what a human reads is the
// daemon's sentence, not the status code.
func TestPullRequestsGroupErrorUsesTheDaemonsMessage(t *testing.T) {
	err := &apiclient.Error{
		Status: 409, Code: "conflict",
		Message: "GitHub is not available for this project: not authenticated",
		Details: map[string]string{"reason": "not_authenticated"},
	}
	if got := githubReasonMessage(err); got != err.Message {
		t.Errorf("githubReasonMessage = %q, want the daemon's message", got)
	}
	if got := githubReasonMessage(errors.New("boom")); got != "boom" {
		t.Errorf("githubReasonMessage = %q, want the plain error", got)
	}
}

// `o` hands the row's own URL to the opener, and a failing opener produces a
// visible note rather than nothing — the one way this differs from the
// clipboard, which fails silently by design.
func TestPullRequestsOpenReportsAFailingOpener(t *testing.T) {
	opened := withFakeOpener(t, errNoOpener)
	v := pullRequestsFixture(testPull(11, "ship it"))
	msg := drain(v.openSelected())
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/octo/api/pull/11" {
		t.Fatalf("opened %v, want the row's URL", *opened)
	}
	v.applyOpened(msg.(openedURLMsg))
	if !v.noteBad || !strings.Contains(v.note, "could not open") {
		t.Fatalf("a failed open left note %q (bad=%v), want a visible failure", v.note, v.noteBad)
	}
	if !strings.Contains(v.render(120, 24), "could not open") {
		t.Error("the failure is not on screen")
	}
}

// A URL that is not http(s) is refused rather than handed to a shell handler
// that would launch whatever is registered for its scheme.
func TestOpenURLRefusesForeignSchemes(t *testing.T) {
	opened := withFakeOpener(t, nil)
	msg := drain(openURLCmd("file:///etc/passwd")).(openedURLMsg)
	if msg.err == nil {
		t.Fatal("a file:// URL was accepted")
	}
	if len(*opened) != 0 {
		t.Fatalf("the platform opener was called with %v", *opened)
	}
}

// enter routes to the claiming task's workspace; on an unclaimed row it is
// inert rather than overloaded into "link this one".
func TestPullRequestsEnterRoutesOnlyForAClaimedRow(t *testing.T) {
	v := pullRequestsFixture(testPull(11, "claimed", claimedBy(7, "auto")), testPull(12, "unclaimed"))
	msg := drain(v.openTask())
	sel, ok := msg.(selectTaskMsg)
	if !ok || sel.id != 7 {
		t.Fatalf("enter produced %#v, want selectTaskMsg for task 7", msg)
	}
	if sel.state != stateRunning {
		t.Errorf("enter carried state %q, want the board's %q", sel.state, stateRunning)
	}
	v.cursor = 1
	if cmd := v.openTask(); cmd != nil {
		t.Fatal("enter on an unclaimed row is not inert")
	}
}

// The link picker offers only the row's own project: POST takes a bare
// number and the daemon resolves the repo from the task's project, so a task
// from elsewhere would link a different repository's number.
func TestPullRequestsLinkPickerIsScopedToTheProject(t *testing.T) {
	v := pullRequestsFixture(testPull(11, "unclaimed"))
	v.openLinkPicker()
	if v.picker == nil {
		t.Fatal("l did not open the task picker")
	}
	for _, opt := range v.picker.options {
		if strings.Contains(opt.label, "somewhere else") {
			t.Fatalf("the picker offers %q, a task in another project", opt.label)
		}
	}
	if len(v.picker.options) != 2 {
		t.Fatalf("the picker offers %d tasks, want the 2 in this project", len(v.picker.options))
	}
}

// Unlink asks first, and the confirmation says the refusal is sticky —
// which is what DELETE does. A prompt that said "clear" would be lying.
func TestPullRequestsUnlinkSaysTheRefusalSticks(t *testing.T) {
	v := pullRequestsFixture(testPull(11, "claimed", claimedBy(7, "auto")))
	v.askUnlink()
	if v.confirm == nil {
		t.Fatal("u did not ask before unlinking")
	}
	if !strings.Contains(v.confirm.text, "will not link it again") {
		t.Errorf("the confirmation reads %q and does not say the refusal sticks", v.confirm.text)
	}
	if !strings.Contains(v.render(120, 24), "will not link it again") {
		t.Error("the confirmation is not on screen")
	}
	// An unclaimed row has nothing to unlink, and says so.
	v.confirm = nil
	v.cursor = 0
	v.groups[0].pulls = []apiclient.GitHubPullRequest{testPull(12, "unclaimed")}
	v.askUnlink()
	if v.confirm != nil {
		t.Fatal("u asked about an unclaimed row")
	}
}

// A reconciler tick re-lists without a keypress.
func TestPullRequestsRefreshOnLinkChangedEvent(t *testing.T) {
	v := pullRequestsFixture(testPull(11, "claimed", claimedBy(7, "auto")))
	cmd := v.updateNote(apiclient.EventNote{
		Event: apiclient.Event{Type: eventTaskGitHubPullChanged},
	})
	if cmd == nil {
		t.Fatal("task.github_pull_changed did not schedule a re-list")
	}
	if !v.refreshWait {
		t.Error("the debounce was not armed")
	}
}
