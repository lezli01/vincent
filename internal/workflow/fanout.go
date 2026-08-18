package workflow

// Creation-time resolution of a fan-out tree (spec §7.6, task 014
// decisions 4, 5, 28).
//
// A `fan_out` lane may name a registry workflow. That name is resolved
// **once**, when the task is created, and the resulting steps are written
// into the task's own snapshot — never read from the registry again. §5.3
// says execution always uses the snapshot precisely so that later edits to
// workflow files cannot mutate an in-flight task, and a lane read from the
// registry six hours into a run would be exactly that mutation, in the one
// place nobody would look for it.
//
// Resolving here is also what makes the tree's shape static: lane lists are
// in the snapshot, so the whole tree is computable in the insert path. That
// is what turns a depth-3 explosion into a 400 in front of the person typing
// rather than two hundred worktrees discovered six hours later.

import (
	"errors"
	"fmt"
	"strings"
)

// Limits bound a resolved fan-out tree (config `fan_out.*`, decisions 5, 28).
type Limits struct {
	// MaxDepth is how many fan-out levels a tree may have. A root with a
	// fan_out step produces depth-1 children; their fan_out steps produce
	// depth 2.
	MaxDepth int
	// MaxTasks bounds the **descendants** a creation may produce, excluding
	// the root (decision 28).
	MaxTasks int
}

// LookupFunc resolves a lane's workflow name through the registry's usual
// builtin < global < project shadowing. ok is false when no such workflow is
// visible to this project.
type LookupFunc func(name string) (wf *Workflow, ok bool)

// TreeError is a fan-out tree that cannot be created: a lane naming an
// unknown or invalid workflow, a cycle, or a tree past its configured bounds.
// It is a 400 — every case is something the person creating the task can act
// on.
type TreeError struct {
	// Path is the YAML path of the offending lane, when one node is at fault.
	Path string
	// Message says what is wrong, including the workflow path for a cycle.
	Message string
}

func (e *TreeError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// ResolveTree resolves every named lane in wf into inline steps, in place on
// a copy, and returns the resolved workflow together with how many
// descendants creating it would produce.
//
// A resolved lane has its `workflow:` name moved to `resolved_from:` and the
// steps that name resolved to written into `steps:`. The move matters: the
// two fields are mutually exclusive in an authored file, so a lane keeping
// both would produce a snapshot that no longer parses. An inline lane is
// already resolved and is only descended into.
func ResolveTree(wf *Workflow, lookup LookupFunc, limits Limits) (*Workflow, int, error) {
	if wf == nil {
		return nil, 0, &TreeError{Message: "no workflow to resolve"}
	}
	out := *wf
	r := &treeResolver{lookup: lookup, limits: limits}
	steps, err := r.steps(wf.Steps, "steps", 0, []string{wf.Name})
	if err != nil {
		return nil, 0, err
	}
	out.Steps = steps
	return &out, r.tasks, nil
}

type treeResolver struct {
	lookup LookupFunc
	limits Limits
	// tasks counts descendants resolved so far, checked against MaxTasks as
	// it grows rather than at the end — a cyclic-looking-but-legal tree
	// should fail on the bound it actually crossed.
	tasks int
}

// steps resolves the fan_out steps in one workflow body. `path` is the YAML
// path of the list, `depth` how many fan-out levels are already above it, and
// `stack` the workflow names on the current path, for cycle detection.
func (r *treeResolver) steps(in []Step, path string, depth int, stack []string) ([]Step, error) {
	out := make([]Step, len(in))
	copy(out, in)
	for i := range out {
		if out[i].Type != StepFanOut {
			continue
		}
		stepPath := fmt.Sprintf("%s[%d]", path, i)
		if depth+1 > r.limits.MaxDepth {
			return nil, &TreeError{
				Path: stepPath,
				Message: fmt.Sprintf(
					"fan-out nests %d levels deep, past fan_out.max_depth (%d)",
					depth+1, r.limits.MaxDepth),
			}
		}
		lanes := make([]Lane, len(out[i].Lanes))
		copy(lanes, out[i].Lanes)
		for j := range lanes {
			resolved, err := r.lane(lanes[j], fmt.Sprintf("%s.lanes[%d]", stepPath, j), depth+1, stack)
			if err != nil {
				return nil, err
			}
			lanes[j] = resolved
		}
		out[i].Lanes = lanes
	}
	return out, nil
}

// lane resolves one lane: its own task, its steps, and whatever they fan out
// into.
func (r *treeResolver) lane(lane Lane, path string, depth int, stack []string) (Lane, error) {
	r.tasks++
	if r.tasks > r.limits.MaxTasks {
		return lane, &TreeError{
			Path: path,
			Message: fmt.Sprintf("fan-out would create more than fan_out.max_tasks (%d) child tasks",
				r.limits.MaxTasks),
		}
	}

	body := lane.Steps
	next := stack
	if lane.Workflow != "" {
		// A cycle is workflow A naming B as a lane while B names A: an
		// infinite spawn, and the one failure here that no bound would catch
		// in a useful way. The path is named because "there is a cycle" sends
		// the reader to grep every workflow file they own.
		if idx := indexOf(stack, lane.Workflow); idx >= 0 {
			return lane, &TreeError{
				Path: path,
				Message: fmt.Sprintf("workflow cycle: %s",
					strings.Join(append(append([]string{}, stack[idx:]...), lane.Workflow), " → ")),
			}
		}
		if r.lookup == nil {
			return lane, &TreeError{Path: path, Message: "lane workflows cannot be resolved here"}
		}
		target, ok := r.lookup(lane.Workflow)
		if !ok {
			return lane, &TreeError{
				Path:    path,
				Message: fmt.Sprintf("lane workflow %q not found", lane.Workflow),
			}
		}
		body = target.Steps
		next = append(append([]string{}, stack...), lane.Workflow)
		lane.ResolvedFrom, lane.Workflow = lane.Workflow, ""
	}

	resolved, err := r.steps(body, path+".steps", depth, next)
	if err != nil {
		return lane, err
	}
	lane.Steps = resolved
	return lane, nil
}

func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

// HasFanOut reports whether a workflow contains a fan_out step at its top
// level. Callers use it to skip tree resolution entirely for the workflows
// that are the overwhelming majority.
func HasFanOut(wf *Workflow) bool {
	if wf == nil {
		return false
	}
	for _, s := range wf.Steps {
		if s.Type == StepFanOut {
			return true
		}
	}
	return false
}

// LaneWorkflowName is the workflow_name a lane's child task carries: the
// registry name a named lane resolved from, and a synthetic path for an
// inline one (decision 4).
//
// The synthetic form provably cannot collide with a registry name, because
// validation rejects `/` in a workflow name.
func LaneWorkflowName(lane Lane, parentWorkflow, stepID string) string {
	if lane.ResolvedFrom != "" {
		return lane.ResolvedFrom
	}
	if lane.Workflow != "" {
		return lane.Workflow
	}
	return fmt.Sprintf("%s/%s/%s", parentWorkflow, stepID, lane.ID)
}

// LaneCycleWarnings reports fan-out cycles reachable from a workflow, for the
// §8.2 registry-load warning (decision 5).
//
// A warning rather than an error, and only at load: a cycle between two files
// is real only once a task picks a root, and shadowing decides which files
// those even are — a project workflow may shadow the very name that closed
// the loop. Task creation is where it becomes a 400.
func LaneCycleWarnings(wf *Workflow, lookup LookupFunc) []string {
	if wf == nil || lookup == nil {
		return nil
	}
	// A generous depth: this is a cycle probe, not a bound check, and the
	// bounds are enforced at creation where the config is in hand.
	r := &treeResolver{lookup: lookup, limits: Limits{MaxDepth: 16, MaxTasks: 1 << 20}}
	if _, err := r.steps(wf.Steps, "steps", 0, []string{wf.Name}); err != nil {
		var te *TreeError
		if errors.As(err, &te) && strings.Contains(te.Message, "cycle") {
			return []string{te.Error()}
		}
	}
	return nil
}
