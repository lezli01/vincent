package taskrun

// The fan-out half of a `fan_out` step (spec §7.6, task 014 — phase 2). The
// join is in join.go; the two are one step (decision 3) but they run in
// different admissions, separated by the parent parking.
//
// The shape is: spawn every lane as a real child task, park the parent in
// `awaiting_children` releasing its slot, and end the actor goroutine. The
// scheduler brings the parent back once every descendant has settled
// (decision 25), and the second admission runs the join.
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

// runFanOut spawns a fan_out step's lanes and parks the parent, or — if the
// lanes already exist, which is what a re-admission after ChildrenSettled
// means — runs the join.
//
// It reports whether the actor should stop. Spawning always stops it: the
// parent holds no slot while its children work.
func (r *Runner) runFanOut(ctx context.Context, env *stepEnv) (outcome stepOutcome, stop bool) {
	existing, err := r.deps.Store.ListChildren(ctx, env.task.ID)
	if err != nil {
		env.log.Error("list lanes", "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}, false
	}
	// Lanes of *this* step. A workflow may fan out more than once, and the
	// earlier step's children are none of this one's business.
	mine := make([]store.Task, 0, len(existing))
	for _, c := range existing {
		if c.ParentStepIndex != nil && *c.ParentStepIndex == env.index {
			mine = append(mine, c)
		}
	}
	if len(mine) > 0 {
		// The children exist, so this admission is the join (decision 3). It
		// runs through the ordinary attempt path, which gives it a
		// step_runs row like any other step.
		//
		// The budget is one attempt unless the author asked for more: the two
		// ways a join fails — a conflict and a lane that did not finish — are
		// both "a human decides", and §7.2 reserves retries for failures a
		// retry can fix. An automatic second merge would abort the first,
		// hit the same conflict, and block anyway.
		joinEnv := *env
		// Asked before the attempt row exists: see resumedFromConflict.
		joinEnv.resumedFromConflict = r.resumedFromConflict(ctx, env)
		if joinEnv.step.MaxRetries == nil {
			zero := 0
			joinEnv.step.MaxRetries = &zero
		}
		return r.runStepWithRetries(ctx, &joinEnv), false
	}

	if err := r.spawnLanes(ctx, env); err != nil {
		// A partial spawn is not left behind: the lanes that were created are
		// cancelled, so a retry starts from a clean slate rather than
		// half a tree.
		r.abandonPartialSpawn(ctx, env)
		env.log.Error("spawn lanes", "error", err)
		r.fail(env.task, ReasonInternalError, env.log, "spawn lanes", err)
		return stepOutcome{}, true
	}
	// Park: the slot is released before the children need one, which is what
	// makes this deadlock-free at any depth (decision 6).
	if r.transition(env.task, taskstate.FanOut, store.TaskChange{}, env.log) {
		env.log.Info("fan-out spawned; awaiting children", "step", env.step.ID)
	}
	return stepOutcome{}, true
}

// spawnLanes creates one child task per lane, in declared order.
//
// Everything a lane inherits is decision 10: the parent's branch is the
// child's base branch, its overrides and priority propagate, and a lane spec
// may override any of them for its own subtree. Fields merge with the
// parent's, the lane winning (decision 29).
func (r *Runner) spawnLanes(ctx context.Context, env *stepEnv) error {
	project, err := r.deps.Store.GetProject(ctx, env.task.ProjectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	for order, lane := range env.step.Lanes {
		child, err := r.laneTask(env, lane, order)
		if err != nil {
			return err
		}
		// The child's branch is resolved inside the insert's own transaction,
		// exactly as any other task's is (task 001) — a lane is not a special
		// case in the one place branch names are decided.
		spec := worktree.BranchSpec{
			ProjectTemplate: project.BranchTemplate,
			ConfigTemplate:  r.deps.Config().BranchTemplate,
		}
		bctx := worktree.NewBranchContext(child.Title, child.BaseBranch, child.Fields,
			worktree.BranchProject{
				Name: project.Name, Path: project.Path, DefaultBranch: project.DefaultBranch,
			})
		resolve := func(id int64) (string, error) {
			name, _, err := worktree.ResolveBranchName(spec, bctx.WithID(id))
			return name, err
		}
		if err := r.deps.Store.CreateTask(r.persistCtx(), child, resolve); err != nil {
			return fmt.Errorf("create lane %q: %w", lane.ID, err)
		}
		env.log.Info("lane spawned", "lane", lane.ID, "child", child.ID, "branch", child.BranchName)
	}
	return nil
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

// abandonPartialSpawn cancels lanes created before a spawn failed part-way.
//
// Without it a retry would create a second set of lanes beside the first, and
// the join would merge a mixture of the two. Cancelling rather than deleting
// keeps the rule that nothing destroys work: the branches and worktrees of
// anything that started survive (decision 11).
func (r *Runner) abandonPartialSpawn(ctx context.Context, env *stepEnv) {
	children, err := r.deps.Store.ListChildren(ctx, env.task.ID)
	if err != nil {
		env.log.Error("list lanes to abandon", "error", err)
		return
	}
	for _, c := range children {
		if c.ParentStepIndex == nil || *c.ParentStepIndex != env.index {
			continue
		}
		if taskstate.Settled(c.State) {
			continue
		}
		if _, err := r.Cancel(r.persistCtx(), c.ID); err != nil {
			env.log.Error("abandon partial lane", "child", c.ID, "error", err)
		}
	}
}
