package workflow

// Creation-time expansion of `type: include` steps (spec §7.9, task 019).
//
// An include names a registry workflow whose steps are **spliced** into the
// caller: the include step is replaced by those steps, each becoming an
// ordinary top-level step with its own step_index. Nothing survives into the
// run — no step_runs row, no cursor, no boundary — which is why §7's engine,
// §11's scheduler and §12.4's recovery need no knowledge of includes at all.
//
// Resolving here rather than at run time is §5.3 restated, and the reason
// fanout.go gives for lanes: execution uses the snapshot precisely so that
// later edits to workflow files cannot mutate an in-flight task, and a callee
// read from the registry six hours into a run would be exactly that mutation,
// in the one place nobody would look for it.
//
// It is also what makes every check below a 400 in front of the person typing:
// the expanded shape is static in the insert path, so a cycle, a depth
// explosion, a duplicate id and an unsupported platform are all decidable
// there.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
)

// IncludeLimits bound one expansion (config `include.*`, decision 8).
type IncludeLimits struct {
	// MaxDepth is how many include levels one expansion may have. A root
	// including a workflow is depth 1; that workflow's own include is depth 2.
	MaxDepth int
}

// IncludeError is an expansion that cannot be created: an include naming an
// unknown workflow, a cycle, an expansion past its depth bound, a step id the
// expansion already used, or a callee this host cannot run. Every case is a
// 400 — something the person creating the task can act on.
type IncludeError struct {
	// Path is the YAML path of the offending include. A path inside a callee
	// carries the include it came through, so one message locates both the
	// call site and the step at fault: `steps[1].include(checks).steps[0]`.
	Path string
	// Message says what is wrong, naming the workflows involved.
	Message string
}

func (e *IncludeError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// ExpandOptions is everything Expand needs beyond the workflow itself.
type ExpandOptions struct {
	// Lookup resolves an include's workflow name through the registry's
	// builtin < global < project shadowing.
	Lookup LookupFunc
	Limits IncludeLimits
	// Override is the task's own (agent, model, effort) selection. It sits
	// *above* a callee's defaults in §8.6's order, so materialisation has to
	// know it to avoid promoting a callee default over the value a human just
	// typed (decision 7). Task-level overrides are immutable — priority is
	// the only mutable task field in v1 — so reading them here is exact
	// rather than a snapshot of something that may move.
	Override agent.Level
	// Platform is the GOOS an included workflow's `platforms:` is judged
	// against. Empty means the running host, which is what task creation
	// wants; tests pin it.
	Platform string
}

// Expand replaces every `include` step in wf, recursively, returning a copy
// with a flat step list and no include left in it.
//
// The returned workflow is a *candidate*: it is structurally expanded but not
// re-validated. Callers re-run Parse over the marshalled result, because the
// nesting rules an expansion can break — no `loop` inside a `loop`, no
// `fan_out` inside a `parallel` — are only decidable once the steps are in
// place (decision 9).
func Expand(wf *Workflow, opts ExpandOptions) (*Workflow, error) {
	if wf == nil {
		return nil, &IncludeError{Message: "no workflow to expand"}
	}
	x := &expander{opts: opts}
	if x.opts.Platform == "" {
		x.opts.Platform = HostPlatform()
	}
	steps, err := x.body(wf.Steps, "steps", 0, []string{wf.Name}, nil)
	if err != nil {
		return nil, err
	}
	out := *wf
	out.Steps = steps
	// Ids are checked over the *result* rather than as the splice goes.
	// Claiming during expansion means claiming each step at whichever level
	// happens to produce it, and a step reached through two includes is
	// produced twice — which reported a workflow as colliding with itself.
	// One pass over the finished list is exactly-once by construction.
	if err := checkDuplicateIDs(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// checkDuplicateIDs reports the first step id used twice in one namespace
// (decision 5). There is one namespace per task the expansion can produce: the
// root's, plus a fresh one per fan_out lane, because each lane becomes a
// separate child task with its own flat snapshot (§8.2).
//
// The message names the *workflows* rather than the expanded paths: after a
// splice, `steps[4]` does not point at anything the author wrote, while "from
// checks" does.
func checkDuplicateIDs(wf *Workflow) error {
	if err := checkScopeIDs(wf.Steps, wf.Name); err != nil {
		return err
	}
	for _, step := range wf.Steps {
		for _, lane := range step.Lanes {
			if err := checkScopeIDs(lane.Steps, wf.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkScopeIDs(steps []Step, root string) error {
	seen := map[string]string{}
	for _, step := range steps {
		for _, owner := range stepIDs(step) {
			origin := originOf(step, root)
			if prev, dup := seen[owner]; dup {
				return &IncludeError{Message: fmt.Sprintf(
					"expanding this workflow uses step id %q twice: once %s, once %s",
					owner, prev, origin)}
			}
			seen[owner] = origin
		}
	}
	return nil
}

// originOf says where a step came from, in the words the author would use.
func originOf(step Step, root string) string {
	if n := len(step.ResolvedFrom); n > 0 {
		return fmt.Sprintf("from the included workflow %q", step.ResolvedFrom[n-1])
	}
	return fmt.Sprintf("in %q itself", root)
}

// HasInclude reports whether a workflow contains an include anywhere a splice
// would reach: at the top level, inside a group or loop body, or inside a
// fan_out lane's inline steps. Callers use it to skip expansion entirely for
// the workflows that are the overwhelming majority.
func HasInclude(wf *Workflow) bool {
	return wf != nil && bodyHasInclude(wf.Steps)
}

func bodyHasInclude(steps []Step) bool {
	for _, s := range steps {
		if s.Type == StepInclude || bodyHasInclude(s.Steps) {
			return true
		}
		for _, lane := range s.Lanes {
			if bodyHasInclude(lane.Steps) {
				return true
			}
		}
	}
	return false
}

// IncludeNames lists the workflows a workflow includes directly, in
// declaration order and without duplicates. It is what `GET /v1/workflows`
// reports, so a client can show what a workflow depends on without resolving
// anything.
func IncludeNames(wf *Workflow) []string {
	if wf == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	var walk func([]Step)
	walk = func(steps []Step) {
		for _, s := range steps {
			if s.Type == StepInclude && s.Workflow != "" && !seen[s.Workflow] {
				seen[s.Workflow] = true
				out = append(out, s.Workflow)
			}
			walk(s.Steps)
			for _, lane := range s.Lanes {
				walk(lane.Steps)
			}
		}
	}
	walk(wf.Steps)
	return out
}

// IncludeWarnings reports what is wrong with a workflow's includes as seen
// from one project's resolved view, for the §8.2 registry-load warning
// (decision 8): a cycle, a name this project cannot resolve, or a callee that
// does not run on this host.
//
// Warnings rather than errors, and only at load, for the reason
// LaneCycleWarnings gives: every one of these is a property of *resolved*
// names, and shadowing decides which files even participate — a project
// workflow may shadow the very name that closed the loop or restricted the
// platform. Task creation, where the root is known, is where each becomes a
// 400.
//
// At most one is reported: expansion stops at the first problem, and a second
// pass would only describe the same file differently.
func IncludeWarnings(wf *Workflow, lookup LookupFunc) []string {
	if wf == nil || lookup == nil || !HasInclude(wf) {
		return nil
	}
	// A generous depth: this is a probe, not a bound check, and the bound is
	// enforced at creation where the config is in hand. Step ids are not
	// checked either — a duplicate is a creation-time error about a specific
	// pair of files, and reporting it as a load warning would blame whichever
	// file happened to be loading. Going through the expander rather than
	// Expand is what leaves that check out.
	x := &expander{opts: ExpandOptions{
		Lookup: lookup,
		Limits: IncludeLimits{MaxDepth: cycleProbeDepth},
	}}
	if _, err := x.body(wf.Steps, "steps", 0, []string{wf.Name}, nil); err != nil {
		var ie *IncludeError
		if errors.As(err, &ie) {
			return []string{ie.Error()}
		}
	}
	return nil
}

// cycleProbeDepth bounds the load-time probe. It is not a policy — the policy
// is include.max_depth at creation — only a guard against walking a legal but
// enormous graph while holding the registry's lock.
const cycleProbeDepth = 64

type expander struct {
	opts ExpandOptions
}

// body expands one step list. `path` is its YAML path, `depth` how many
// include levels are already above it, `stack` the workflow names on the
// current path for cycle detection, `chain` the provenance every step produced
// here inherits, and `ids` the namespace this body's steps share.
func (x *expander) body(in []Step, path string, depth int, stack, chain []string) ([]Step, error) {
	out := make([]Step, 0, len(in))
	for i, step := range in {
		stepPath := fmt.Sprintf("%s[%d]", path, i)
		if step.Type != StepInclude {
			expanded, err := x.nested(step, stepPath, depth, stack, chain)
			if err != nil {
				return nil, err
			}
			out = append(out, expanded)
			continue
		}
		spliced, err := x.splice(step, stepPath, depth, stack, chain)
		if err != nil {
			return nil, err
		}
		out = append(out, spliced...)
	}
	return out, nil
}

// nested expands the includes inside a step that is not itself an include: a
// group's or a loop's body, and a fan_out lane's inline steps. A lane's steps
// become a child task's snapshot, so an include there is resolved here for the
// same reason one at the top level is — by the time that child runs, the
// registry is no longer consulted.
func (x *expander) nested(step Step, path string, depth int, stack, chain []string) (Step, error) {
	if len(step.Steps) > 0 {
		sub, err := x.body(step.Steps, path+".steps", depth, stack, chain)
		if err != nil {
			return step, err
		}
		step.Steps = sub
	}
	if len(step.Lanes) == 0 {
		return step, nil
	}
	lanes := make([]Lane, len(step.Lanes))
	copy(lanes, step.Lanes)
	for j := range lanes {
		if len(lanes[j].Steps) == 0 {
			continue
		}
		lanePath := fmt.Sprintf("%s.lanes[%d].steps", path, j)
		sub, err := x.body(lanes[j].Steps, lanePath, depth, stack, chain)
		if err != nil {
			return step, err
		}
		lanes[j].Steps = sub
	}
	step.Lanes = lanes
	return step, nil
}

// splice resolves one include and returns the steps that replace it.
func (x *expander) splice(step Step, path string, depth int, stack, chain []string) ([]Step, error) {
	// A cycle is workflow A including B while B includes A: an expansion that
	// never terminates. The path is named because "there is a cycle" sends the
	// reader to grep every workflow file they own.
	//
	// Checked *before* the depth bound, because a cycle crosses any bound
	// eventually and the bound's message is actively misleading about it:
	// "past include.max_depth" invites raising a limit that will never help,
	// while the cycle is a structural error no configuration fixes.
	if idx := indexOf(stack, step.Workflow); idx >= 0 {
		return nil, &IncludeError{
			Path: path,
			Message: fmt.Sprintf("workflow cycle: %s",
				strings.Join(append(append([]string{}, stack[idx:]...), step.Workflow), " → ")),
		}
	}
	if depth+1 > x.opts.Limits.MaxDepth {
		return nil, &IncludeError{
			Path: path,
			Message: fmt.Sprintf("includes nest %d levels deep, past include.max_depth (%d)",
				depth+1, x.opts.Limits.MaxDepth),
		}
	}
	if x.opts.Lookup == nil {
		return nil, &IncludeError{Path: path, Message: "included workflows cannot be resolved here"}
	}
	callee, ok := x.opts.Lookup(step.Workflow)
	if !ok {
		return nil, &IncludeError{
			Path:    path,
			Message: fmt.Sprintf("included workflow %q not found", step.Workflow),
		}
	}
	// §8.1.1 travels with the steps: a caller that splices in a POSIX-only
	// fragment cannot run here either. The caller's own `platforms:` is not
	// rewritten — it stays a property of the file as written, so
	// Entry.RunsHere keeps one meaning (decision 8).
	if !callee.SupportsPlatform(x.opts.Platform) {
		return nil, &IncludeError{
			Path: path,
			Message: fmt.Sprintf("included workflow %q does not run on %s (platforms: %s)",
				step.Workflow, x.opts.Platform, strings.Join(callee.Platforms, ", ")),
		}
	}

	next := append(append([]string{}, stack...), step.Workflow)
	nextChain := append(append([]string{}, chain...), step.Workflow)

	// Innermost first: the callee's own includes are spliced before its
	// defaults are materialised over the result, so a step that came through
	// two includes keeps the *nearest* enclosing workflow's defaults and the
	// outer one only fills what is still unset (decision 7).
	body, err := x.body(callee.Steps, fmt.Sprintf("%s.include(%s).steps", path, step.Workflow),
		depth+1, next, nextChain)
	if err != nil {
		return nil, err
	}

	out := make([]Step, len(body))
	for i, spliced := range body {
		// Materialise first, then attribute. A step that came through a
		// deeper include already carries the nearer workflow's defaults and
		// its own chain; this pass fills what that one left unset and leaves
		// the chain alone, which is what makes "nearest wins" fall out of the
		// recursion rather than needing a rule (decision 7).
		out[i] = attribute(materialise(spliced, callee.Defaults, x.opts.Override), nextChain)
	}
	return out, nil
}

// attribute records where a spliced step came from, recursing into a group's
// sub-steps, a loop's body and a lane's inline steps so a step nested inside a
// spliced structure step says so too.
//
// A chain already present is kept: it was written by a deeper splice and names
// the workflow the step actually came from, which is the nearer and more
// useful answer (decision 6).
func attribute(step Step, chain []string) Step {
	if len(step.ResolvedFrom) > 0 {
		return step
	}
	step.ResolvedFrom = chain
	if len(step.Steps) > 0 {
		subs := make([]Step, len(step.Steps))
		for i, sub := range step.Steps {
			subs[i] = attribute(sub, chain)
		}
		step.Steps = subs
	}
	if len(step.Lanes) > 0 {
		lanes := make([]Lane, len(step.Lanes))
		copy(lanes, step.Lanes)
		for j := range lanes {
			if len(lanes[j].Steps) == 0 {
				continue
			}
			subs := make([]Step, len(lanes[j].Steps))
			for i, sub := range lanes[j].Steps {
				subs[i] = attribute(sub, chain)
			}
			lanes[j].Steps = subs
		}
		step.Lanes = lanes
	}
	return step
}

// stepIDs is a step's id together with its sub-steps' or loop body's, which
// share its step_index and live in the same namespace (§8.2).
func stepIDs(step Step) []string {
	out := make([]string, 0, 1+len(step.Steps))
	if step.ID != "" {
		out = append(out, step.ID)
	}
	for _, sub := range step.Steps {
		if sub.ID != "" {
			out = append(out, sub.ID)
		}
	}
	return out
}

// materialise writes a callee's `defaults:` onto one spliced step, so a
// fragment keeps the behaviour it was written with instead of silently
// adopting its caller's (decision 7).
//
// The rule per field is §8.6's order made explicit: a value the step itself
// sets is left alone; a value the *task* overrides is left unset, because the
// run-time resolver applies the override before the snapshot's own defaults
// and reaches the same answer; only then does the callee's default get
// written. A field no level supplies stays empty, so the caller's defaults and
// the daemon default still apply at run time exactly as they would have.
//
// It recurses into a group's sub-steps, a loop's body, a fan_out lane's inline
// steps and a merge's agent step, because all four run steps that would
// otherwise inherit the caller's defaults — a lane's child task is built with
// the *root* snapshot's defaults (`internal/taskrun/fanout.go`), which after a
// splice is the caller's.
func materialise(step Step, callee Defaults, override agent.Level) Step {
	if len(step.Steps) > 0 {
		subs := make([]Step, len(step.Steps))
		for i, sub := range step.Steps {
			subs[i] = materialise(sub, callee, override)
		}
		step.Steps = subs
	}
	if len(step.Lanes) > 0 {
		lanes := make([]Lane, len(step.Lanes))
		copy(lanes, step.Lanes)
		for j := range lanes {
			if len(lanes[j].Steps) == 0 {
				continue
			}
			subs := make([]Step, len(lanes[j].Steps))
			for i, sub := range lanes[j].Steps {
				subs[i] = materialise(sub, callee, override)
			}
			lanes[j].Steps = subs
		}
		step.Lanes = lanes
	}
	if step.Merge != nil && step.Merge.Agent != nil {
		merge := *step.Merge
		resolved := materialise(*merge.Agent, callee, override)
		merge.Agent = &resolved
		step.Merge = &merge
	}

	// `timeout`, `max_retries` and `retry_backoff` bind to an attempt, which a
	// `condition` and a `break` do not have — writing any of them onto one
	// would produce a snapshot §8.2 rejects. A `loop` has a timeout but no
	// attempt of its own.
	switch step.Type {
	case StepCondition, StepBreak:
	case StepLoop:
		step.Timeout = firstDuration(step.Timeout, callee.Timeout)
	default:
		step.Timeout = firstDuration(step.Timeout, callee.Timeout)
		if step.MaxRetries == nil {
			step.MaxRetries = callee.MaxRetries
		}
		step.RetryBackoff = firstDuration(step.RetryBackoff, callee.RetryBackoff)
	}

	// The rest of `defaults:` is agent vocabulary, which §8.2 allows only on
	// an agent step.
	if step.Type != StepAgent {
		return step
	}
	if step.PermissionMode == "" {
		step.PermissionMode = callee.PermissionMode
	}
	if step.OnInput == "" {
		step.OnInput = callee.OnInput
	}
	step.InputTimeout = firstDuration(step.InputTimeout, callee.InputTimeout)

	// The (agent, model, effort) triple is resolved as a unit rather than
	// merged field by field: agent.ResolveWithSources is agent-scoped — a
	// level whose agent differs from the resolved one contributes nothing but
	// its agent field — so a per-field merge would carry a model across
	// adapters that the resolver would have dropped.
	//
	// A callee naming none of the three is left completely alone, which keeps
	// the common case free of baked-in values: with nothing to contribute,
	// the run-time resolve reaches the same answer from the caller's defaults.
	if callee.Agent == "" && callee.Model == "" && callee.Effort == "" {
		return step
	}
	sel := agent.Resolve(
		agent.Level{Agent: step.Agent, Model: step.Model, Effort: step.Effort},
		override,
		agent.Level{Agent: callee.Agent, Model: callee.Model, Effort: callee.Effort},
	)
	step.Agent, step.Model, step.Effort = sel.Agent, sel.Model, sel.Effort
	return step
}

func firstDuration(step, callee *config.Duration) *config.Duration {
	if step != nil {
		return step
	}
	return callee
}
