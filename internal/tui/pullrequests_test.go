package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// pullRequestsFixture is the takeover on project "api", whose probe says yes,
// with its listing already in. Project "web" answered no.
func pullRequestsFixture(pulls ...apiclient.GitHubPullRequest) *pullRequestsView {
	v := newPullRequestsView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	v.probed = true
	v.probes = []githubProject{
		{project: testProject(1, "api"), status: apiclient.GitHubStatus{Enabled: true, Available: true, Repo: "octo/api"}},
		{project: testProject(2, "web"), status: apiclient.GitHubStatus{Reason: "not_github", Message: "origin is not a github.com repository"}},
	}
	v.pulls = pulls
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

// withLiveFakeOpener is withFakeOpener for the live harness, whose pump runs
// each command on its own goroutine the way the runtime does: the recorder
// is written there and read from the test's, so it is locked, and the test
// reads a copy.
func withLiveFakeOpener(t *testing.T) func() []string {
	t.Helper()
	var (
		mu     sync.Mutex
		opened []string
	)
	prev := openURL
	openURL = func(_ context.Context, url string) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, url)
		return nil
	}
	t.Cleanup(func() { openURL = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(opened)
	}
}

// drain runs a tea.Cmd to its message, in-process: every command these
// tests exercise is either pure or points at offlineClient.
func drain(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// The nav row, its palette and help rows, and the workspace's pull-request
// keys follow the selected project's §13.2 probe (task 132.11): with two
// projects, one answering yes, a switch between them shows and withholds
// them all.
func TestPullRequestsGateFollowsTheSelectedProject(t *testing.T) {
	m := connectedRoot(t)
	m.selectProject(testProject(1, "api"), whyFirstName)
	m.Update(githubProbeMsg{projects: []githubProject{
		{project: testProject(1, "api"), status: apiclient.GitHubStatus{Available: true, Repo: "octo/api"}},
		{project: testProject(2, "web"), status: apiclient.GitHubStatus{Reason: "not_github"}},
	}})
	offered := func() bool {
		for _, e := range paletteEntries(ctxTasks, taskActions{}, false, true, m.githubAvailable(), nil, nil) {
			if e.nav && e.navTarget == viewPullRequests {
				return true
			}
		}
		return false
	}
	if !m.githubAvailable() || !offered() {
		t.Fatal("the pull-requests row is withheld on a project whose probe said yes")
	}
	if !strings.Contains(m.helpSheet(), "pull requests —") {
		t.Error("the ? overlay hides the pull-requests view on a GitHub project")
	}

	m.selectProject(testProject(2, "web"), "test")
	if m.githubAvailable() || offered() {
		t.Fatal("the pull-requests row is offered on a project whose probe said no")
	}
	if strings.Contains(m.helpSheet(), "pull requests —") {
		t.Error("the ? overlay names the pull-requests view on a project without GitHub")
	}
	if got := len(bindingsFor(ctxTaskDetails)) - len(withoutGitHub(bindingsFor(ctxTaskDetails), m.githubAvailable())); got != 2 {
		t.Errorf("withoutGitHub dropped %d task-details rows, want the 2 pull-request keys", got)
	}

	m.selectProject(testProject(1, "api"), "test")
	if !m.githubAvailable() || !offered() {
		t.Fatal("switching back did not offer the pull-requests row again")
	}
}

// Probes that have not answered yet are not "available": the row must not
// flicker into existence and out again while the fan-out lands.
func TestPullRequestsProbeInFlightIsNotAvailable(t *testing.T) {
	m := &root{views: newViews(t.Context(), newHyperlinkHolder(), newLevelHolder())}
	m.sel = projectSel{id: 1, name: "api"}
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

// A failed listing is one error line, the daemon's sentence, in place of
// the rows (task 132.11): there is one project's listing, so nothing else
// to keep.
func TestPullRequestsFailedListingIsOneErrorLine(t *testing.T) {
	v := pullRequestsFixture()
	v.applyLoaded(prLoadedMsg{err: "GitHub is not available for this project: not authenticated"})
	out := v.render(120, 24)
	if strings.Count(out, "⚠") != 1 || !strings.Contains(out, "not authenticated") {
		t.Errorf("the failed listing is not one error line with its reason:\n%s", out)
	}
}

// Open on a GitHub project, switch to one without: the view stays, shows
// that project's own reason, and issues no listing (task 132.11). Switching
// back reloads.
func TestPullRequestsSwitchToAProjectWithoutGitHub(t *testing.T) {
	v := pullRequestsFixture(testPull(11, "ship it"))
	if cmd := v.setProject(projectSel{id: 2, name: "web"}); cmd != nil {
		t.Fatal("a switch to a project without GitHub issued a listing")
	}
	out := v.render(120, 24)
	if !strings.Contains(out, "This project has no usable GitHub integration: origin is not a github.com repository") {
		t.Errorf("the screen does not carry the project's reason:\n%s", out)
	}
	if strings.Contains(out, "ship it") {
		t.Errorf("the previous project's rows survived the switch:\n%s", out)
	}
	if cmd := v.setProject(projectSel{id: 1, name: "api"}); cmd == nil {
		t.Fatal("switching back to the GitHub project did not reload")
	}
}

// The listing's 409 arrives as an apiclient.Error; what a human reads is the
// daemon's sentence, not the status code.
func TestPullRequestsErrorLineUsesTheDaemonsMessage(t *testing.T) {
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

// The link picker offers only the selected project's tasks: POST takes a bare
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
	v.pulls = []apiclient.GitHubPullRequest{testPull(12, "unclaimed")}
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
