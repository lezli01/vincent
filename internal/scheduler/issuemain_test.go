package scheduler

import (
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// issue creates an issue in projectID.
func (h *harness) issue(t *testing.T, projectID int64, title string) int64 {
	t.Helper()
	is, err := h.store.CreateIssue(t.Context(), store.NewIssue{ProjectID: projectID, Title: title}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return is.ID
}

// issueTask creates a root task of issueID in role ("main", "side" or "").
//
// A main task after the issue's first joins the first's branch and is bound
// as adopted, so task 125's directory claim would hold it on its own. These
// tests are about the issue predicate (task 134 decisions 7, 8), so every
// such task is moved to a branch of its own after creation: the claim then
// has nothing to see, and only the predicate can hold it.
func (h *harness) issueTask(
	t *testing.T, projectID, issueID int64, title, role string, state store.TaskState, age time.Duration,
	mutate func(*store.Task),
) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: projectID, Title: title,
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: "vincent/" + title,
		State: state, CreatedAt: time.Now().Add(-age),
		IssueID: &issueID, IssueWorktree: role,
	}
	if role == store.IssueWorktreeSide {
		task.MergeOnConflict = store.MergeOnConflictAgent
	}
	if mutate != nil {
		mutate(task)
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask(%s): %v", title, err)
	}
	if task.BranchName != "vincent/"+title {
		task.BranchName = "vincent/" + title
		if err := h.store.UpdateTask(t.Context(), task); err != nil {
			t.Fatalf("UpdateTask(%s): %v", title, err)
		}
	}
	return task
}

// queuedUntouched asserts a held task is still queued and the walk wrote
// nothing to it: no queued_reason, no hold (task 134 decision 10).
func (h *harness) queuedUntouched(t *testing.T, id int64) {
	t.Helper()
	got, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != store.TaskQueued || got.QueuedReason != "" || got.AdmitNotBefore != nil {
		t.Fatalf("held task = (%s, %q, %v), want queued with nothing written",
			got.State, got.QueuedReason, got.AdmitNotBefore)
	}
}

// TestOccupiedIssueHoldsItsMainTask: in every state that occupies an issue's
// main worktree, the issue's next main task stays queued, and the rest of the
// queue is still admitted.
func TestOccupiedIssueHoldsItsMainTask(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	for _, state := range []store.TaskState{
		store.TaskRunning, store.TaskBlocked, store.TaskAwaitingGate, store.TaskPaused,
		store.TaskAwaitingChildren, store.TaskQueued,
	} {
		t.Run(string(state), func(t *testing.T) {
			h := newHarness(t, 10)
			p := h.project(t, "proj", nil)
			is := h.issue(t, p, "the issue")
			occ := h.issueTask(t, p, is, "occupant", store.IssueWorktreeMain, state, 3*time.Minute,
				func(task *store.Task) { task.StartedAt = &started })
			waiter := h.issueTask(t, p, is, "waiter", store.IssueWorktreeMain, store.TaskQueued, 2*time.Minute, nil)
			free := h.task(t, p, "free", store.TaskQueued, 0, time.Minute)

			h.sched.admit(t.Context())

			want := []int64{free.ID}
			if state == store.TaskQueued || state == store.TaskAwaitingChildren {
				// An interrupted occupant is a candidate itself, and is
				// re-admitted ahead of the waiter. So is a parked parent with
				// no live children: the walk re-queues it first (§7.6).
				want = []int64{occ.ID, free.ID}
			}
			if got := h.admitter.ids(); !slices.Equal(got, want) {
				t.Fatalf("admitted %v, want %v", got, want)
			}
			h.queuedUntouched(t, waiter.ID)
		})
	}
}

// TestDoneOccupantWithAnOpenChatHoldsItsIssue: a settled main task whose
// worktree a human is still working in through a linked chat keeps the issue
// occupied; closing the chat releases it on the next walk.
func TestDoneOccupantWithAnOpenChatHoldsItsIssue(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	is := h.issue(t, p, "the issue")
	started := time.Now().Add(-time.Hour)
	occ := h.issueTask(t, p, is, "occupant", store.IssueWorktreeMain, store.TaskDone, 2*time.Minute,
		func(task *store.Task) { task.StartedAt, task.WorktreePath = &started, "/wt/occupant" })
	chat := &store.Chat{Title: "look", Agent: "claude", PermissionMode: "full_auto"}
	if err := h.store.OpenLinkedChat(t.Context(), occ.ID, store.TaskDone, chat); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	waiter := h.issueTask(t, p, is, "waiter", store.IssueWorktreeMain, store.TaskQueued, time.Minute, nil)

	h.sched.admit(t.Context())
	if got := h.admitter.ids(); len(got) != 0 {
		t.Fatalf("admitted %v while the occupant's chat is open, want nothing", got)
	}
	h.queuedUntouched(t, waiter.ID)

	if _, err := h.store.CloseChat(t.Context(), chat.ID); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); !slices.Equal(got, []int64{waiter.ID}) {
		t.Fatalf("admitted %v after the chat closed, want [%d]", got, waiter.ID)
	}
}

// TestIssueMainTasksCountThisWalksAdmissions is #749's shape for the issue
// predicate: two never-started main tasks of one issue both read
// IssueOccupied == false, so the walk's own tally must hold the second. A
// main task of another issue in the same walk is still admitted.
func TestIssueMainTasksCountThisWalksAdmissions(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	is := h.issue(t, p, "the issue")
	other := h.issue(t, p, "another issue")
	first := h.issueTask(t, p, is, "first", store.IssueWorktreeMain, store.TaskQueued, 3*time.Minute, nil)
	second := h.issueTask(t, p, is, "second", store.IssueWorktreeMain, store.TaskQueued, 2*time.Minute, nil)
	elsewhere := h.issueTask(t, p, other, "elsewhere", store.IssueWorktreeMain, store.TaskQueued, time.Minute, nil)

	h.sched.admit(t.Context())
	if got := h.admitter.ids(); !slices.Equal(got, []int64{first.ID, elsewhere.ID}) {
		t.Fatalf("admitted %v, want [%d %d] (one main task per issue per walk)", got, first.ID, elsewhere.ID)
	}
	h.queuedUntouched(t, second.ID)

	// Admitted, the first now occupies the issue from the row itself.
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); len(got) != 2 {
		t.Fatalf("admitted %v, want the second still waiting on the first", got)
	}

	// Once the occupant is done, with no chat open on it, the waiter goes.
	if _, _, err := h.store.TransitionTask(t.Context(), first.ID,
		store.TaskRunning, store.TaskDone, store.TaskChange{}); err != nil {
		t.Fatalf("finish first: %v", err)
	}
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); !slices.Equal(got, []int64{first.ID, elsewhere.ID, second.ID}) {
		t.Fatalf("admitted %v, want the second admitted once the first is done", got)
	}
}

// TestSideAndRolelessTasksIgnoreTheOccupant: only main-role tasks wait on
// the issue's main worktree. Side tasks have worktrees of their own, and a
// task with no role is not on the issue's main line at all.
func TestSideAndRolelessTasksIgnoreTheOccupant(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	is := h.issue(t, p, "the issue")
	started := time.Now().Add(-time.Hour)
	h.issueTask(t, p, is, "occupant", store.IssueWorktreeMain, store.TaskRunning, 3*time.Minute,
		func(task *store.Task) { task.StartedAt = &started })
	side := h.issueTask(t, p, is, "side", store.IssueWorktreeSide, store.TaskQueued, 2*time.Minute, nil)
	roleless := h.issueTask(t, p, is, "roleless", "", store.TaskQueued, time.Minute, nil)

	h.sched.admit(t.Context())
	if got := h.admitter.ids(); !slices.Equal(got, []int64{side.ID, roleless.ID}) {
		t.Fatalf("admitted %v, want [%d %d]", got, side.ID, roleless.ID)
	}
}

// TestRecoveredOccupantIsReadmittedAheadOfTheWaiter: §12.4 recovery returns
// a running occupant to the queue with `started_at` kept, so it still holds
// its issue. It excludes itself, so it is re-admitted, and the waiter is not.
func TestRecoveredOccupantIsReadmittedAheadOfTheWaiter(t *testing.T) {
	h := newHarness(t, 10)
	p := h.project(t, "proj", nil)
	is := h.issue(t, p, "the issue")
	occ := h.issueTask(t, p, is, "occupant", store.IssueWorktreeMain, store.TaskQueued, 3*time.Minute, nil)
	h.sched.admit(t.Context())
	if got := h.admitter.ids(); !slices.Equal(got, []int64{occ.ID}) {
		t.Fatalf("admitted %v, want the occupant", got)
	}
	// Created after the occupant was admitted, and older in queue order, so
	// it is walked first and must be the one held.
	waiter := h.issueTask(t, p, is, "waiter", store.IssueWorktreeMain, store.TaskQueued, 10*time.Minute, nil)

	// The crash: recovery interrupts the running task back to the queue.
	if _, _, err := h.store.InterruptTask(t.Context(), occ.ID,
		store.TaskRunning, store.TaskQueued, "interrupted"); err != nil {
		t.Fatalf("InterruptTask: %v", err)
	}
	if got, err := h.store.GetTask(t.Context(), occ.ID); err != nil || got.StartedAt == nil {
		t.Fatalf("recovered occupant = %+v, %v; want started_at kept", got, err)
	}

	h.sched.admit(t.Context())
	if got := h.admitter.ids(); !slices.Equal(got, []int64{occ.ID, occ.ID}) {
		t.Fatalf("admitted %v, want the occupant re-admitted and the waiter held", got)
	}
	h.queuedUntouched(t, waiter.ID)
}
