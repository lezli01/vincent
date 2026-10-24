package taskrun

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

const (
	// explicitSnapshot is quickSnapshot under `hand_off: explicit`.
	explicitSnapshot = `name: quick
hand_off: explicit
steps:
  - id: noop
    type: command
    run: exit 0
`
	// explicitGatedSnapshot is gatedSnapshot under `hand_off: explicit`.
	explicitGatedSnapshot = `name: gated
hand_off: explicit
steps:
  - id: gate
    type: manual
    instructions: Hold the working directory.
`
	// explicitSlowSnapshot runs long enough for a test to record a delivery
	// while its step runs, the way `vincent task deliver --ready` would.
	explicitSlowSnapshot = `name: slow
hand_off: explicit
steps:
  - id: work
    type: command
    run: sleep 3
`
)

// wantReady fails unless d carries the readiness want, by.
func wantReady(t *testing.T, what string, d *store.Delivery, want bool, by store.ReadyBy) {
	t.Helper()
	if d == nil || d.Ready == nil || *d.Ready != want || d.ReadyBy != by {
		t.Fatalf("%s delivery = %+v, want ready %v by %q", what, d, want, by)
	}
}

// TestDoneStampsReadinessFromHandOff: a task's `→ done` stores readiness
// explicitly (task 137 decision 13) — ready by the workflow under the
// default `auto`, not ready under `explicit`.
func TestDoneStampsReadinessFromHandOff(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	for _, c := range []struct {
		snapshot string
		want     bool
		by       store.ReadyBy
	}{
		{quickSnapshot, true, store.ReadyByWorkflow},
		{explicitSnapshot, false, ""},
	} {
		task := h.createTask(t, c.snapshot)
		done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
		if done.State != store.TaskDone {
			t.Fatalf("task = %s (%s)", done.State, done.BlockReason)
		}
		wantReady(t, done.WorkflowName, done.Delivery, c.want, c.by)
	}
}

// TestDeliverReadyStandsUnderExplicit: a step's `deliver --ready` — stored
// while the task runs — makes an explicit workflow's delivery ready by the
// task.
func TestDeliverReadyStandsUnderExplicit(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, explicitSlowSnapshot)
	h.waitForState(t, task.ID, store.TaskRunning)
	ready := true
	if _, err := h.store.SetTaskDelivery(t.Context(), task.ID, &store.Delivery{
		Kind: store.DeliveryReply, Title: "Answered", Ready: &ready,
	}); err != nil {
		t.Fatalf("SetTaskDelivery: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s)", done.State, done.BlockReason)
	}
	wantReady(t, "deliver --ready", done.Delivery, true, store.ReadyByTask)
	if done.Delivery.Title != "Answered" {
		t.Errorf("delivery title = %q, want the recorded one", done.Delivery.Title)
	}
}

// TestHandOffReadFromTheSnapshot: the hand_off that decides is the one the
// task ran under, whatever the live workflow file says by the time it ends.
func TestHandOffReadFromTheSnapshot(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTaskWith(t, explicitGatedSnapshot, func(task *store.Task) { task.WorkflowName = "gated" })
	gated := h.waitForState(t, task.ID, store.TaskAwaitingGate, store.TaskBlocked)
	if gated.State != store.TaskAwaitingGate {
		t.Fatalf("task = %s (%s), want awaiting_gate", gated.State, gated.BlockReason)
	}
	testrepo.WriteFile(t, h.repo, ".vincent/workflows/gated.yaml",
		"name: gated\nhand_off: auto\nsteps:\n  - id: gate\n    type: manual\n")
	if _, err := h.runner.Approve(t.Context(), task.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	wantReady(t, "edited workflow", done.Delivery, false, "")
}

// TestMergeBackInheritsReadiness: a merge-back, whose __merge_back workflow
// has no hand_off, carries its side task's readiness — ready or not.
func TestMergeBackInheritsReadiness(t *testing.T) {
	for _, c := range []struct {
		name, snapshot string
		want           bool
		by             store.ReadyBy
	}{
		{"auto", gatedSnapshot, true, store.ReadyByWorkflow},
		{"explicit", explicitGatedSnapshot, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newEngineHarness(t)
			iss := newIssue(t, h)
			h.start(t)
			h.settledMainTask(t, iss, "main", quickSnapshot)
			side := h.createTaskWith(t, c.snapshot, func(task *store.Task) {
				task.Title, task.IssueID, task.IssueWorktree = "side work", &iss, store.IssueWorktreeSide
				task.MergeOnConflict = store.MergeOnConflictBlock
			})
			gated := h.waitForState(t, side.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
			if gated.State != store.TaskAwaitingGate {
				t.Fatalf("side task = %s (%s), want awaiting_gate", gated.State, gated.BlockReason)
			}
			commitFile(t, gated.WorktreePath, "side.txt", "side work\n")
			h.finishSide(t, gated)
			wantReady(t, "side", h.task(t, side.ID).Delivery, c.want, c.by)
			mb := h.oneMergeBack(t, side.ID)
			done := h.waitForState(t, mb.ID, store.TaskDone, store.TaskBlocked)
			if done.State != store.TaskDone {
				t.Fatalf("merge-back = %s (%s)", done.State, done.BlockReason)
			}
			wantReady(t, "merge-back", done.Delivery, c.want, c.by)
		})
	}
}

// TestBackfillLeavesReadinessAlone: the backfill supplies a legacy task's
// text, never a decision — an unmarked one keeps no readiness (it reads
// ready, the rule it finished under), and one a human marked since keeps
// the mark, with its title filled from the task.
func TestBackfillLeavesReadinessAlone(t *testing.T) {
	h := newEngineHarness(t)
	finished := time.Now().UTC().Add(-24 * time.Hour)
	mk := func(iss int64, title, branch string) *store.Task {
		t.Helper()
		task := &store.Task{
			ProjectID: h.projectID, Title: title, Description: "legacy work", WorkflowName: "quick",
			WorkflowSnapshot: explicitSnapshot, BaseBranch: "main", BranchName: branch,
			State: store.TaskDone, IssueID: &iss, FinishedAt: &finished,
		}
		if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	plain := mk(newIssue(t, h), "legacy plain", "legacy-plain")
	markedIssue := newIssue(t, h)
	marked := mk(markedIssue, "legacy marked", "legacy-marked")
	if _, err := h.store.MarkIssueUnready(t.Context(), markedIssue, issuestate.Human); err != nil {
		t.Fatalf("MarkIssueUnready: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if n, err := BackfillDeliveries(t.Context(), h.runner.deps, log); err != nil || n != 2 {
		t.Fatalf("BackfillDeliveries = %d, %v; want 2", n, err)
	}
	if d := h.task(t, plain.ID).Delivery; d == nil || d.Kind == "" || d.Ready != nil || d.ReadyBy != "" {
		t.Errorf("plain legacy delivery = %+v, want one with no readiness", d)
	}
	d := h.task(t, marked.ID).Delivery
	wantReady(t, "marked legacy", d, false, store.ReadyByHuman)
	if d.Kind == "" || d.Title != "legacy marked" || d.Body != "legacy work" {
		t.Errorf("marked legacy delivery = %+v, want its text filled from the task", d)
	}
}
