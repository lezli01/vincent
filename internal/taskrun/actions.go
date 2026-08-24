package taskrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// InvalidActionError reports a human action that §6 does not allow from the
// task's current state. The API answers 409 and reports State so a client
// can re-issue against what it found (spec §13.1).
type InvalidActionError struct {
	TaskID int64
	Action taskstate.Action
	State  store.TaskState
}

func (e *InvalidActionError) Error() string {
	return fmt.Sprintf("task %d cannot %s while %s", e.TaskID, e.Action, e.State)
}

// OverrideMismatchError reports an edit+retry override that does not fit the
// step being retried — a prompt for a command step, or a command for an
// agent step. The API answers 400.
type OverrideMismatchError struct {
	StepType string
	Field    string
}

func (e *OverrideMismatchError) Error() string {
	return fmt.Sprintf("%s is not valid for a %s step", e.Field, e.StepType)
}

// RepairPromptError reports a repair asked for with nothing to say. The API
// answers 400: an agent launched with no instructions is spend with no
// question attached (task 025).
type RepairPromptError struct {
	TaskID int64
}

func (e *RepairPromptError) Error() string {
	return fmt.Sprintf("task %d: a repair needs a prompt", e.TaskID)
}

// Cancel aborts a task and stops any process it is running (§6). The task
// reaches `aborted` first, so a client that observes the state knows the
// decision is final even while the process tree is still winding down.
func (r *Runner) Cancel(ctx context.Context, id int64) (*store.Task, error) {
	task, err := r.humanAction(ctx, id, taskstate.Cancel, store.TaskChange{})
	if err != nil {
		return nil, err
	}
	log := r.deps.Logger.With("task", id)
	// A fan-out's lanes are cancelled with it (decision 11): nothing should
	// keep burning agent time for a join that will never happen. Their
	// branches and worktrees survive — they are stopped, not erased.
	r.cascadeCancel(ctx, id)
	if lr, ok := r.lookupRun(id); ok {
		// Tearing the process tree down takes up to the §6 grace period, and
		// the caller has already got its answer: the task is durably aborted.
		// Blocking the response on it would stall a client for ten seconds to
		// tell it something it already knows. The actor closes its own step
		// run — it is that row's only writer.
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			lr.stop(cancelGrace, log)
			// The actor is unwinding now; once it is gone, close anything it
			// could not — a row it lost at a transition boundary would
			// otherwise stay open until the startup sweep.
			<-lr.done
			if n, err := r.deps.Store.TerminalizeOpenStepRuns(r.persistCtx(), id,
				store.StepInterrupted, ReasonCanceled); err != nil {
				log.Error("cancel: close open step runs", "error", err)
			} else if n > 0 {
				log.Info("cancel: closed step runs the actor left open", "rows", n)
			}
		}()
		return task, nil
	}
	// No actor: nothing else will close the rows this task left open — the
	// manual row an `awaiting_gate` task is parked on, or a row orphaned by
	// a crash (§6). This write outlives the request: the transition it
	// belongs with has already committed.
	if n, err := r.deps.Store.TerminalizeOpenStepRuns(r.persistCtx(), id,
		store.StepInterrupted, ReasonCanceled); err != nil {
		log.Error("cancel: close open step runs", "error", err)
	} else if n > 0 {
		log.Info("cancel: closed step runs left open", "rows", n)
	}
	return task, nil
}

// Pause holds a task at its next step boundary (§6). A queued task pauses
// at once; a running one finishes its current step first, and the request is
// persisted so a crash — which re-queues the task — cannot discard it. Which
// of the two it is comes from §6's Deferred flag and is decided in applyAction,
// so a pause that re-reads its state after losing a race decides again.
func (r *Runner) Pause(ctx context.Context, id int64) (*store.Task, error) {
	return r.humanAction(ctx, id, taskstate.Pause, store.TaskChange{})
}

// requestPause records a deferred pause on a task whose step is still running
// (§6): the flag is persisted, so a crash — which re-queues the task — cannot
// discard it, and mirrored onto the live actor, which is what actually reads
// it at its next step boundary.
func (r *Runner) requestPause(ctx context.Context, id int64) (*store.Task, error) {
	updated, err := r.deps.Store.RequestPause(ctx, id)
	if err != nil {
		return nil, err
	}
	if lr, ok := r.lookupRun(id); ok {
		lr.mu.Lock()
		lr.pauseRequested = true
		lr.mu.Unlock()
	}
	return updated, nil
}

// Resume returns a paused task to the queue (§6).
func (r *Runner) Resume(ctx context.Context, id int64) (*store.Task, error) {
	return r.humanAction(ctx, id, taskstate.Resume, store.TaskChange{})
}

// Retry re-runs the step a blocked task failed on, as a fresh attempt with
// the retry budget reset (§6). A non-empty override rewrites that step in
// this task's snapshot — the snapshot stays the single execution truth
// (§5.3) — and is handed to the actor for the attempt it creates.
func (r *Runner) Retry(ctx context.Context, id int64, ov store.Override) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, taskstate.Retry) {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Retry, State: task.State}
	}
	now := time.Now()
	ch := store.TaskChange{RetryCursorAt: &now}
	if !ov.Empty() {
		target, err := r.overrideTarget(ctx, task)
		if err != nil {
			return nil, err
		}
		snapshot, err := applyOverride(task, ov, target)
		if err != nil {
			return nil, err
		}
		ch.Snapshot = &snapshot
		ch.PendingOverride = &ov
	}
	return r.transitionFrom(ctx, task, taskstate.Retry, ch)
}

// Repair launches a one-off agent in the blocked task's existing worktree
// (§6, task 025). The request is persisted on the task and the task is
// re-queued; the scheduler admits it exactly like anything else, so both §11
// caps apply and internal/scheduler stays the only producer of
// `queued → running`. The actor that admission starts runs the agent and
// returns the task to `blocked` at the same step with the same reason.
//
// Unlike Retry it must not stamp `retry_cursor_at`: a repair is not a retry,
// and moving the cursor would silently hand the blocked step a fresh budget
// the human did not ask for (§7.2).
func (r *Runner) Repair(ctx context.Context, id int64, req store.RepairRequest) (*store.Task, error) {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return nil, &RepairPromptError{TaskID: id}
	}
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, taskstate.Repair) {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Repair, State: task.State}
	}
	// The reason rides the request because the transition about to happen
	// clears `block_reason`, and the re-block afterwards has to put the same
	// one back: a repair decides nothing about the blocked step.
	req.BlockReason = task.BlockReason
	return r.transitionFrom(ctx, task, taskstate.Repair, store.TaskChange{PendingRepair: &req})
}

// Skip marks the current step skipped and advances (§6). From a gate the
// open manual row is closed in place; from blocked the failed row stays and
// a fresh `skipped` row records the decision, so every step index a task
// passes through has at least one row.
func (r *Runner) Skip(ctx context.Context, id int64) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, taskstate.Skip) {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Skip, State: task.State}
	}
	if err := r.recordStepDecision(ctx, task, store.StepSkipped, ""); err != nil {
		return nil, err
	}
	// Skipping moves past the step an edit+retry was aimed at; a surviving
	// override must not drain onto some later step's attempt.
	ch := advance(task)
	var noOverride store.Override
	ch.PendingOverride = &noOverride
	return r.transitionFrom(ctx, task, taskstate.Skip, ch)
}

// Approve passes a manual gate and advances to the next step (§6).
func (r *Runner) Approve(ctx context.Context, id int64) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, taskstate.Approve) {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Approve, State: task.State}
	}
	if err := r.recordStepDecision(ctx, task, store.StepApproved, ""); err != nil {
		return nil, err
	}
	return r.transitionFrom(ctx, task, taskstate.Approve, advance(task))
}

// Reject fails a manual gate: the step is rejected and the task blocks, from
// which a human can retry an earlier step, skip, or abort (§6).
func (r *Runner) Reject(ctx context.Context, id int64) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, taskstate.Reject) {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Reject, State: task.State}
	}
	if err := r.recordStepDecision(ctx, task, store.StepRejected, ReasonRejected); err != nil {
		return nil, err
	}
	reason := ReasonRejected
	// The step cursor stays put: a rejected gate is retried or skipped from
	// where it stands.
	return r.transitionFrom(ctx, task, taskstate.Reject, store.TaskChange{BlockReason: &reason})
}

// Archive removes the task's worktree, marks the record archived, and then —
// and only then — deletes the branch if it carries no commits past its base
// (§6, §10, task 008). Removal happens first: an archived task still pointing
// at a live worktree would be a lie, so a dirty worktree without force leaves
// the task exactly as it was, and the branch step is unreachable.
//
// The returned BranchOutcome is what happened to the branch; its zero value
// means the branch step did not run. It is never an error: the archive has
// already committed by then, and a branch problem must not be able to reverse
// it. Failures are logged here and reported to the caller.
func (r *Runner) Archive(
	ctx context.Context, id int64, force bool,
) (*store.Task, worktree.BranchOutcome, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, worktree.BranchOutcome{}, err
	}
	if !taskstate.Can(task.State, taskstate.Archive) {
		return nil, worktree.BranchOutcome{},
			&InvalidActionError{TaskID: id, Action: taskstate.Archive, State: task.State}
	}
	// Archiving a fan-out parent archives its whole subtree, so it refuses
	// while any lane is still working — pulling a worktree out from under a
	// running lane is not something `force` should be able to ask for
	// (decision 11).
	if err := r.refuseUnfinishedDescendants(ctx, id); err != nil {
		return nil, worktree.BranchOutcome{}, err
	}
	if err := r.cascadeArchive(ctx, id, force); err != nil {
		return nil, worktree.BranchOutcome{}, err
	}
	empty := ""
	if task.WorktreePath == "" {
		// No worktree ever existed, so no branch was ever created for this task
		// either — `git worktree add -b` makes both or neither. That matters
		// beyond tidiness: a task that blocked with `branch_exists` (§10, task
		// 001) carries a branch_name naming *somebody else's* branch, and this
		// is the check that keeps the branch step away from it.
		out, err := r.transitionFrom(ctx, task, taskstate.Archive,
			store.TaskChange{WorktreePath: &empty})
		return out, worktree.BranchOutcome{}, err
	}
	project, err := r.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		return nil, worktree.BranchOutcome{}, err
	}
	// Remove-then-clear runs under the manager's claim lock, the mirror of
	// creation's create-then-claim (task 005). Between the two the row still
	// names a directory that is already gone, and a gc scan landing there
	// would report the task as a reverse mismatch it never was.
	var out *store.Task
	if err := r.deps.Worktrees.RemoveAndRelease(ctx, project.Path, task.WorktreePath, force,
		func() error {
			var err error
			out, err = r.transitionFrom(ctx, task, taskstate.Archive,
				store.TaskChange{WorktreePath: &empty})
			return err
		}); err != nil {
		return nil, worktree.BranchOutcome{}, err
	}
	// Outside the callback, not inside it: the branch is checked out in the
	// worktree until the worktree is gone, and the archive transition must not
	// be reversible by a branch problem.
	return out, r.deleteArchivedBranch(ctx, task, project.Path), nil
}

// deleteArchivedBranch applies the §10 empty-branch rule to a task that has
// just reached `archived`. The flags are read through Deps.Config, so a hot
// reload reaches the next archive with no extra plumbing.
func (r *Runner) deleteArchivedBranch(
	ctx context.Context, task *store.Task, projectPath string,
) worktree.BranchOutcome {
	cfg := r.deps.Config()
	if !cfg.DeleteEmptyBranchOnArchive || task.BranchName == "" {
		return worktree.BranchOutcome{}
	}
	log := r.deps.Logger.With("task", task.ID, "branch", task.BranchName)
	// The remote leg is attended-only by §10, and this is the attended path:
	// every caller of Archive is a human asking for this one task.
	out, err := r.deps.Worktrees.DeleteEmptyBranch(ctx, projectPath,
		task.BaseBranch, task.BranchName, cfg.DeleteRemoteBranchOnArchive)
	if err != nil {
		log.Warn("archive: branch kept", "result", out.Result, "error", err)
		return out
	}
	switch out.Result {
	case worktree.BranchDeleted:
		log.Info("archive: deleted the branch, which had no commits past its base",
			"base", task.BaseBranch, "remote", remoteResult(out))
	case worktree.BranchHasCommits:
		log.Info("archive: kept the branch, which has commits past its base",
			"base", task.BaseBranch)
	}
	return out
}

// remoteResult names the remote leg for a log line; "" when it did not run.
func remoteResult(out worktree.BranchOutcome) string {
	if out.Remote == nil {
		return ""
	}
	return out.Remote.Result
}

// SetPriority reorders admission without changing state (§6: queued and
// paused only). It emits its own durable event, since a reorder never
// reaches the transition path that writes one.
func (r *Runner) SetPriority(ctx context.Context, id int64, priority int) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.CanSetPriority(task.State) {
		return nil, &InvalidActionError{TaskID: id, Action: "set_priority", State: task.State}
	}
	updated, err := r.deps.Store.SetTaskPriority(ctx, id, priority)
	if err != nil {
		return nil, err
	}
	r.emit(updated, store.EventTaskPriorityChanged, map[string]any{
		"priority": priority, "previous": task.Priority,
	})
	return updated, nil
}

// humanAction validates a plain action against §6 and applies it.
func (r *Runner) humanAction(
	ctx context.Context, id int64, action taskstate.Action, ch store.TaskChange,
) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, action) {
		return nil, &InvalidActionError{TaskID: id, Action: action, State: task.State}
	}
	return r.transitionFrom(ctx, task, action, ch)
}

// transitionFrom applies an action to a task already loaded and validated,
// and is the one compare-and-swap every §6 human action goes through.
//
// The swap is on the state the caller read, so a concurrent writer can take
// it. Losing it is not by itself a refusal: when the state the task actually
// reached still allows this action, the action is applied once more from that
// state (issue #127). The scheduler is why — `queued → running` is
// bookkeeping, not intent, and a human who asked for something §6 allows from
// both states learns nothing from a conflict about a race they cannot see or
// influence. This supersedes the PR C decision that a lost cancel takes no
// internal retry; see the §6 amendment of 2026-08-24.
//
// It stays a no-op for the human-vs-human races `Runner.transition` protects,
// because a winning human transition almost always lands somewhere the loser's
// action is invalid — a cancelled task cannot be cancelled again.
//
// Three properties of the shape here, each load-bearing:
//
//   - The retry re-enters §6 rather than re-issuing the same swap, so a `pause`
//     that lands on `running` defers at the step boundary instead of parking a
//     task whose process is still live.
//   - It resumes at the swap, never at the caller, so pre-CAS work — Archive's
//     worktree removal, Retry's snapshot rewrite — is never repeated.
//   - Once, not in a loop: a second conflict is returned as it stands, so no
//     interleave can spin here.
func (r *Runner) transitionFrom(
	ctx context.Context, task *store.Task, action taskstate.Action, ch store.TaskChange,
) (*store.Task, error) {
	updated, err := r.applyAction(ctx, task, action, ch)
	conflict, lost := store.AsStateConflict(err)
	if !lost || !taskstate.Can(conflict.Got, action) {
		return updated, err
	}
	fresh, err := r.deps.Store.GetTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	return r.applyAction(ctx, fresh, action, ch)
}

// applyAction is one attempt at an action, from the state the task it is
// given carries. Every human action but `pause` clears a pending pause: each
// of them is a human saying "go" (§6).
func (r *Runner) applyAction(
	ctx context.Context, task *store.Task, action taskstate.Action, ch store.TaskChange,
) (*store.Task, error) {
	tr, ok := taskstate.Next(task.State, action)
	if !ok {
		return nil, &InvalidActionError{TaskID: task.ID, Action: action, State: task.State}
	}
	// A deferred action is accepted now and applied by the engine later, so
	// it is not a swap at all. §6 has exactly one — `pause` from `running`,
	// which reaches `paused` through Park at the next step boundary.
	if tr.Deferred {
		return r.requestPause(ctx, task.ID)
	}
	if ch.PauseRequested == nil && action != taskstate.Pause {
		noPause := false
		ch.PauseRequested = &noPause
	}
	updated, _, err := r.deps.Store.TransitionTask(ctx, task.ID, task.State, tr.To, ch)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// recordStepDecision terminalizes the open step run a human just decided on.
// A gate has one open (the actor wrote it on entry and exited); a blocked
// task has none, so the decision gets a row of its own — every step index a
// task passes through has at least one (phase 2 decision).
func (r *Runner) recordStepDecision(
	ctx context.Context, task *store.Task, state store.StepRunState, reason string,
) error {
	n, err := r.deps.Store.TerminalizeOpenStepRuns(ctx, task.ID, state, reason)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if task.State != store.TaskBlocked {
		// From a gate the decision closes the actor's open row; zero rows
		// means a concurrent action beat this one to it. Writing a fresh row
		// here would record a decision that is about to lose its CAS.
		return nil
	}
	stepID, stepType := describeStep(task, task.CurrentStep)
	attempts, err := r.deps.Store.CountStepAttempts(ctx,
		store.StepRef{TaskID: task.ID, StepIndex: task.CurrentStep, StepID: stepID}, time.Time{})
	if err != nil {
		return err
	}
	now := time.Now()
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: task.CurrentStep, StepID: stepID, StepType: stepType,
		Attempt: attempts.Last + 1, State: state, FailureReason: reason,
		StartedAt: now, FinishedAt: &now,
	}
	if err := r.deps.Store.CreateStepRun(ctx, run); err != nil {
		return err
	}
	return nil
}

// advance moves the step cursor past the current step, in the same
// compare-and-swap as the state change.
func advance(task *store.Task) store.TaskChange {
	next := task.CurrentStep + 1
	return store.TaskChange{CurrentStep: &next}
}

// overrideTarget names the step an `edit + retry` rewrites: the current step,
// or — when that is a `loop` — the body step whose failure blocked the task.
//
// A loop occupies one step_index and writes no row of its own, so the cursor
// alone cannot say what the human is editing; the rows can, and they are the
// only thing that knows (§7.8, decision 7). An empty string means "the
// current step itself", which is every task that is not blocked inside a
// loop.
//
// The edit lands in the snapshot, so it applies to **every remaining
// iteration** (decision 12). That is the useful behaviour — fix the prompt,
// let it keep going — rather than a one-shot patch the loop discards on its
// next pass.
func (r *Runner) overrideTarget(ctx context.Context, task *store.Task) (string, error) {
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		return "", fmt.Errorf("parse workflow snapshot: %w", err)
	}
	if task.CurrentStep < 0 || task.CurrentStep >= len(wf.Steps) ||
		wf.Steps[task.CurrentStep].Type != workflow.StepLoop {
		return "", nil
	}
	history, err := r.deps.Store.ListStepRunsAt(ctx, task.ID, task.CurrentStep)
	if err != nil {
		return "", err
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].State == store.StepFailed {
			return history[i].StepID, nil
		}
	}
	return "", fmt.Errorf("task %d is blocked in a loop with no failed body step to edit", task.ID)
}

// applyOverride rewrites one step's prompt or run inside the task's own
// snapshot copy (§5.3) and returns the new YAML.
//
// bodyID selects a step inside the current `loop`; empty means the current
// step itself.
func applyOverride(task *store.Task, ov store.Override, bodyID string) (string, error) {
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		return "", fmt.Errorf("parse workflow snapshot: %w", err)
	}
	if task.CurrentStep < 0 || task.CurrentStep >= len(wf.Steps) {
		return "", fmt.Errorf("task %d has no step %d to override", task.ID, task.CurrentStep)
	}
	step := &wf.Steps[task.CurrentStep]
	if bodyID != "" {
		step = nil
		for i := range wf.Steps[task.CurrentStep].Steps {
			if body := &wf.Steps[task.CurrentStep].Steps[i]; body.ID == bodyID {
				step = body
				break
			}
		}
		if step == nil {
			return "", fmt.Errorf("task %d has no body step %q to override", task.ID, bodyID)
		}
	}
	if ov.Prompt != "" {
		if step.Type != workflow.StepAgent {
			return "", &OverrideMismatchError{StepType: step.Type, Field: "prompt_override"}
		}
		step.Prompt = ov.Prompt
	}
	if ov.Run != "" {
		if step.Type != workflow.StepCommand {
			return "", &OverrideMismatchError{StepType: step.Type, Field: "run_override"}
		}
		step.Run = ov.Run
	}
	out, err := workflow.Marshal(wf)
	if err != nil {
		return "", fmt.Errorf("re-encode workflow snapshot: %w", err)
	}
	return string(out), nil
}

// describeStep names a step of the task's snapshot, tolerating a snapshot
// that no longer parses: the row is bookkeeping, not execution.
func describeStep(task *store.Task, index int) (stepID, stepType string) {
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil || index < 0 || index >= len(wf.Steps) {
		return fmt.Sprintf("step-%d", index), workflow.StepManual
	}
	return wf.Steps[index].ID, wf.Steps[index].Type
}

// AsInvalidAction extracts an *InvalidActionError from err, if that is what
// it is.
func AsInvalidAction(err error) (*InvalidActionError, bool) {
	var e *InvalidActionError
	ok := errors.As(err, &e)
	return e, ok
}

// AsOverrideMismatch extracts an *OverrideMismatchError from err, if that is
// what it is.
func AsOverrideMismatch(err error) (*OverrideMismatchError, bool) {
	var e *OverrideMismatchError
	ok := errors.As(err, &e)
	return e, ok
}

// AsRepairPrompt extracts a *RepairPromptError from err, if that is what it
// is.
func AsRepairPrompt(err error) (*RepairPromptError, bool) {
	var e *RepairPromptError
	ok := errors.As(err, &e)
	return e, ok
}
