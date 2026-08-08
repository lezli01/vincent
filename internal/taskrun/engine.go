package taskrun

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// Failure and block reasons produced by the run path. They share one
// vocabulary with worktree.Reason* so a block_reason means the same thing
// wherever it came from (T1.5/T1.6 decision).
const (
	ReasonTimeout          = "timeout"
	ReasonInterrupted      = "interrupted"
	ReasonNonzeroExit      = "nonzero_exit"
	ReasonAgentError       = "agent_error"
	ReasonAgentUnavailable = "agent_unavailable"
	ReasonCheckFailed      = "check_failed"
	ReasonTemplateError    = "template_error"
	ReasonInvalidSnapshot  = "invalid_snapshot"
	ReasonRejected         = "rejected"
	ReasonCanceled         = "canceled"
	ReasonInternalError    = "internal_error"
)

// Durable event types the engine emits (spec §13.3). State changes emit
// task.state_changed from inside the transition itself.
const (
	eventStepStarted  = "step.started"
	eventStepFinished = "step.finished"
	eventStepRetrying = "step.retrying"
	eventGateWaiting  = "gate.waiting"
)

// resultSummaryLimit caps result_summary rows; the full text lives in the
// transcript.
const resultSummaryLimit = 4096

// outputTailLines is how much command output feeds `.Steps[…].Result` and
// the §8.4 retry failure block.
const outputTailLines = 200

// stepEnv is everything one step needs to run.
type stepEnv struct {
	task    *store.Task
	project *store.Project
	wf      *workflow.Workflow
	step    workflow.Step
	index   int
	log     *slog.Logger
}

// stepOutcome is the result of one attempt.
type stepOutcome struct {
	state         store.StepRunState
	reason        string
	result        string
	output        string // tail of the attempt's output, for the retry block
	exitCode      *int
	checkExitCode *int
}

// execute runs one admission of a task: it walks the snapshot's steps from
// the task's current step until the task finishes, parks, or fails. The
// goroutine is the sole writer of this task's state, and it exits whenever
// the task stops holding its concurrency slot (phase 2 decision).
func (r *Runner) execute(ctx context.Context, task *store.Task) {
	log := r.deps.Logger.With("task", task.ID)

	project, err := r.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		r.fail(task, ReasonInternalError, log, "load project", err)
		return
	}
	wf, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		// The snapshot validated at creation, so this is corruption or a
		// vincent downgrade — either way the task cannot run.
		r.fail(task, ReasonInvalidSnapshot, log, "parse workflow snapshot", err)
		return
	}
	if err := r.ensureWorktree(ctx, task, project, log); err != nil {
		return // ensureWorktree already blocked or re-queued the task
	}

	for index := task.CurrentStep; index < len(wf.Steps); index++ {
		// A cancel or shutdown sets its flag before it terminates the process,
		// and the run context is only canceled after the grace (§6, §12.4) —
		// the flag is what stops the actor from starting new work for a task
		// that is already ending.
		if ctx.Err() != nil || r.interrupting(task.ID) {
			r.interrupt(task, log)
			return
		}
		if r.pauseRequested(task.ID) {
			r.park(task, log)
			return
		}
		env := &stepEnv{
			task: task, project: project, wf: wf,
			step: wf.Steps[index], index: index,
			log: log.With("step", wf.Steps[index].ID, "step_index", index),
		}
		if env.step.Type == workflow.StepManual {
			r.enterGate(ctx, env)
			return
		}
		outcome := r.runStepWithRetries(ctx, env)
		switch outcome.state {
		case store.StepSucceeded:
			task.CurrentStep = index + 1
			next := task.CurrentStep
			if err := r.deps.Store.SetTaskProgress(ctx, task.ID, &next, nil); err != nil {
				env.log.Error("persist step advance", "error", err)
			}
		case store.StepInterrupted:
			r.interrupt(task, log)
			return
		case store.StepFailed, store.StepRunning, store.StepApproved, store.StepRejected, store.StepSkipped:
			r.fail(task, outcome.reason, env.log, "step failed", nil)
			return
		}
	}
	r.complete(task, log)
}

// ensureWorktree creates the task's worktree on first admission (§10); a
// queued task that never ran leaves none behind.
func (r *Runner) ensureWorktree(ctx context.Context, task *store.Task, project *store.Project, log *slog.Logger) error {
	if task.WorktreePath != "" {
		return nil
	}
	if task.BranchName == "" { // crash between insert and branch assignment
		task.BranchName = worktree.BranchName(task.ID, task.Title)
	}
	path, err := r.deps.Worktrees.Create(ctx, project.Path, task.ID, task.BranchName, task.BaseBranch)
	if err != nil {
		if ctx.Err() != nil {
			// A shutdown mid-create is an interruption, not a git failure.
			r.interrupt(task, log)
			return err
		}
		reason := worktree.ReasonOf(err)
		if reason == "" {
			reason = worktree.ReasonGitError
		}
		r.fail(task, reason, log, "create worktree", err)
		return err
	}
	task.WorktreePath = path
	if err := r.deps.Store.SetTaskProgress(ctx, task.ID, nil, &path); err != nil {
		log.Error("persist worktree path", "error", err)
	}
	return nil
}

// runStepWithRetries runs one step until it succeeds, is interrupted, or
// exhausts its retry budget (§7.2). Interrupted attempts consume no retry.
// The budget counts failures since the task's retry cursor — a human retry
// stamps it, granting the fresh budget §6 promises.
func (r *Runner) runStepWithRetries(ctx context.Context, env *stepEnv) stepOutcome {
	maxRetries := resolveMaxRetries(env.step, env.wf.Defaults)
	var since time.Time
	if env.task.RetryCursorAt != nil {
		since = *env.task.RetryCursorAt
	}
	attempts, err := r.deps.Store.CountStepAttempts(ctx, env.task.ID, env.index, since)
	if err != nil {
		env.log.Error("count step attempts", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	last := r.previousFailure(ctx, env, attempts.Last)
	for {
		attempts.Last++
		last = r.runAttempt(ctx, env, attempts.Last, last)
		if last.state != store.StepFailed {
			return last
		}
		attempts.Failed++
		if attempts.Failed > maxRetries {
			env.log.Warn("step failed; retries exhausted",
				"reason", last.reason, "attempts", attempts.Last, "max_retries", maxRetries)
			return last
		}
		// A cancel or shutdown that lands between attempts must not spawn
		// another process for a task that is already ending; the attempt that
		// just failed did so on its own and keeps its row.
		if ctx.Err() != nil || r.interrupting(env.task.ID) {
			return stepOutcome{state: store.StepInterrupted, reason: ReasonInterrupted}
		}
		env.log.Info("retrying step", "reason", last.reason, "next_attempt", attempts.Last+1)
		r.emit(env.task, eventStepRetrying, map[string]any{
			"step_id": env.step.ID, "step_index": env.index,
			"attempt": attempts.Last, "reason": last.reason,
		})
	}
}

// previousFailure reconstructs `.LastFailure` for the first attempt of an
// admission that resumes a step with failed history — the human-retry path,
// where §8.4's failure block matters most. Within an admission the loop
// carries the outcome in memory; across admissions the row is what survives,
// so the block draws on its failure reason and result summary.
func (r *Runner) previousFailure(ctx context.Context, env *stepEnv, lastAttempt int) stepOutcome {
	if lastAttempt == 0 {
		return stepOutcome{}
	}
	prev, err := r.deps.Store.LastFailedStepRun(ctx, env.task.ID, env.index)
	if err != nil {
		env.log.Warn("previous failure unavailable for retry context", "error", err)
		return stepOutcome{}
	}
	if prev == nil {
		return stepOutcome{}
	}
	return stepOutcome{state: store.StepFailed, reason: prev.FailureReason, output: prev.ResultSummary}
}

// runAttempt runs a single attempt end to end: render, execute, check,
// persist. previous carries the last failed attempt, which feeds
// `.LastFailure` and the §8.4 failure block.
func (r *Runner) runAttempt(ctx context.Context, env *stepEnv, attempt int, previous stepOutcome) stepOutcome {
	rc, err := r.renderContext(ctx, env, attempt, previous)
	if err != nil {
		env.log.Error("assemble template context", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}

	run := &store.StepRun{
		TaskID:    env.task.ID,
		StepIndex: env.index,
		StepID:    env.step.ID,
		StepType:  env.step.Type,
		Attempt:   attempt,
		State:     store.StepRunning,
	}
	var sel selection
	if env.step.Type == workflow.StepAgent {
		sel = resolveSelection(env.step, env.wf.Defaults, env.task)
		run.Agent, run.Model, run.Effort = sel.Agent, sel.Model, sel.Effort
	}

	tr, err := openTranscript(r.deps.DataDir, env.task.ID, env.index, attempt)
	if err != nil {
		env.log.Error("open transcript", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	defer tr.Close()
	run.TranscriptPath = tr.Path()

	// An `edit + retry` left its text on the task, because the handler ran
	// while the task was blocked and this row did not exist yet (§6). The
	// insert drains it in the same transaction — it marks the attempt the
	// human edited, not the automatic retries that may follow, and a crash
	// cannot clear it without recording it.
	if err := r.deps.Store.CreateStepRunTakingOverride(r.persistCtx(), run); err != nil {
		env.log.Error("create step run", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	tr.Note("step_started", map[string]any{
		"task_id": env.task.ID, "step_id": env.step.ID, "attempt": attempt,
		"type": env.step.Type, "agent": run.Agent, "model": run.Model, "effort": run.Effort,
	})
	r.emit(env.task, eventStepStarted, map[string]any{
		"step_id": env.step.ID, "step_index": env.index, "attempt": attempt,
		"step_type": env.step.Type, "run_id": run.ID,
	})

	var outcome stepOutcome
	switch env.step.Type {
	case workflow.StepAgent:
		outcome = r.runAgentStep(ctx, env, sel, rc, run, tr)
	case workflow.StepCommand:
		outcome = r.runCommandStep(ctx, env, rc, run, tr)
	default:
		outcome = stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	if outcome.state == store.StepSucceeded && env.step.Check != "" {
		outcome = r.runCheck(ctx, env, rc, run, tr, outcome)
	}
	// A cancel reaches the step as an interruption; recording it as one
	// would lose the fact that a human ended this deliberately (§6).
	if outcome.state == store.StepInterrupted && r.canceling(env.task.ID) {
		outcome.reason = ReasonCanceled
	}

	r.finishStepRun(run, outcome, env.log)
	tr.Note("step_finished", map[string]any{
		"state": string(outcome.state), "failure_reason": outcome.reason,
		"exit_code": outcome.exitCode, "check_exit_code": outcome.checkExitCode,
	})
	r.emit(env.task, eventStepFinished, map[string]any{
		"step_id": env.step.ID, "step_index": env.index, "attempt": attempt,
		"run_id": run.ID, "state": string(outcome.state), "failure_reason": outcome.reason,
	})
	return outcome
}

// renderContext assembles the §8.4 template context, including the results
// of steps completed so far.
func (r *Runner) renderContext(ctx context.Context, env *stepEnv, attempt int, previous stepOutcome) (workflow.RenderContext, error) {
	runs, err := r.deps.Store.ListStepRuns(ctx, env.task.ID)
	if err != nil {
		return workflow.RenderContext{}, fmt.Errorf("list step runs: %w", err)
	}
	steps := map[string]workflow.StepResult{}
	for i := range runs {
		run := runs[i]
		switch run.State {
		case store.StepSucceeded, store.StepApproved, store.StepSkipped:
		default:
			continue
		}
		res := workflow.StepResult{Status: string(run.State), Result: run.ResultSummary}
		if run.ExitCode != nil {
			res.ExitCode = *run.ExitCode
		}
		steps[run.StepID] = res
	}
	return workflow.RenderContext{
		Task: workflow.TaskContext{
			ID: env.task.ID, Title: env.task.Title, Description: env.task.Description,
			Fields: env.task.Fields, BaseBranch: env.task.BaseBranch, BranchName: env.task.BranchName,
		},
		Project: workflow.ProjectContext{
			Name: env.project.Name, Path: env.project.Path, DefaultBranch: env.project.DefaultBranch,
		},
		Workflow: workflow.Info{Name: env.wf.Name, Description: env.wf.Description},
		Step: workflow.StepContext{
			ID: env.step.ID, Name: env.step.DisplayName(), Index: env.index, Attempt: attempt,
		},
		Steps:       steps,
		Worktree:    workflow.WorktreeContext{Path: env.task.WorktreePath},
		LastFailure: workflow.Failure{Reason: previous.reason, Output: previous.output},
	}, nil
}

// enterGate parks the task at a manual step: the attempt row is written on
// entry so the gate's wait is visible in the timeline, and the task gives up
// its concurrency slot until a human approves or rejects (§6, §11).
func (r *Runner) enterGate(ctx context.Context, env *stepEnv) {
	attempts, err := r.deps.Store.CountStepAttempts(ctx, env.task.ID, env.index, time.Time{})
	if err != nil {
		env.log.Error("count gate attempts", "error", err)
		r.fail(env.task, ReasonInternalError, env.log, "count gate attempts", err)
		return
	}
	instructions := env.step.Instructions
	if rc, err := r.renderContext(ctx, env, attempts.Last+1, stepOutcome{}); err == nil {
		if rendered, rerr := workflow.Render("instructions", env.step.Instructions, rc); rerr == nil {
			instructions = rendered
		} else {
			env.log.Warn("gate instructions did not render; showing them raw", "error", rerr)
		}
	}
	run := &store.StepRun{
		TaskID: env.task.ID, StepIndex: env.index, StepID: env.step.ID,
		StepType: workflow.StepManual, Attempt: attempts.Last + 1,
		State: store.StepRunning, ResultSummary: truncate(instructions, resultSummaryLimit),
	}
	if err := r.deps.Store.CreateStepRun(r.persistCtx(), run); err != nil {
		env.log.Error("create gate step run", "error", err)
	}
	r.emit(env.task, eventGateWaiting, map[string]any{
		"step_id": env.step.ID, "step_index": env.index, "run_id": run.ID,
	})
	r.transition(env.task, taskstate.Gate, store.TaskChange{}, env.log)
	env.log.Info("task waiting at a manual gate")
}

// transition applies an engine action through the §6 state machine and
// persists it with its durable event.
func (r *Runner) transition(task *store.Task, action taskstate.Action, ch store.TaskChange, log *slog.Logger) bool {
	from := task.State
	tr, ok := taskstate.Next(from, action)
	if !ok {
		log.Error("engine attempted an invalid transition", "from", from, "action", action)
		return false
	}
	updated, _, err := r.deps.Store.TransitionTask(r.persistCtx(), task.ID, task.State, tr.To, ch)
	if err != nil {
		if conflict, isConflict := store.AsStateConflict(err); isConflict {
			// A human acted first (cancel, for instance); their transition wins.
			log.Info("transition superseded", "action", action, "state", conflict.Got)
			return false
		}
		log.Error("persist transition", "action", action, "error", err)
		return false
	}
	*task = *updated
	return true
}

func (r *Runner) complete(task *store.Task, log *slog.Logger) {
	if r.transition(task, taskstate.Complete, store.TaskChange{}, log) {
		log.Info("task done")
	}
}

// fail blocks the task with a reason (§7.2: retries exhausted → blocked).
func (r *Runner) fail(task *store.Task, reason string, log *slog.Logger, what string, err error) {
	if err != nil {
		log.Error(what, "error", err, "reason", reason)
	}
	if r.transition(task, taskstate.Fail, store.TaskChange{BlockReason: &reason}, log) {
		log.Warn("task blocked", "reason", reason)
	}
}

// interrupt re-queues a task whose step was cut short by shutdown or crash:
// the attempt consumes no retry and re-runs on the next admission (§12.4).
func (r *Runner) interrupt(task *store.Task, log *slog.Logger) {
	if r.transition(task, taskstate.Interrupt, store.TaskChange{}, log) {
		log.Info("task interrupted; re-queued")
	}
}

// park completes a pause requested while the task was running: §6 lets the
// current step finish first. The persisted flag has served its purpose and
// clears with the transition — `paused` itself is the durable fact now.
func (r *Runner) park(task *store.Task, log *slog.Logger) {
	noPause := false
	if r.transition(task, taskstate.Park, store.TaskChange{PauseRequested: &noPause}, log) {
		log.Info("task paused at a step boundary")
	}
}

func (r *Runner) finishStepRun(run *store.StepRun, outcome stepOutcome, log *slog.Logger) {
	now := time.Now()
	run.State = outcome.state
	run.FailureReason = outcome.reason
	run.ResultSummary = truncate(outcome.result, resultSummaryLimit)
	run.ExitCode = outcome.exitCode
	run.CheckExitCode = outcome.checkExitCode
	run.PID = nil
	run.FinishedAt = &now
	if err := r.deps.Store.UpdateStepRun(r.persistCtx(), run); err != nil {
		log.Error("persist step run", "run", run.ID, "error", err)
	}
}

// emit appends a durable event (spec §13.3). Failures are logged, never
// fatal: losing an event must not lose the work.
func (r *Runner) emit(task *store.Task, evType string, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		r.deps.Logger.Error("marshal event", "type", evType, "error", err)
		return
	}
	ev := &store.Event{
		Type: evType, TaskID: &task.ID, ProjectID: &task.ProjectID, Payload: body,
	}
	if err := r.deps.Store.AppendEvent(r.persistCtx(), ev); err != nil {
		r.deps.Logger.Error("append event", "type", evType, "error", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
