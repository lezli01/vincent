package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// liveBranches is the listing both forms' branch rows are given: a plain
// branch, the one the project's own checkout has out, and one a vincent
// worktree is holding.
func liveBranches() []apiclient.Branch {
	return []apiclient.Branch{
		{Name: "feature/login"},
		{Name: "main", CheckedOutIn: "/src/vincent", MainCheckout: true, Current: true},
		{Name: "vincent/12-thing", CheckedOutIn: "/data/worktrees/12"},
	}
}

// branchForm is a loaded new-task form whose branch listing has landed.
func branchForm(t *testing.T) *newTask {
	t.Helper()
	n := loadedForm(t)
	// loadedForm holds two projects and no hint, so the row is deliberately
	// left empty; the branch listing is project-scoped, so pick one first.
	n.setProject(n.projects[0])
	n.update(ntBranchesMsg{projectID: n.projectID, branches: liveBranches()})
	return n
}

// pickListed walks the branch picker to a listed branch and commits it. The
// first option clears the row, so the branches follow it.
func pickListed(t *testing.T, n *newTask, row ntRow, name string) {
	t.Helper()
	moveTo(n, row)
	press(n, "enter")
	if n.mode != ntPicking || n.pick == nil {
		t.Fatalf("enter on row %d opened no picker (mode %d)", row, n.mode)
	}
	// The picker opens on whatever the row already holds, so walk to the top
	// before looking for the wanted row.
	for n.pick.cursor > 0 {
		press(n, "up")
	}
	for range len(n.pick.options) {
		opt, ok := n.pick.current()
		if ok && opt.value == name {
			press(n, "enter")
			return
		}
		press(n, "down")
	}
	t.Fatalf("the picker on row %d never offered %q", row, name)
}

// pickFree types a name into the picker's free-text row.
func pickFree(t *testing.T, n *newTask, row ntRow, name string) {
	t.Helper()
	moveTo(n, row)
	press(n, "enter")
	if n.mode != ntPicking || n.pick == nil {
		t.Fatalf("enter on row %d opened no picker (mode %d)", row, n.mode)
	}
	press(n, "t")
	if !n.pick.editing {
		t.Fatalf("t on row %d did not start free-text entry", row)
	}
	typeText(n, name)
	press(n, "enter")
}

// TestBranchRowShapeDecidesTheRequest is the whole of task 125.9 decision 1,
// and the test that fails if pickerResult.free is ever dropped: the picker's
// listed rows and its free row commit to two different request shapes, so
// adoption stays chosen and is never inferred from a name that happens to
// exist (task 125 decision 1).
func TestBranchRowShapeDecidesTheRequest(t *testing.T) {
	n := branchForm(t)
	pickListed(t, n, ntBranchName, "feature/login")
	req := n.request()
	if req.BranchName == nil || *req.BranchName != "feature/login" {
		t.Fatalf("branch_name = %v, want the branch that was chosen", req.BranchName)
	}
	if req.ExistingBranch == nil || !*req.ExistingBranch {
		t.Fatalf("existing_branch = %v, want true — a listed branch is §10's adopt mode",
			req.ExistingBranch)
	}

	n = branchForm(t)
	pickFree(t, n, ntBranchName, "brand/new")
	req = n.request()
	if req.BranchName == nil || *req.BranchName != "brand/new" {
		t.Fatalf("branch_name = %v, want the name that was typed", req.BranchName)
	}
	if req.ExistingBranch != nil {
		t.Fatalf("existing_branch = %v, want it absent — free text cuts a new branch",
			*req.ExistingBranch)
	}

	// Free text naming a branch the listing does carry is still free text:
	// the shape comes from the row that was committed, not from the name.
	n = branchForm(t)
	pickFree(t, n, ntBranchName, "feature/login")
	if req = n.request(); req.ExistingBranch != nil {
		t.Fatal("a listed name typed into the free row set existing_branch; the shape is the row, not the name")
	}
}

// TestBranchRowClearsBackToTheTemplate holds the option a picker row needs
// that a text row got for free: the field can be emptied again, and emptying
// it takes the adopt shape with it.
func TestBranchRowClearsBackToTheTemplate(t *testing.T) {
	n := branchForm(t)
	pickListed(t, n, ntBranchName, "feature/login")
	pickListed(t, n, ntBranchName, "")
	req := n.request()
	if req.BranchName != nil {
		t.Fatalf("branch_name = %q, want it back to the §5.3 chain", *req.BranchName)
	}
	if req.ExistingBranch != nil {
		t.Fatal("existing_branch survived a cleared branch row")
	}
}

// TestBaseBranchRowNeverAdopts: the base is a fork point. Choosing it off the
// same listing says nothing about §10's adopt mode, which is a statement about
// the task's own branch one row down.
func TestBaseBranchRowNeverAdopts(t *testing.T) {
	n := branchForm(t)
	pickListed(t, n, ntBranch, "feature/login")
	req := n.request()
	if req.BaseBranch == nil || *req.BaseBranch != "feature/login" {
		t.Fatalf("base_branch = %v, want the branch that was chosen", req.BaseBranch)
	}
	if req.ExistingBranch != nil {
		t.Fatal("choosing a base branch set existing_branch")
	}

	// And it stays that way once the branch row has been adopted too.
	pickListed(t, n, ntBranchName, "vincent/12-thing")
	req = n.request()
	if req.ExistingBranch == nil || !*req.ExistingBranch {
		t.Fatal("the branch row stopped adopting once a base was chosen")
	}
	if req.BaseBranch == nil || *req.BaseBranch != "feature/login" {
		t.Fatalf("base_branch = %v, want it untouched", req.BaseBranch)
	}
}

// TestMainCheckoutBranchSaysSoBeforeSubmit is the issue's acceptance
// criterion: a branch the project's own checkout has out is marked in the
// picker, and the consequence of adopting it — the task runs *there* rather
// than in a worktree — is on the row and in the Review stage before anything
// is posted, not afterwards as an `adopt_branch_checked_out` block reason
// (§18).
func TestMainCheckoutBranchSaysSoBeforeSubmit(t *testing.T) {
	n := branchForm(t)
	moveTo(n, ntBranchName)
	press(n, "enter")
	var note string
	for _, o := range n.pick.options {
		if o.value == "main" {
			note = o.note
		}
	}
	if !strings.Contains(note, "checkout") {
		t.Fatalf("the main-checkout row's note = %q, want it to say the branch is checked out here", note)
	}
	// The picker's filter matches the note as well as the label, so the notes
	// are the narrowing control too.
	n.pick.filter.SetValue("checkout")
	n.pick.applyFilter()
	if len(n.pick.matches) != 1 {
		t.Fatalf("filtering on \"checkout\" matched %d rows, want the one consequential branch",
			len(n.pick.matches))
	}
	press(n, "esc") // a narrowed list clears the filter first
	press(n, "esc")
	if n.mode != ntNavigating {
		t.Fatalf("two escapes left the form in mode %d, want it back on the row", n.mode)
	}

	pickListed(t, n, ntBranchName, "main")
	if n.mainCheckoutConsequence() == "" {
		t.Fatal("adopting the main checkout's branch produced no consequence line")
	}
	row := n.renderRow(ntBranchName)
	if !strings.Contains(row, "not a worktree") {
		t.Fatalf("the branch row does not carry the consequence:\n%s", row)
	}
	review, _ := n.renderReview(nil)
	if !strings.Contains(strings.Join(review, "\n"), "not a worktree") {
		t.Fatalf("the Review stage does not carry the consequence:\n%s", strings.Join(review, "\n"))
	}

	// A branch nothing has checked out carries neither.
	pickListed(t, n, ntBranchName, "feature/login")
	if n.mainCheckoutConsequence() != "" {
		t.Fatal("an ordinary branch produced the main-checkout consequence")
	}
}

// TestBranchHeldByAWorktreeIsListedAndSelectable holds task 125.9 decision 3.
// The listing is a listing and not a validator: the worktree can be removed
// between choosing the branch and admitting the task, and free text could name
// it anyway, so the row says what it would cost rather than refusing.
func TestBranchHeldByAWorktreeIsListedAndSelectable(t *testing.T) {
	n := branchForm(t)
	moveTo(n, ntBranchName)
	press(n, "enter")
	var held pickerOption
	for _, o := range n.pick.options {
		if o.value == "vincent/12-thing" {
			held = o
		}
	}
	if held.value == "" {
		t.Fatal("a branch a vincent worktree holds was left out of the listing")
	}
	if held.disabled {
		t.Fatal("the row is disabled; the client is not a validator over state that changes underneath it")
	}
	if !strings.Contains(held.note, "would block") {
		t.Fatalf("the row's note = %q, want it to say the task would block", held.note)
	}
	press(n, "esc")

	pickListed(t, n, ntBranchName, "vincent/12-thing")
	req := n.request()
	if req.ExistingBranch == nil || !*req.ExistingBranch {
		t.Fatal("a branch held by a worktree could not be adopted")
	}
}

// TestPullSeededDraftOffersNoBranchToAdopt: `existing_branch` with
// `github_pull` is a 400, so the form does not offer the shape at all rather
// than offering a request the daemon refuses.
func TestPullSeededDraftOffersNoBranchToAdopt(t *testing.T) {
	n := branchForm(t)
	n.pull = &apiclient.GitHubPullRequest{Number: 7, Title: "a pull request"}
	moveTo(n, ntBranchName)
	press(n, "enter")
	if n.pick == nil {
		t.Fatal("the branch row opened no picker on a pull-seeded draft")
	}
	for _, o := range n.pick.options {
		if o.value != "" {
			t.Fatalf("the picker offered %q to adopt on a pull-seeded draft", o.value)
		}
	}
	if !n.pick.allowFree {
		t.Fatal("free text is refused on a pull-seeded draft; naming a new branch is still allowed")
	}
	press(n, "esc")

	// Belt and braces: even a branchAdopt set before the seed lands cannot
	// reach the wire alongside github_pull.
	n.branchAdopt = true
	n.branchName.SetValue("feature/login")
	if req := n.request(); req.ExistingBranch != nil {
		t.Fatal("existing_branch travelled with github_pull, which the daemon 400s")
	}
}

// TestHandoffDraftOpensNoBranchPicker: the handoff form's two branch rows name
// a worktree that already exists, so there is nothing to decide on either
// (task 074).
func TestHandoffDraftOpensNoBranchPicker(t *testing.T) {
	n := branchForm(t)
	n.handoff = &apiclient.Chat{ID: 3, Branch: "vincent/chat-3", BaseBranch: "main"}
	n.branch.SetValue("main")
	n.branchName.SetValue("vincent/chat-3")
	for _, row := range []ntRow{ntBranch, ntBranchName} {
		moveTo(n, row)
		press(n, "enter")
		if n.pick != nil {
			t.Fatalf("row %d opened a picker on a handoff draft", row)
		}
	}
	if got := n.rowValue(ntBranchName); got != "vincent/chat-3" {
		t.Fatalf("the handoff branch row renders %q, want the chat's branch unqualified", got)
	}
}

// TestBranchListingFailureLeavesTheRowTypeable: a repository the daemon cannot
// read is a listing that failed, not a row that closes. The picker draws the
// error above a free-text row.
func TestBranchListingFailureLeavesTheRowTypeable(t *testing.T) {
	n := loadedForm(t)
	n.setProject(n.projects[0])
	n.update(ntBranchesMsg{projectID: n.projectID, err: errors.New("project path is gone")})
	if n.branchesErr == "" {
		t.Fatal("a failed listing left no message to show")
	}
	moveTo(n, ntBranchName)
	press(n, "enter")
	if n.pick == nil {
		t.Fatal("a failed listing closed the branch row")
	}
	if !strings.Contains(n.pick.err, "project path is gone") {
		t.Fatalf("the picker's error = %q, want the daemon's words", n.pick.err)
	}
	press(n, "t")
	typeText(n, "brand/new")
	press(n, "enter")
	req := n.request()
	if req.BranchName == nil || *req.BranchName != "brand/new" {
		t.Fatalf("branch_name = %v, want the name typed over a failed listing", req.BranchName)
	}
	if req.ExistingBranch != nil {
		t.Fatal("free text over a failed listing set existing_branch")
	}
}

// TestBranchListingIsKeyedByProject: a reply for a project the user has left
// is dropped rather than applied, the way ntGitHubMsg's is, and switching
// project takes the branch chosen from the old one with it.
func TestBranchListingIsKeyedByProject(t *testing.T) {
	n := branchForm(t)
	pickListed(t, n, ntBranchName, "feature/login")

	n.update(ntBranchesMsg{projectID: n.projectID + 1, branches: []apiclient.Branch{{Name: "stale"}}})
	for _, b := range n.branches {
		if b.Name == "stale" {
			t.Fatal("a listing for another project was applied to this one")
		}
	}

	moveTo(n, ntProject)
	press(n, "enter")
	press(n, "down")
	press(n, "enter")
	if n.branchAdopt || strings.TrimSpace(n.branchName.Value()) != "" {
		t.Fatalf("switching project kept branch %q (adopt=%v); a name from another repository is not a branch this one can run on",
			n.branchName.Value(), n.branchAdopt)
	}
	if len(n.branches) != 0 {
		t.Fatalf("switching project kept %d branches of the old one", len(n.branches))
	}
}

// TestNewChatBranchRowIsAdoptOnly holds task 125.9 decision 2: a chat cannot
// cut a branch under a name you chose — `branch_name` without
// `existing_branch` is a 400 — so the row has exactly one meaning, and the
// pair travels together or not at all.
func TestNewChatBranchRowIsAdoptOnly(t *testing.T) {
	f := chatFormWithCatalogs()
	f.applyBranches(newChatBranchesMsg{projectID: f.projectID, branches: liveBranches()})

	if req := f.request(); req.BranchName != "" || req.ExistingBranch {
		t.Fatalf("an untouched branch row sent %+v, want neither half", req)
	}

	openAt(t, f, ncBranch)
	if f.pick == nil {
		t.Fatal("enter on the branch row opened no list")
	}
	for range len(f.pick.options) {
		if opt, ok := f.pick.current(); ok && opt.value == "feature/login" {
			break
		}
		f.update(registryKey(t, "down"), nil)
	}
	f.update(registryKey(t, "enter"), nil)

	req := f.request()
	if req.BranchName != "feature/login" || !req.ExistingBranch {
		t.Fatalf("request = %+v, want branch_name and existing_branch together", req)
	}

	// Free text is allowed and means the same thing: the row's only shape is
	// "adopt this existing branch", and the daemon is the authority on whether
	// the name names one.
	openAt(t, f, ncBranch)
	f.update(registryKey(t, "t"), nil)
	for _, k := range keys("typed/branch") {
		f.update(k, nil)
	}
	f.update(registryKey(t, "enter"), nil)
	if req = f.request(); req.BranchName != "typed/branch" || !req.ExistingBranch {
		t.Fatalf("request = %+v, want the typed name adopted", req)
	}
}

// TestNewChatBranchRowShowsTheMainCheckoutConsequence: the same fact the task
// form puts on its row, on the form that has nowhere else to put it.
func TestNewChatBranchRowShowsTheMainCheckoutConsequence(t *testing.T) {
	f := chatFormWithCatalogs()
	f.applyBranches(newChatBranchesMsg{projectID: f.projectID, branches: liveBranches()})
	f.branch.SetValue("main")
	if got := f.branchValue(); !strings.Contains(got, "not a worktree") {
		t.Fatalf("the branch row renders %q, want the main-checkout consequence", got)
	}
	f.branch.SetValue("feature/login")
	if got := f.branchValue(); strings.Contains(got, "not a worktree") {
		t.Fatalf("an ordinary branch renders %q, want no main-checkout consequence", got)
	}
}

// TestNewChatBranchFailureLandsOnTheBranchRow holds task 125 decision 2's
// client half: the 409 for a working directory another task or chat already
// owns names a branch, and the row that chose it is where it can be changed —
// not the form-wide error line.
func TestNewChatBranchFailureLandsOnTheBranchRow(t *testing.T) {
	f := chatFormWithCatalogs()
	f.applyBranches(newChatBranchesMsg{projectID: f.projectID, branches: liveBranches()})
	f.branch.SetValue("feature/login")
	f.applyFailure(&apiclient.Error{
		Code:    "invalid_state",
		Message: `branch "feature/login" is already being worked on by task #4; one working directory has at most one owner`,
	})
	if f.branchErr == "" {
		t.Fatal("the 409 did not land on the branch row")
	}
	if f.err != "" {
		t.Fatalf("the 409 also filled the form-wide line with %q", f.err)
	}
	if f.focus != ncBranch {
		t.Fatalf("focus = %d, want the branch row the complaint is about", f.focus)
	}
	if out := f.render(80, 40); !strings.Contains(out, "already being worked on") {
		t.Fatalf("the form does not draw the branch complaint:\n%s", out)
	}

	// A refusal about something else still belongs to the form.
	f = chatFormWithCatalogs()
	f.applyFailure(errors.New("the daemon is not listening"))
	if f.branchErr != "" || f.err == "" {
		t.Fatalf("a non-branch failure went to the branch row (branchErr=%q, err=%q)", f.branchErr, f.err)
	}
}

// TestNewChatBranchListingFollowsTheProject: a listing for a project the user
// has left is dropped, and switching project drops a branch chosen from the
// old one.
func TestNewChatBranchListingFollowsTheProject(t *testing.T) {
	f := chatFormWithCatalogs()
	f.applyBranches(newChatBranchesMsg{projectID: f.projectID, branches: liveBranches()})
	f.branch.SetValue("feature/login")

	f.applyBranches(newChatBranchesMsg{projectID: f.projectID + 100, branches: []apiclient.Branch{{Name: "stale"}}})
	for _, b := range f.branches {
		if b.Name == "stale" {
			t.Fatal("a listing for another project was applied to this one")
		}
	}

	f.setProject(f.projectID + 1)
	if f.branch.Value() != "" {
		t.Fatalf("switching project kept branch %q", f.branch.Value())
	}
	if len(f.branches) != 0 {
		t.Fatalf("switching project kept %d branches of the old one", len(f.branches))
	}
}
