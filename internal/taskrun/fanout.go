package taskrun

// The fan-out half of a `fan_out` step (spec §7.6, task 014 — phase 2, and
// task 080). The merge half is in join.go; the two are one step (decision 3)
// but they run in different admissions, separated by the parent parking.
//
// The shape is a set of **rounds**. On each admission the parent merges every
// lane that is done and not yet on its branch, spawns the lanes whose `needs:`
// those merges have just satisfied, parks in `awaiting_children` releasing its
// slot, and ends the actor goroutine. The scheduler brings it back once every
// descendant has settled (decision 25). When nothing is left unspawned and
// everything is merged, the step succeeds and the cursor advances.
//
// Wave structure is **derived from the graph** (workflow.LaneWaves), never
// declared: no workflow names a wave and none names a maximum. A lane list
// with no `needs:` is exactly one round, which is what makes every workflow
// written before task 080 behave bit-for-bit as it did — spawn, park, merge,
// advance.
//
// The rounds ride on `step_runs.iteration` (task 080 decision 3). That column
// was a loop's alone; it is now "which repeat of this step's row is this",
// which is what keeps round 2's merge from spending round 1's retry budget —
// stepEnv.ref() scopes every attempt count, failure lookup and transcript name
// by it.
//
// A child is an ordinary task in every respect (decision 1): its own row,
// worktree, branch, slot, gates, blocks, transcripts, recovery, gc and doctor
// coverage. The only difference is four columns saying where it came from.

import (
	"context"
	"fmt"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// ReasonFanOutLimit is a derived lane list the run refused to spawn: longer
// than the step's `max_lanes:`, or a tree that would pass `fan_out.max_tasks`
// (§18, task 080 decision 6). It is §7.8's `loop_limit` for the other dynamic
// step — a bound that cannot be checked at creation because the width is a
// fact the run discovers.
const ReasonFanOutLimit = "fan_out_limit"

// ReasonFanOutInvalid is a derived lane list that is not a lane list: an item
// that is not a JSON object, an id that is not a slug, two items rendering to
// one id, or a `needs:` graph with a dangling edge or a cycle (§18, task 080).
//
// Every one of these is a load-time error for a *static* lanes: list. A
// derived list has no ids until it is rendered, so the identical check runs at
// spawn and blocks — decision 6's price for a width nobody can count at
// creation.
const ReasonFanOutInvalid = "fan_out_invalid"

// runFanOut runs one round of a fan_out step: merge what the last round
// finished, spawn what those merges made ready, and park — or, when nothing is
// left, succeed.
//
// It reports whether the actor should stop. Spawning always stops it: the
// parent holds no slot while its children work.
func (r *Runner) runFanOut(ctx context.Context, env *stepEnv) (outcome stepOutcome, stop bool) {
	mine, err := r.laneChildren(ctx, env)
	if err != nil {
		env.log.Error("list lanes", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}, false
	}
	declaredEager := env.step.ScheduleMode() == workflow.ScheduleEager
	if declaredEager {
		// Read at the *start* of the admission, not at the park: a child
		// settling while this admission runs then moves the live count past
		// the recorded position and wakes the parent again, rather than
		// falling into the gap between the two reads (decision 1).
		//
		// Direct children, which is every lane of every fan_out step this
		// task has run — the same set the scheduler counts. Counting this
		// step's lanes alone would undercount by the lanes an earlier
		// fan_out left settled, and the watermark would be exceeded the
		// moment it was written.
		settled, wErr := r.deps.Store.SettledChildren(ctx, env.task.ID)
		if wErr != nil {
			env.log.Error("count settled lanes", "error", wErr)
			return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}, false
		}
		env.laneWatermark = settled
	}
	if unsettled := unsettledLanes(mine); unsettled > 0 && !declaredEager {
		// The lanes exist but have not all settled, so this admission is not
		// a round boundary: the parent was admitted without having parked.
		// The route is a park transition that did not commit — a lost CAS or
		// a DB error after a successful spawn — after which recovery
		// re-queues a `running` parent whose lanes are still `queued`.
		// Merging here would read them as "not done" and block `lane_failed`
		// on lanes that are about to run perfectly well, so the parent parks
		// now and the scheduler brings it back when they have settled (§7.6).
		//
		// Under `schedule: eager` an unsettled sibling is the ordinary case
		// rather than a fault, so the guard is skipped here and re-applied
		// below for a declared-eager step whose selected lanes turn out flat
		// (decision 4) — the only way to know that is to have selected them.
		return r.parkForUnsettled(env, mine, unsettled)
	}

	// Derivation runs once, before anything is spawned, in the parent's
	// render context, and writes its result into the task's snapshot
	// (decision 5). After it the step is an ordinary static `fan_out` and
	// every snapshot consumer — graph, preview, editor, workflowdef — reads
	// it as one with no special case.
	if env.step.Lane != nil && len(mine) == 0 {
		if outcome, ok := r.deriveLanes(ctx, env); !ok {
			return outcome, true
		}
	}

	selected, err := r.selectLanes(ctx, env)
	if err != nil {
		r.recordDecisionRow(ctx, env, store.StepFailed, "", ReasonConditionError, "")
		r.fail(env.task, ReasonConditionError, env.log, "evaluate lane guard", err)
		return stepOutcome{}, true
	}
	if len(selected) == 0 && len(mine) == 0 {
		// Every lane was guarded off, or the derived list was empty. The step
		// is reached, chooses nothing and advances: a fan-out whose conditions
		// all said "not this time" decided correctly (§7.6, task 015
		// decision 11), and an empty `for_each` is §7.8's same case.
		//
		// It must not park. A parent in `awaiting_children` with no children
		// would be re-queued by the scheduler the moment it looked, find no
		// lanes, spawn none and park again — a loop with no exit.
		env.log.Info("fan-out selected no lanes; nothing to spawn")
		r.recordDecisionRow(ctx, env, store.StepSucceeded, "", "",
			"no lane was selected: every lane's `if:` was false, or the derived list was empty")
		return stepOutcome{state: store.StepSucceeded}, false
	}

	// Decision 4: `schedule: eager` on a lane list whose *selected* lanes
	// declare no `needs:` among themselves is redundant, not wrong. Such a
	// list spawns in one round under either mode, so eager could only change
	// when the lanes merge — and merging them as they finish is exactly the
	// widening of task 080 decision 2 that #301 kept out of flat lists. So it
	// runs as a barrier, whatever the field says, and the flat-list guarantee
	// is unconditional.
	env.eager = declaredEager && lanesNeedOrdering(env.step.Lanes, selected)
	if !env.eager {
		if unsettled := unsettledLanes(mine); unsettled > 0 {
			return r.parkForUnsettled(env, mine, unsettled)
		}
	}

	if len(mine) == 0 {
		// Round 0. Nothing has been spawned, so there is nothing to merge and
		// no row to write: the first admission of a flat lane list is exactly
		// what it always was.
		return r.spawnRound(ctx, env, selected, nil, 0)
	}

	if env.eager {
		// A wake that finds nothing to merge and nothing that has failed must
		// not write a step_runs row: an eager parent is woken once per lane
		// settling, and a row per wake would spend the merge's retry budget
		// on admissions that did no work. It falls through to spawnRound,
		// which spawns what is ready or parks again holding no slot.
		due, dErr := r.eagerJoinDue(ctx, env, mine)
		if dErr != nil {
			env.log.Error("check for mergeable lanes", "error", dErr)
			r.fail(env.task, ReasonInternalError, env.log, "check for mergeable lanes", dErr)
			return stepOutcome{}, true
		}
		if !due {
			env.log.Info("eager wake found nothing to merge", "step", env.step.ID)
			return r.spawnRound(ctx, env, selected, mine, r.mergeCount(ctx, env))
		}
	}

	// A round boundary: merge, then spawn whatever those merges made ready.
	// The merge runs through the ordinary attempt path, which gives it a
	// step_runs row like any other step, at this round's `iteration`.
	//
	// The budget is one attempt unless the author asked for more: the two
	// ways a merge fails — a conflict and a lane that did not finish — are
	// both "a human decides", and §7.2 reserves retries for failures a retry
	// can fix. An automatic second merge would abort the first, hit the same
	// conflict, and block anyway.
	round := r.roundOf(env.step.Lanes, selected, mine)
	if env.eager {
		// Under eager the wave a lane sits at is no longer the round number:
		// two admissions can merge two lanes of the same wave and would
		// compute the same `iteration`, colliding on the retry budget and the
		// §12.2 transcript name (decision 2). The counter is instead how many
		// merges this step has already completed. Barrier keeps the
		// wave-derived number, and the two agree there — a barrier step's Nth
		// merge admission merges wave N — which is what lets the default path
		// stay bit-for-bit what it was.
		round = r.mergeCount(ctx, env)
	}
	roundEnv := *env
	roundEnv.round = round
	// Asked before the attempt row exists: see resumedFromConflict.
	roundEnv.resumedFromConflict = r.resumedFromConflict(ctx, &roundEnv)
	if roundEnv.step.MaxRetries == nil {
		zero := 0
		roundEnv.step.MaxRetries = &zero
	}
	outcome = r.runStepWithRetries(ctx, &roundEnv)
	if outcome.state != store.StepSucceeded {
		return outcome, false
	}
	return r.spawnRound(ctx, env, selected, mine, round+1)
}

// spawnRound spawns the lanes whose `needs:` are satisfied and parks, or
// reports the step done when there is nothing left to spawn.
func (r *Runner) spawnRound(
	ctx context.Context, env *stepEnv, selected []int, mine []store.Task, round int,
) (stepOutcome, bool) {
	ready, err := r.readyLanes(ctx, env, selected, mine)
	if err != nil {
		env.log.Error("compute the ready lane set", "error", err)
		r.fail(env.task, ReasonInternalError, env.log, "compute the ready lane set", err)
		return stepOutcome{}, true
	}
	if len(ready) == 0 {
		if unsettled := unsettledLanes(mine); env.eager && unsettled > 0 {
			// Eager only: a barrier admission cannot reach here with an
			// unsettled lane, because the guard at the top of runFanOut sent
			// it back. Both of the branches below would be wrong for it —
			// nothing is unspawned *and* unreachable, and the step is not
			// finished either, it is waiting.
			return r.parkForUnsettled(env, mine, unsettled)
		}
		if spawned := len(mine); spawned < len(selected) {
			// Unreachable for a graph the load-time or spawn-time check
			// accepted: something is unspawned and nothing is ready only if
			// its dependencies never merge, which is a cycle. Said out loud
			// rather than parked forever.
			env.log.Error("fan-out has unspawned lanes but none are ready",
				"selected", len(selected), "spawned", spawned)
			r.fail(env.task, ReasonFanOutInvalid, env.log,
				"no lane is ready and some are unspawned: the needs graph cannot be satisfied", nil)
			return stepOutcome{}, true
		}
		unit := "rounds"
		if env.eager {
			unit = "eager merges"
		}
		return stepOutcome{
			state:  store.StepSucceeded,
			result: fmt.Sprintf("merged %d lanes in %d %s", len(mine), round, unit),
		}, false
	}
	if err := r.spawnLanes(ctx, env, ready); err != nil {
		// Nothing to clean up: the lanes are created in one transaction
		// (store.CreateTasks), so a failure leaves *no* lane behind and a
		// retry re-spawns from a clean slate. Cancelling half a tree used to
		// stand here, and it was the bug: a cancelled lane stays attached to
		// this step, so the retry took the merge path and blocked
		// `lane_failed` on lanes that had never run.
		env.log.Error("spawn lanes", "error", err)
		r.fail(env.task, ReasonInternalError, env.log, "spawn lanes", err)
		return stepOutcome{}, true
	}
	// Park: the slot is released before the children need one, which is what
	// makes this deadlock-free at any depth (decision 6).
	if r.parkFanOut(env) {
		env.log.Info("fan-out round spawned; awaiting children",
			"step", env.step.ID, "round", round, "lanes", len(ready), "eager", env.eager)
	}
	return stepOutcome{}, true
}

// parkForUnsettled parks the parent because lanes it has spawned have not
// settled. It reports the fan-out's stop-the-actor outcome, so a caller can
// return it directly.
func (r *Runner) parkForUnsettled(env *stepEnv, mine []store.Task, unsettled int) (stepOutcome, bool) {
	env.log.Info("fan-out lanes are still running; parking",
		"lanes", len(mine), "unsettled", unsettled, "eager", env.eager)
	if r.parkFanOut(env) {
		env.log.Info("awaiting children", "step", env.step.ID)
	}
	return stepOutcome{}, true
}

// parkFanOut moves the parent to `awaiting_children`, recording an eager
// step's wake position in the same statement as the state change (decision 1).
//
// A barrier park writes nothing: nil leaves the column NULL, which is what the
// scheduler reads as "wake me when the whole subtree has settled". It cannot
// carry an earlier eager step's number either — TransitionTask clears the
// column on the way *out* of `awaiting_children`.
func (r *Runner) parkFanOut(env *stepEnv) bool {
	ch := store.TaskChange{}
	if env.eager {
		watermark := env.laneWatermark
		ch.SettledChildrenWatermark = &watermark
	}
	return r.transition(env.task, taskstate.FanOut, ch, env.log)
}

// eagerJoinDue reports whether an eager admission has anything for the join to
// do: a lane that settled without finishing — which blocks `lane_failed` —
// or a finished lane whose branch is not yet on the parent's.
//
// It is the guard behind "a wake that finds nothing mergeable records no
// step_runs row". "Merged" is asked of git rather than of a cursor, for the
// reason readyLanes gives.
func (r *Runner) eagerJoinDue(ctx context.Context, env *stepEnv, mine []store.Task) (bool, error) {
	for i := range mine {
		lane := &mine[i]
		if lane.State != store.TaskDone {
			if taskstate.Settled(lane.State) {
				return true, nil
			}
			continue
		}
		merged, err := r.deps.Worktrees.Merged(ctx, env.task.WorktreePath, lane.BranchName)
		if err != nil {
			return false, err
		}
		if !merged {
			return true, nil
		}
	}
	return false, nil
}

// mergeCount is how many merges this fan_out step has already completed — the
// `iteration` an eager admission writes its own merge row at (decision 2).
//
// A read failure degrades to 0 rather than failing the step: the cost is a
// merge row colliding with an earlier one's retry budget, and the cost of the
// alternative is a whole fan-out tree abandoned because one COUNT did not
// answer.
func (r *Runner) mergeCount(ctx context.Context, env *stepEnv) int {
	n, err := r.deps.Store.SucceededIterations(ctx, env.task.ID, env.index, env.step.ID)
	if err != nil {
		env.log.Error("count merge rows", "error", err)
		return 0
	}
	return n
}

// lanesNeedOrdering reports whether the selected lanes declare any `needs:`
// among themselves — whether this list is a DAG at all rather than a set.
//
// A `needs:` naming a lane that is not in play imposes no ordering, matching
// readyLanes exactly: a lane that will never spawn cannot be waited for.
func lanesNeedOrdering(lanes []workflow.Lane, selected []int) bool {
	inPlay := make(map[string]bool, len(selected))
	for _, order := range selected {
		inPlay[lanes[order].ID] = true
	}
	for _, order := range selected {
		for _, need := range lanes[order].Needs {
			if inPlay[need] {
				return true
			}
		}
	}
	return false
}

// laneChildren is the children of *this* step. A workflow may fan out more
// than once, and the earlier step's children are none of this one's business.
func (r *Runner) laneChildren(ctx context.Context, env *stepEnv) ([]store.Task, error) {
	all, err := r.deps.Store.ListChildren(ctx, env.task.ID)
	if err != nil {
		return nil, err
	}
	mine := make([]store.Task, 0, len(all))
	for _, c := range all {
		if c.ParentStepIndex != nil && *c.ParentStepIndex == env.index {
			mine = append(mine, c)
		}
	}
	return mine, nil
}

// readyLanes is the declared indices of the selected lanes with no child row
// yet whose `needs:` are all merged into the parent's branch.
//
// "Merged" is asked of git, not of a stored cursor, for the reason join.go
// gives about the merge cursor: a human who reset the branch by hand is
// telling the truth and a persisted copy is not (decision 9). A `needs:` entry
// naming a lane this run did not select — one guarded off — imposes no
// ordering: a lane that will never spawn cannot be waited for, and waiting
// would strand its dependents with nothing saying why.
func (r *Runner) readyLanes(
	ctx context.Context, env *stepEnv, selected []int, mine []store.Task,
) ([]int, error) {
	byLane := make(map[string]*store.Task, len(mine))
	for i := range mine {
		byLane[mine[i].LaneID] = &mine[i]
	}
	inPlay := make(map[string]bool, len(selected))
	for _, order := range selected {
		inPlay[env.step.Lanes[order].ID] = true
	}
	merged := make(map[string]bool, len(mine))
	for _, child := range mine {
		if child.State != store.TaskDone {
			continue
		}
		ok, err := r.deps.Worktrees.Merged(ctx, env.task.WorktreePath, child.BranchName)
		if err != nil {
			return nil, err
		}
		merged[child.LaneID] = ok
	}
	ready := make([]int, 0, len(selected))
	for _, order := range selected {
		lane := env.step.Lanes[order]
		if byLane[lane.ID] != nil {
			continue // already spawned in an earlier round
		}
		blocked := false
		for _, need := range lane.Needs {
			if inPlay[need] && !merged[need] {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, order)
		}
	}
	return ready, nil
}

// roundOf is which round this admission is: the deepest wave any spawned lane
// belongs to. Admission k merges wave k and spawns wave k+1, so the wave the
// spawned lanes sit at *is* the round number, and a flat lane list — every
// lane at wave 0 — writes its one merge row at `iteration: 0`, exactly where a
// pre-task-080 fan-out wrote it.
func (r *Runner) roundOf(lanes []workflow.Lane, selected []int, mine []store.Task) int {
	inPlay := make([]workflow.Lane, 0, len(selected))
	for _, order := range selected {
		inPlay = append(inPlay, lanes[order])
	}
	waves := workflow.LaneWaves(inPlay)
	round := 0
	for _, child := range mine {
		if w, ok := waves[child.LaneID]; ok && w > round {
			round = w
		}
	}
	return round
}

// spawnLanes creates one child task per lane, in declared order.
//
// Everything a lane inherits is decision 10: the parent's branch is the
// child's base branch, its overrides and priority propagate, and a lane spec
// may override any of them for its own subtree. Fields merge with the
// parent's, the lane winning (decision 29).
func (r *Runner) spawnLanes(ctx context.Context, env *stepEnv, ready []int) error {
	project, err := r.deps.Store.GetProject(ctx, env.task.ProjectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	spec := worktree.BranchSpec{
		ProjectTemplate: project.BranchTemplate,
		ConfigTemplate:  r.deps.Config().BranchTemplate,
	}
	children := make([]*store.Task, 0, len(ready))
	for _, order := range ready {
		lane := env.step.Lanes[order]
		// order is the lane's **declared** index, not its position among the
		// selected: `lane_order` is what the join merges by (§7.6,
		// decision 7), so renumbering around a guarded-off lane would make a
		// re-run merge in a different order than the first run.
		child, err := r.laneTask(env, lane, order)
		if err != nil {
			return err
		}
		children = append(children, child)
	}
	// The children's branch names are resolved inside the insert's own
	// transaction, exactly as any other task's are (task 001) — a lane is not
	// a special case in the one place branch names are decided. resolveBranch
	// is called once per row, and the row it is called for is the one whose ID
	// the insert has just assigned: matching on that rather than counting
	// calls keeps this correct however CreateTasks orders its inserts.
	resolve := func(id int64) (string, error) {
		child := laneByID(children, id)
		if child == nil {
			return "", fmt.Errorf("resolve lane branch: no lane holds task id %d", id)
		}
		bctx := worktree.NewBranchContext(child.Title, child.BaseBranch, child.Fields,
			worktree.BranchProject{
				Name: project.Name, Path: project.Path, DefaultBranch: project.DefaultBranch,
			})
		name, _, err := worktree.ResolveBranchName(spec, bctx.WithID(id))
		return name, err
	}
	// One transaction for every lane (§7.6): a partial spawn is the one
	// fan-out state with no honest recovery, so it is made unreachable rather
	// than cleaned up. A lane that fails to insert takes its siblings with it
	// and the step blocks with nothing spawned.
	if err := r.deps.Store.CreateTasks(r.persistCtx(), children, resolve); err != nil {
		return fmt.Errorf("create %d lanes of step %q: %w", len(children), env.step.ID, err)
	}
	for _, child := range children {
		env.log.Info("lane spawned", "lane", child.LaneID, "child", child.ID, "branch", child.BranchName)
	}
	return nil
}

// laneByID is the child CreateTasks has just assigned id to. insertTaskTx
// writes the id onto the row before it asks for a branch name, which is what
// makes this lookup possible at all.
func laneByID(children []*store.Task, id int64) *store.Task {
	for _, child := range children {
		if child.ID == id {
			return child
		}
	}
	return nil
}

// unsettledLanes counts the lanes of one fan_out step that have not reached a
// terminal state. A non-zero count means the parent is mid-fan-out and must
// park, not join.
func unsettledLanes(lanes []store.Task) int {
	n := 0
	for i := range lanes {
		if !taskstate.Settled(lanes[i].State) {
			n++
		}
	}
	return n
}

// selectLanes evaluates every lane's `if:` and returns the declared indices of
// the lanes to spawn (§7.6, task 015 decision 11).
//
// Evaluated here, at run time and in the parent's context, rather than at task
// creation: that is what lets a lane depend on what an earlier step found,
// which is the use case the feature exists for. The price is paid at creation,
// where `fan_out.max_depth` and `fan_out.max_tasks` go on counting guarded
// lanes — an over-approximation §7.6 states rather than leaves to be
// discovered.
func (r *Runner) selectLanes(ctx context.Context, env *stepEnv) ([]int, error) {
	selected := make([]int, 0, len(env.step.Lanes))
	for i, lane := range env.step.Lanes {
		if !lane.Guarded() {
			selected = append(selected, i)
			continue
		}
		rc, err := r.renderContext(ctx, env, r.nextAttempt(ctx, env), stepOutcome{})
		if err != nil {
			return nil, err
		}
		pass, err := workflow.Evaluate(fmt.Sprintf("lanes[%d].if", i), lane.If, rc)
		if err != nil {
			return nil, err
		}
		if !pass {
			// No child, so no row of its own to record it in: a lane that was
			// never spawned has no task to carry the fact. The parent's log
			// and the absent child are the record, which is the same way a
			// lane list that never mentioned it would read.
			env.log.Info("lane skipped by its guard", "lane", lane.ID)
			continue
		}
		selected = append(selected, i)
	}
	return selected, nil
}

// laneTask builds the child task row for one lane, without inserting it.
func (r *Runner) laneTask(env *stepEnv, lane workflow.Lane, order int) (*store.Task, error) {
	// The child's snapshot is the lane's steps as a flat workflow of its own.
	// After this it is indistinguishable from a hand-created task: edit+retry,
	// Marshal and the locator never meet a nested workflow, because nesting
	// exists at authoring time only (decision 4).
	// Two different names, and conflating them does not work. The *column*
	// gets the synthetic {parent}/{step}/{lane}, whose `/` is what proves it
	// cannot collide with a registry name (decision 4). The *snapshot* cannot:
	// the engine re-parses it on every admission, and validation rejects `/`
	// in a workflow name — the child would block with invalid_snapshot on its
	// first admission. So the snapshot is named after where its steps came
	// from: the registry workflow for a resolved lane, the lane id for an
	// inline one. That is also what `.Workflow.Name` should render to.
	laneName := lane.ResolvedFrom
	if laneName == "" {
		laneName = lane.ID
	}
	body := &workflow.Workflow{
		Name:     laneName,
		Defaults: env.wf.Defaults,
		Steps:    lane.Steps,
	}
	snapshot, err := workflow.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode lane %q snapshot: %w", lane.ID, err)
	}

	index := env.index
	child := &store.Task{
		ProjectID:   env.task.ProjectID,
		Title:       fmt.Sprintf("%s — %s", env.task.Title, lane.ID),
		Description: env.task.Description,
		Fields:      mergeFields(env.task.Fields, lane.Fields),
		// The parent's issue snapshot, verbatim (task 035 decision 9). No
		// lane re-fetches anything: `.Issue` is a property of the work, and
		// a lane is doing part of the parent's work.
		GitHubIssue: cloneIssue(env.task.GitHubIssue),
		// workflow_name records where the lane came from: the registry name
		// for a resolved lane, the synthetic path for an inline one.
		WorkflowName:     workflow.LaneWorkflowName(lane, env.wf.Name, env.step.ID),
		WorkflowSnapshot: string(snapshot),
		// The parent's branch, not the project's default: that is what makes
		// the lanes' work land on the branch the task already owns.
		BaseBranch:      env.task.BranchName,
		Priority:        env.task.Priority,
		AgentOverride:   env.task.AgentOverride,
		ModelOverride:   env.task.ModelOverride,
		EffortOverride:  env.task.EffortOverride,
		State:           store.TaskQueued,
		ParentTaskID:    &env.task.ID,
		ParentStepIndex: &index,
		LaneID:          lane.ID,
		LaneOrder:       order,
		// A lane's steps come from the parent's snapshot, resolved at the
		// *parent's* creation (§7.6) — the child never reads a registry. So
		// its origin is `derived` naming the parent, not a copy of the
		// parent's file and digest, which would claim these steps came from a
		// file they did not come from. Leaving it NULL is not an option
		// either: that reads as a pre-0017 row (task 043 decision 6).
		WorkflowOrigin: &store.WorkflowOrigin{
			Scope:        store.WorkflowScopeDerived,
			ParentTaskID: &env.task.ID,
		},
	}
	// A lane spec overrides the inheritance for its own subtree.
	if lane.Agent != "" {
		child.AgentOverride = lane.Agent
	}
	if lane.Model != "" {
		child.ModelOverride = lane.Model
	}
	if lane.Effort != "" {
		child.EffortOverride = lane.Effort
	}
	if lane.Priority != nil {
		child.Priority = *lane.Priority
	}
	return child, nil
}

// mergeFields merges a lane's fields over the parent's, the lane winning
// (decision 29). Neither input is modified.
func mergeFields(parent, lane map[string]string) map[string]string {
	if len(parent) == 0 && len(lane) == 0 {
		return nil
	}
	out := make(map[string]string, len(parent)+len(lane))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range lane {
		out[k] = v
	}
	return out
}
