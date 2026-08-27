package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/store"
)

// The new-task form's GitHub issue row, against the real handlers and a real
// daemon-side GitHub client pointed at cmd/fakegh (task 035, §15).
//
// The row is conditional, so the interesting assertions are about what is
// *absent*: no row when the integration cannot be used, and — asserted at the
// process level, not assumed — no GitHub call either.

const ghLiveOrigin = "https://github.com/octo/repo.git"

// githubLiveHarness wires the fake `gh` into the live harness and hands back
// the argv log, so a test can assert on the calls the daemon made.
func newGitHubLiveHarness(t *testing.T, opts liveOptions) (*newTaskLiveHarness, string) {
	t.Helper()
	fake := githubtest.BuildFakeGH(t)
	argvLog := filepath.Join(t.TempDir(), "gh-argv.txt")
	t.Setenv("FAKEGH_ARGV_FILE", argvLog)
	if os.Getenv("FAKEGH_SCENARIO") == "" {
		t.Setenv("FAKEGH_SCENARIO", "success")
	}
	opts.github = github.New(github.Options{
		GHPath: fake,
		Getenv: func(string) string { return "" },
	})
	return newNewTaskLiveHarnessWith(t, opts), argvLog
}

func ghLiveCalls(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// openForm presses n and waits for the catalogs plus the capability probe.
func (h *newTaskLiveHarness) openForm(t *testing.T) *newTask {
	t.Helper()
	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	n := h.form(t)
	h.p.until(10*time.Second, "the form to load", func() bool { return n.loaded })
	h.p.until(10*time.Second, "the GitHub probe to answer",
		func() bool { return n.githubProject == n.projectID })
	return n
}

// TestIssueRowAppearsWhenAvailable: a github.com origin and a working
// credential put the row on the form, between workflow and title.
func TestIssueRowAppearsWhenAvailable(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	n := h.openForm(t)

	if !n.githubAvailable() {
		t.Fatalf("probe = %+v, want available", n.github)
	}
	if !n.rowVisible(ntIssue) {
		t.Fatal("the issue row is not offered on a reachable GitHub project")
	}
	h.p.until(10*time.Second, "the issue listing", func() bool { return len(n.issues) > 0 })
	if got := n.rowValue(ntIssue); !strings.Contains(got, "enter to pick") {
		t.Errorf("issue row reads %q, want an invitation to pick", got)
	}
	// The order is §15's: the row sits between workflow and title.
	rows := n.visibleRows()
	var order []ntRow
	for _, r := range rows {
		if r == ntWorkflow || r == ntIssue || r == ntTitle {
			order = append(order, r)
		}
	}
	if len(order) != 3 || order[0] != ntWorkflow || order[1] != ntIssue || order[2] != ntTitle {
		t.Errorf("row order around the issue row = %v", order)
	}
}

// TestIssueRowAbsentWhenDisabled is the acceptance criterion in full: no row,
// and no GitHub call — the second half asserted from the fake's argv log
// rather than inferred from the first.
func TestIssueRowAbsentWhenDisabled(t *testing.T) {
	off := func() config.Config {
		c := config.Default()
		c.GitHub.Enabled = false
		return c
	}
	h, argv := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin, config: off})
	n := h.openForm(t)

	if n.githubAvailable() {
		t.Fatalf("probe = %+v, want unavailable with the integration off", n.github)
	}
	if n.rowVisible(ntIssue) {
		t.Error("the issue row is offered while the integration is disabled")
	}
	for _, row := range n.visibleRows() {
		if row == ntIssue {
			t.Fatal("ntIssue is in the visible row list")
		}
	}
	if calls := ghLiveCalls(t, argv); calls != "" {
		t.Errorf("a disabled integration still invoked gh:\n%s", calls)
	}
}

// TestIssueRowAbsentOnANonGitHubProject: same absence, different reason, and
// still no call.
func TestIssueRowAbsentOnANonGitHubProject(t *testing.T) {
	h, argv := newGitHubLiveHarness(t, liveOptions{remote: "https://gitlab.com/octo/repo.git"})
	n := h.openForm(t)

	if n.rowVisible(ntIssue) {
		t.Error("the issue row is offered on a project whose origin is not GitHub")
	}
	if n.github.Reason != github.ReasonNotGitHub {
		t.Errorf("reason = %q, want %q", n.github.Reason, github.ReasonNotGitHub)
	}
	if calls := ghLiveCalls(t, argv); calls != "" {
		t.Errorf("a non-GitHub project still invoked gh:\n%s", calls)
	}
}

// TestCursorSkipsTheHiddenIssueRow: a hidden row must be stepped over, not
// landed on — a cursor parked where nothing draws is a form that appears to
// swallow keystrokes.
func TestCursorSkipsTheHiddenIssueRow(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: "https://gitlab.com/octo/repo.git"})
	n := h.openForm(t)

	n.cursor = ntWorkflow
	n.moveCursor(1)
	if n.cursor != ntTitle {
		t.Errorf("cursor after moving past the hidden row = %v, want ntTitle", n.cursor)
	}
	n.moveCursor(-1)
	if n.cursor != ntWorkflow {
		t.Errorf("cursor moving back = %v, want ntWorkflow", n.cursor)
	}
}

// TestPickingAnIssueFillsEditableRows is the acceptance criterion: the pick
// fills title, description, link line and matching declared fields, and every
// one of them is still editable and clearable.
func TestPickingAnIssueFillsEditableRows(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	n := h.openForm(t)
	h.p.until(10*time.Second, "the issue listing", func() bool { return len(n.issues) > 0 })

	// Open the picker through the shell, exactly as a user would.
	n.cursor = ntIssue
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n.mode != ntPicking || n.pick == nil {
		t.Fatalf("mode = %v, pick = %v; want the issue picker open", n.mode, n.pick)
	}
	// The list narrows by typed text, like every other picker.
	h.sendKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	h.typeText("200")
	if len(n.pick.matches) != 1 {
		t.Fatalf("filtering for 200 left %d matches", len(n.pick.matches))
	}
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if n.issue == nil || n.issue.Number != 200 {
		t.Fatalf("linked issue = %+v, want #200", n.issue)
	}
	if n.titleText() == "" {
		t.Error("the title row was not filled from the issue")
	}
	description := n.desc.Value()
	if !strings.Contains(description,
		"GitHub issue #200: https://github.com/octo/repo/issues/200") {
		t.Errorf("description carries no link line:\n%q", description)
	}

	// Nothing is locked: the human rewrites the title and clears the
	// description, and the request carries what is on screen.
	n.titleIn.SetValue("My own framing")
	n.desc.SetValue("")
	req := n.request()
	if req.Title != "My own framing" {
		t.Errorf("request title = %q, want the edited value", req.Title)
	}
	if req.Description == nil || *req.Description != "" {
		t.Errorf("request description = %v, want an explicit empty (a cleared row)", req.Description)
	}
	if req.GitHubIssue == nil || *req.GitHubIssue != 200 {
		t.Errorf("request github_issue = %v, want 200", req.GitHubIssue)
	}

	// And the link can be removed outright.
	n.applyIssuePick("")
	if n.issue != nil {
		t.Error("choosing (none) left the issue linked")
	}
	if n.request().GitHubIssue != nil {
		t.Error("an unlinked draft still sends github_issue")
	}
}

// TestCreateFromAnIssueStoresTheSnapshot closes the loop: what the form posts
// is a task the daemon records with its issue, which is what `.Issue` renders
// from.
func TestCreateFromAnIssueStoresTheSnapshot(t *testing.T) {
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	n := h.openForm(t)
	h.p.until(10*time.Second, "the issue listing", func() bool { return len(n.issues) > 0 })

	n.applyIssuePick("200")
	h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	h.p.until(20*time.Second, "the task to be created", func() bool {
		tasks, err := h.st.ListTasks(t.Context(), store.TaskFilter{})
		return err == nil && len(tasks) > 0
	})
	tasks, err := h.st.ListTasks(t.Context(), store.TaskFilter{})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	created := tasks[0]
	if created.GitHubIssue == nil || created.GitHubIssue.Number != 200 {
		t.Fatalf("stored issue = %+v, want #200", created.GitHubIssue)
	}
	if !strings.Contains(created.Description, "GitHub issue #200") {
		t.Errorf("stored description carries no link line:\n%q", created.Description)
	}
}
