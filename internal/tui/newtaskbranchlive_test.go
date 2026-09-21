package tui

import (
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/testrepo"
)

// waitForBranches runs the form's own listing command against the real
// handlers and feeds the answer back, which is the two halves of one delivery
// the runtime performs.
func waitForBranches(t *testing.T, n *newTask, projectID int64) {
	t.Helper()
	msg := runCmd(t, n.branchesCmd(projectID), 10*time.Second)
	got, ok := msg.(ntBranchesMsg)
	if !ok {
		t.Fatalf("the branch fetch produced %T, want ntBranchesMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("GET /v1/projects/%d/branches failed: %v", projectID, got.err)
	}
	n.update(got)
}

// TestNewTaskBranchRowAdoptsALiveBranch is the client-side half of task 125.9
// end to end: the listing the picker offers is the one
// `GET /v1/projects/{id}/branches` actually serves, and the body the form
// builds from a chosen row is one `POST /v1/tasks` accepts as §10's adopt
// mode. Both sides are real, which is what keeps `apiclient.Branch` and the
// server's `branchBody` from drifting.
func TestNewTaskBranchRowAdoptsALiveBranch(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	testrepo.Run(t, h.repo, "branch", "feature/login")

	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	n := h.form(t)
	h.p.until(10*time.Second, "the form to load its catalogs", func() bool { return n.loaded })
	waitForBranches(t, n, h.projectID)

	// The daemon's own answer, not a fixture: `main` is what the project's
	// checkout has out, and the picker says so.
	var main, feature apiclient.Branch
	for _, b := range n.branches {
		switch b.Name {
		case "main":
			main = b
		case "feature/login":
			feature = b
		}
	}
	if main.Name == "" || !main.MainCheckout || !main.Current {
		t.Fatalf("main = %+v, want the project's own checkout at HEAD", main)
	}
	if feature.Name == "" || feature.CheckedOutIn != "" {
		t.Fatalf("feature/login = %+v, want a branch nothing has checked out", feature)
	}

	moveTo(n, ntTitle)
	press(n, "enter")
	typeText(n, "Continue the login work")
	press(n, "enter")
	pickListed(t, n, ntBranchName, "feature/login")

	req := n.request()
	if req.BranchName == nil || *req.BranchName != "feature/login" {
		t.Fatalf("branch_name = %v", req.BranchName)
	}
	if req.ExistingBranch == nil || !*req.ExistingBranch {
		t.Fatal("existing_branch absent; a listed branch is the adopt mode")
	}

	msg := runCmd(t, n.submit(), 20*time.Second)
	created, ok := msg.(taskCreatedMsg)
	if !ok {
		t.Fatalf("submit produced %T (%v), want a created task", msg, msg)
	}
	if created.task.BranchName != "feature/login" {
		t.Fatalf("the task's branch = %q, want the branch that was adopted", created.task.BranchName)
	}
	if !created.task.AdoptedBranch {
		t.Fatal("adopted_branch is false; the daemon cut a branch instead of adopting one")
	}
}

// TestNewTaskBranchFreeTextStillCutsABranch is the other half of decision 1
// against the live daemon: a name the listing does not carry is the ordinary
// cut-a-new-branch mode, unchanged, and `existing_branch` never travels with
// it.
func TestNewTaskBranchFreeTextStillCutsABranch(t *testing.T) {
	h := newNewTaskLiveHarness(t)

	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	n := h.form(t)
	h.p.until(10*time.Second, "the form to load its catalogs", func() bool { return n.loaded })
	waitForBranches(t, n, h.projectID)

	moveTo(n, ntTitle)
	press(n, "enter")
	typeText(n, "Start something new")
	press(n, "enter")
	pickFree(t, n, ntBranchName, "brand/new")

	msg := runCmd(t, n.submit(), 20*time.Second)
	created, ok := msg.(taskCreatedMsg)
	if !ok {
		t.Fatalf("submit produced %T (%v), want a created task", msg, msg)
	}
	if created.task.BranchName != "brand/new" {
		t.Fatalf("the task's branch = %q, want the name that was typed", created.task.BranchName)
	}
	if created.task.AdoptedBranch {
		t.Fatal("adopted_branch is true; free text must still cut a branch")
	}
}

// TestNewChatBranchRowAdoptsALiveBranch is the same seam on the new-chat
// form, where the pair `branch_name` + `existing_branch` is the only shape
// the daemon accepts — either half alone is a 400, so a form that sent one
// without the other would be unusable rather than merely wrong.
func TestNewChatBranchRowAdoptsALiveBranch(t *testing.T) {
	m := newChatLiveRoot(t)

	_, cmd := m.Update(registryKey(t, "n"))
	if cmd == nil {
		t.Fatal("n opened no fetch for the pickers' contents")
	}
	msg := runCmd(t, cmd, 10*time.Second)
	fields, ok := msg.(newChatFieldsMsg)
	if !ok {
		t.Fatalf("the form's fetch produced %T, want newChatFieldsMsg", msg)
	}
	m.Update(msg)

	f := chatsForm(t, m)
	if len(f.projects) == 0 {
		t.Fatal("the form holds no projects")
	}
	// The root hands the form its client on every keystroke; the test drives
	// it directly, so it hands it the same one.
	client := m.client
	// A branch to adopt, cut in the project's own repository so the listing
	// the daemon serves is the one under test.
	var path string
	for _, p := range fields.projects {
		if p.ID == f.projectID {
			path = p.Path
		}
	}
	if path == "" {
		t.Fatalf("no path for project %d", f.projectID)
	}
	testrepo.Run(t, path, "branch", "colleague/work")

	branches := runCmd(t, f.branchesCmd(f.projectID), 10*time.Second)
	got, ok := branches.(newChatBranchesMsg)
	if !ok {
		t.Fatalf("the branch fetch produced %T, want newChatBranchesMsg", branches)
	}
	if got.err != nil {
		t.Fatalf("GET /v1/projects/%d/branches failed: %v", f.projectID, got.err)
	}
	f.applyBranches(got)

	var names []string
	for _, b := range f.branches {
		names = append(names, b.Name)
	}
	if !slices.Contains(names, "colleague/work") {
		t.Fatalf("the listing is %v, want the branch that was just cut", names)
	}

	f.moveFocusTo(ncTitle)
	for _, k := range keys("keep the login work going") {
		f.update(k, client)
	}
	f.focus = ncBranch
	f.update(registryKey(t, "enter"), client)
	if f.pick == nil {
		t.Fatal("enter on the branch row opened no list")
	}
	for range len(f.pick.options) {
		if opt, ok := f.pick.current(); ok && opt.value == "colleague/work" {
			break
		}
		f.update(registryKey(t, "down"), client)
	}
	f.update(registryKey(t, "enter"), client)

	req := f.request()
	if req.BranchName != "colleague/work" || !req.ExistingBranch {
		t.Fatalf("request = %+v, want the pair together", req)
	}

	created := runCmd(t, f.submit(), 20*time.Second)
	out, ok := created.(chatCreatedMsg)
	if !ok {
		t.Fatalf("submit produced %T, want chatCreatedMsg", created)
	}
	if out.err != nil {
		t.Fatalf("POST /v1/chats refused the form's body: %v", out.err)
	}
	if out.chat.Branch != "colleague/work" || !out.chat.AdoptedBranch {
		t.Fatalf("chat branch = %q adopted = %v, want the adopted branch",
			out.chat.Branch, out.chat.AdoptedBranch)
	}

	// And an untouched branch row still leaves the daemon to cut one, which
	// is what keeps the row optional.
	f.branch.SetValue("")
	if req = f.request(); req.BranchName != "" || req.ExistingBranch {
		t.Fatalf("a cleared branch row sent %+v, want neither half", req)
	}
}
