package taskrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/pathx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestSecondMainTaskOfAnIssueRunsOnItsMainBranch is review F1 of #768 end to
// end, as 134.12 left it: two main tasks of one issue, both queued before the
// scheduler walks. The second is bound to the first's branch (task 134
// decision 3) without adopting it. Its higher priority admits it first, so it
// cuts the issue's branch; the first waits in the queue while the second
// occupies the main worktree, and once the second settles the first receives
// that same directory and runs on the same branch.
func TestSecondMainTaskOfAnIssueRunsOnItsMainBranch(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	first := h.mainTask(t, iss, "first", quickSnapshot, nil)
	second := h.mainTask(t, iss, "second", gatedSnapshot, func(task *store.Task) { task.Priority = 1 })
	if second.BranchName != first.BranchName || second.AdoptedBranch || first.AdoptedBranch {
		t.Fatalf("second = (%q, adopted %v), first = (%q, adopted %v); want one branch, adopted by neither",
			second.BranchName, second.AdoptedBranch, first.BranchName, first.AdoptedBranch)
	}
	h.start(t)

	gate := h.waitForState(t, second.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if gate.State != store.TaskAwaitingGate {
		t.Fatalf("second task = %s (%s), want awaiting_gate", gate.State, gate.BlockReason)
	}
	if got := h.task(t, first.ID); got.State != store.TaskQueued {
		t.Fatalf("first task = %s (%s), want queued behind the second's main worktree", got.State, got.BlockReason)
	}
	if _, err := h.runner.Approve(t.Context(), second.ID); err != nil {
		t.Fatalf("Approve(second): %v", err)
	}

	done := h.waitForState(t, first.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("first task = %s (%s), want done", done.State, done.BlockReason)
	}
	if done.WorktreePath != gate.WorktreePath {
		t.Errorf("first task works in %q, want the directory the second handed on, %q",
			done.WorktreePath, gate.WorktreePath)
	}
	if head := testrepo.Run(t, done.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); head != first.BranchName {
		t.Errorf("first task works on %q, want the issue's main branch %q", head, first.BranchName)
	}
}

// TestMainWorktreeIsHandedOnWithItsUncommittedWork is the hand-over itself
// (134.12): the successor receives the settled predecessor's directory — the
// same path, uncommitted work and all — and from then on exactly one row
// names it. The predecessor records the tip as end_sha and the successor
// starts from it, with no base refresh. Archiving the predecessor afterwards
// succeeds with nothing to remove, and leaves the shared branch and the
// successor's directory where they are.
func TestMainWorktreeIsHandedOnWithItsUncommittedWork(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	h.start(t)
	first := h.settledMainTask(t, iss, "first", quickSnapshot)
	testrepo.WriteFile(t, first.WorktreePath, "wip.txt", "not committed yet\n")
	tip := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+first.BranchName)

	second := h.mainTask(t, iss, "second", gatedSnapshot, nil)
	got := h.waitForState(t, second.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if got.State != store.TaskAwaitingGate {
		t.Fatalf("second task = %s (%s: %s), want awaiting_gate", got.State, got.BlockReason, got.BlockDetail)
	}
	if got.WorktreePath != first.WorktreePath {
		t.Fatalf("second task works in %q, want its predecessor's %q", got.WorktreePath, first.WorktreePath)
	}
	if b, err := os.ReadFile(filepath.Join(got.WorktreePath, "wip.txt")); err != nil || string(b) != "not committed yet\n" {
		t.Errorf("the predecessor's uncommitted work did not come with the directory: %q, %v", b, err)
	}
	if got.BaseSHA != tip || got.BaseRefresh != nil {
		t.Errorf("second base = (%q, %+v), want the tip %s and no refresh", got.BaseSHA, got.BaseRefresh, tip)
	}
	pred := h.task(t, first.ID)
	if pred.WorktreePath != "" || pred.EndSHA != tip {
		t.Errorf("predecessor = (path %q, end_sha %q), want no path and end_sha %s", pred.WorktreePath, pred.EndSHA, tip)
	}
	h.assertOneClaim(t, got.WorktreePath, second.ID)

	archived, branch, err := h.runner.Archive(t.Context(), first.ID, false)
	if err != nil {
		t.Fatalf("Archive(handed-on predecessor): %v", err)
	}
	if archived.State != store.TaskArchived || archived.EndSHA != tip {
		t.Errorf("archived predecessor = (%s, end_sha %q), want archived and end_sha kept at %s",
			archived.State, archived.EndSHA, tip)
	}
	if branch.Result != worktree.BranchNotOurs {
		t.Errorf("predecessor's branch outcome = %+v, want %q while the successor carries it",
			branch, worktree.BranchNotOurs)
	}
	if _, err := os.Stat(filepath.Join(got.WorktreePath, "wip.txt")); err != nil {
		t.Errorf("archiving the predecessor touched the successor's directory: %v", err)
	}
	testrepo.Run(t, h.repo, "rev-parse", "--verify", "refs/heads/"+first.BranchName)
	h.assertOneClaim(t, got.WorktreePath, second.ID)
}

// TestLastMainTaskArchiveAppliesTheEmptyBranchRule is decision 17.2: the
// issue's branch goes with the archive of the last main task carrying it,
// and only by task 008's empty-branch rule — judged against the issue's base
// branch, so a last task that committed nothing of its own keeps a branch
// its predecessors committed to. The archive stamps end_sha, which no
// transfer did for the last task.
func TestLastMainTaskArchiveAppliesTheEmptyBranchRule(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first string
		want  string
	}{
		{"empty branch is deleted", quickSnapshot, worktree.BranchDeleted},
		{"predecessor's commit keeps it", committingSnapshot, worktree.BranchHasCommits},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEngineHarness(t)
			iss := newIssue(t, h)
			h.start(t)
			first := h.settledMainTask(t, iss, "first", tc.first)
			second := h.settledMainTask(t, iss, "second", quickSnapshot)
			if second.WorktreePath != first.WorktreePath {
				t.Fatalf("second works in %q, want %q", second.WorktreePath, first.WorktreePath)
			}
			tip := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+first.BranchName)
			if _, _, err := h.runner.Archive(t.Context(), first.ID, false); err != nil {
				t.Fatalf("Archive(first): %v", err)
			}
			archived, branch, err := h.runner.Archive(t.Context(), second.ID, false)
			if err != nil {
				t.Fatalf("Archive(second): %v", err)
			}
			if archived.EndSHA != tip {
				t.Errorf("last task's end_sha = %q, want the tip %s stamped on archive", archived.EndSHA, tip)
			}
			if branch.Result != tc.want {
				t.Errorf("last task's branch outcome = %+v, want %q", branch, tc.want)
			}
			if _, err := os.Stat(second.WorktreePath); !os.IsNotExist(err) {
				t.Errorf("the issue's worktree survived the last archive (stat err %v)", err)
			}
		})
	}
}

// TestHalfFinishedRebaseInTheMainWorktreeBlocksTheSuccessor: a git operation
// left partway through is not handed on (134.12). The successor blocks
// repo_operation_in_progress and the predecessor keeps the directory. The
// rebase is a hand-built marker, as TestInProgressOp's are, because that is
// the form that behaves the same on all three platforms.
func TestHalfFinishedRebaseInTheMainWorktreeBlocksTheSuccessor(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	h.start(t)
	first := h.settledMainTask(t, iss, "first", quickSnapshot)
	gitDir := testrepo.Run(t, first.WorktreePath, "rev-parse", "--absolute-git-dir")
	if err := os.MkdirAll(filepath.Join(filepath.FromSlash(gitDir), "rebase-merge"), 0o700); err != nil {
		t.Fatal(err)
	}

	second := h.mainTask(t, iss, "second", gatedSnapshot, nil)
	got := h.waitForState(t, second.ID, store.TaskBlocked, store.TaskAwaitingGate, store.TaskDone)
	if got.State != store.TaskBlocked || got.BlockReason != worktree.ReasonRepoOperationInProgress {
		t.Fatalf("second task = %s (%s), want blocked %s",
			got.State, got.BlockReason, worktree.ReasonRepoOperationInProgress)
	}
	if got.WorktreePath != "" {
		t.Errorf("blocked successor names %q, want nothing", got.WorktreePath)
	}
	if pred := h.task(t, first.ID); pred.WorktreePath != first.WorktreePath || pred.EndSHA != "" {
		t.Errorf("predecessor = (path %q, end_sha %q), want it to keep %q untouched",
			pred.WorktreePath, pred.EndSHA, first.WorktreePath)
	}
}

// TestMainTaskAfterAnArchivedPredecessorGetsAFreshWorktree is case 3: every
// main task that held a directory is archived, but the successor still
// carries the branch. It gets a fresh worktree on that branch, records the
// tip as base_sha with no refresh, and the branch is vincent's own.
func TestMainTaskAfterAnArchivedPredecessorGetsAFreshWorktree(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	h.start(t)
	first := h.settledMainTask(t, iss, "first", committingSnapshot)
	second := h.mainTask(t, iss, "second", gatedSnapshot, func(task *store.Task) { task.State = store.TaskPaused })
	if _, _, err := h.runner.Archive(t.Context(), first.ID, false); err != nil {
		t.Fatalf("Archive(first): %v", err)
	}
	tip := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+first.BranchName)

	if _, err := h.runner.Resume(t.Context(), second.ID); err != nil {
		t.Fatalf("Resume(second): %v", err)
	}
	got := h.waitForState(t, second.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if got.State != store.TaskAwaitingGate {
		t.Fatalf("second task = %s (%s: %s), want awaiting_gate", got.State, got.BlockReason, got.BlockDetail)
	}
	if got.WorktreePath == "" || got.WorktreePath == first.WorktreePath || pathx.SameDir(got.WorktreePath, h.repo) {
		t.Errorf("second task works in %q, want a fresh worktree of its own", got.WorktreePath)
	}
	if head := testrepo.Run(t, got.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); head != first.BranchName {
		t.Errorf("second task works on %q, want the issue's main branch %q", head, first.BranchName)
	}
	if got.BaseSHA != tip || got.BaseRefresh != nil {
		t.Errorf("second base = (%q, %+v), want the tip %s and no refresh", got.BaseSHA, got.BaseRefresh, tip)
	}
	if !h.runner.branchOurs(t.Context(), got, h.runner.deps.Logger) {
		t.Error("the issue's main branch is not vincent's to delete for its last main task")
	}
	h.assertOneClaim(t, got.WorktreePath, second.ID)
}

// TestMainTaskBlocksWhenItsBranchIsInTheMainCheckout is decision 16: the
// issue's main branch checked out in the human's checkout blocks
// issue_branch_checked_out, and the task never runs in the project path.
func TestMainTaskBlocksWhenItsBranchIsInTheMainCheckout(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	h.start(t)
	first := h.settledMainTask(t, iss, "first", quickSnapshot)
	second := h.mainTask(t, iss, "second", gatedSnapshot, func(task *store.Task) { task.State = store.TaskPaused })
	if _, _, err := h.runner.Archive(t.Context(), first.ID, false); err != nil {
		t.Fatalf("Archive(first): %v", err)
	}
	testrepo.Run(t, h.repo, "checkout", "-q", first.BranchName)

	if _, err := h.runner.Resume(t.Context(), second.ID); err != nil {
		t.Fatalf("Resume(second): %v", err)
	}
	got := h.waitForState(t, second.ID, store.TaskBlocked, store.TaskAwaitingGate, store.TaskDone)
	if got.State != store.TaskBlocked || got.BlockReason != worktree.ReasonIssueBranchCheckedOut {
		t.Fatalf("second task = %s (%s), want blocked %s",
			got.State, got.BlockReason, worktree.ReasonIssueBranchCheckedOut)
	}
	if got.WorktreePath != "" {
		t.Errorf("blocked task names %q, want nothing — never the project path", got.WorktreePath)
	}
	if runs := h.stepRuns(t, second.ID); len(runs) != 0 {
		t.Errorf("blocked task has %d step runs, want none", len(runs))
	}
}

// TestLegacyAdoptedMainTaskRoutesAsMain: a main row bound before 134.12
// carries adopted_branch = 1. The role wins — it receives its predecessor's
// directory rather than adopting the branch — and its branch is vincent's.
func TestLegacyAdoptedMainTaskRoutesAsMain(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	h.start(t)
	first := h.settledMainTask(t, iss, "first", quickSnapshot)
	second := h.mainTask(t, iss, "second", gatedSnapshot, func(task *store.Task) { task.AdoptedBranch = true })
	if !second.AdoptedBranch {
		t.Fatal("fixture: the legacy row lost its adopted_branch flag")
	}
	got := h.waitForState(t, second.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if got.State != store.TaskAwaitingGate || got.WorktreePath != first.WorktreePath {
		t.Fatalf("legacy main task = %s (%s) in %q, want awaiting_gate in its predecessor's %q",
			got.State, got.BlockReason, got.WorktreePath, first.WorktreePath)
	}
	if _, err := h.runner.Cancel(t.Context(), second.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	h.waitForState(t, second.ID, store.TaskAborted)
	if _, _, err := h.runner.Archive(t.Context(), first.ID, false); err != nil {
		t.Fatalf("Archive(first): %v", err)
	}
	if !h.runner.branchOurs(t.Context(), h.task(t, second.ID), h.runner.deps.Logger) {
		t.Error("a legacy adopted main task's branch is not vincent's")
	}
}

// TestCrashAfterTheHandOverResumesInTheTransferredDirectory: the transfer is
// one transaction, so a daemon that dies after it and before the first step
// leaves the successor naming the directory. Recovery re-queues it, and
// re-admission returns early from ensureWorktree and runs in that directory.
func TestCrashAfterTheHandOverResumesInTheTransferredDirectory(t *testing.T) {
	h := newEngineHarness(t)
	iss := newIssue(t, h)
	h.start(t)
	first := h.settledMainTask(t, iss, "first", quickSnapshot)
	testrepo.WriteFile(t, first.WorktreePath, "wip.txt", "kept\n")
	second := h.mainTask(t, iss, "second", gatedSnapshot, func(task *store.Task) { task.State = store.TaskPaused })
	tip := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/"+first.BranchName)

	// What the crashed admission got as far as: admitted, and the directory
	// handed over, with no step run yet.
	if err := h.store.TransferIssueWorktree(t.Context(), first.ID, second.ID, first.WorktreePath, tip); err != nil {
		t.Fatalf("TransferIssueWorktree: %v", err)
	}
	if _, _, err := h.store.TransitionTask(t.Context(), second.ID, store.TaskPaused, store.TaskRunning,
		store.TaskChange{}); err != nil {
		t.Fatalf("TransitionTask(paused → running): %v", err)
	}
	if n, err := Recover(t.Context(), h.store, h.runner.deps.Logger); err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v; want the successor re-queued", n, err)
	}

	got := h.waitForState(t, second.ID, store.TaskAwaitingGate, store.TaskBlocked, store.TaskDone)
	if got.State != store.TaskAwaitingGate || got.WorktreePath != first.WorktreePath {
		t.Fatalf("recovered successor = %s (%s) in %q, want awaiting_gate in %q",
			got.State, got.BlockReason, got.WorktreePath, first.WorktreePath)
	}
	if _, err := os.Stat(filepath.Join(got.WorktreePath, "wip.txt")); err != nil {
		t.Errorf("the handed-on work is gone after recovery: %v", err)
	}
	h.assertOneClaim(t, got.WorktreePath, second.ID)
}

const (
	quickSnapshot = `name: quick
steps:
  - id: noop
    type: command
    run: exit 0
`
	gatedSnapshot = `name: gated
steps:
  - id: gate
    type: manual
    instructions: Hold the working directory.
`
	// A body in the sh ∩ pwsh intersection (CLAUDE.md): one commit on the
	// branch, so the branch carries something past its base.
	committingSnapshot = `name: committing
steps:
  - id: commit
    type: command
    run: git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m work
`
)

func newIssue(t *testing.T, h *engineHarness) int64 {
	t.Helper()
	iss, err := h.store.CreateIssue(t.Context(),
		store.NewIssue{ProjectID: h.projectID, Title: "Lock file leaks"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return iss.ID
}

// mainTask creates a main-role task of the issue.
func (h *engineHarness) mainTask(
	t *testing.T, issueID int64, title, snapshot string, mutate func(*store.Task),
) *store.Task {
	t.Helper()
	return h.createTaskWith(t, snapshot, func(task *store.Task) {
		task.Title, task.IssueID, task.IssueWorktree = title, &issueID, store.IssueWorktreeMain
		if mutate != nil {
			mutate(task)
		}
	})
}

// settledMainTask creates a main-role task and waits for it to finish done,
// its actor gone, holding the issue's main worktree.
func (h *engineHarness) settledMainTask(t *testing.T, issueID int64, title, snapshot string) *store.Task {
	t.Helper()
	task := h.mainTask(t, issueID, title, snapshot, nil)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone || done.WorktreePath == "" {
		t.Fatalf("%s = %s (%s: %s) in %q, want done in a worktree",
			title, done.State, done.BlockReason, done.BlockDetail, done.WorktreePath)
	}
	h.waitForActorExit(t, task.ID)
	return done
}

func (h *engineHarness) task(t *testing.T, id int64) *store.Task {
	t.Helper()
	got, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return got
}

// assertOneClaim is the §10 reclaimer's view after a hand-over: exactly one
// row, owner's, names path, path is on disk, and every directory under the
// worktree root is claimed — no orphan and no missing claim.
func (h *engineHarness) assertOneClaim(t *testing.T, path string, owner int64) {
	t.Helper()
	claims, err := h.store.ListWorktreeClaims(t.Context())
	if err != nil {
		t.Fatalf("ListWorktreeClaims: %v", err)
	}
	claimed := map[string]bool{}
	var holders []int64
	for _, c := range claims {
		if c.Path == "" {
			continue
		}
		claimed[filepath.Clean(c.Path)] = true
		if c.Path == path {
			holders = append(holders, c.TaskID)
		}
		if _, err := os.Stat(c.Path); err != nil {
			t.Errorf("task %d claims %s, which is missing: %v", c.TaskID, c.Path, err)
		}
	}
	if len(holders) != 1 || holders[0] != owner {
		t.Errorf("rows naming %s = %v, want only task %d", path, holders, owner)
	}
	root := h.runner.deps.Worktrees.Root()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read worktree root: %v", err)
	}
	for _, e := range entries {
		if dir := filepath.Join(root, e.Name()); !claimed[filepath.Clean(dir)] {
			t.Errorf("%s is under the worktree root with no claim: an orphan", dir)
		}
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
