package taskrun

// Parallel step groups (spec §7, task 014 — phase 1). A `type: parallel` step
// runs its sub-steps concurrently in the task's one worktree: no branch, no
// child task, no merge. Everything that makes a step a step — retries,
// timeouts, checks, transcripts, live output — is the ordinary machinery,
// reached once per sub-step instead of once per step.
//
// Two properties are worth stating because the rest of the file assumes them:
//
//   - The group holds **one** scheduler slot however many sub-steps it runs,
//     so `max_parallel` is a second concurrency dimension the §11 caps do not
//     see (decision 30). That is why it has a configured default.
//   - The group itself owns no `step_runs` row (decision 17). Its outcome is
//     collected from its sub-steps' rows, and a re-admission derives what is
//     left to do from those same rows rather than from a stored cursor.

import (
	"context"
	"errors"
	"sync"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// runGroup executes one `parallel` step and returns the outcome the engine's
// step loop acts on, exactly as if the group had been a single step.
//
// Sub-steps run in goroutines forked from the actor's own, bounded by
// `max_parallel`. Each owns its `step_runs` rows exclusively — no row has two
// writers, and nothing here touches the *task* row, which stays the actor's
// alone (the invariant in CLAUDE.md).
func (r *Runner) runGroup(ctx context.Context, env *stepEnv) stepOutcome {
	subs, skipped, err := r.pendingSubSteps(ctx, env)
	if err != nil {
		env.log.Error("read group history", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	if len(subs) == 0 {
		// Every sub-step already succeeded under an earlier admission. The
		// group is done, and re-running it would discard work a human may
		// have waited an hour for.
		env.log.Info("parallel group already complete", "sub_steps", skipped)
		return stepOutcome{state: store.StepSucceeded}
	}

	subs, guarded, err := r.applySubStepGuards(ctx, env, subs)
	if err != nil {
		env.log.Error("evaluate sub-step guard", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonConditionError}
	}
	if len(subs) == 0 {
		// Every sub-step was guarded off. A group whose conditions all said
		// "not this time" decided correctly, so it succeeds having run
		// nothing (§7.5, task 015).
		env.log.Info("parallel group ran nothing: every sub-step was guarded off",
			"guarded_off", guarded)
		return stepOutcome{state: store.StepSucceeded}
	}

	// A group-level `timeout:` bounds the whole group; each sub-step still
	// has its own. Expiry cancels the sub-steps, which end as interruptions —
	// the group turns that back into a failure below, because the work ran
	// out of the time the workflow gave it rather than stopping for the
	// daemon's sake (§7.2).
	// One context, not two: the unconditional WithCancel that used to stand
	// here was overwritten — cancel and all — when the step carried a
	// `timeout:`, leaking its child context onto the parent for the rest of
	// the task's run.
	groupCtx, cancel := ctx, func() {}
	if env.step.Timeout != nil {
		groupCtx, cancel = context.WithTimeout(ctx, env.step.Timeout.Std())
	}
	defer cancel()

	limit := r.groupLimit(env.step)
	env.log.Info("parallel group started",
		"sub_steps", len(subs), "already_succeeded", skipped,
		"guarded_off", guarded, "max_parallel", limit)

	outcomes := make([]stepOutcome, len(subs))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, sub := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A sibling's failure does not cancel this one (decision 18): the
			// group waits for everything it started, so a nearly-finished test
			// run is not thrown away by a linter that failed first.
			subEnv := &stepEnv{
				task: env.task, project: env.project, wf: env.wf,
				step: sub, index: env.index, inGroup: true,
				followUp: env.followUp,
				log:      env.log.With("sub_step", sub.ID),
			}
			out := r.runStepWithRetries(groupCtx, subEnv)
			// `allow_failure` on a sub-step keeps its `failed` row and its
			// reason — the failure happened — while taking it out of the
			// group's own verdict (§7.2, task 015 decision 5).
			//
			// A sub-step owed a paced retry is excluded: its budget is not
			// spent, so this is not its verdict yet, and swallowing it here
			// would turn `retry_backoff` off for every allow_failure sub-step
			// (task 028).
			if out.state == store.StepFailed && out.backoffUntil == nil && allowFailure(sub, out.reason) {
				subEnv.log.Info("sub-step failed; allowed by allow_failure", "reason", out.reason)
				out = stepOutcome{state: store.StepSucceeded}
			}
			outcomes[i] = out
		}()
	}
	wg.Wait()

	// The group's own deadline, not the daemon's: a shutdown cancels the
	// parent context too, and that one must stay an interruption so the task
	// is re-admitted rather than blocked.
	if errors.Is(groupCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		env.log.Warn("parallel group timed out", "timeout", env.step.Timeout)
		return stepOutcome{state: store.StepFailed, reason: ReasonTimeout}
	}
	return collectGroup(outcomes)
}

// pendingSubSteps is the sub-steps this admission must actually run, in
// declaration order, plus how many were skipped as already done.
//
// A human retry re-admits the task at the group's index, and a sub-step whose
// latest attempt succeeded must not run again — 014.2's "a retry re-runs only
// the failed sub-step". The fact is derived from the rows rather than stored,
// for the reason decisions 9 and 13 give: a second copy can disagree.
func (r *Runner) pendingSubSteps(ctx context.Context, env *stepEnv) (subs []workflow.Step, skipped int, err error) {
	latest, err := r.deps.Store.LatestStepStates(ctx, env.task.ID, env.index)
	if err != nil {
		return nil, 0, err
	}
	for _, sub := range env.step.Steps {
		if latest[sub.ID] == store.StepSucceeded {
			skipped++
			continue
		}
		subs = append(subs, sub)
	}
	return subs, skipped, nil
}

// applySubStepGuards drops the sub-steps whose `if:` is false, recording a
// `skipped` row for each, and returns the rest in declaration order.
//
// Guards are evaluated **before anything in the group starts**, one after
// another, so within an admission no sibling has run and none can be in
// `.Steps` for another's guard to read. Across admissions that ordering is not
// enough — a re-admitted group's already-succeeded siblings still have their
// rows — so §7.5's sibling-blindness is enforced by stepEnv.blindTo rather
// than by this timing alone. A group is a set, so a false guard subsets it
// rather than stopping anything — the same meaning `if:` has on a fan-out lane
// (task 015 decision 3), and the reason a `condition` step is refused in here
// at all.
func (r *Runner) applySubStepGuards(
	ctx context.Context, env *stepEnv, subs []workflow.Step,
) (kept []workflow.Step, guardedOff int, err error) {
	for _, sub := range subs {
		if !sub.Guarded() {
			kept = append(kept, sub)
			continue
		}
		subEnv := &stepEnv{
			task: env.task, project: env.project, wf: env.wf,
			step: sub, index: env.index, inGroup: true,
			followUp: env.followUp,
			log:      env.log.With("sub_step", sub.ID),
		}
		pass, evalErr := r.evaluateGuard(ctx, subEnv)
		if evalErr != nil {
			r.recordGuardOutcome(ctx, subEnv, store.StepFailed, "", ReasonConditionError)
			return nil, 0, evalErr
		}
		if !pass {
			r.recordGuardOutcome(ctx, subEnv, store.StepSkipped, store.SkipReasonCondition, "")
			subEnv.log.Info("sub-step skipped by its guard")
			guardedOff++
			continue
		}
		kept = append(kept, sub)
	}
	return kept, guardedOff, nil
}

// subStepIDOf names the transcript of an attempt: empty for an ordinary step,
// whose index already identifies it, and the step id for a member of a group
// or a loop body, whose siblings share that index (task 014 decision 16,
// task 016 decision 13).
func subStepIDOf(env *stepEnv) string {
	// A follow-up round is the third case (task 027 decision 2): every step
	// of the round shares the round's index, so the id is what keeps two of
	// them from writing the same transcript file.
	if env.inGroup || env.loop != nil || env.followUp != nil {
		return env.step.ID
	}
	return ""
}

// groupLimit resolves how many sub-steps run at once: the group's own
// `max_parallel:`, else the daemon default. Read here rather than cached, so
// a hot reload governs the next group (decision 30).
func (r *Runner) groupLimit(step workflow.Step) int {
	if step.MaxParallel != nil && *step.MaxParallel > 0 {
		return *step.MaxParallel
	}
	if n := r.deps.Config().Parallel.MaxParallel; n > 0 {
		return n
	}
	return 1
}

// collectGroup reduces the sub-step outcomes to the group's own, in
// declaration order so the same set of failures always reports the same
// reason.
//
// Precedence is interruption, then a failure with its budget spent, then a
// failure owed a paced retry, then success. An interruption means the daemon
// is stopping or a quota is spent: that attempt consumed no retry and the task
// will be re-admitted, so reporting a sibling's failure instead would block a
// task that is only paused.
//
// A spent failure outranks a backoff-pending one (task 028) for the same
// reason: waiting on a sibling whose budget is *already* gone only delays the
// block. The task would be held, re-admitted, and blocked anyway — one hold
// later, with nothing learned.
func collectGroup(outcomes []stepOutcome) stepOutcome {
	var failure, backoff *stepOutcome
	for i := range outcomes {
		switch outcomes[i].state {
		case store.StepInterrupted:
			return outcomes[i]
		case store.StepFailed:
			if outcomes[i].backoffUntil != nil {
				if backoff == nil {
					backoff = &outcomes[i]
				}
				continue
			}
			if failure == nil {
				failure = &outcomes[i]
			}
		case store.StepSucceeded, store.StepRunning, store.StepApproved,
			store.StepRejected, store.StepSkipped, store.StepStopped:
		}
	}
	if failure != nil {
		return *failure
	}
	if backoff != nil {
		return *backoff
	}
	// Every sub-step succeeded, so the group did: that is the whole of its
	// success condition (014.2).
	return stepOutcome{state: store.StepSucceeded}
}
