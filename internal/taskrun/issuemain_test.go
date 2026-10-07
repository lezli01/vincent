package taskrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestSecondMainTaskOfAnIssueRunsOnItsMainBranch is review F1 of #768 end to
// end: two main tasks of one issue, both queued before the scheduler walks.
// The second is bound to the first's branch (task 134 decision 3), and used to
// block `branch_exists` at admission because it tried to cut a branch the
// first had already cut. It now adopts the branch: it waits in the queue —
// even ahead of the first in priority, before the branch exists — while the
// first holds the branch's working directory, and runs on the same branch
// once the first is archived. The first's archive keeps the branch although
// it carries no commits, because the second still carries it.
func TestSecondMainTaskOfAnIssueRunsOnItsMainBranch(t *testing.T) {
	h := newEngineHarness(t)
	iss, err := h.store.CreateIssue(t.Context(),
		store.NewIssue{ProjectID: h.projectID, Title: "Lock file leaks"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	const quick = `name: quick
steps:
  - id: noop
    type: command
    run: exit 0
`
	const gated = `name: gated
steps:
  - id: gate
    type: manual
    instructions: Hold the working directory.
`
	main := func(title, snapshot string, priority int) *store.Task {
		return h.createTaskWith(t, snapshot, func(task *store.Task) {
			task.Title = title
			task.IssueID = &iss.ID
			task.IssueWorktree = store.IssueWorktreeMain
			task.Priority = priority
		})
	}
	first := main("first", quick, 0)
	// Higher priority, so the scheduler reaches it first in the walk where
	// the first has not cut the branch yet.
	second := main("second", gated, 1)
	if second.BranchName != first.BranchName || !second.AdoptedBranch || first.AdoptedBranch {
		t.Fatalf("second = (%q, adopted %v), first = (%q, adopted %v); want the second to adopt the first's branch",
			second.BranchName, second.AdoptedBranch, first.BranchName, first.AdoptedBranch)
	}
	h.start(t)

	done := h.waitForState(t, first.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("first task = %s (%s), want done", done.State, done.BlockReason)
	}
	waiting, err := h.store.GetTask(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if waiting.State != store.TaskQueued {
		t.Fatalf("second task = %s (%s), want queued behind the first's working directory",
			waiting.State, waiting.BlockReason)
	}

	_, branch, err := h.runner.Archive(t.Context(), first.ID, false)
	if err != nil {
		t.Fatalf("Archive(first): %v", err)
	}
	if branch.Result != worktree.BranchNotOurs {
		t.Errorf("first's branch outcome = %+v, want %q while the second carries it",
			branch, worktree.BranchNotOurs)
	}

	gate := h.waitForState(t, second.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if gate.State != store.TaskAwaitingGate {
		t.Fatalf("second task = %s (%s), want awaiting_gate on the issue's main branch",
			gate.State, gate.BlockReason)
	}
	head := testrepo.Run(t, gate.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if head != first.BranchName {
		t.Errorf("second task works on %q, want the issue's main branch %q", head, first.BranchName)
	}
}

// TestSideTaskIsCutFromTheMainBranchWithoutFetching is task 134.13 end to
// end, and the hazard decision 13 names. The issue's main branch has an
// upstream that is one commit ahead, and is checked out — clean — in the
// worktree of the main task holding it at a gate. A side task admitted with
// `fetch_base_branch: true` still fetches nothing and fast-forwards nothing:
// the main branch, its worktree's HEAD and its files are as they were, and
// the side task starts at the main branch's tip, records it as base_sha,
// and runs in a worktree of its own.
func TestSideTaskIsCutFromTheMainBranchWithoutFetching(t *testing.T) {
	h := newEngineHarness(t)
	if !h.config().FetchBaseBranch {
		t.Fatal("fixture: fetch_base_branch is off by default; the test needs it on")
	}
	iss, err := h.store.CreateIssue(t.Context(),
		store.NewIssue{ProjectID: h.projectID, Title: "Lock file leaks"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	const gated = `name: gated
steps:
  - id: gate
    type: manual
    instructions: Hold the main worktree.
`
	main := h.createTaskWith(t, gated, func(task *store.Task) {
		task.Title, task.IssueID, task.IssueWorktree = "main", &iss.ID, store.IssueWorktreeMain
	})
	h.start(t)
	held := h.waitForState(t, main.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if held.State != store.TaskAwaitingGate {
		t.Fatalf("main task = %s (%s), want awaiting_gate", held.State, held.BlockReason)
	}

	// The main branch gets an upstream, which then moves ahead by one commit
	// that changes a file — what a fetch and fast-forward would bring in.
	remote := testrepo.InitBare(t)
	testrepo.Run(t, h.repo, "remote", "add", "origin", remote)
	testrepo.Run(t, held.WorktreePath, "push", "-q", "--set-upstream", "origin", main.BranchName)
	ahead := filepath.Join(t.TempDir(), "ahead")
	testrepo.Run(t, h.repo, "worktree", "add", "-q", "--detach", ahead, main.BranchName)
	testrepo.WriteFile(t, ahead, "upstream.txt", "somebody else's work\n")
	testrepo.Run(t, ahead, "add", ".")
	testrepo.Run(t, ahead, "commit", "-q", "-m", "upstream commit")
	testrepo.Run(t, ahead, "push", "-q", "origin", "HEAD:refs/heads/"+main.BranchName)
	testrepo.Run(t, h.repo, "worktree", "remove", "--force", ahead)

	mainTip := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+main.BranchName)
	mainHead := testrepo.Run(t, held.WorktreePath, "rev-parse", "HEAD")

	side := h.createTaskWith(t, refreshSnapshot(), func(task *store.Task) {
		task.Title, task.IssueID = "side", &iss.ID
		task.IssueWorktree, task.MergeOnConflict = store.IssueWorktreeSide, store.MergeOnConflictBlock
	})
	if side.BaseBranch != main.BranchName {
		t.Fatalf("side base = %q, want the main branch %q", side.BaseBranch, main.BranchName)
	}
	done := h.waitForState(t, side.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("side task = %s (%s), want done", done.State, done.BlockReason)
	}

	if got := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+main.BranchName); got != mainTip {
		t.Errorf("main branch moved to %s, want it left at %s", got, mainTip)
	}
	if got := testrepo.Run(t, held.WorktreePath, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("main worktree HEAD moved to %s, want %s", got, mainHead)
	}
	if _, err := os.Stat(filepath.Join(held.WorktreePath, "upstream.txt")); !os.IsNotExist(err) {
		t.Errorf("the upstream commit's file reached the main worktree (stat err %v)", err)
	}

	if done.BaseSHA != mainTip {
		t.Errorf("side base_sha = %q, want the main branch's tip %s", done.BaseSHA, mainTip)
	}
	if got := testrepo.Run(t, done.WorktreePath, "rev-parse", "HEAD"); got != mainTip {
		t.Errorf("side worktree starts at %s, want the main branch's tip %s", got, mainTip)
	}
	r := done.BaseRefresh
	if r == nil || r.Fetch.Result != worktree.FetchDisabled || r.FastForward.Result != worktree.FastForwardNotAttempted {
		t.Errorf("side base_refresh = %+v, want fetch %q and fast_forward %q",
			r, worktree.FetchDisabled, worktree.FastForwardNotAttempted)
	}
	if done.AdoptedBranch || done.BranchName == main.BranchName || done.WorktreePath == held.WorktreePath {
		t.Errorf("side task = (%q, adopted %v, %s), want its own branch and worktree beside the main one at %s",
			done.BranchName, done.AdoptedBranch, done.WorktreePath, held.WorktreePath)
	}
}
