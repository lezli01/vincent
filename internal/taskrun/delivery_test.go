package taskrun

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// tip is the commit branch points at in the harness repository.
func (h *engineHarness) tip(t *testing.T, branch string) string {
	t.Helper()
	return strings.TrimSpace(testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+branch))
}

// TestImplicitDeliveryAtDone: a run that records nothing finishes with an
// implicit delivery written by its `→ done` (task 137 decision 3): `change`
// at the branch tip when the branch holds a commit its base does not, titled
// and described by the task.
func TestImplicitDeliveryAtDone(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTaskWith(t, gatedSnapshot, func(task *store.Task) {
		task.Title, task.Description = "Fix the lock leak", "The lock file outlives the process."
	})
	gated := h.waitForState(t, task.ID, store.TaskAwaitingGate, store.TaskBlocked)
	if gated.State != store.TaskAwaitingGate {
		t.Fatalf("task = %s (%s), want awaiting_gate", gated.State, gated.BlockReason)
	}
	commitFile(t, gated.WorktreePath, "fix.txt", "fixed\n")
	if _, err := h.runner.Approve(t.Context(), task.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	d := done.Delivery
	if d == nil {
		t.Fatal("a finished task has no delivery")
	}
	if !d.Implicit || d.Kind != store.DeliveryChange {
		t.Errorf("delivery = (implicit %v, kind %q), want an implicit change", d.Implicit, d.Kind)
	}
	if d.Title != "Fix the lock leak" || d.Body != "The lock file outlives the process." {
		t.Errorf("delivery text = (%q, %q), want the task's", d.Title, d.Body)
	}
	if d.Branch != done.BranchName || d.Base != "main" || d.HeadSHA != h.tip(t, done.BranchName) {
		t.Errorf("delivery at (%q onto %q @ %s), want %q onto main @ %s",
			d.Branch, d.Base, d.HeadSHA, done.BranchName, h.tip(t, done.BranchName))
	}
	if d.RecordedAt.IsZero() {
		t.Error("delivery is unstamped")
	}
}

// TestImplicitDeliveryWithoutCommitsIsNone: a run that changed nothing
// delivered nothing to integrate.
func TestImplicitDeliveryWithoutCommitsIsNone(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, quickSnapshot)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.Delivery == nil || done.Delivery.Kind != store.DeliveryNone || !done.Delivery.Implicit {
		t.Fatalf("delivery = %+v, want an implicit none", done.Delivery)
	}
}

// TestBuildDeliveryKeepsWhatTheRunRecorded: a delivery the run recorded is
// kept as written, and only what it left empty — the branch, the base, the
// head — is filled.
func TestBuildDeliveryKeepsWhatTheRunRecorded(t *testing.T) {
	h := newEngineHarness(t)
	testrepo.Run(t, h.repo, "branch", "answered", "main")
	project, err := h.store.GetProject(t.Context(), h.projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		ID: 1, ProjectID: h.projectID, Title: "a question", BranchName: "answered", BaseBranch: "main",
		Delivery: &store.Delivery{Kind: store.DeliveryReply, Title: "Answered in the docs", Body: "See §7."},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now().UTC()
	d := buildDelivery(t.Context(), h.store, h.runner.deps.Worktrees, project, task, now, log)
	if d.Kind != store.DeliveryReply || d.Title != "Answered in the docs" || d.Body != "See §7." || d.Implicit {
		t.Errorf("delivery = %+v, want the recorded reply kept", d)
	}
	if d.Branch != "answered" || d.Base != "main" || d.HeadSHA != h.tip(t, "answered") || !d.RecordedAt.Equal(now) {
		t.Errorf("delivery not filled from the task: %+v", d)
	}
	if task.Delivery.Branch != "" {
		t.Error("buildDelivery wrote through to the task's own delivery")
	}
}

// TestMergeBackDeliversItsSource: a merge-back's delivery is its side task's
// — the work, not "Merge task N" — on the issue's main branch at the merge
// commit, onto the base the main line was cut from; and it is the issue's
// latest delivery.
func TestMergeBackDeliversItsSource(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	commitFile(t, f.side.WorktreePath, "side.txt", "side work\n")
	h.finishSide(t, f.side)
	side := h.task(t, f.side.ID)
	if side.Delivery == nil || side.Delivery.Branch != side.BranchName || side.Delivery.Kind != store.DeliveryChange {
		t.Fatalf("side delivery = %+v, want a change on %q", side.Delivery, side.BranchName)
	}
	mb := h.oneMergeBack(t, f.side.ID)
	done := h.waitForState(t, mb.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("merge-back = %s (%s)", done.State, done.BlockReason)
	}
	d := done.Delivery
	if d == nil {
		t.Fatal("merge-back has no delivery")
	}
	if d.Title != "side work" || d.Body != "Make the lock file go away." || d.Kind != store.DeliveryChange {
		t.Errorf("merge-back delivery = (%q, %q, %q), want the side task's", d.Title, d.Body, d.Kind)
	}
	if d.Branch != f.main.BranchName || d.Base != f.main.BaseBranch || d.HeadSHA != h.tip(t, f.main.BranchName) {
		t.Errorf("merge-back delivery at (%q onto %q @ %s), want %q onto %q @ the merge %s",
			d.Branch, d.Base, d.HeadSHA, f.main.BranchName, f.main.BaseBranch, h.tip(t, f.main.BranchName))
	}
	iss, err := h.store.GetIssue(t.Context(), f.issue)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Delivery == nil || iss.Delivery.HeadSHA != d.HeadSHA {
		t.Errorf("issue delivery = %+v, want the merge-back's", iss.Delivery)
	}
}

// TestBackfillDeliveries: a finished issue task with no delivery — one that
// finished before deliveries existed — is given the delivery its `→ done`
// would have written, once.
func TestBackfillDeliveries(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	testrepo.Run(t, h.repo, "branch", "legacy-empty", "main")
	testrepo.Run(t, h.repo, "checkout", "-q", "-b", "legacy-work", "main")
	commitFile(t, h.repo, "legacy.txt", "work\n")
	testrepo.Run(t, h.repo, "checkout", "-q", "main")
	finished := time.Now().UTC().Add(-24 * time.Hour)
	mk := func(branch string) *store.Task {
		t.Helper()
		task := &store.Task{
			ProjectID: h.projectID, Title: "legacy " + branch, WorkflowName: "test",
			WorkflowSnapshot: quickSnapshot, BaseBranch: "main", BranchName: branch,
			State: store.TaskDone, IssueID: &iss, FinishedAt: &finished,
		}
		if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	empty, work, gone := mk("legacy-empty"), mk("legacy-work"), mk("legacy-deleted")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n, err := BackfillDeliveries(t.Context(), h.runner.deps, log)
	if err != nil || n != 3 {
		t.Fatalf("BackfillDeliveries = %d, %v; want 3", n, err)
	}
	for _, c := range []struct {
		task *store.Task
		want store.DeliveryKind
	}{{empty, store.DeliveryNone}, {work, store.DeliveryChange}, {gone, store.DeliveryNone}} {
		got := h.task(t, c.task.ID)
		if got.Delivery == nil || got.Delivery.Kind != c.want || !got.Delivery.Implicit {
			t.Errorf("%s: delivery = %+v, want an implicit %s", c.task.BranchName, got.Delivery, c.want)
		}
	}
	if n, err := BackfillDeliveries(t.Context(), h.runner.deps, log); err != nil || n != 0 {
		t.Fatalf("second BackfillDeliveries = %d, %v; want 0", n, err)
	}
}

// TestEmptySideTaskDeliversNone: a side task whose tip is still its base_sha
// delivered nothing, and the issue's delivery stays its main line's.
func TestEmptySideTaskDeliversNone(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	h.finishSide(t, f.side)
	side := h.task(t, f.side.ID)
	if side.Delivery == nil || side.Delivery.Kind != store.DeliveryNone {
		t.Fatalf("empty side task delivery = %+v, want none", side.Delivery)
	}
	iss, err := h.store.GetIssue(t.Context(), f.issue)
	if err != nil {
		t.Fatal(err)
	}
	mainTask := h.task(t, f.main.ID)
	if iss.Delivery == nil || mainTask.Delivery == nil || !iss.Delivery.RecordedAt.Equal(mainTask.Delivery.RecordedAt) {
		t.Fatalf("issue delivery = %+v, want the main task's %+v", iss.Delivery, mainTask.Delivery)
	}
}
