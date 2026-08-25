package taskrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/lezli01/vincent/internal/agent"
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
	// ReasonInputTimeout is a §7.4 wait expiring: the pending input request
	// was never answered within input_timeout.
	ReasonInputTimeout = "input_timeout"
	// ReasonInputProtocolError is a control request vincent could not parse
	// or that broke the serial-request contract — the attempt fails rather
	// than wait on a request it can't render (§18).
	ReasonInputProtocolError = "input_protocol_error"
	// ReasonRestrictedUnsupported is a `restricted` step whose adapter cannot
	// restrict on this platform (§9.4). Distinct from agent_unavailable on
	// purpose: the CLI is installed and healthy, so "not found" would send
	// the user to reinstall something that is already there.
	ReasonRestrictedUnsupported = "restricted_unsupported"
	// ReasonTranscriptLimit is an attempt whose transcript passed
	// `transcript_max_bytes` (§12.3, §18). The run is killed rather than
	// allowed to fill the disk; the partial transcript is kept, because the
	// lines that got there are exactly what explains the runaway.
	ReasonTranscriptLimit = "transcript_limit"
	// ReasonTranscriptIOError is an attempt whose evidence did not land: a
	// transcript write, JSON encode or close that failed (§12.2, §18, #139).
	// Disk-full, a revoked permission and a short write all arrive as this.
	//
	// It exists because the alternative is worse than a failure. Judging a
	// `command` step from its exit code alone reports success over a record
	// that is missing the run it claims to describe, and "everything is
	// transcripted" (docs/security-model.md) is then false with nothing
	// saying so. It runs the ordinary §7.2 budget — the next attempt writes a
	// new file, and a transient ENOSPC is exactly the kind of thing a retry
	// clears — and it is absent from allowFailure's set on §7.2's own rule:
	// vincent failing to record the step is not an outcome the step produced,
	// so a workflow must not be able to branch on "the disk filled up" as
	// though it were a test result (task 015 decision 5).
	//
	// It is not the reason for an over-long line. Capture keeps those, in
	// bounded chunks; `transcript_max_bytes` stays the single size-based
	// failure (§12.3).
	ReasonTranscriptIOError = "transcript_io_error"
	// ReasonAgentProtocolError is an agent run whose stream vincent could not
	// read to the end: the adapter's line reader stopped on an error (§9.1,
	// §18, #139).
	//
	// Deliberately not `agent_error`, which means "the CLI reported a
	// failure" and would send the reader to a CLI that did nothing wrong —
	// the reader that failed is vincent's. Deliberately not
	// `input_protocol_error` either: that names a control message vincent
	// could not render, which is a message that arrived intact.
	ReasonAgentProtocolError = "agent_protocol_error"
	// ReasonUsageLimit is an agent run the CLI stopped because the account's
	// usage quota is spent (task 003, §7.2, §18). It is the one reason in this
	// vocabulary that is *not* a failure: the attempt is recorded
	// `interrupted`, consumes no retry, and the task returns to `queued` with
	// an admission hold until the window is plausibly back. It therefore
	// appears as a `queued_reason`, never as a `block_reason`.
	ReasonUsageLimit = "usage_limit"
	// ReasonAgentUnauthenticated is a run the CLI refused because it is not
	// logged in (task 003, §18). An ordinary failure under §7.2's budget —
	// waiting cannot fix it, and short-circuiting the budget would make this
	// the first reason in vincent to bypass §7.2 to save one process spawn.
	// The value of the reason is that it names the fix instead of sending the
	// reader to a transcript.
	ReasonAgentUnauthenticated = "agent_unauthenticated"
	// ReasonPlatformUnsupported is a snapshot whose `platforms:` list does not
	// admit this host (§8.1.1, task 010). Task creation rejects that outright,
	// so reaching it here means the task and its daemon parted company — a
	// data directory carried to another OS, or a workflow edited to exclude
	// this one after the task was queued. Distinct from invalid_snapshot: the
	// snapshot is perfectly valid, just not here.
	ReasonPlatformUnsupported = "platform_unsupported"
	// ReasonInputUnsupported is a step declaring `on_input: require` whose
	// resolved adapter cannot stop and ask (§7.4, task 013). Task creation
	// refuses these, so reaching it here means the task and its daemon parted
	// company: claude upgraded past the §9.3 version ceiling, a data directory
	// carried to a machine where that agent is a different build, or a
	// workflow edited after the task was queued. Distinct from
	// agent_unavailable on the same grounds restricted_unsupported is — the
	// CLI is installed and healthy, just not able to hold a conversation.
	ReasonInputUnsupported = "input_unsupported"
	// ReasonConditionError is a step guard that did not produce a verdict
	// (§7.7, task 015): the `if:` template failed to render, or it rendered
	// to something that is neither `true` nor `false`.
	//
	// It blocks without consuming the retry budget, unlike every other
	// failure in this vocabulary, because a guard is evaluated *before* the
	// step becomes an attempt — there is no attempt to retry, and re-rendering
	// an unchanged template against an unchanged context cannot answer
	// differently. §7.2's budget bounds work that a second try could complete;
	// the second try here is the human's, after they fix the workflow
	// (task 015 decision 14).
	ReasonConditionError = "condition_error"
	// ReasonLoopLimit is a `loop` step that cannot run within its
	// `max_iterations` (§7.8, task 016 decision 5): a `for_each` list longer
	// than the ceiling, or a `count:` the ceiling moved under.
	//
	// It blocks rather than truncating, and rather than advancing. A loop
	// that ran out of iterations did not achieve what it was looping for, and
	// advancing would hand every downstream guard a `.Steps` that says the
	// work is finished. `condition` (§7.7) is how a workflow stops and
	// succeeds when it genuinely has nothing more to do — that is a decision
	// the workflow made. Running out of tries is not a decision, it is a
	// wall.
	ReasonLoopLimit     = "loop_limit"
	ReasonInternalError = "internal_error"
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
//
// A member of a `parallel` group gets its own stepEnv carrying the group's
// index and its own step, with inGroup set — the flag decides transcript
// naming and nothing else, because in every other respect a sub-step runs
// exactly like the step it would have been on its own (task 014).
type stepEnv struct {
	task    *store.Task
	project *store.Project
	wf      *workflow.Workflow
	step    workflow.Step
	index   int
	inGroup bool
	// conflicts are the files a fan_out join is asking an `on_conflict:
	// agent` resolver to fix (§7.6, task 014 decision 24). Empty everywhere
	// else.
	conflicts []string
	// resumedFromConflict marks a join re-entered by a human retry after a
	// merge_conflict block, as opposed to after a crash. Only the crash may
	// run `git merge --abort` — aborting over a conflict somebody spent an
	// hour resolving is the failure decision 9 exists to prevent.
	//
	// It is set by runFanOut *before* the attempt row is created, because the
	// evidence is the previous attempt's outcome and creating this attempt's
	// row hides it.
	resumedFromConflict bool
	// loop is where this step sits inside an enclosing `loop` body (§7.8,
	// task 016). Nil for every step outside one, which is where both
	// `iteration = 0` on the row and `.Loop.Index: 0` in the context come
	// from — a shared template can therefore tell whether it is in a loop
	// without the engine keeping a second flag (decision 9).
	loop *loopEnv
	// followUp is where this step sits inside a follow-up run, and nil for
	// every step of an ordinary admission (§6, task 027 decision 4). It is
	// what makes a round's rows legible to each other and blind to the rows
	// of earlier rounds — see blindTo and precedes.
	followUp *followUpEnv
	log      *slog.Logger
}

// iteration is the 1-based pass of the enclosing loop, 0 outside one.
func (e *stepEnv) iteration() int {
	if e.loop == nil {
		return 0
	}
	return e.loop.iteration
}

// ref is the position this step's rows belong to: index, id and iteration
// (§7.8, decision 7). Every attempt count, failure lookup and transcript name
// is scoped by it, which is what keeps a loop body step's retry budget its
// own in each iteration (decision 6).
func (e *stepEnv) ref() store.StepRef {
	return store.StepRef{
		TaskID: e.task.ID, StepIndex: e.index, StepID: e.step.ID, Iteration: e.iteration(),
	}
}

// precedes reports whether run sits before this step in the run order —
// (step_index, iteration, body position) — which is the §8.4 visibility rule
// for a *failed* row (decision 9).
//
// Before task 016 this was `run.StepIndex < env.index`, and that is still
// what it says outside a loop. Inside one it cannot be: a loop's body steps
// share the loop's index, so under the old rule a `break` guard could not
// read the `allow_failure` probe two lines above it in its own body, and the
// converge loop would never break.
//
// A `parallel` sub-step has no body position — a group is a set — so no
// sibling ever precedes another, and §7.5's sibling-blindness is preserved
// by the nil check rather than by the index comparison.
func (e *stepEnv) precedes(run *store.StepRun) bool {
	if run.StepIndex != e.index {
		return run.StepIndex < e.index
	}
	if e.loop != nil {
		if run.Iteration != e.loop.iteration {
			return run.Iteration < e.loop.iteration
		}
		pos, ok := e.loop.order[run.StepID]
		return ok && pos < e.loop.pos
	}
	if e.followUp != nil {
		// A follow-up round's steps share one index for the reason a loop
		// body's do, and are ordered by position for the same reason
		// (task 027 decision 2). A round is a *sequence*, not a set, so an
		// earlier step's `allow_failure` row is readable by a later one.
		pos, ok := e.followUp.order[run.StepID]
		return ok && pos < e.followUp.pos
	}
	return false // a group sibling, or this step's own earlier attempt
}

// blindTo reports whether run is a row this step's context may not see. Two
// unrelated rules share the gate because both are "this row is not a result of
// a step that precedes me".
//
// The second is the simpler one: a `loop` step's own row is never a `.Steps`
// entry. A loop contributes results under its *body* steps' ids, and the one
// row it writes under its own — the task 018 D6 row saying an empty `for_each`
// ran nothing — is a record for a detail view, not a result. Letting it
// through would make the loop's id a `.Steps` key that is present exactly when
// the loop did nothing and absent when it did something, which is a worse
// signal than no signal. A `fan_out`'s row is a real result ("merged 3 lanes")
// and stays visible; only `loop` is filtered, and every row a loop step writes
// under its own id carries that type.
//
// The first rule: run is a `parallel` sibling this step may not see at all,
// whatever state it ended in (§7.5).
//
// A group is a *set*: §7.5 promises that no sibling can be read by another's
// guard, and gives as its reason that guards are evaluated before anything in
// the group starts. That reason only covers the first admission. A group
// re-admitted after one sub-step failed skips the ones that already succeeded
// (pendingSubSteps) and their `succeeded` rows are still on disk, so without
// this the surviving sub-step's guard would read a sibling on the retry and
// not on the first run — the same guard, the same context, two verdicts,
// decided by whether a human had pressed retry. Making the blindness
// unconditional is what keeps §7.5's set semantics a fact rather than a
// property of timing.
//
// It is deliberately narrower than precedes, which answers a different
// question for *failed* rows only: this one is about the whole set, so it
// covers every state, and it says nothing about a step's own rows — a
// sub-step being retried still reads its own history through `.LastFailure`.
func (e *stepEnv) blindTo(run *store.StepRun) bool {
	if run.StepType == workflow.StepLoop {
		return true
	}
	// The third rule, and the plainest: a repair is not a step of the
	// workflow (task 025). Its row sits at the blocked step's index under a
	// reserved id, and letting it through would put `__repair` in `.Steps`
	// for every step after it — a key no workflow author wrote, present
	// exactly when somebody happened to press a key.
	if run.StepID == RepairStepID {
		return true
	}
	// The fourth: an *earlier* follow-up round's rows (task 027 decision 9).
	// Rows below the snapshot's length are the original workflow's and stay
	// visible — a follow-up agent reading `.Steps.review.Output` is the point
	// — and this round's own rows are its own. Everything else past that line
	// belongs to a round nobody wrote into the workflow being run, exactly as
	// a `__repair` row does.
	if e.followUp != nil && run.StepIndex >= e.followUp.base && run.StepIndex != e.index {
		return true
	}
	if !e.inGroup || e.loop != nil {
		return false
	}
	return run.StepIndex == e.index && run.StepID != e.step.ID
}

// stepOutcome is the result of one attempt.
type stepOutcome struct {
	state         store.StepRunState
	reason        string
	result        string
	output        string // tail of the attempt's output, for the retry block
	exitCode      *int
	checkExitCode *int
	// retryAfter carries a reason=usage_limit outcome's reset time, when the
	// CLI reported one (task 003). nil means it did not, and the hold falls
	// back to `usage_limit_recheck_interval`.
	retryAfter *time.Time
	// agentName is the adapter an agent attempt ran on (task 026). It is what
	// makes the quota observation per-adapter rather than per-task, so the
	// hold can record *which* window closed. Empty for every non-agent step,
	// and for an aggregated outcome (`parallel`, `loop`) whose collected
	// attempt was not one.
	agentName string
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
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		// The snapshot validated at creation, so this is corruption or a
		// vincent downgrade — either way the task cannot run.
		r.fail(task, ReasonInvalidSnapshot, log, "parse workflow snapshot", err)
		return
	}
	if mismatch := wf.PlatformMismatch(workflow.HostPlatform()); mismatch != "" {
		// Creation refuses this (§8.1.1), so the task outlived the host it was
		// created on. Blocking names the reason; the human moves it back or
		// widens the workflow.
		r.fail(task, ReasonPlatformUnsupported, log, "workflow platform restriction",
			errors.New(mismatch))
		return
	}
	// A follow-up a human abandoned with `skip` from the block one of its
	// steps produced (task 027 decision 6). It restores the origin state and
	// runs nothing — before ensureWorktree, deliberately: a task with nothing
	// left to run must not be able to block on creating a worktree it will
	// never use.
	if req := task.PendingFollowUp; req != nil && !req.Empty() && req.Abandoned {
		r.finishFollowUp(task, *req, log)
		return
	}
	if err := r.ensureWorktree(ctx, task, project, log); err != nil {
		return // ensureWorktree already blocked or re-queued the task
	}
	// An ad-hoc repair the human asked for while the task was blocked (§6,
	// task 025). It is a whole admission of its own: one agent runs in this
	// worktree and the task goes back to `blocked` at the same step, so the
	// step walk below never starts.
	//
	// It sits after ensureWorktree deliberately. A task blocked before its
	// worktree existed (`branch_exists`, `base_branch_missing`) re-blocks on
	// the same reason above without spawning an agent, which is the right
	// outcome reached by code that already existed.
	if req := task.PendingRepair; req != nil && !req.Empty() {
		r.runRepair(ctx, task, project, wf, *req, log)
		return
	}
	// A follow-up run the human asked for from `done` or `aborted` (§6, task
	// 027). Like a repair it is a whole admission of its own — the snapshot's
	// steps are finished and are not walked again — and unlike a repair it
	// walks a workflow of its own, with a cursor of its own, before returning
	// the task to the state it came from (decisions 4, 5).
	//
	// It sits after PendingRepair because a follow-up that blocked and was
	// then repaired carries both: the repair runs, re-blocks, and leaves the
	// follow-up request for the retry that comes after it.
	if req := task.PendingFollowUp; req != nil && !req.Empty() {
		r.runFollowUp(ctx, task, project, wf, *req, log)
		return
	}

	r.runSteps(ctx, project, &stepWalk{
		task:     task,
		wf:       wf,
		steps:    wf.Steps,
		from:     task.CurrentStep,
		rowIndex: func(pos int) int { return pos },
		persist: func(ctx context.Context, pos int, log *slog.Logger) {
			task.CurrentStep = pos
			r.persistStepCursor(ctx, task, log)
		},
		finish: func() { r.complete(task, log) },
		log:    log,
	})
}

// stepWalk is one pass over a list of steps: where it starts, where its rows
// go, how its cursor is persisted, and what ending it reaches when it runs
// off the end.
//
// There are exactly two (task 027 decision 4). An ordinary admission walks
// the task's snapshot from `current_step`, writes a row per step at that
// step's own index, and ends in `complete`. A follow-up walks its own
// workflow from the request's cursor, writes every row of the round at one
// index past the snapshot's end (decision 2), and ends by restoring the state
// the follow-up came from. Everything between those two ends — guards,
// gates, groups, loops, fan-outs, retries, pause and interruption — is
// identical, which is why it is written once here rather than twice.
type stepWalk struct {
	task *store.Task
	// wf is the workflow the steps belong to: its `defaults:` feed §8.6 and
	// §7.2 resolution, and its name reaches `.Workflow` in §8.4's context.
	wf    *workflow.Workflow
	steps []workflow.Step
	// from is the position in steps to resume at.
	from int
	// rowIndex maps a position in steps to the `step_index` its rows are
	// written at. Identity for an ordinary walk; constant for a follow-up.
	rowIndex func(pos int) int
	// persist records the cursor after a step is finished with.
	persist func(ctx context.Context, pos int, log *slog.Logger)
	// finish ends the walk: the last step succeeded, or a `condition` stopped
	// it early (§7.7).
	finish func()
	// followUp marks this walk as a follow-up round and carries what its
	// steps need to know about it; nil for an ordinary admission.
	followUp *followUpEnv
	log      *slog.Logger
}

// runSteps walks a step list until it finishes, parks, or fails. It is the
// body of one admission for an ordinary run and of one for a follow-up; the
// two differ only in the stepWalk they are handed.
func (r *Runner) runSteps(ctx context.Context, project *store.Project, w *stepWalk) {
	task, wf, log := w.task, w.wf, w.log
	for pos := w.from; pos < len(w.steps); pos++ {
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
		index := w.rowIndex(pos)
		env := &stepEnv{
			task: task, project: project, wf: wf,
			step: w.steps[pos], index: index,
			followUp: w.stepFollowUp(pos),
			log:      log.With("step", w.steps[pos].ID, "step_index", index),
		}
		// Guards run before the dispatch below, so every step type is
		// guarded by the same code — including the three that never reach
		// runAttempt (§7.7). A verdict is computed fresh here and nowhere
		// else (decision 10).
		if env.step.Guarded() || env.step.Type == workflow.StepCondition {
			pass, err := r.evaluateGuard(ctx, env)
			switch {
			case err != nil:
				r.recordGuardOutcome(ctx, env, store.StepFailed, "", ReasonConditionError)
				r.fail(task, ReasonConditionError, env.log, "evaluate step guard", err)
				return
			case env.step.Type == workflow.StepCondition && !pass:
				// The sequence ends here and the task is `done` (§7.7,
				// decision 8). The cursor goes to the end rather than
				// staying put, so completion runs the same path every other
				// finished task runs and no client reads a done task as
				// mid-run.
				r.recordGuardOutcome(ctx, env, store.StepStopped, "", "")
				w.persist(ctx, len(w.steps), env.log)
				env.log.Info("workflow stopped early by a condition step")
				w.finish()
				return
			case env.step.Type == workflow.StepCondition:
				r.recordGuardOutcome(ctx, env, store.StepSucceeded, "", "")
				w.persist(ctx, pos+1, env.log)
				continue
			case !pass:
				// Skip and carry on: the step did not run, the workflow did
				// not stop, and the row says which of the two kinds of skip
				// this was (decision 9).
				r.recordGuardOutcome(ctx, env, store.StepSkipped, store.SkipReasonCondition, "")
				w.persist(ctx, pos+1, env.log)
				env.log.Info("step skipped by its guard")
				continue
			}
		}
		if env.step.Type == workflow.StepManual {
			r.enterGate(ctx, env)
			return
		}
		// A group has no attempt of its own to retry: each sub-step carries its
		// own budget, so the group's outcome is the collected one rather than
		// anything runStepWithRetries could produce (task 014 decisions 17, 18).
		outcome := stepOutcome{}
		switch env.step.Type {
		case workflow.StepParallel:
			outcome = r.runGroup(ctx, env)
		case workflow.StepLoop:
			// Like a group: no attempt of its own, so no retry budget of its
			// own either. The outcome is collected from the body's rows
			// (§7.8, decision 7).
			outcome = r.runLoop(ctx, env)
		case workflow.StepFanOut:
			// Spawning parks the task and ends this goroutine; joining
			// returns an ordinary outcome the switch below acts on
			// (§7.6, decision 3).
			var stop bool
			outcome, stop = r.runFanOut(ctx, env)
			if stop {
				return
			}
		default:
			outcome = r.runStepWithRetries(ctx, env)
		}
		switch outcome.state {
		case store.StepSucceeded:
			w.persist(ctx, pos+1, env.log)
		case store.StepInterrupted:
			// A quota stop is interruption-shaped — no retry consumed, the
			// slot released — but it must not be re-admitted immediately, or
			// the task simply walks back into the same wall (task 003).
			if outcome.reason == ReasonUsageLimit {
				r.holdForUsageLimit(task, outcome.agentName, outcome.retryAfter, env.log)
				return
			}
			r.interrupt(task, log)
			return
		case store.StepFailed, store.StepRunning, store.StepApproved,
			store.StepRejected, store.StepSkipped, store.StepStopped:
			// `allow_failure` advances past the failures the step itself
			// produced (§7.2, task 015 decision 5). The row keeps its
			// `failed` state and its reason — the failure happened, it just
			// did not stop the workflow — and that row is what a later
			// guard reads through `.Steps`.
			if outcome.state == store.StepFailed && allowFailure(env.step, outcome.reason) {
				env.log.Info("step failed; advancing on allow_failure", "reason", outcome.reason)
				w.persist(ctx, pos+1, env.log)
				continue
			}
			r.fail(task, outcome.reason, env.log, "step failed", nil)
			return
		}
	}
	w.finish()
}

// stepFollowUp is the follow-up position of the step at pos, or nil when this
// walk is not a follow-up round.
func (w *stepWalk) stepFollowUp(pos int) *followUpEnv {
	if w.followUp == nil {
		return nil
	}
	at := *w.followUp
	at.pos = pos
	return &at
}

// ensureWorktree creates the task's worktree on first admission (§10); a
// queued task that never ran leaves none behind.
func (r *Runner) ensureWorktree(ctx context.Context, task *store.Task, project *store.Project, log *slog.Logger) error {
	if task.WorktreePath != "" {
		return nil
	}
	// No recompute of an empty BranchName here any more (task 001). It used to
	// cover a crash between the insert and a second write that assigned the name;
	// CreateTask now writes the name in the insert's own transaction, so the
	// window is gone. Keeping the recompute would have been actively harmful once
	// names became configurable: it produced the *built-in* name, so a task whose
	// user chose `feat/OPS-123` would have silently run on `vincent/{id}-{slug}`.
	// CreateAndClaim rather than Create: the directory exists before the row
	// names it, and a gc scan slipping into that window would delete a live
	// task's working tree (task 005). The claim is what closes it.
	path, err := r.deps.Worktrees.CreateAndClaim(ctx, project.Path, task.ID,
		task.BranchName, task.BaseBranch, func(p string) error {
			// A failed persist is logged and the run continues, as it always
			// has: the worktree is real and the step can use it. What it
			// leaves behind is an unclaimed directory, which is precisely the
			// crash case `vincent gc` reclaims.
			if err := r.deps.Store.SetTaskProgress(ctx, task.ID, nil, &p); err != nil {
				log.Error("persist worktree path", "error", err)
			}
			return nil
		})
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
	attempts, err := r.deps.Store.CountStepAttempts(ctx, env.ref(), since)
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
	prev, err := r.deps.Store.LastFailedStepRun(ctx, env.ref())
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
		Iteration: env.iteration(),
		LoopItem:  env.loopItem(),
		State:     store.StepRunning,
	}
	var sel agent.Selection
	if env.step.Type == workflow.StepAgent {
		sel = resolveSelection(env.step, env.wf.Defaults, env.task)
		run.Agent, run.Model, run.Effort = sel.Agent, sel.Model, sel.Effort
	}

	tr, err := openTranscript(r.deps.DataDir, env.task.ID, env.index, env.iteration(), attempt, subStepIDOf(env))
	if err != nil {
		env.log.Error("open transcript", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	}
	defer tr.Close()
	// The cap is read per attempt, not cached: config hot-reloads (§12.3), and
	// an operator who lowers it after a runaway step should see the new value
	// on the retry rather than after a daemon restart.
	tr.SetMax(r.deps.Config().TranscriptMaxBytes.Bytes())
	run.TranscriptPath = tr.Path()
	if r.deps.Config().Debug {
		// Where the log went, said out loud. A transcript nobody can find is
		// not a record — this is the line a user greps for when asked to
		// paste one.
		env.log.Info("step transcript", "attempt", run.Attempt, "path", tr.Path())
	}

	// An `edit + retry` left its text on the task, because the handler ran
	// while the task was blocked and this row did not exist yet (§6). The
	// insert drains it in the same transaction — it marks the attempt the
	// human edited, not the automatic retries that may follow, and a crash
	// cannot clear it without recording it.
	create := r.deps.Store.CreateStepRunTakingOverride
	if env.step.ID == RepairStepID {
		// A repair is not the attempt the human edited (task 025): draining
		// their override onto it would record the edit against a row that is
		// not the step, and take it away from the retry that is.
		create = r.deps.Store.CreateStepRun
	}
	if err := create(r.persistCtx(), run); err != nil {
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
	case workflow.StepFanOut:
		// The join, reached only on a re-admission: the spawn parked the task
		// before this path could run (§7.6, decision 3). Going through
		// runAttempt gives it the row, transcript and events every other step
		// has, rather than a merge that happens invisibly.
		outcome = r.runJoinStep(ctx, env, tr)
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

	tr.Note("step_finished", map[string]any{
		"state": string(outcome.state), "failure_reason": outcome.reason,
		"exit_code": outcome.exitCode, "check_exit_code": outcome.checkExitCode,
	})
	// Close before the attempt is judged, not after it. A buffered filesystem
	// reports ENOSPC at close and nowhere else, so a close error is part of
	// the answer to "did the evidence land" — and a discarded one is the
	// difference between a transcript that is short and one that is *known*
	// to be short (§12.2, #139). The deferred Close above stays as the guard
	// for the early returns; this one is idempotent with it.
	tr.Close()
	// Only a success is overridden. A failure already names something that
	// went wrong, and replacing `nonzero_exit` with the reason the record of
	// it could not be written would hide the more useful fact.
	if err := tr.Err(); err != nil && outcome.state == store.StepSucceeded {
		env.log.Error("transcript incomplete; attempt cannot be called a success",
			"step", env.step.ID, "attempt", attempt, "path", tr.Path(), "error", err)
		outcome.state, outcome.reason = store.StepFailed, ReasonTranscriptIOError
		outcome.result = "transcript incomplete: " + err.Error()
	}
	r.finishStepRun(run, outcome, env.log)
	r.emit(env.task, eventStepFinished, map[string]any{
		"step_id": env.step.ID, "step_index": env.index, "attempt": attempt,
		"run_id": run.ID, "state": string(outcome.state), "failure_reason": outcome.reason,
	})
	return outcome
}

// persistStepCursor writes the task's advanced step cursor. A failed write is
// logged rather than fatal, as it always has been: the in-memory cursor is
// what this admission walks, and recovery re-runs the step the row still
// names (§12.4).
func (r *Runner) persistStepCursor(ctx context.Context, task *store.Task, log *slog.Logger) {
	next := task.CurrentStep
	if err := r.deps.Store.SetTaskProgress(ctx, task.ID, &next, nil); err != nil {
		log.Error("persist step advance", "error", err)
	}
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
		if env.blindTo(&run) {
			continue
		}
		switch run.State {
		case store.StepSucceeded, store.StepApproved, store.StepSkipped:
		case store.StepFailed:
			// A failed step is visible only once the engine has advanced past
			// it — which happens only under `allow_failure` (§7.2), and is
			// the whole point of that field: a guard downstream reads what
			// the probe found (task 015 decision 6).
			//
			// A step's own failed attempt stays out of `.Steps["itself"]`
			// mid-retry, because `.LastFailure` is already that channel and
			// two spellings of one fact is how a context rots. Sub-steps of a
			// group share the group's index, so this also keeps concurrent
			// siblings invisible to each other, which §7.5 requires — and a
			// loop body's earlier steps become visible to its later ones,
			// which §7.8 requires (task 016 decision 9).
			if !env.precedes(&run) {
				continue
			}
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
		Loop:        env.loopContext(),
		Host:        workflow.HostContext{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Worktree:    workflow.WorktreeContext{Path: env.task.WorktreePath},
		LastFailure: workflow.Failure{Reason: previous.reason, Output: previous.output},
		Conflicts:   env.conflicts,
	}, nil
}

// enterGate parks the task at a manual step: the attempt row is written on
// entry so the gate's wait is visible in the timeline, and the task gives up
// its concurrency slot until a human approves or rejects (§6, §11).
func (r *Runner) enterGate(ctx context.Context, env *stepEnv) {
	attempts, err := r.deps.Store.CountStepAttempts(ctx, env.ref(), time.Time{})
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

// holdForUsageLimit re-queues a task whose agent reported a spent usage quota
// (task 003, §11). It is `interrupt` plus an admission hold: the same
// running → queued transition, the same "consumes no retry" (the attempt is
// already recorded `interrupted` by finishStepRun, so the timeline shows it
// and the budget does not), and the slot is released by leaving `running`.
//
// Nothing sleeps here. The actor ends with the admission per the phase 2
// decision, and the scheduler picks the task up within a tick of the hold
// expiring — a sleeping actor would hold the slot for a whole quota window,
// which with max_parallel_tasks slots held that way means nothing runs at all.
func (r *Runner) holdForUsageLimit(
	task *store.Task, agentName string, retryAfter *time.Time, log *slog.Logger,
) {
	// The interval is read now rather than cached, so a config hot-reload
	// (§12.3) reaches the next hold rather than the next daemon restart.
	until := r.now().Add(r.deps.Config().UsageLimitRecheckInterval.Std()).UTC()
	if retryAfter != nil {
		until = retryAfter.UTC()
	}
	// The same effective reset the hold acts on, recorded per adapter so it
	// outlives this task's hold (task 026). It is written before the
	// transition because the transition is what clears `admit_not_before`'s
	// only other copy on the next move out of `queued`.
	r.recordUsageLimit(agentName, until, retryAfter != nil, log)
	reason := ReasonUsageLimit
	ch := store.TaskChange{
		AdmitNotBefore: &until,
		QueuedReason:   &reason,
		EventPayload: map[string]any{
			"queued_reason":    reason,
			"admit_not_before": until.Format(time.RFC3339),
		},
	}
	if r.transition(task, taskstate.Interrupt, ch, log) {
		log.Info("agent usage limit reached; task re-queued until it resets",
			"admit_not_before", until.Format(time.RFC3339),
			"reported_by_cli", retryAfter != nil)
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
	// The ids are copied, never aliased into task. This event is published to
	// the broker and read on every subscriber's goroutine, while the runner
	// keeps rewriting *task on its own (transition does `*task = *updated`) —
	// so an Event pointing into task hands every SSE reader a pointer to
	// memory the engine is still writing.
	taskID, projectID := task.ID, task.ProjectID
	ev := &store.Event{
		Type: evType, TaskID: &taskID, ProjectID: &projectID, Payload: body,
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
