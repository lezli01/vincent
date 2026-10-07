package scheduler

import (
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// TestSuccessorMainTaskIsAdmittedOnceItsPredecessorSettles: an issue's next
// main task shares its predecessor's branch and directory, and receives that
// directory by transfer at admission (task 134.12). So it is admitted as soon
// as the predecessor is done or aborted — not when it is archived, which is
// what task 125's working-directory claim would wait for. A row bound as
// adopted before 134.12 is admitted the same way: the main role wins over
// the flag.
func TestSuccessorMainTaskIsAdmittedOnceItsPredecessorSettles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		settle  store.TaskState
		adopted bool
	}{
		{"done", store.TaskDone, false},
		{"aborted", store.TaskAborted, false},
		{"legacy adopted row", store.TaskDone, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 10)
			p := h.project(t, "proj", nil)
			is := h.issue(t, p, "the issue")

			pred := &store.Task{
				ProjectID: p, Title: "pred",
				WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
				BaseBranch: "main", BranchName: "issue/main",
				State: store.TaskQueued, CreatedAt: time.Now().Add(-2 * time.Minute),
				IssueID: &is, IssueWorktree: store.IssueWorktreeMain,
			}
			if err := h.store.CreateTask(t.Context(), pred, nil); err != nil {
				t.Fatalf("CreateTask(pred): %v", err)
			}
			if _, _, err := h.store.TransitionTask(t.Context(), pred.ID,
				store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
				t.Fatalf("run pred: %v", err)
			}
			if err := h.store.ClaimTaskWorktree(t.Context(), pred.ID, "/wt/pred", "abc", nil); err != nil {
				t.Fatalf("ClaimTaskWorktree: %v", err)
			}

			succ := &store.Task{
				ProjectID: p, Title: "succ",
				WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
				BaseBranch: "main", BranchName: "vincent/succ", AdoptedBranch: tc.adopted,
				State: store.TaskQueued, CreatedAt: time.Now().Add(-time.Minute),
				IssueID: &is, IssueWorktree: store.IssueWorktreeMain,
			}
			if err := h.store.CreateTask(t.Context(), succ, nil); err != nil {
				t.Fatalf("CreateTask(succ): %v", err)
			}
			if succ.BranchName != "issue/main" || succ.AdoptedBranch != tc.adopted {
				t.Fatalf("succ = (%q, adopted %v), want (issue/main, %v)",
					succ.BranchName, succ.AdoptedBranch, tc.adopted)
			}

			h.sched.admit(t.Context())
			if got := h.admitter.ids(); len(got) != 0 {
				t.Fatalf("admitted %v while the predecessor runs, want nothing", got)
			}
			h.queuedUntouched(t, succ.ID)

			// Settled, unarchived, still naming its directory: the successor
			// goes, and receives the directory rather than waiting for it.
			if _, _, err := h.store.TransitionTask(t.Context(), pred.ID,
				store.TaskRunning, tc.settle, store.TaskChange{}); err != nil {
				t.Fatalf("settle pred: %v", err)
			}
			h.sched.admit(t.Context())
			if got := h.admitter.ids(); !slices.Equal(got, []int64{succ.ID}) {
				t.Fatalf("admitted %v after the predecessor settled, want [%d]", got, succ.ID)
			}
		})
	}
}
