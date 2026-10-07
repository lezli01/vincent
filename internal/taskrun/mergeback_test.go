package taskrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
	"github.com/lezli01/vincent/internal/workflow"
)

// mergeBackFixture is an issue with a settled main task holding the main
// worktree, and a side task of it parked at a gate in its own worktree, so a
// test can commit there before approving it to `done`.
type mergeBackFixture struct {
	issue int64
	main  *store.Task
	side  *store.Task
}

func newMergeBackFixture(t *testing.T, h *engineHarness, policy string) mergeBackFixture {
	t.Helper()
	iss := newIssue(t, h)
	h.start(t)
	main := h.settledMainTask(t, iss, "main", quickSnapshot)
	side := h.createTaskWith(t, gatedSnapshot, func(task *store.Task) {
		task.Title, task.Description, task.IssueID = "side work", "Make the lock file go away.", &iss
		task.IssueWorktree, task.MergeOnConflict = store.IssueWorktreeSide, policy
	})
	gated := h.waitForState(t, side.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if gated.State != store.TaskAwaitingGate {
		t.Fatalf("side task = %s (%s: %s), want awaiting_gate", gated.State, gated.BlockReason, gated.BlockDetail)
	}
	return mergeBackFixture{issue: iss, main: main, side: gated}
}

// commit writes file in dir and commits it.
func commitFile(t *testing.T, dir, file, content string) {
	t.Helper()
	testrepo.WriteFile(t, dir, file, content)
	testrepo.Run(t, dir, "add", file)
	testrepo.Run(t, dir, "commit", "-q", "-m", "write "+file)
}

// finishSide approves the side task to done.
func (h *engineHarness) finishSide(t *testing.T, side *store.Task) {
	t.Helper()
	if _, err := h.runner.Approve(t.Context(), side.ID); err != nil {
		t.Fatalf("Approve(side): %v", err)
	}
	done := h.waitForState(t, side.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("side task = %s (%s), want done", done.State, done.BlockReason)
	}
}

// mergeBacks lists the merge-back tasks of source, oldest first.
func (h *engineHarness) mergeBacks(t *testing.T, source int64) []store.Task {
	t.Helper()
	all, err := h.store.ListTasks(t.Context(), store.TaskFilter{ProjectID: h.projectID, Archived: store.ArchivedAll})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var out []store.Task
	for _, task := range all {
		if task.MergeSourceTaskID != nil && *task.MergeSourceTaskID == source {
			out = append(out, task)
		}
	}
	if len(out) > 1 && out[0].ID > out[1].ID {
		out[0], out[1] = out[1], out[0]
	}
	return out
}

func (h *engineHarness) oneMergeBack(t *testing.T, source int64) store.Task {
	t.Helper()
	mbs := h.mergeBacks(t, source)
	if len(mbs) != 1 {
		t.Fatalf("task %d has %d merge-back tasks, want 1", source, len(mbs))
	}
	return mbs[0]
}

// TestSideTaskMergesBackCleanly is decision 11 end to end: a side task with
// a commit reaches done, one merge-back task is inserted with it, receives
// the issue's main worktree and lands a --no-ff merge carrying its own
// message — not a lane's, so `diff?by=lane` is unaffected.
func TestSideTaskMergesBackCleanly(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	commitFile(t, f.side.WorktreePath, "side.txt", "side work\n")
	h.finishSide(t, f.side)

	mb := h.oneMergeBack(t, f.side.ID)
	if mb.IssueWorktree != store.IssueWorktreeMain || mb.WorkflowName != workflow.MergeBackName ||
		mb.MergeOnConflict != store.MergeOnConflictBlock || mb.BranchName != f.main.BranchName ||
		mb.Title != workflow.MergeBackTitle(f.side.ID, f.issue) {
		t.Fatalf("merge-back = (%q, %q, %q, %q, %q), want a main task on %q",
			mb.IssueWorktree, mb.WorkflowName, mb.MergeOnConflict, mb.BranchName, mb.Title, f.main.BranchName)
	}
	done := h.waitForState(t, mb.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("merge-back = %s (%s: %s), want done", done.State, done.BlockReason, done.BlockDetail)
	}
	if done.WorktreePath != f.main.WorktreePath {
		t.Errorf("merge-back worked in %q, want the issue's main worktree %q", done.WorktreePath, f.main.WorktreePath)
	}
	if !h.fileOnBranch(t, f.main.BranchName, "side.txt") {
		t.Error("side.txt did not reach the issue's main branch")
	}
	subject := testrepo.Run(t, h.repo, "log", "-1", "--format=%s", f.main.BranchName)
	if subject != mb.Title {
		t.Errorf("main branch head = %q, want the merge-back's merge %q", subject, mb.Title)
	}
	if parents := testrepo.Run(t, h.repo, "log", "-1", "--format=%P", f.main.BranchName); len(strings.Fields(parents)) != 2 {
		t.Errorf("main branch head has parents %q, want a --no-ff merge", parents)
	}
}

// TestEmptySideTaskCreatesNoMergeBack is 134.14-b.
func TestEmptySideTaskCreatesNoMergeBack(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	h.finishSide(t, f.side)
	if mbs := h.mergeBacks(t, f.side.ID); len(mbs) != 0 {
		t.Fatalf("an empty side task created %d merge-back tasks, want none", len(mbs))
	}
}

// conflictingMergeBack commits different content to one file on the main
// branch and on the side branch, and returns the merge-back blocked or done.
func conflictingMergeBack(t *testing.T, h *engineHarness, f mergeBackFixture) *store.Task {
	t.Helper()
	commitFile(t, f.main.WorktreePath, "shared.txt", "main\n")
	commitFile(t, f.side.WorktreePath, "shared.txt", "side\n")
	h.finishSide(t, f.side)
	mb := h.oneMergeBack(t, f.side.ID)
	return h.waitForState(t, mb.ID, store.TaskBlocked, store.TaskDone)
}

// TestMergeBackConflictBlocksThenRetryCommitsTheResolution: `block` leaves
// the conflicted merge in the main worktree, which the blocked merge-back
// keeps; a hand resolution plus retry commits it.
func TestMergeBackConflictBlocksThenRetryCommitsTheResolution(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	blocked := conflictingMergeBack(t, h, f)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonMergeConflict {
		t.Fatalf("merge-back = %s/%q, want blocked/%s", blocked.State, blocked.BlockReason, ReasonMergeConflict)
	}
	if blocked.WorktreePath != f.main.WorktreePath {
		t.Fatalf("blocked merge-back names %q, want the main worktree %q", blocked.WorktreePath, f.main.WorktreePath)
	}
	if in, err := h.runner.deps.Worktrees.InMerge(t.Context(), blocked.WorktreePath); err != nil || !in {
		t.Fatalf("InMerge = %v, %v; want the conflicted merge left in place", in, err)
	}

	testrepo.WriteFile(t, blocked.WorktreePath, "shared.txt", "both\n")
	testrepo.Run(t, blocked.WorktreePath, "add", "shared.txt")
	if _, _, err := h.runner.Retry(t.Context(), blocked.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	done := h.waitForState(t, blocked.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("retried merge-back = %s (%s), want done", done.State, done.BlockReason)
	}
	got := testrepo.Run(t, h.repo, "show", f.main.BranchName+":shared.txt")
	if got != "both" {
		t.Errorf("shared.txt on the main branch = %q, want the hand resolution", got)
	}
}

// TestCancelAbortsAConflictedMergeBack is decision 15: cancel aborts the
// merge, and the next main task is admitted into a clean worktree.
func TestCancelAbortsAConflictedMergeBack(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	blocked := conflictingMergeBack(t, h, f)
	if blocked.BlockReason != ReasonMergeConflict {
		t.Fatalf("merge-back = %s/%q, want blocked/%s", blocked.State, blocked.BlockReason, ReasonMergeConflict)
	}
	if _, err := h.runner.Cancel(t.Context(), blocked.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if in, err := h.runner.deps.Worktrees.InMerge(t.Context(), blocked.WorktreePath); err != nil || in {
		t.Fatalf("InMerge after cancel = %v, %v; want the merge aborted", in, err)
	}
	next := h.mainTask(t, f.issue, "next", quickSnapshot, nil)
	done := h.waitForState(t, next.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone || done.WorktreePath != f.main.WorktreePath {
		t.Fatalf("next main task = %s (%s) in %q, want done in %q",
			done.State, done.BlockReason, done.WorktreePath, f.main.WorktreePath)
	}
}

// TestSkipEndsAMergeBackWithoutMerging: skip is §6's ordinary skip.
func TestSkipEndsAMergeBackWithoutMerging(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	commitFile(t, f.side.WorktreePath, "side.txt", "side work\n")
	testrepo.Run(t, h.repo, "branch", "-m", f.side.BranchName, f.side.BranchName+"-gone")
	h.finishSide(t, f.side)
	mb := h.oneMergeBack(t, f.side.ID)
	blocked := h.waitForState(t, mb.ID, store.TaskBlocked, store.TaskDone)
	if blocked.BlockReason != ReasonMergeSourceMissing {
		t.Fatalf("merge-back = %s/%q, want blocked/%s", blocked.State, blocked.BlockReason, ReasonMergeSourceMissing)
	}
	head := testrepo.Run(t, h.repo, "rev-parse", f.main.BranchName)
	if _, err := h.runner.Skip(t.Context(), mb.ID); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	done := h.waitForState(t, mb.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("skipped merge-back = %s (%s), want done", done.State, done.BlockReason)
	}
	if got := testrepo.Run(t, h.repo, "rev-parse", f.main.BranchName); got != head {
		t.Errorf("main branch moved to %s on skip, want it left at %s", got, head)
	}
}

// TestSkipAConflictedMergeBackAbortsItsMerge (review F1 of #771): skip ends a
// merge-back blocked merge_conflict without merging, and the conflicted merge
// it left in the issue's main worktree goes with it, so the next main task is
// handed a clean directory rather than blocking repo_operation_in_progress.
func TestSkipAConflictedMergeBackAbortsItsMerge(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	blocked := conflictingMergeBack(t, h, f)
	if blocked.BlockReason != ReasonMergeConflict {
		t.Fatalf("merge-back = %s/%q, want blocked/%s", blocked.State, blocked.BlockReason, ReasonMergeConflict)
	}
	head := testrepo.Run(t, h.repo, "rev-parse", f.main.BranchName)
	if _, err := h.runner.Skip(t.Context(), blocked.ID); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	done := h.waitForState(t, blocked.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("skipped merge-back = %s (%s), want done", done.State, done.BlockReason)
	}
	if in, err := h.runner.deps.Worktrees.InMerge(t.Context(), blocked.WorktreePath); err != nil || in {
		t.Fatalf("InMerge after skip = %v, %v; want the merge aborted", in, err)
	}
	if got := testrepo.Run(t, h.repo, "rev-parse", f.main.BranchName); got != head {
		t.Errorf("main branch moved to %s on skip, want it left at %s", got, head)
	}
	next := h.mainTask(t, f.issue, "next", quickSnapshot, nil)
	if got := h.waitForState(t, next.ID, store.TaskDone, store.TaskBlocked); got.State != store.TaskDone {
		t.Fatalf("next main task = %s (%s: %s), want done", got.State, got.BlockReason, got.BlockDetail)
	}
}

// TestCancelAbortsARunningMergeBackResolver (review F3 of #771): cancelling a
// merge-back while its `agent` resolver works in the conflicted merge aborts
// that merge once the resolver is gone — decision 15 is about a conflicted
// merge-back, not only a blocked one.
func TestCancelAbortsARunningMergeBackResolver(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictAgent)
	commitFile(t, f.main.WorktreePath, "shared.txt", "main\n")
	commitFile(t, f.side.WorktreePath, "shared.txt", "side\n")
	h.finishSide(t, f.side)
	mb := h.oneMergeBack(t, f.side.ID)
	wait.Until(t, "the merge-back's resolver working in a conflicted merge", func() bool {
		got := h.task(t, mb.ID)
		if got.State != store.TaskRunning || got.WorktreePath == "" {
			return false
		}
		in, err := h.runner.deps.Worktrees.InMerge(t.Context(), got.WorktreePath)
		return err == nil && in
	})
	if _, err := h.runner.Cancel(t.Context(), mb.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	h.waitForActorExit(t, mb.ID)
	wait.Until(t, "the cancelled merge-back's merge aborted", func() bool {
		in, err := h.runner.deps.Worktrees.InMerge(t.Context(), f.main.WorktreePath)
		return err == nil && !in
	})
	t.Setenv("FAKEAGENT_SCENARIO", "")
	next := h.mainTask(t, f.issue, "next", quickSnapshot, nil)
	if got := h.waitForState(t, next.ID, store.TaskDone, store.TaskBlocked); got.State != store.TaskDone {
		t.Fatalf("next main task = %s (%s: %s), want done", got.State, got.BlockReason, got.BlockDetail)
	}
}

// TestDeletedSourceBlocksMergeSourceMissing: the merge-back queues behind
// the main worktree's occupant, and the source row deleted meanwhile blocks
// it once it is admitted.
func TestDeletedSourceBlocksMergeSourceMissing(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	// Holding the main worktree keeps the merge-back queued while the
	// source goes.
	holder := h.mainTask(t, f.issue, "holder", gatedSnapshot, nil)
	h.waitForState(t, holder.ID, store.TaskAwaitingGate)
	commitFile(t, f.side.WorktreePath, "side.txt", "side work\n")
	h.finishSide(t, f.side)
	mb := h.oneMergeBack(t, f.side.ID)
	// Admission: the merge-back waits while another main task occupies the
	// issue's main worktree (decision 11).
	if got := h.task(t, mb.ID); got.State != store.TaskQueued || got.StartedAt != nil {
		t.Fatalf("merge-back = %s (started %v) while the main worktree is occupied, want queued",
			got.State, got.StartedAt)
	}
	h.waitForActorExit(t, f.side.ID)
	if _, _, err := h.runner.Archive(t.Context(), f.side.ID, false); err != nil {
		t.Fatalf("Archive(side): %v", err)
	}
	if err := h.store.DeleteTaskCascade(t.Context(), f.side.ID); err != nil {
		t.Fatalf("DeleteTaskCascade(side): %v", err)
	}
	if _, err := h.runner.Approve(t.Context(), holder.ID); err != nil {
		t.Fatalf("Approve(holder): %v", err)
	}
	blocked := h.waitForState(t, mb.ID, store.TaskBlocked, store.TaskDone)
	if blocked.BlockReason != ReasonMergeSourceMissing || blocked.MergeSourceTaskID != nil {
		t.Fatalf("merge-back = %s/%q source %v, want blocked/%s with no source",
			blocked.State, blocked.BlockReason, blocked.MergeSourceTaskID, ReasonMergeSourceMissing)
	}
}

// TestMergeBackRevivesAnArchivedMainBranch is 134.14-a: every main task
// archived, the merge-back checks the old main branch out fresh and merges
// into it; with that branch deleted too it blocks merge_target_missing.
func TestMergeBackRevivesAnArchivedMainBranch(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "revived"
		if deleted {
			name = "target missing"
		}
		t.Run(name, func(t *testing.T) {
			h := newEngineHarness(t)
			f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
			commitFile(t, f.main.WorktreePath, "main.txt", "main work\n")
			if _, _, err := h.runner.Archive(t.Context(), f.main.ID, false); err != nil {
				t.Fatalf("Archive(main): %v", err)
			}
			if deleted {
				testrepo.Run(t, h.repo, "branch", "-D", f.main.BranchName)
			}
			commitFile(t, f.side.WorktreePath, "side.txt", "side work\n")
			h.finishSide(t, f.side)
			mb := h.oneMergeBack(t, f.side.ID)
			if mb.BranchName != f.main.BranchName {
				t.Fatalf("merge-back branch = %q, want the side task's base %q", mb.BranchName, f.main.BranchName)
			}
			got := h.waitForState(t, mb.ID, store.TaskDone, store.TaskBlocked)
			if deleted {
				if got.State != store.TaskBlocked || got.BlockReason != ReasonMergeTargetMissing {
					t.Fatalf("merge-back = %s/%q, want blocked/%s", got.State, got.BlockReason, ReasonMergeTargetMissing)
				}
				// The documented way past it (review F2 of #771): skip
				// ends the merge-back rather than blocking it again on the
				// branch that is still missing.
				if _, err := h.runner.Skip(t.Context(), mb.ID); err != nil {
					t.Fatalf("Skip: %v", err)
				}
				if got := h.waitForState(t, mb.ID, store.TaskDone, store.TaskBlocked); got.State != store.TaskDone {
					t.Fatalf("skipped merge-back = %s/%q, want done", got.State, got.BlockReason)
				}
				return
			}
			if got.State != store.TaskDone {
				t.Fatalf("merge-back = %s (%s: %s), want done", got.State, got.BlockReason, got.BlockDetail)
			}
			if !h.fileOnBranch(t, f.main.BranchName, "side.txt") || !h.fileOnBranch(t, f.main.BranchName, "main.txt") {
				t.Error("the revived main branch is missing the side's or the archived main task's work")
			}
		})
	}
}

// TestFollowUpOnAMergedSideTaskMergesAgain: the side task's next → done
// creates a new merge-back once the first has settled.
func TestFollowUpOnAMergedSideTaskMergesAgain(t *testing.T) {
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictBlock)
	commitFile(t, f.side.WorktreePath, "side.txt", "side work\n")
	h.finishSide(t, f.side)
	first := h.oneMergeBack(t, f.side.ID)
	if got := h.waitForState(t, first.ID, store.TaskDone, store.TaskBlocked); got.State != store.TaskDone {
		t.Fatalf("first merge-back = %s (%s), want done", got.State, got.BlockReason)
	}
	h.waitForActorExit(t, f.side.ID)
	commitFile(t, f.side.WorktreePath, "more.txt", "more\n")
	if _, err := h.runner.FollowUp(t.Context(), f.side.ID, agentFollowUp(t, "one more thing")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	h.waitForMergeBacks(t, f.side.ID, 2)
	mbs := h.mergeBacks(t, f.side.ID)
	if got := h.waitForState(t, mbs[1].ID, store.TaskDone, store.TaskBlocked); got.State != store.TaskDone {
		t.Fatalf("second merge-back = %s (%s), want done", got.State, got.BlockReason)
	}
	if !h.fileOnBranch(t, f.main.BranchName, "more.txt") {
		t.Error("the follow-up's commit did not reach the main branch")
	}
}

func (h *engineHarness) waitForMergeBacks(t *testing.T, source int64, n int) {
	t.Helper()
	wait.Until(t, "the side task's merge-back tasks", func() bool { return len(h.mergeBacks(t, source)) >= n })
}

// TestMergeBackAgentResolverResolves is `on_conflict: agent`: the built-in
// resolver, given the conflicted files and the side task, resolves it.
func TestMergeBackAgentResolverResolves(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompts.jsonl")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)
	t.Setenv("FAKEAGENT_WRITE_FILE", "shared.txt")
	t.Setenv("FAKEAGENT_WRITE_CONTENT", "resolved\n")
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictAgent)
	done := conflictingMergeBack(t, h, f)
	if done.State != store.TaskDone {
		t.Fatalf("merge-back = %s (%s), want done", done.State, done.BlockReason)
	}
	if got := testrepo.Run(t, h.repo, "show", f.main.BranchName+":shared.txt"); got != "resolved" {
		t.Errorf("shared.txt on the main branch = %q, want the resolver's", got)
	}
	prompts, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("read the resolver's prompt: %v", err)
	}
	for _, want := range []string{"- shared.txt", "side work", "Make the lock file go away."} {
		if !strings.Contains(string(prompts), want) {
			t.Errorf("resolver prompt lacks %q: %s", want, prompts)
		}
	}
}

// TestMergeBackAgentResolverFailingBlocks: a failing resolver falls back to
// the block.
func TestMergeBackAgentResolverFailingBlocks(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "nonzero-exit")
	h := newEngineHarness(t)
	f := newMergeBackFixture(t, h, store.MergeOnConflictAgent)
	blocked := conflictingMergeBack(t, h, f)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonMergeConflict {
		t.Fatalf("merge-back = %s/%q, want blocked/%s", blocked.State, blocked.BlockReason, ReasonMergeConflict)
	}
}
