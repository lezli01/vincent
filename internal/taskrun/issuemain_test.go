package taskrun

import (
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
