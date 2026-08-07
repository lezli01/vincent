package taskrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
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

// Cancel aborts a task and stops any process it is running (§6). The task
// reaches `aborted` first, so a client that observes the state knows the
// decision is final even while the process tree is still winding down.
func (r *Runner) Cancel(ctx context.Context, id int64) (*store.Task, error) {
	task, err := r.humanAction(ctx, id, taskstate.Cancel, store.TaskChange{})
	if err != nil {
		return nil, err
	}
	log := r.deps.Logger.With("task", id)
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
// persisted so a crash — which re-queues the task — cannot discard it.
func (r *Runner) Pause(ctx context.Context, id int64) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	tr, ok := taskstate.Next(task.State, taskstate.Pause)
	if !ok {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Pause, State: task.State}
	}
	if !tr.Deferred {
		return r.transitionFrom(ctx, task, taskstate.Pause, store.TaskChange{})
	}
	updated, err := r.deps.Store.RequestPause(ctx, id)
	if err != nil {
		return nil, err
	}
	// The persisted flag covers the crash path; the live flag is what the
	// actor actually reads at its next step boundary.
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
		snapshot, err := applyOverride(task, ov)
		if err != nil {
			return nil, err
		}
		ch.Snapshot = &snapshot
		ch.PendingOverride = &ov
	}
	return r.transitionFrom(ctx, task, taskstate.Retry, ch)
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
	return r.transitionFrom(ctx, task, taskstate.Skip, advance(task))
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

// Archive removes the task's worktree and marks the record archived (§6,
// §10). The branch is never deleted. Removal happens first: an archived task
// still pointing at a live worktree would be a lie, so a dirty worktree
// without force leaves the task exactly as it was.
func (r *Runner) Archive(ctx context.Context, id int64, force bool) (*store.Task, error) {
	task, err := r.deps.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if !taskstate.Can(task.State, taskstate.Archive) {
		return nil, &InvalidActionError{TaskID: id, Action: taskstate.Archive, State: task.State}
	}
	if task.WorktreePath != "" {
		project, err := r.deps.Store.GetProject(ctx, task.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := r.deps.Worktrees.Remove(ctx, project.Path, task.WorktreePath, force); err != nil {
			return nil, err
		}
	}
	empty := ""
	return r.transitionFrom(ctx, task, taskstate.Archive,
		store.TaskChange{WorktreePath: &empty})
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

// transitionFrom applies an action to a task already loaded and validated.
// Every human action but `pause` clears a pending pause: each of them is a
// human saying "go" (§6).
func (r *Runner) transitionFrom(
	ctx context.Context, task *store.Task, action taskstate.Action, ch store.TaskChange,
) (*store.Task, error) {
	tr, ok := taskstate.Next(task.State, action)
	if !ok {
		return nil, &InvalidActionError{TaskID: task.ID, Action: action, State: task.State}
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
	attempts, err := r.deps.Store.CountStepAttempts(ctx, task.ID, task.CurrentStep, time.Time{})
	if err != nil {
		return err
	}
	stepID, stepType := describeStep(task, task.CurrentStep)
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

// applyOverride rewrites the current step's prompt or run inside the task's
// own snapshot copy (§5.3) and returns the new YAML.
func applyOverride(task *store.Task, ov store.Override) (string, error) {
	wf, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		return "", fmt.Errorf("parse workflow snapshot: %w", err)
	}
	if task.CurrentStep < 0 || task.CurrentStep >= len(wf.Steps) {
		return "", fmt.Errorf("task %d has no step %d to override", task.ID, task.CurrentStep)
	}
	step := &wf.Steps[task.CurrentStep]
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
	wf, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
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
