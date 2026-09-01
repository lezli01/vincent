package taskrun

// Deriving a `fan_out` step's lanes at run time (spec §7.6, task 080 phase 2).
//
// A step carrying `for_each:` and a single `lane:` template has no lane list
// until it runs. This derives one — once, at spawn, in the parent's render
// context — and writes it into the task's own snapshot. After that the step is
// an ordinary static `fan_out` and nothing downstream needs a case for it.
//
// Materializing rather than re-deriving is deliberate. §5.3 says execution
// always uses the snapshot; a step that re-rendered its lane list on every
// admission would be reading the world twice and could disagree with itself
// between round 1 and round 3, which is the one failure a fan-out cannot
// recover from — half a plan spawned against a list that no longer exists.
// `applyOverride` (actions.go, `edit + retry`) is the precedent for rewriting a
// snapshot mid-run; this is the first time the runner does it outside a human
// action, which is safe for the reason that one is: the task's own actor is
// the sole writer of that task's rows (decision 5).
//
// What may vary per item is `id`, `needs`, `fields` and `if`. The lane's
// `workflow:` may **not** — it is resolved once, at task creation, by
// workflow.ResolveTree, which is what keeps §5.3 true and keeps that
// function's cycle detection and `fan_out.max_depth` check meaningful over a
// fan-out whose width nobody yet knows.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// deriveLanes renders the step's `lane:` template once per `for_each` item,
// checks the resulting list the way a static one is checked at load, and
// materializes it into the task's snapshot.
//
// It reports whether the step may carry on. A false report has already
// recorded the row and blocked the task, and the caller must stop.
func (r *Runner) deriveLanes(ctx context.Context, env *stepEnv) (stepOutcome, bool) {
	block := func(reason, msg string) (stepOutcome, bool) {
		r.recordDecisionRow(ctx, env, store.StepFailed, "", reason, msg)
		r.fail(env.task, reason, env.log, msg, nil)
		return stepOutcome{}, false
	}

	rc, err := r.renderContext(ctx, env, r.nextAttempt(ctx, env), stepOutcome{})
	if err != nil {
		return block(ReasonFanOutInvalid, fmt.Sprintf("assemble the render context: %v", err))
	}
	items, err := resolveForEach(env.step.ForEach, rc)
	if err != nil {
		return block(ReasonFanOutInvalid, fmt.Sprintf("render for_each: %v", err))
	}
	// The ceiling is checked on the *list*, before a single lane is built, so
	// a runaway producer costs one block and no worktrees (decision 6).
	limit := env.step.MaxLanes
	ceiling := r.deps.Config().FanOut.MaxTasks
	if limit != nil {
		ceiling = *limit
	}
	if ceiling > 0 && len(items) > ceiling {
		return block(ReasonFanOutLimit, fmt.Sprintf(
			"for_each produced %d lanes, past the %d-lane ceiling; raise max_lanes on this step "+
				"or fan_out.max_tasks in config", len(items), ceiling))
	}

	lanes := make([]workflow.Lane, 0, len(items))
	seen := make(map[string]int, len(items))
	for i, item := range items {
		lane, err := r.renderLane(*env.step.Lane, item, rc, i)
		if err != nil {
			return block(ReasonFanOutInvalid, err.Error())
		}
		if !isLaneSlug(lane.ID) {
			return block(ReasonFanOutInvalid, fmt.Sprintf(
				"item %d rendered lane id %q, which is not a slug (lowercase letters, digits, '-', '_', '.')",
				i+1, lane.ID))
		}
		if prev, dup := seen[lane.ID]; dup {
			// Uniqueness is a load-time check for a static list; two items can
			// render to one id, so for a derived one it blocks here rather
			// than silently collapsing two lanes into one child task.
			return block(ReasonFanOutInvalid, fmt.Sprintf(
				"items %d and %d both render lane id %q", prev+1, i+1, lane.ID))
		}
		seen[lane.ID] = i
		lanes = append(lanes, lane)
	}
	// The same graph check a static list gets at load, in the one place a
	// derived list's ids first exist (decision 6).
	if problems := workflow.LaneGraphProblems(lanes); len(problems) > 0 {
		return block(ReasonFanOutInvalid, problems[0].Message)
	}

	// The tree bound, run-time this time. §13.4 is the precedent: `mcp.*` is
	// enforced with a recursive CTE at run time for exactly this reason.
	if err := r.checkFanOutTreeSize(ctx, env, len(lanes)); err != nil {
		return block(ReasonFanOutLimit, err.Error())
	}

	if err := r.materializeLanes(env, lanes); err != nil {
		env.log.Error("materialize derived lanes", "error", err)
		r.fail(env.task, ReasonInternalError, env.log, "materialize derived lanes", err)
		return stepOutcome{}, false
	}
	env.log.Info("fan-out derived its lanes", "step", env.step.ID, "lanes", len(lanes))
	return stepOutcome{}, true
}

// renderLane renders one lane from one item. The item is a JSON object, which
// is the widening of §8.4's plain-string rule decision 1 records: a DAG node
// carries an identity *and* its edges, and one string cannot say both.
func (r *Runner) renderLane(
	tmpl workflow.Lane, item string, rc workflow.RenderContext, i int,
) (workflow.Lane, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(item), &obj); err != nil || obj == nil {
		return workflow.Lane{}, fmt.Errorf(
			"item %d is not a JSON object: %s", i+1, truncateItem(item))
	}
	lane := tmpl

	id, err := workflow.RenderLane(fmt.Sprintf("lane[%d].id", i), tmpl.ID, rc, obj)
	if err != nil {
		return workflow.Lane{}, err
	}
	lane.ID = strings.TrimSpace(id)
	if tmpl.If != "" {
		guard, err := workflow.RenderLane(fmt.Sprintf("lane[%d].if", i), tmpl.If, rc, obj)
		if err != nil {
			return workflow.Lane{}, err
		}
		// Rendered to a literal here rather than left as a template: the
		// snapshot this lands in is read on every later admission, and a
		// template mentioning `.Item` would not render in a context that no
		// longer has one.
		lane.If = strings.TrimSpace(guard)
	}
	lane.Needs = nil
	for j, need := range tmpl.Needs {
		rendered, err := workflow.RenderLane(fmt.Sprintf("lane[%d].needs[%d]", i, j), need, rc, obj)
		if err != nil {
			return workflow.Lane{}, err
		}
		lane.Needs = append(lane.Needs, workflow.SplitNeeds(rendered)...)
	}
	if len(tmpl.Fields) > 0 {
		lane.Fields = make(map[string]string, len(tmpl.Fields))
		for k, v := range tmpl.Fields {
			rendered, err := workflow.RenderLane(fmt.Sprintf("lane[%d].fields.%s", i, k), v, rc, obj)
			if err != nil {
				return workflow.Lane{}, err
			}
			lane.Fields[k] = rendered
		}
	}
	return lane, nil
}

// materializeLanes writes the derived lanes into the task's snapshot, in place
// of the `lane:`/`for_each:` pair that produced them (decision 5).
func (r *Runner) materializeLanes(env *stepEnv, lanes []workflow.Lane) error {
	body := *env.wf
	steps := make([]workflow.Step, len(env.wf.Steps))
	copy(steps, env.wf.Steps)
	step := steps[env.index]
	step.Lanes = lanes
	step.Lane = nil
	step.ForEach = nil
	steps[env.index] = step
	body.Steps = steps

	snapshot, err := workflow.Marshal(&body)
	if err != nil {
		return fmt.Errorf("encode the materialized snapshot: %w", err)
	}
	if err := r.deps.Store.SetTaskSnapshot(r.persistCtx(), env.task.ID, string(snapshot)); err != nil {
		return err
	}
	// The in-memory copies follow the row, so this admission goes on to
	// select, spawn and merge against the list it just wrote.
	env.task.WorkflowSnapshot = string(snapshot)
	env.wf = &body
	env.step = step
	return nil
}

// checkFanOutTreeSize refuses a derived list that would take the whole fan-out
// tree past `fan_out.max_tasks`. The bound counts descendants of the tree's
// root, excluding it, which is task 014 decision 28's rule unchanged — only
// the moment it is enforced is new (decision 6).
func (r *Runner) checkFanOutTreeSize(ctx context.Context, env *stepEnv, adding int) error {
	ceiling := r.deps.Config().FanOut.MaxTasks
	if ceiling <= 0 {
		return nil
	}
	root := env.task.ID
	ancestors, err := r.deps.Store.FanOutAncestors(ctx, env.task.ID)
	if err != nil {
		return fmt.Errorf("locate the fan-out root: %w", err)
	}
	if len(ancestors) > 0 {
		root = ancestors[len(ancestors)-1]
	}
	size, err := r.deps.Store.FanOutTreeSize(ctx, root)
	if err != nil {
		return err
	}
	if size+adding > ceiling {
		return fmt.Errorf(
			"spawning %d lanes would take the fan-out tree to %d child tasks, past fan_out.max_tasks (%d)",
			adding, size+adding, ceiling)
	}
	return nil
}

// isLaneSlug mirrors the load-time lane id rule for a derived id. The rule
// lives in internal/workflow; this is the one caller outside it, and a lane id
// that is not a slug becomes a branch name and a directory.
func isLaneSlug(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// truncateItem keeps a bad item's message readable when the "item" is a whole
// paragraph an agent printed.
func truncateItem(item string) string {
	const limit = 120
	if len(item) <= limit {
		return item
	}
	return item[:limit] + "…"
}
