package taskrun

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// actionSnapshot is a two-step workflow, so advancing the cursor and
// overriding either step type are both observable.
const actionSnapshot = `name: test
steps:
  - id: implement
    type: agent
    prompt: do the thing
  - id: publish
    type: command
    run: echo done
`

type actionHarness struct {
	store     *store.Store
	runner    *Runner
	repo      string
	projectID int64
	// cfg is read through Deps.Config on every call, so a test can move a
	// setting the way a hot reload does.
	cfg config.Config
}

func newActionHarness(t *testing.T) *actionHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "actions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h := &actionHarness{store: st, repo: repo, projectID: project.ID, cfg: config.Default()}
	h.runner = New(Deps{
		Store:     st,
		Config:    func() config.Config { return h.cfg },
		Worktrees: worktree.NewManager(git, dataDir),
		DataDir:   dataDir,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h
}

// branchExists asks the repository itself, not the manager under test.
func (h *actionHarness) branchExists(t *testing.T, branch string) bool {
	t.Helper()
	return testrepo.Run(t, h.repo,
		"for-each-ref", "--format=%(refname:short)", "refs/heads/"+branch) != ""
}

// commitInWorktree gives the task's branch a commit of its own, which is what
// makes it worth keeping past archive.
func (h *actionHarness) commitInWorktree(t *testing.T, path string) {
	t.Helper()
	testrepo.WriteFile(t, path, "work.txt", "real work\n")
	testrepo.Run(t, path, "add", ".")
	testrepo.Run(t, path, "commit", "-q", "-m", "the work")
}

func (h *actionHarness) task(t *testing.T, state store.TaskState) *store.Task {
	t.Helper()
	return h.taskAtStep(t, state, 0)
}

func (h *actionHarness) taskAtStep(t *testing.T, state store.TaskState, step int) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: "action test",
		WorkflowName: "test", WorkflowSnapshot: actionSnapshot,
		BaseBranch: "main",
		State:      state, CurrentStep: step,
	}
	// Derived from the id so two tasks in one test cannot claim the same branch.
	resolve := func(id int64) (string, error) { return worktree.BranchName(id, task.Title), nil }
	if err := h.store.CreateTask(t.Context(), task, resolve); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (h *actionHarness) get(t *testing.T, id int64) *store.Task {
	t.Helper()
	task, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return task
}

// invoke calls an action by name, so the table tests can drive all of them
// through one code path.
func (h *actionHarness) invoke(
	ctx context.Context, action taskstate.Action, id int64,
) (*store.Task, error) {
	switch action {
	case taskstate.Cancel:
		return h.runner.Cancel(ctx, id)
	case taskstate.Pause:
		return h.runner.Pause(ctx, id)
	case taskstate.Resume:
		return h.runner.Resume(ctx, id)
	case taskstate.Retry:
		return h.runner.Retry(ctx, id, store.Override{})
	case taskstate.Repair:
		return h.runner.Repair(ctx, id, store.RepairRequest{Prompt: "fix it"})
	case taskstate.Skip:
		return h.runner.Skip(ctx, id)
	case taskstate.Approve:
		return h.runner.Approve(ctx, id)
	case taskstate.Reject:
		return h.runner.Reject(ctx, id)
	case taskstate.Archive:
		task, _, err := h.runner.Archive(ctx, id, false)
		return task, err
	case taskstate.FollowUp:
		return h.runner.FollowUp(ctx, id, store.FollowUpRequest{
			Form:     store.FollowUpCommand,
			Run:      "git status",
			Workflow: followUpTestWorkflow,
		})
	default:
		t := &InvalidActionError{TaskID: id, Action: action}
		return nil, t
	}
}

// TestActionsFromEveryValidState walks the §6 table itself: every human
// action, from every state that allows it, must land where the table says.
// The cases are not listed by hand — they are derived from taskstate, so a
// change to the state machine that nothing here covers cannot slip through.
func TestActionsFromEveryValidState(t *testing.T) {
	for _, from := range taskstate.All {
		for _, action := range taskstate.HumanActionsFrom(from) {
			if action == taskstate.Answer {
				continue // T2.12 owns answer; it needs a live input request
			}
			t.Run(string(from)+"/"+string(action), func(t *testing.T) {
				h := newActionHarness(t)
				task := h.task(t, from)
				want, _ := taskstate.Next(from, action)

				got, err := h.invoke(t.Context(), action, task.ID)
				if err != nil {
					t.Fatalf("%s from %s: %v", action, from, err)
				}
				if got.State != want.To {
					t.Errorf("%s from %s → %s, want %s", action, from, got.State, want.To)
				}
			})
		}
	}
}

// TestActionsFromInvalidStateConflict asserts every action refuses a state
// §6 does not allow, reporting the state it found so the API can answer 409
// with it.
func TestActionsFromInvalidStateConflict(t *testing.T) {
	actions := []taskstate.Action{
		taskstate.Cancel, taskstate.Pause, taskstate.Resume, taskstate.Retry,
		taskstate.Repair, taskstate.Skip, taskstate.Approve, taskstate.Reject,
		taskstate.Archive, taskstate.FollowUp,
	}
	for _, action := range actions {
		// Find a state this action is not valid from.
		var invalid store.TaskState
		for _, s := range taskstate.All {
			if !taskstate.Can(s, action) {
				invalid = s
				break
			}
		}
		t.Run(string(action), func(t *testing.T) {
			h := newActionHarness(t)
			task := h.task(t, invalid)

			_, err := h.invoke(t.Context(), action, task.ID)
			e, ok := AsInvalidAction(err)
			if !ok {
				t.Fatalf("%s from %s: err = %v, want InvalidActionError", action, invalid, err)
			}
			if e.State != invalid {
				t.Errorf("reported state %s, want %s", e.State, invalid)
			}
		})
	}
}

// TestPauseDefersWhileRunning covers §6's deferral: a running task keeps
// running, and the request is persisted so a crash cannot discard it.
func TestPauseDefersWhileRunning(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskRunning)

	got, err := h.runner.Pause(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got.State != store.TaskRunning {
		t.Errorf("state = %s, want running (the step finishes first)", got.State)
	}
	if !got.PauseRequested {
		t.Error("pause_requested = false; the request would not survive a crash")
	}
}

// TestHumanActionsClearPendingPause is the other half of the pause
// lifecycle: every action but pause is a human saying "go".
func TestHumanActionsClearPendingPause(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskRunning)
	if _, err := h.runner.Pause(t.Context(), task.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, _, err := h.store.TransitionTask(t.Context(), task.ID,
		store.TaskRunning, store.TaskBlocked, store.TaskChange{}); err != nil {
		t.Fatalf("block: %v", err)
	}

	if _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if h.get(t, task.ID).PauseRequested {
		t.Error("pause_requested survived a retry; the task would park at once")
	}
}

// TestApproveAdvancesAndClosesGateRow asserts the gate's open row is closed
// in place and the cursor moves, both in one action (§6).
func TestApproveAdvancesAndClosesGateRow(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskAwaitingGate)
	h.openGateRow(t, task)

	got, err := h.runner.Approve(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got.CurrentStep != 1 {
		t.Errorf("current_step = %d, want 1", got.CurrentStep)
	}
	runs := h.runs(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepApproved {
		t.Fatalf("step runs = %+v, want one approved row", runs)
	}
	if runs[0].FinishedAt == nil {
		t.Error("approved gate row has no finished_at; its wait time is unmeasurable")
	}
}

// TestRejectHoldsCursorAndBlocks: a rejected gate is retried or skipped from
// where it stands, so the cursor must not move (§6).
func TestRejectHoldsCursorAndBlocks(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskAwaitingGate)
	h.openGateRow(t, task)

	got, err := h.runner.Reject(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got.State != store.TaskBlocked || got.CurrentStep != 0 {
		t.Errorf("state=%s step=%d, want blocked at step 0", got.State, got.CurrentStep)
	}
	if got.BlockReason != ReasonRejected {
		t.Errorf("block_reason = %q, want %q", got.BlockReason, ReasonRejected)
	}
	if runs := h.runs(t, task.ID); len(runs) != 1 || runs[0].State != store.StepRejected {
		t.Fatalf("step runs = %+v, want one rejected row", runs)
	}
}

// TestSkipFromGateClosesRow and TestSkipFromBlockedAppendsRow are the two
// shapes of skip: one has an open row to close, the other does not.
func TestSkipFromGateClosesRow(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskAwaitingGate)
	h.openGateRow(t, task)

	if _, err := h.runner.Skip(t.Context(), task.ID); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	runs := h.runs(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepSkipped {
		t.Fatalf("step runs = %+v, want the open row closed as skipped", runs)
	}
}

func TestSkipFromBlockedAppendsRow(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskBlocked)
	h.failedRow(t, task, 1)

	got, err := h.runner.Skip(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if got.CurrentStep != 1 {
		t.Errorf("current_step = %d, want 1", got.CurrentStep)
	}
	runs := h.runs(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("step runs = %d, want 2 (the failure plus the skip)", len(runs))
	}
	if runs[1].State != store.StepSkipped || runs[1].Attempt != 2 {
		t.Errorf("second row = %s attempt %d, want skipped attempt 2", runs[1].State, runs[1].Attempt)
	}
}

// TestApproveWithoutOpenRowFabricatesNothing: zero open rows at a gate means
// a concurrent action already decided it; a fresh row would record a
// decision whose CAS is about to lose.
func TestApproveWithoutOpenRowFabricatesNothing(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskAwaitingGate)

	if _, err := h.runner.Approve(t.Context(), task.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if runs := h.runs(t, task.ID); len(runs) != 0 {
		t.Fatalf("step runs = %d, want none fabricated at a gate with no open row", len(runs))
	}
}

// TestSkipClearsPendingOverride: skipping moves past the step an edit+retry
// was aimed at; a surviving override must not drain onto a later step's
// attempt.
func TestSkipClearsPendingOverride(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskBlocked)
	if _, err := h.runner.Retry(t.Context(), task.ID, store.Override{Prompt: "edited"}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	// The retried attempt never drained the override (it failed before its
	// row was created); the human gives up on the step and skips it.
	for _, hop := range [][2]store.TaskState{
		{store.TaskQueued, store.TaskRunning}, {store.TaskRunning, store.TaskBlocked},
	} {
		if _, _, err := h.store.TransitionTask(t.Context(), task.ID,
			hop[0], hop[1], store.TaskChange{}); err != nil {
			t.Fatalf("transition %s → %s: %v", hop[0], hop[1], err)
		}
	}

	if _, err := h.runner.Skip(t.Context(), task.ID); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if got := h.get(t, task.ID); got.PendingOverride != nil {
		t.Error("pending override survived the skip; it would mislabel a later step's attempt")
	}
}

// TestSkipPastLastStepCompletes asserts the final-step skip needs no special
// case: the cursor lands past the end and the actor finishes the task.
func TestSkipPastLastStepCompletes(t *testing.T) {
	h := newActionHarness(t)
	task := h.taskAtStep(t, store.TaskBlocked, 1)

	got, err := h.runner.Skip(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if got.State != store.TaskQueued || got.CurrentStep != 2 {
		t.Fatalf("state=%s step=%d, want queued at step 2 (past the last step)", got.State, got.CurrentStep)
	}
}

// TestRetryResetsBudgetCursor asserts the retry cursor is stamped, which is
// what makes the retry budget reset (§6, §7.2).
func TestRetryResetsBudgetCursor(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskBlocked)
	before := time.Now()

	got, err := h.runner.Retry(t.Context(), task.ID, store.Override{})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got.RetryCursorAt == nil {
		t.Fatal("retry_cursor_at is nil; the retry budget would never reset")
	}
	if got.RetryCursorAt.Before(before.Add(-time.Second)) {
		t.Errorf("retry_cursor_at = %s, want ~now", got.RetryCursorAt)
	}
}

// TestRetryWithPromptOverride asserts edit+retry rewrites the snapshot and
// leaves the text where the actor will find it — the handler cannot write
// the step_run itself, because that row does not exist yet.
func TestRetryWithPromptOverride(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskBlocked)

	got, err := h.runner.Retry(t.Context(), task.ID, store.Override{Prompt: "try harder"})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if !strings.Contains(got.WorkflowSnapshot, "try harder") {
		t.Errorf("snapshot was not rewritten:\n%s", got.WorkflowSnapshot)
	}
	if got.PendingOverride == nil || got.PendingOverride.Prompt != "try harder" {
		t.Fatalf("pending override = %+v, want the prompt", got.PendingOverride)
	}

	// The actor drains it exactly once, so later automatic retries of the
	// same step are not mislabelled as human edits.
	ov, err := h.store.TakePendingOverride(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("TakePendingOverride: %v", err)
	}
	if ov.Prompt != "try harder" {
		t.Errorf("drained %+v, want the prompt", ov)
	}
	if again, err := h.store.TakePendingOverride(t.Context(), task.ID); err != nil || !again.Empty() {
		t.Errorf("second drain = %+v, %v; want empty", again, err)
	}
}

// TestRetryOverrideMustMatchStepType: a command override on an agent step is
// a client error, not a silently ignored field.
func TestRetryOverrideMustMatchStepType(t *testing.T) {
	h := newActionHarness(t)
	agentStep := h.task(t, store.TaskBlocked)
	if _, err := h.runner.Retry(t.Context(), agentStep.ID, store.Override{Run: "echo hi"}); err == nil {
		t.Error("run_override accepted on an agent step")
	} else if _, ok := AsOverrideMismatch(err); !ok {
		t.Errorf("err = %v, want OverrideMismatchError", err)
	}

	commandStep := h.taskAtStep(t, store.TaskBlocked, 1)
	if _, err := h.runner.Retry(t.Context(), commandStep.ID, store.Override{Prompt: "hi"}); err == nil {
		t.Error("prompt_override accepted on a command step")
	} else if _, ok := AsOverrideMismatch(err); !ok {
		t.Errorf("err = %v, want OverrideMismatchError", err)
	}

	if _, err := h.runner.Retry(t.Context(), commandStep.ID, store.Override{Run: "echo hi"}); err != nil {
		t.Errorf("run_override on a command step: %v", err)
	}
}

// TestCancelClosesOpenRowsWithoutActor: nothing else will close the manual
// row an awaiting_gate task is parked on.
func TestCancelClosesOpenRowsWithoutActor(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskAwaitingGate)
	h.openGateRow(t, task)

	got, err := h.runner.Cancel(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.State != store.TaskAborted {
		t.Errorf("state = %s, want aborted", got.State)
	}
	runs := h.runs(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepInterrupted {
		t.Fatalf("step runs = %+v, want the open row interrupted", runs)
	}
	if runs[0].FailureReason != ReasonCanceled {
		t.Errorf("failure_reason = %q, want %q — a cancel is not a crash",
			runs[0].FailureReason, ReasonCanceled)
	}
}

// TestArchiveRemovesWorktreeBeforeTransition and its dirty counterpart:
// `archived` must mean the worktree is gone (§10).
func TestArchiveRemovesWorktree(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskDone)
	path := h.worktree(t, task)

	got, branch, err := h.runner.Archive(t.Context(), task.ID, false)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got.State != store.TaskArchived {
		t.Errorf("state = %s, want archived", got.State)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after archiving", path)
	}
	// The default is on, and this task's branch never received a commit
	// (task 008).
	if branch.Result != worktree.BranchDeleted {
		t.Errorf("branch outcome = %+v, want %q", branch, worktree.BranchDeleted)
	}
	if h.branchExists(t, task.BranchName) {
		t.Errorf("branch %s survived an archive that reported it deleted", task.BranchName)
	}
}

func TestArchiveRefusesDirtyWorktreeWithoutForce(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskDone)
	path := h.worktree(t, task)
	if err := os.WriteFile(filepath.Join(path, "scratch.txt"), []byte("wip"), 0o600); err != nil {
		t.Fatalf("dirty the worktree: %v", err)
	}

	if _, _, err := h.runner.Archive(t.Context(), task.ID, false); err == nil {
		t.Fatal("archived a dirty worktree without force")
	} else if worktree.ReasonOf(err) != worktree.ReasonWorktreeDirty {
		t.Fatalf("err = %v, want a dirty-worktree refusal", err)
	}
	// The refusal must leave the task alone: an archived task pointing at a
	// live worktree would be a lie either way round.
	if got := h.get(t, task.ID); got.State != store.TaskDone {
		t.Errorf("state = %s after a refused archive, want done", got.State)
	}
	// And the branch step must be unreachable behind that refusal — it runs
	// after the worktree removal, which did not happen (task 008).
	if !h.branchExists(t, task.BranchName) {
		t.Errorf("branch %s was deleted by an archive that was refused", task.BranchName)
	}

	if _, _, err := h.runner.Archive(t.Context(), task.ID, true); err != nil {
		t.Fatalf("Archive(force): %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists after a forced archive", path)
	}
}

// TestArchiveKeepsABranchWithCommits: the rule is about branches that hold
// nothing, and a task that did work is the case that must never lose it.
func TestArchiveKeepsABranchWithCommits(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskDone)
	path := h.worktree(t, task)
	h.commitInWorktree(t, path)

	got, branch, err := h.runner.Archive(t.Context(), task.ID, false)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got.State != store.TaskArchived {
		t.Errorf("state = %s, want archived", got.State)
	}
	if branch.Result != worktree.BranchHasCommits {
		t.Errorf("branch outcome = %+v, want %q", branch, worktree.BranchHasCommits)
	}
	if !h.branchExists(t, task.BranchName) {
		t.Errorf("branch %s was deleted despite carrying a commit", task.BranchName)
	}
}

// TestArchiveWithBranchCleanupOff asserts the pre-008 behaviour directly: with
// the setting off, no path deletes anything and the outcome says nothing.
func TestArchiveWithBranchCleanupOff(t *testing.T) {
	h := newActionHarness(t)
	h.cfg.DeleteEmptyBranchOnArchive = false
	task := h.task(t, store.TaskDone)
	h.worktree(t, task)

	got, branch, err := h.runner.Archive(t.Context(), task.ID, false)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got.State != store.TaskArchived {
		t.Errorf("state = %s, want archived", got.State)
	}
	if branch.Checked() {
		t.Errorf("branch outcome = %+v, want the zero value", branch)
	}
	if !h.branchExists(t, task.BranchName) {
		t.Errorf("branch %s was deleted with delete_empty_branch_on_archive off", task.BranchName)
	}
}

// TestArchiveSurvivesABranchFailure: every branch-side failure still lands the
// task in `archived` and reports what happened. Here the base branch is gone,
// so git cannot judge the branch at all.
func TestArchiveSurvivesABranchFailure(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskDone)
	h.worktree(t, task)
	// The recorded base no longer resolves: renamed out from under the task.
	testrepo.Run(t, h.repo, "branch", "-m", "main", "trunk")

	got, branch, err := h.runner.Archive(t.Context(), task.ID, false)
	if err != nil {
		t.Fatalf("Archive: %v — a branch problem must never fail the archive", err)
	}
	if got.State != store.TaskArchived {
		t.Errorf("state = %s, want archived", got.State)
	}
	if branch.Result != worktree.BranchUnknown {
		t.Errorf("branch outcome = %+v, want %q", branch, worktree.BranchUnknown)
	}
	if branch.Error == "" {
		t.Error("an unknown outcome carries no error text to log")
	}
	if !h.branchExists(t, task.BranchName) {
		t.Errorf("branch %s was deleted on a check nobody could make", task.BranchName)
	}
}

// TestArchiveWithoutAWorktreeTouchesNoBranch: a task that never got a worktree
// never got a branch either, and its branch_name may name somebody else's —
// that is exactly what a `branch_exists` block records (task 001).
func TestArchiveWithoutAWorktreeTouchesNoBranch(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskAborted)
	// A branch of that name exists, put there by something other than this
	// task. Nothing may touch it.
	testrepo.Run(t, h.repo, "branch", task.BranchName)

	got, branch, err := h.runner.Archive(t.Context(), task.ID, false)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got.State != store.TaskArchived {
		t.Errorf("state = %s, want archived", got.State)
	}
	if branch.Checked() {
		t.Errorf("branch outcome = %+v, want the zero value", branch)
	}
	if !h.branchExists(t, task.BranchName) {
		t.Errorf("branch %s was deleted although this task never created it", task.BranchName)
	}
}

// TestSetPriorityReordersAndEmits asserts a reorder is visible: it never
// reaches the transition path that would write an event.
func TestSetPriorityReordersAndEmits(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskQueued)

	got, err := h.runner.SetPriority(t.Context(), task.ID, 7)
	if err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	if got.Priority != 7 {
		t.Errorf("priority = %d, want 7", got.Priority)
	}
	if got.State != store.TaskQueued {
		t.Errorf("state = %s, want queued (priority is not a transition)", got.State)
	}
	events, err := h.store.ListEvents(t.Context(),
		store.EventFilter{Types: []string{store.EventTaskPriorityChanged}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) == 0 {
		t.Errorf("no %s event; a client watching the queue would miss the reorder",
			store.EventTaskPriorityChanged)
	}
}

func TestSetPriorityRejectedWhileRunning(t *testing.T) {
	h := newActionHarness(t)
	task := h.task(t, store.TaskRunning)

	_, err := h.runner.SetPriority(t.Context(), task.ID, 3)
	if _, ok := AsInvalidAction(err); !ok {
		t.Fatalf("err = %v, want InvalidActionError (queued/paused only)", err)
	}
}

// --- helpers ---

// openGateRow writes the running manual row the engine leaves behind when it
// parks a task at a gate.
func (h *actionHarness) openGateRow(t *testing.T, task *store.Task) {
	t.Helper()
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: task.CurrentStep, StepID: "gate",
		StepType: "manual", Attempt: 1, State: store.StepRunning,
	}
	if err := h.store.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
}

func (h *actionHarness) failedRow(t *testing.T, task *store.Task, attempt int) {
	t.Helper()
	now := time.Now()
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: task.CurrentStep, StepID: "implement",
		StepType: "agent", Attempt: attempt, State: store.StepFailed,
		FailureReason: ReasonNonzeroExit, StartedAt: now, FinishedAt: &now,
	}
	if err := h.store.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
}

func (h *actionHarness) runs(t *testing.T, taskID int64) []store.StepRun {
	t.Helper()
	runs, err := h.store.ListStepRuns(t.Context(), taskID)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	return runs
}

// worktree creates the task's real worktree and records it on the task.
func (h *actionHarness) worktree(t *testing.T, task *store.Task) string {
	t.Helper()
	path, err := h.runner.deps.Worktrees.Create(
		t.Context(), h.repo, task.ID, task.BranchName, task.BaseBranch, false)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &path, nil); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	return path
}
