package taskrun

// Follow-up runs on a finished task (spec §6, task 027).
//
// A task that reaches `done` or `aborted` still owns its worktree, its branch
// and its commits until `archive` runs. A follow-up is how vincent does one
// more piece of work in that window — rebase the branch, add the commit a
// review asked for, drop the stray file — inside the daemon's own ledger,
// with a step run, a transcript, events and cost accounting, instead of
// someone doing it by hand in the worktree.
//
// Three shapes are offered and one is executed. An agent prompt and a shell
// command are compiled into a synthetic one-step workflow by the handler, so
// what arrives here is always a workflow (decision 3) — and every step type
// is allowed in it, including the gates and fan-outs that park a task
// mid-run.
//
// Two invariants make that affordable without a `step_runs` migration:
//
//   - **Rows go past the end of the snapshot.** Round n writes at
//     `step_index = len(snapshot.Steps) + n - 1` (decision 2). That cursor
//     space is unused, so a row there is unambiguously a follow-up row;
//     distinct rounds occupy distinct indices, so CountStepAttempts'
//     `(task_id, step_index, step_id, iteration)` key separates them for
//     free; and `iteration` keeps its §7.8 loop meaning instead of being
//     commandeered for the round number. The workflow snapshot is not
//     touched: §5.3 says it is the workflow as authored, and a follow-up
//     appends nothing to it.
//   - **The run has a cursor of its own**, persisted in the request
//     (decision 4). `current_step` stays where the finished run left it and is
//     never walked by a follow-up, which is what lets a `manual` gate park and
//     be approved, and a `fan_out` park and be joined, without two meanings of
//     one column.
//
// A follow-up decides nothing about the task's verdict: `done` returns to
// `done` and `aborted` to `aborted` (decision 5). Making a human's abort
// reversible by any command that exits 0 is the thing that rule exists to
// prevent.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
)

// FollowUpWorkflowName is the name the synthetic workflow compiled from an
// agent prompt or a shell command carries. It reaches `.Workflow.Name` in
// §8.4's context, so it says what the run is rather than repeating the
// finished task's workflow, which is not what is running.
const FollowUpWorkflowName = "follow-up"

// FollowUpStepID is the step id the synthetic one-step workflow uses. Unlike
// RepairStepID it is an ordinary slug, not a reserved underscore name: a
// follow-up row is told from a workflow step by *where* it sits — past the
// snapshot's last index — and never by its id, because a workflow-form
// follow-up's rows carry the ids their author wrote (decision 2).
const FollowUpStepID = "follow-up"

// followUpEnv is where one step sits inside a follow-up round (decision 4).
// It is the follow-up's answer to loopEnv, and carries the same position pair
// for the same reason: a round's steps share one step_index, so declaration
// order is what puts them in sequence.
type followUpEnv struct {
	// base is `len(snapshot.Steps)`. Every row below it belongs to the
	// task's own workflow and stays visible to a follow-up step's context;
	// every row at or past it belongs to some follow-up round.
	base int
	// round is 1-based; this round's rows sit at base+round-1.
	round int
	// pos is this step's position in the follow-up workflow, and order maps
	// every step id in it to its own.
	pos   int
	order map[string]int
}

// followUpRowIndex is decision 2's placement: round n of a task whose
// snapshot has `base` steps writes its rows at base+n-1. Round 1 lands
// exactly one past the last step, which is where a finished task's
// `current_step` already points.
func followUpRowIndex(base, round int) int {
	if round < 1 {
		round = 1
	}
	return base + round - 1
}

// runFollowUp executes one follow-up admission (§6, task 027) and returns the
// task to the state the follow-up was launched from.
//
// snapshot is the *task's* workflow — read only for its length, which is
// where this round's rows go. What actually runs is the workflow carried by
// the request, spliced at request time exactly as a task's snapshot is at
// creation, so a registry edit mid-follow-up cannot mutate a run in flight.
func (r *Runner) runFollowUp(
	ctx context.Context, task *store.Task, project *store.Project,
	snapshot *workflow.Workflow, req store.FollowUpRequest, log *slog.Logger,
) {
	// A cancel or shutdown that landed between admission and here must not
	// spawn a process for a task that is already ending; the request stays
	// set, so the follow-up resumes from its cursor on the next admission.
	if ctx.Err() != nil || r.interrupting(task.ID) {
		r.interrupt(task, log)
		return
	}
	if req.Abandoned {
		// `skip` from the block a follow-up step produced (decision 6). The
		// origin is still on the request, which is the whole reason the
		// record is kept rather than dropped.
		r.finishFollowUp(task, req, log)
		return
	}
	fu, err := followUpWorkflow(req)
	if err != nil {
		// The handler parsed, expanded and re-validated this document before
		// persisting it, so a failure here is corruption or a vincent
		// downgrade — the case §12.4 already treats a snapshot that no longer
		// parses as. The task blocks; a human retries, skips or cancels.
		r.fail(task, ReasonInvalidSnapshot, log, "parse follow-up workflow", err)
		return
	}
	base := len(snapshot.Steps)
	index := followUpRowIndex(base, req.Round)
	log = log.With("follow_up_round", req.Round, "follow_up_index", index, "follow_up_form", req.Form)

	// The cursor is copied, not aliased: `transition` overwrites *task with
	// the row it read back, and this is what the walk advances and the ending
	// reads.
	pending := req
	r.runSteps(ctx, project, &stepWalk{
		task:  task,
		wf:    fu,
		steps: fu.Steps,
		from:  clampCursor(req.Cursor, len(fu.Steps)),
		// Every step of the round writes at the round's own index. Rows are
		// told apart by step id — and, inside a loop, by iteration — exactly
		// as a `parallel` group's sub-steps are (decision 2).
		rowIndex: func(int) int { return index },
		persist: func(_ context.Context, pos int, log *slog.Logger) {
			pending.Cursor = pos
			// persistCtx, not ctx: the cursor must survive the shutdown that
			// is cancelling this admission, or recovery would re-run a step
			// the run had finished with.
			if err := r.deps.Store.SetPendingFollowUp(r.persistCtx(), task.ID, &pending); err != nil {
				log.Error("persist follow-up cursor", "error", err)
			}
		},
		finish:   func() { r.finishFollowUp(task, pending, log) },
		followUp: &followUpEnv{base: base, round: req.Round, order: bodyOrder(fu.Steps)},
		log:      log,
	})
}

// finishFollowUp returns the task to the state the follow-up came from
// (decision 5): `done → done` through the ordinary Complete, `aborted →
// aborted` through Restore, which exists for this and nothing else
// (decision 7).
//
// The request is not drained here. TransitionTask drops a pending follow-up
// on any transition into a settled state, which is exactly these two and the
// `cancel` that ends a follow-up early — one rule rather than three callers
// remembering to (decision 6).
func (r *Runner) finishFollowUp(task *store.Task, req store.FollowUpRequest, log *slog.Logger) {
	action := taskstate.Complete
	if req.Origin == store.TaskAborted {
		action = taskstate.Restore
	}
	if !r.transition(task, action, store.TaskChange{}, log) {
		return
	}
	if req.Abandoned {
		log.Info("follow-up abandoned; task restored", "state", string(task.State))
		return
	}
	log.Info("follow-up finished; task restored", "state", string(task.State))
}

// followUpWorkflow parses the workflow the request carries.
//
// Parse runs with zero Options because the document was validated against the
// daemon's real ones at request time and stored as it will run — the same
// reason execute() parses a task's snapshot that way.
func followUpWorkflow(req store.FollowUpRequest) (*workflow.Workflow, error) {
	wf, _, err := workflow.Parse([]byte(req.Workflow), workflow.Options{})
	if err != nil {
		return nil, err
	}
	if len(wf.Steps) == 0 {
		return nil, fmt.Errorf("follow-up workflow %q has no steps", wf.Name)
	}
	return wf, nil
}

// clampCursor keeps a persisted cursor inside the workflow it points into. A
// cursor past the end means the run finished and the ending transition did
// not commit; the walk then does nothing and reaches the ending again, which
// is the idempotence §12.4 asks of every resumed step.
func clampCursor(cursor, n int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > n {
		return n
	}
	return cursor
}

// followUpAt is the follow-up run a human action is about: the request, the
// index its rows sit at, and the step its cursor points at.
//
// Everywhere a human action asks "which step is this about", the answer is
// `current_step` for an ordinary task and this for one with a follow-up in
// flight (decision 4). The five callers are Skip, Approve, Reject's row,
// repair's failure context and the `retry` that refuses an edit.
type followUpAt struct {
	req   store.FollowUpRequest
	index int
	// step is the step the cursor points at. A cursor past the end leaves it
	// zero, which is legitimate: the run finished and its ending transition
	// has not landed yet.
	step workflow.Step
	// hasStep reports whether step is a real step rather than the zero value.
	hasStep bool
}

// followUpOf reports the follow-up run in flight on a task, and false when
// there is none — which is every ordinary task, and a task whose follow-up a
// human has already abandoned with `skip`.
//
// A request whose documents no longer parse reports false rather than an
// error: a human action must not fail because a stored document rotted, and
// the admission that follows will block the task with `invalid_snapshot`
// where the reason is legible.
func (r *Runner) followUpOf(task *store.Task) (followUpAt, bool) {
	req := task.PendingFollowUp
	if req == nil || req.Empty() || req.Abandoned {
		return followUpAt{}, false
	}
	snapshot, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		return followUpAt{}, false
	}
	fu, err := followUpWorkflow(*req)
	if err != nil {
		return followUpAt{}, false
	}
	at := followUpAt{req: *req, index: followUpRowIndex(len(snapshot.Steps), req.Round)}
	if cursor := clampCursor(req.Cursor, len(fu.Steps)); cursor < len(fu.Steps) {
		at.step, at.hasStep = fu.Steps[cursor], true
	}
	return at, true
}

// advanceFollowUp moves a follow-up's own cursor past the step a human just
// decided on — the `approve` or gate `skip` of decision 4. `current_step` is
// deliberately untouched: it names where the finished workflow run ended, and
// a follow-up never walks it.
func advanceFollowUp(at followUpAt) store.TaskChange {
	req := at.req
	req.Cursor++
	return store.TaskChange{PendingFollowUp: &req}
}

// abandonFollowUp records a follow-up a human ended with `skip` from the
// block one of its steps produced (decision 6).
//
// The request is marked rather than dropped, because Origin is still needed:
// the admission this transition produces restores `done` or `aborted` without
// running anything. Dropping it would leave the task `queued` with the
// snapshot's steps all finished, which completes it — turning a `skip` on an
// aborted task's follow-up into a promotion to `done`.
func abandonFollowUp(at followUpAt) store.TaskChange {
	req := at.req
	req.Abandoned = true
	return store.TaskChange{PendingFollowUp: &req}
}

// followUpRound numbers the next follow-up on a task: one past the highest
// round its rows record, and 1 when it has none.
//
// It is derived from the rows rather than counted on the task, so the number
// and the rows it places can never disagree — a stored counter that drifted
// would put two rounds at one index and merge their attempt numbering
// (decision 2).
func (r *Runner) followUpRound(ctx context.Context, task *store.Task) (int, error) {
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		// The snapshot no longer parses, so the admission this request
		// produces will block with `invalid_snapshot` before the round is
		// ever used to place a row. Refusing here instead would take the
		// diagnosis away and give back a worse error.
		return 1, nil //nolint:nilerr // deliberately tolerated; see above
	}
	maxIndex, ok, err := r.deps.Store.MaxStepIndex(ctx, task.ID)
	if err != nil {
		return 0, err
	}
	base := len(wf.Steps)
	if !ok || maxIndex < base {
		return 1, nil
	}
	return maxIndex - base + 2, nil
}

// ApplyFollowUpSelection fills each agent step's blank agent/model/effort
// from the request, which stands in for the step level of §8.6's chain
// (decision 12).
//
// A field the step declares is left alone: §8.6 already says a step field
// outranks everything below it, and a workflow-form follow-up's author wrote
// that field on purpose. The walk descends into `parallel` and `loop` bodies,
// whose members are steps in every other respect. It stops at `fan_out`
// lanes, which are not steps at all — a lane becomes a child task and
// inherits the parent task's own overrides (§7.6 decision 10).
func ApplyFollowUpSelection(steps []workflow.Step, req store.FollowUpRequest) {
	for i := range steps {
		if len(steps[i].Steps) > 0 {
			ApplyFollowUpSelection(steps[i].Steps, req)
		}
		if steps[i].Type != workflow.StepAgent {
			continue
		}
		steps[i].Agent = firstNonEmpty(steps[i].Agent, req.Agent)
		steps[i].Model = firstNonEmpty(steps[i].Model, req.Model)
		steps[i].Effort = firstNonEmpty(steps[i].Effort, req.Effort)
	}
}

// CompileFollowUp turns an agent prompt or a shell command into the one-step
// workflow the engine runs, so runFollowUp has exactly one shape to execute
// (decision 3).
//
// The text is escaped, not templated. It is prose or a command line typed at
// a form, and §8.4 renders with `missingkey=error` — a stray `{{` would fail
// the run before the process started. This is the same rule a repair prompt
// follows, and it is why a follow-up is not a workflow-authoring surface: an
// operator who wants `{{.Task.BranchName}}` writes a workflow and runs that.
//
// The budget is one attempt. A failed one-off run blocks and a human decides,
// rather than silently paying for a second agent run — the built-in `adhoc`
// workflow's reasoning (phase 2 decision) applied to the same shape of run.
func CompileFollowUp(req store.FollowUpRequest) (*workflow.Workflow, error) {
	noRetries := 0
	step := workflow.Step{
		ID:         FollowUpStepID,
		Name:       "follow-up",
		MaxRetries: &noRetries,
	}
	switch req.Form {
	case store.FollowUpAgent:
		step.Type = workflow.StepAgent
		step.Prompt = workflow.EscapeTemplate(req.Prompt)
	case store.FollowUpCommand:
		step.Type = workflow.StepCommand
		step.Run = workflow.EscapeTemplate(req.Run)
	default:
		return nil, fmt.Errorf("follow-up form %q has no compiled shape", req.Form)
	}
	return &workflow.Workflow{
		Name:        FollowUpWorkflowName,
		Description: "an ad-hoc follow-up run on a finished task",
		Steps:       []workflow.Step{step},
	}, nil
}
