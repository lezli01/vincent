package taskrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/pathx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestOneOwnerInTheMainCheckout is issue #749 end to end: two tasks adopt the
// branch the human has checked out in the project's main checkout, and both
// are queued when the scheduler first walks. Only one may work in the
// project path (§10, task 125 decision 2); the other waits in the queue. The
// bug admitted both in one walk, and both agents ran in the human's tree.
func TestOneOwnerInTheMainCheckout(t *testing.T) {
	h := newEngineHarness(t)
	testrepo.Run(t, h.repo, "checkout", "-q", "-b", "shared/branch")

	snapshot := `name: gated
steps:
  - id: gate
    type: manual
    instructions: Hold the working directory.
`
	adopt := func(title string) *store.Task {
		t.Helper()
		task := &store.Task{
			ProjectID: h.projectID, Title: title, Description: "a task",
			WorkflowName: "gated", WorkflowSnapshot: snapshot,
			BaseBranch: "main", BranchName: "shared/branch", AdoptedBranch: true,
			State: store.TaskQueued,
		}
		if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	first := adopt("first")
	second := adopt("second")
	// Both are queued before the scheduler starts, so its first walk sees
	// both with no claimant in the database.
	h.start(t)

	gated := h.waitForState(t, first.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if gated.State != store.TaskAwaitingGate {
		t.Fatalf("first task = %s (%s), want awaiting_gate", gated.State, gated.BlockReason)
	}
	if !pathx.SameDir(gated.WorktreePath, h.repo) {
		t.Fatalf("first task works in %q, want the main checkout %q", gated.WorktreePath, h.repo)
	}

	// The first walk moved every task it admitted to running before any
	// actor ran, so by the time the first reached its gate an admitted
	// second would be past queued.
	waiting, err := h.store.GetTask(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if waiting.State != store.TaskQueued {
		t.Fatalf("second task = %s (%s), want queued behind the first's claim on the project path",
			waiting.State, waiting.BlockReason)
	}
	if waiting.WorktreePath != "" {
		t.Errorf("second task works in %q, want no working directory while it waits", waiting.WorktreePath)
	}
	if runs := h.stepRuns(t, second.ID); len(runs) != 0 {
		t.Errorf("second task has %d step runs, want none", len(runs))
	}
}
