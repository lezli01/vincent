package taskrun

import (
	"context"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// Step guards (spec §7.7, task 015).
//
// A guard is evaluated *before* the step becomes an attempt: it decides
// whether there is an attempt at all. That is why everything here writes its
// own row rather than going through runAttempt — a skipped step has no
// process, no transcript and no retry budget, and a `condition` step has none
// of those by construction (decision 7).

// evaluateGuard renders a step's `if:` against the §8.4 context and returns
// its verdict. The context is assembled fresh on every call: a verdict is
// never cached and never persisted (decision 10), so a human `retry` after
// fixing a workflow re-asks the question rather than replaying an answer
// computed against facts that have since changed.
func (r *Runner) evaluateGuard(ctx context.Context, env *stepEnv) (bool, error) {
	rc, err := r.renderContext(ctx, env, r.nextAttempt(ctx, env), stepOutcome{})
	if err != nil {
		return false, err
	}
	return workflow.Evaluate("if", env.step.If, rc)
}

// nextAttempt is the attempt number a guard renders `.Step.Attempt` as: the
// one the step would run as if the guard let it. A counting failure falls
// back to 1 rather than failing the guard — the count is context for a
// template, not the decision itself.
func (r *Runner) nextAttempt(ctx context.Context, env *stepEnv) int {
	attempts, err := r.deps.Store.CountStepAttempts(ctx, env.ref(), time.Time{})
	if err != nil {
		env.log.Warn("count attempts for guard context", "error", err)
		return 1
	}
	return attempts.Last + 1
}

// recordGuardOutcome writes the one row a guard decision produces. Every
// caller is terminal for the step: it skipped, it stopped, or it passed.
func (r *Runner) recordGuardOutcome(
	ctx context.Context, env *stepEnv, state store.StepRunState, skipReason, failureReason string,
) {
	r.recordDecisionRow(ctx, env, state, skipReason, failureReason, env.step.If)
}

// recordDecisionRow writes a row for a step that reached a verdict without
// running a process. summary is what the detail view shows in place of output:
// the guard itself for a guarded step, a sentence for a fan-out that selected
// no lanes.
func (r *Runner) recordDecisionRow(
	ctx context.Context, env *stepEnv, state store.StepRunState,
	skipReason, failureReason, summary string,
) {
	finished := time.Now()
	run := &store.StepRun{
		TaskID:    env.task.ID,
		StepIndex: env.index,
		StepID:    env.step.ID,
		StepType:  env.step.Type,
		Attempt:   r.nextAttempt(ctx, env),
		// A decision row inside a loop body carries its position like any
		// other (§7.8, task 016 decision 7). Leaving it at 0 would sort every
		// `break` and `condition` verdict ahead of the iteration it belongs
		// to, and hide a `stopped` row from the derivation that resumes the
		// loop.
		Iteration:     env.iteration(),
		LoopItem:      env.loopItem(),
		LoopTotal:     env.loopTotal(),
		State:         state,
		SkipReason:    skipReason,
		FailureReason: failureReason,
		ResultSummary: truncate(summary, resultSummaryLimit),
		FinishedAt:    &finished,
	}
	if err := r.deps.Store.CreateStepRun(r.persistCtx(), run); err != nil {
		env.log.Error("record guard outcome", "state", state, "error", err)
		return
	}
	r.emit(env.task, eventStepFinished, map[string]any{
		"step_id": env.step.ID, "step_index": env.index, "attempt": run.Attempt,
		"run_id": run.ID, "state": string(state), "skip_reason": skipReason,
		"failure_reason": failureReason,
	})
}

// allowFailure reports whether `allow_failure: true` on this step turns the
// given failure reason into an advance (§7.2, decision 5).
//
// The rule is "outcomes the step itself produced". Everything absent from
// this set is vincent failing to *run* the step — an absent CLI, an expired
// login, a snapshot this host cannot honour — and swallowing those would let
// a workflow branch on "the agent is not installed" as though that were a
// test result. `usage_limit` and `interrupted` are absent for a different
// reason: §7.2 says they are not failures at all, so there is nothing to
// allow.
func allowFailure(step workflow.Step, reason string) bool {
	if !step.AllowFailure {
		return false
	}
	switch reason {
	case ReasonNonzeroExit, ReasonCheckFailed, ReasonAgentError,
		ReasonTimeout, ReasonTranscriptLimit:
		return true
	default:
		return false
	}
}
