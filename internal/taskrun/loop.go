package taskrun

// Loops (spec §7.8, task 016). A `type: loop` step runs its body — a
// *sequence* — repeatedly in the task's one worktree: no branch, no child
// task, no merge, and no concurrency. Where §7.5's group is a set run once,
// this is a sequence run more than once.
//
// Three properties the rest of the file assumes, each of them task 014
// decision 17 restated:
//
//   - The loop owns **no** `step_runs` row. Its outcome is collected from its
//     body's rows, and the body's rows all carry the loop's `step_index`.
//   - No loop cursor is persisted anywhere. Position is
//     `(step_index, iteration, body position)`, derived from those rows on
//     every admission, so a re-admitted loop resumes **mid-iteration** rather
//     than restarting work a human may have waited an hour for.
//   - A `for_each` item is the one thing that *is* recorded, on the row
//     (decision 8). The list is the loop's extent, not a question, and
//     re-deriving it mid-loop would make "iteration 3" name different work
//     than the row already says it ran.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// loopEnv is where one step sits inside a loop body, and everything §8.4's
// `.Loop` reports about it.
type loopEnv struct {
	iteration int // 1-based
	item      string
	isFirst   bool
	isLast    bool
	// pos is this step's position in the body, and order maps every body
	// step id to its own. Together they are the third component of the
	// position comparison stepEnv.precedes makes (decision 9).
	pos   int
	order map[string]int
}

// loopContext is §8.4's `.Loop` for this step. Outside a loop it is the zero
// value, whose `Index: 0` is how a template shared between a loop body and an
// ordinary step tells the two apart (decision 9).
func (e *stepEnv) loopContext() workflow.LoopContext {
	if e.loop == nil {
		return workflow.LoopContext{}
	}
	return workflow.LoopContext{
		Index:   e.loop.iteration,
		Item:    e.loop.item,
		IsFirst: e.loop.isFirst,
		IsLast:  e.loop.isLast,
	}
}

// loopItem is the `for_each` item recorded on this step's rows.
func (e *stepEnv) loopItem() string {
	if e.loop == nil {
		return ""
	}
	return e.loop.item
}

// loopPlan is a loop's extent for one admission: how many iterations it will
// run, and — for `for_each` — what each one runs on.
type loopPlan struct {
	driver string
	total  int
	// items is empty for a `count:` loop. For `for_each` it is one entry per
	// iteration, drawn from the rows for iterations that already have them
	// and from the re-derived list for the rest (decision 8).
	items []string
}

// runLoop executes one `loop` step and returns the outcome the engine's step
// loop acts on, exactly as if the loop had been a single step.
func (r *Runner) runLoop(ctx context.Context, env *stepEnv) stepOutcome {
	limit := r.loopLimit(env.step)
	history, err := r.deps.Store.ListStepRunsAt(ctx, env.task.ID, env.index)
	if err != nil {
		env.log.Error("read loop history", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	plan, err := r.planLoop(ctx, env, history, limit)
	switch {
	case errors.Is(err, errLoopLimit):
		// Blocking rather than truncating: a loop with more work than it is
		// allowed to do did not decide to stop, it hit a wall, and advancing
		// would hand every downstream guard a `.Steps` that says the work is
		// finished (decision 5).
		env.log.Warn("loop exceeds its iteration ceiling", "error", err, "max_iterations", limit)
		return stepOutcome{state: store.StepFailed, reason: ReasonLoopLimit, result: err.Error()}
	case err != nil:
		env.log.Error("resolve loop driver", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonConditionError, result: err.Error()}
	}
	if plan.total == 0 {
		// An empty `for_each` list. The loop had nothing to iterate, which is
		// a correct answer rather than a failure — the same way a group whose
		// every sub-step was guarded off succeeds having run nothing (§7.5).
		//
		// It records a row saying so (task 018 D6). "The loop has no row of
		// its own" is about its *iterations*: those are the body's rows, and
		// here there are none, so without this the step index a task passed
		// through would have no row at all — breaking the phase 2 invariant
		// that every one has at least one, and leaving a detail view unable to
		// tell "ran nothing" from "never reached". A `fan_out` that selects no
		// lane has recorded exactly this row since task 015; this is the same
		// degenerate case in the other structure step.
		env.log.Info("loop ran nothing: the for_each list is empty")
		r.recordDecisionRow(ctx, env, store.StepSucceeded, "", "",
			"the for_each list is empty: the loop ran nothing")
		return stepOutcome{state: store.StepSucceeded}
	}

	// A loop-level `timeout:` bounds the **whole** loop, mirroring §7.5's
	// group timeout; each body step still has its own. Expiry cancels the
	// body step in flight, which ends as an interruption — turned back into a
	// failure below, because the work ran out of the time the workflow gave
	// it rather than stopping for the daemon's sake (§7.2).
	loopCtx, cancel := ctx, func() {}
	if env.step.Timeout != nil {
		loopCtx, cancel = context.WithTimeout(ctx, env.step.Timeout.Std())
	}
	defer cancel()

	order := bodyOrder(env.step.Steps)
	env.log.Info("loop started",
		"driver", plan.driver, "iterations", plan.total, "max_iterations", limit,
		"body", len(env.step.Steps))

	for iteration := 1; iteration <= plan.total; iteration++ {
		outcome, stop := r.runIteration(loopCtx, env, plan, order, iteration, history)
		if stop {
			return r.loopStop(ctx, loopCtx, env, outcome)
		}
	}
	// The driver is exhausted, which ends the loop **successfully** (§7.8):
	// `count: 10` means "run ten times", and running ten times achieved it.
	// A converge loop that never broke is the workflow's own statement that
	// five repairs were the budget — what it did *not* fix is for the step
	// after the loop to notice.
	env.log.Info("loop finished", "iterations", plan.total)
	return stepOutcome{state: store.StepSucceeded}
}

// runIteration runs one pass of the body. It returns the outcome that ends
// the loop and whether the loop is over: a `break`, a failure, an
// interruption, or the body running out.
func (r *Runner) runIteration(
	ctx context.Context, env *stepEnv, plan loopPlan, order map[string]int,
	iteration int, history []store.StepRun,
) (stepOutcome, bool) {
	latest := latestStatesIn(history, iteration)
	for pos, body := range env.step.Steps {
		if latest[body.ID] == store.StepSucceeded {
			// This body step already succeeded in this iteration under an
			// earlier admission. Re-running it would discard finished work,
			// which is §7.5's rule verbatim (decision 7).
			continue
		}
		bodyEnv := &stepEnv{
			task: env.task, project: env.project, wf: env.wf,
			step: body, index: env.index,
			loop: &loopEnv{
				iteration: iteration,
				item:      itemAt(plan.items, iteration),
				isFirst:   iteration == 1,
				isLast:    iteration == plan.total,
				pos:       pos,
				order:     order,
			},
			log: env.log.With("body_step", body.ID, "iteration", iteration),
		}
		outcome, stop := r.runBodyStep(ctx, bodyEnv)
		if stop {
			return outcome, true
		}
		if outcome.state == store.StepStopped {
			// A `condition` whose guard is false ends *this iteration*; the
			// loop carries on with the next. That is `continue`, spelled with
			// the meaning §7.7 already gave the word and no third step type
			// (decision 3).
			bodyEnv.log.Info("iteration ended early by a condition step")
			return stepOutcome{}, false
		}
	}
	return stepOutcome{}, false
}

// runBodyStep runs one member of a loop body. stop reports that the loop is
// over — a `break` taken, a failure, or an interruption; a returned
// `stopped` outcome without stop ends only the iteration.
func (r *Runner) runBodyStep(ctx context.Context, env *stepEnv) (out stepOutcome, stop bool) {
	if env.step.Guarded() || env.step.Type == workflow.StepCondition {
		pass, err := r.evaluateGuard(ctx, env)
		switch {
		case err != nil:
			r.recordGuardOutcome(ctx, env, store.StepFailed, "", ReasonConditionError)
			return stepOutcome{state: store.StepFailed, reason: ReasonConditionError}, true
		case env.step.Type == workflow.StepBreak && pass:
			// The loop ends here and **succeeds**; the cursor advances past
			// it (§7.8). A break is a decision the workflow made, which is
			// exactly what separates it from `loop_limit` (decision 5).
			r.recordGuardOutcome(ctx, env, store.StepStopped, "", "")
			env.log.Info("loop ended by a break step")
			return stepOutcome{state: store.StepSucceeded}, true
		case env.step.Type == workflow.StepBreak:
			r.recordGuardOutcome(ctx, env, store.StepSucceeded, "", "")
			return stepOutcome{}, false
		case env.step.Type == workflow.StepCondition && !pass:
			r.recordGuardOutcome(ctx, env, store.StepStopped, "", "")
			return stepOutcome{state: store.StepStopped}, false
		case env.step.Type == workflow.StepCondition:
			r.recordGuardOutcome(ctx, env, store.StepSucceeded, "", "")
			return stepOutcome{}, false
		case !pass:
			r.recordGuardOutcome(ctx, env, store.StepSkipped, store.SkipReasonCondition, "")
			env.log.Info("body step skipped by its guard")
			return stepOutcome{}, false
		}
	}
	if env.step.Type == workflow.StepBreak {
		// Unreachable: validation requires `if:` on a break, so the guard
		// branch above always claims it. Refusing to fall through to
		// runStepWithRetries keeps a corrupted snapshot from spawning a
		// process for a step type that has none.
		return stepOutcome{state: store.StepFailed, reason: ReasonInvalidSnapshot}, true
	}
	outcome := r.runStepWithRetries(ctx, env)
	switch outcome.state {
	case store.StepSucceeded:
		return stepOutcome{}, false
	case store.StepFailed:
		if allowFailure(env.step, outcome.reason) {
			// The row keeps its `failed` state and its reason — the failure
			// happened — and that row is what the `break` guard two lines
			// below reads (§7.2, task 015 decision 5).
			env.log.Info("body step failed; continuing on allow_failure", "reason", outcome.reason)
			return stepOutcome{}, false
		}
		return outcome, true
	default:
		// Interrupted, and everything a body step cannot produce. Either way
		// the loop stops and the engine decides what that means for the task.
		return outcome, true
	}
}

// loopStop turns the outcome that ended the loop into the loop's own. Its one
// job is the group's: a loop-level timeout arrives as an interruption of
// whatever was running, and must be reported as the loop failing `timeout`
// rather than as the daemon stopping.
func (r *Runner) loopStop(ctx context.Context, loopCtx context.Context, env *stepEnv, out stepOutcome) stepOutcome {
	if errors.Is(loopCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		env.log.Warn("loop timed out", "timeout", env.step.Timeout)
		return stepOutcome{state: store.StepFailed, reason: ReasonTimeout}
	}
	return out
}

// planLoop resolves the loop's extent for this admission (§7.8).
//
// `count:` is the count. `for_each:` renders its entries against the §8.4
// context, then takes each iteration that already has rows from *those* rows
// rather than from the fresh list (decision 8) — which is what keeps a
// resumed iteration 3 on the work iteration 3 actually did.
func (r *Runner) planLoop(
	ctx context.Context, env *stepEnv, history []store.StepRun, limit int,
) (loopPlan, error) {
	if env.step.Count != nil {
		n := *env.step.Count
		if n > limit {
			// Validation refuses this at load, so reaching it means the
			// ceiling moved under a queued task: `loop.max_iterations` was
			// lowered, and config is read per loop so a hot reload governs
			// this one (§12.3).
			return loopPlan{}, fmt.Errorf("%w: count %d exceeds max_iterations %d", errLoopLimit, n, limit)
		}
		return loopPlan{driver: workflow.DriverCount, total: n}, nil
	}
	rc, err := r.renderContext(ctx, env, 1, stepOutcome{})
	if err != nil {
		return loopPlan{}, err
	}
	items, err := resolveForEach(env.step.ForEach, rc)
	if err != nil {
		return loopPlan{}, err
	}
	if len(items) > limit {
		// Before iteration 1, naming the count: a list that cannot be
		// finished is worth saying so about before spending the first agent
		// run on it (§7.8).
		return loopPlan{}, fmt.Errorf("%w: for_each produced %d items, max_iterations is %d",
			errLoopLimit, len(items), limit)
	}
	// Recorded iterations win over the fresh list, and they also *extend* it:
	// the list is re-derived on every admission (decision 8), so a source
	// whose output shrank between admissions would otherwise leave the loop
	// with fewer iterations than it already has rows for — it would report
	// success having silently skipped work iterations 1..n are on record as
	// having started. Clamping to the highest recorded iteration keeps the
	// loop's extent monotonic without persisting a plan.
	recorded := recordedItems(history)
	highest := 0
	for iteration := range recorded {
		if iteration > highest {
			highest = iteration
		}
	}
	for len(items) < highest {
		items = append(items, "")
	}
	for iteration, item := range recorded {
		items[iteration-1] = item
	}
	if len(items) > limit {
		// Re-checked after the clamp: rows past a ceiling that has since been
		// lowered are still work this loop is not allowed to finish.
		return loopPlan{}, fmt.Errorf("%w: for_each has %d iterations on record, max_iterations is %d",
			errLoopLimit, len(items), limit)
	}
	return loopPlan{driver: workflow.DriverForEach, total: len(items), items: items}, nil
}

// errLoopLimit marks a plan that cannot run within `max_iterations`. It is a
// sentinel rather than a reason string so planLoop can report the numbers in
// its message and the caller still knows which block reason to use.
var errLoopLimit = errors.New("loop iteration limit")

// resolveForEach renders a `for_each` list into its items.
//
// Every entry is rendered, trimmed and split on newlines with empty lines
// dropped, whichever spelling the author used. That is what makes the scalar
// form — `for_each: '{{ .Steps.changed.Result }}'`, the case that opened
// task 016 — and a hand-written sequence one mechanism rather than two.
//
// A list drawn from a step's output is bounded by outputTailLines: `.Steps[…]
// .Result` is the *tail* of what the command printed, so a producer emitting
// more paths than that silently loses the earliest ones. Producing a list
// longer than the loop may run blocks with `loop_limit` anyway, and that
// ceiling is an order of magnitude lower.
func resolveForEach(list workflow.ForEach, rc workflow.RenderContext) ([]string, error) {
	var out []string
	for i, entry := range list {
		rendered, err := workflow.Render(fmt.Sprintf("for_each[%d]", i), entry, rc)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(rendered, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out, nil
}

// recordedItems is the item each already-started iteration ran on, taken from
// its rows (decision 8).
func recordedItems(history []store.StepRun) map[int]string {
	out := map[int]string{}
	for i := range history {
		run := &history[i]
		if run.Iteration > 0 && run.LoopItem != "" {
			out[run.Iteration] = run.LoopItem
		}
	}
	return out
}

// latestStatesIn is the state of the newest attempt of each body step within
// one iteration — the same derivation `parallel` makes across a group, one
// iteration at a time (decision 7).
func latestStatesIn(history []store.StepRun, iteration int) map[string]store.StepRunState {
	out := map[string]store.StepRunState{}
	// history is ordered by (iteration, attempt, id), so the last row for a
	// step id within the iteration is its newest attempt.
	for i := range history {
		if run := &history[i]; run.Iteration == iteration {
			out[run.StepID] = run.State
		}
	}
	return out
}

// bodyOrder maps each body step id to its declaration position, which is the
// third component of the §8.4 visibility comparison (decision 9).
func bodyOrder(body []workflow.Step) map[string]int {
	order := make(map[string]int, len(body))
	for i, step := range body {
		order[step.ID] = i
	}
	return order
}

// itemAt is the `for_each` item of one iteration, empty for a `count:` loop.
func itemAt(items []string, iteration int) string {
	if iteration < 1 || iteration > len(items) {
		return ""
	}
	return items[iteration-1]
}

// loopLimit resolves how many iterations a loop may run: the step's own
// `max_iterations:`, else the daemon default. Read here rather than cached,
// so a hot reload governs the next loop (§12.3, decision 5).
func (r *Runner) loopLimit(step workflow.Step) int {
	if step.MaxIterations != nil && *step.MaxIterations > 0 {
		return *step.MaxIterations
	}
	if n := r.deps.Config().Loop.MaxIterations; n > 0 {
		return n
	}
	return 1
}
