package scheduler

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// adopted creates a queued task on an existing branch (§10, task 125).
func (h *harness) adopted(t *testing.T, projectID int64, title, branch string, age time.Duration) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: projectID, Title: title,
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: branch, AdoptedBranch: true,
		State: store.TaskQueued, CreatedAt: time.Now().Add(-age),
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// TestClaimedWorkingDirSkipsAndAdmitsLater is the point of decision 2 over a
// block: a queued task whose directory is taken waits and is admitted on a
// later walk, rather than burning a `blocked` state a human has to retry.
func TestClaimedWorkingDirSkipsAndAdmitsLater(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)

	// The claimant is the first task, already working in the directory the
	// branch resolves to.
	holder := h.adopted(t, p, "holder", "shared/branch", 2*time.Minute)
	if err := h.store.ClaimTaskWorktree(t.Context(), holder.ID, "/wt/1", "abc", nil); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}
	if _, _, err := h.store.TransitionTask(t.Context(), holder.ID,
		store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("mark holder running: %v", err)
	}
	waiting := h.adopted(t, p, "waiting", "shared/branch", time.Minute)
	// A task on a free branch must still be admitted in the same walk: the
	// skip is a skip, not a stop.
	free := h.adopted(t, p, "free", "other/branch", 0)

	h.sched.admit(t.Context())

	got := h.admitter.ids()
	if len(got) != 1 || got[0] != free.ID {
		t.Fatalf("admitted %v, want [%d] (the claimed branch waits, the walk continues)", got, free.ID)
	}
	if st := h.state(t, waiting.ID); st != store.TaskQueued {
		t.Fatalf("waiting task is %s, want %s — the claim must not block it", st, store.TaskQueued)
	}

	// The claimant finishes and gives the directory up. Both halves matter:
	// a task that still holds a slot is still working in it, and one whose
	// row still names the directory has not released it.
	if _, _, err := h.store.TransitionTask(t.Context(), holder.ID,
		store.TaskRunning, store.TaskDone, store.TaskChange{}); err != nil {
		t.Fatalf("finish holder: %v", err)
	}
	if err := h.store.ClaimTaskWorktree(t.Context(), holder.ID, "", "abc", nil); err != nil {
		t.Fatalf("release worktree: %v", err)
	}
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); len(got) != 2 || got[1] != waiting.ID {
		t.Fatalf("admitted %v across both walks, want the waiting task %d second", got, waiting.ID)
	}
}
