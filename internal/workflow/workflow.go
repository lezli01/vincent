package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
)

// Step types (spec §8.2). `parallel` is a group of sub-steps run concurrently
// in the task's one worktree (§7, task 014) — it produces no branch, no child
// task and no merge, which is what separates it from `fan_out`.
const (
	StepAgent    = "agent"
	StepCommand  = "command"
	StepManual   = "manual"
	StepParallel = "parallel"
	// StepFanOut spawns each lane as a real child task and merges their
	// branches back into this task's own (§7.6, task 014). Where `parallel`
	// runs processes, this creates tasks — they share concepts and nothing
	// else, which is why they are two types (decision 2).
	StepFanOut = "fan_out"
	// StepCondition evaluates its `if:` and nothing else (§7.7, task 015).
	// True continues; false ends the sequence and the task is `done`.
	//
	// It is a *type* rather than a second guard field because the
	// consequence belongs on its own line: every CI system spells
	// skip-and-continue `if:`, so a guard field that stopped the run instead
	// would be a false friend whose failure mode is a task reaching `done`
	// having silently done half its work (task 015 decision 2).
	StepCondition = "condition"
	// StepLoop runs its body — a sequence — repeatedly in the task's one
	// worktree (§7.8, task 016). It is `parallel`'s structural twin: one
	// step_index, one scheduler slot, no branch, no child task and nothing to
	// merge. Where a group is a set run once, a loop is a sequence run more
	// than once.
	StepLoop = "loop"
	// StepBreak ends the enclosing loop successfully when its `if:` is true
	// (§7.8, decision 3). It is a type rather than a field for the reason
	// `condition` is: it starts no process, so it cannot time out, be
	// retried, be interrupted or write a transcript.
	//
	// `continue` has no type of its own. A `condition` inside a loop body
	// ends *that iteration*, which is what continue means, using the meaning
	// §7.7 already gave the word.
	StepBreak = "break"
	// StepInclude splices another registry workflow's steps into this one
	// (§7.9, task 019). It is resolved at **task creation**: the callee's
	// steps replace it in the snapshot, each becoming an ordinary top-level
	// step with its own step_index, and no include survives into the run.
	//
	// That is what separates it from every other structure type here. A
	// `parallel` or a `loop` owns a step_index and runs its body under it; an
	// include owns nothing, because by the time anything runs there is no
	// include left. Splicing rather than nesting is decision 1: a callee may
	// then itself contain a `loop`, a `parallel` or a `fan_out`, which a
	// nested body could not, since two position derivations cannot share one
	// step_index (task 016 decision 10).
	StepInclude = "include"
)

// StepTypes is the step-type vocabulary (§8.2), in the order the spec lists
// it. It is exported because the served schema descriptor (task 065) is
// checked against it: a type added here without a form is a type the editor
// silently cannot write.
var StepTypes = []string{
	StepAgent, StepCommand, StepManual, StepParallel, StepFanOut, StepCondition,
	StepLoop, StepBreak, StepInclude,
}

// stepTypeList is the same vocabulary as one string, so the two "one of …"
// messages cannot drift apart as the set grows.
var stepTypeList = strings.Join(StepTypes, ", ")

// Conflict policies for a fan_out step's join (§7.6, task 014 decision 8).
const (
	// ConflictBlock stops the task with `merge_conflict` and leaves the
	// worktree conflicted for a human. The default, deliberately: an agent
	// silently resolving a semantic conflict and the merge commit landing
	// unread is the one outcome that turns a time-saving feature into a
	// correctness liability.
	ConflictBlock = "block"
	// ConflictAgent attempts an agent resolution first, gated by that step's
	// own `check`, and falls back to the block when it fails.
	ConflictAgent = "agent"
)

// Permission modes (spec §9.4).
const (
	PermissionFullAuto   = "full-auto"
	PermissionRestricted = "restricted"
)

// Input policies (spec §7.4). `require` is `wait` plus a precondition: the
// step will only run on an adapter that can stop and ask (task 013).
const (
	InputWait    = "wait"
	InputDeny    = "deny"
	InputRequire = "require"
)

// Shells a command step may pin (spec §8.3).
const (
	ShellSh   = "sh"
	ShellPwsh = "pwsh"
	ShellCmd  = "cmd"
)

// Workflow is a parsed workflow definition (spec §8.1).
type Workflow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Platforms restricts where the workflow may run (§8.1.1, task 010).
	// Empty means every platform; see platform.go for the token set.
	Platforms []string `yaml:"platforms,omitempty"`
	// Fields declares the ordered public task inputs this workflow expects
	// (§8.1.2, task 022). The task still stores one open map[string]string:
	// declarations guide clients and validate named values without rejecting
	// any additional fields a caller supplies.
	Fields   []FieldDefinition `yaml:"fields,omitempty"`
	Defaults Defaults          `yaml:"defaults"`
	Steps    []Step            `yaml:"steps"`
}

// Defaults are the workflow-level fallbacks for step fields. Pointer fields
// distinguish "not set" (inherit the daemon default) from an explicit value.
type Defaults struct {
	Agent          string           `yaml:"agent"`
	Model          string           `yaml:"model"`
	Effort         string           `yaml:"effort"`
	PermissionMode string           `yaml:"permission_mode"`
	OnInput        string           `yaml:"on_input"`
	InputTimeout   *config.Duration `yaml:"input_timeout"`
	MaxRetries     *int             `yaml:"max_retries"`
	RetryBackoff   *config.Duration `yaml:"retry_backoff"`
	Timeout        *config.Duration `yaml:"timeout"`
	// Container overrides the daemon's `container:` block for tasks created
	// from this workflow (§16, task 061 decision 6). It is the second and
	// last level of the §8.6 precedence chain for containerization: workflow
	// `defaults:` beats `config.yaml`, and there is no task level.
	Container *config.ContainerOverride `yaml:"container"`
}

// Step is one workflow step. The struct is flat across all three step types;
// fields that do not belong to a step's type are rejected by validation
// (spec §8.2), which keeps the YAML shape and its errors simple.
type Step struct {
	ID         string `yaml:"id"`
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	MaxRetries *int   `yaml:"max_retries"`
	// RetryBackoff paces this step's retries (§7.2, task 028): how long to
	// wait after a failed attempt before the next one. Unset and zero both
	// mean today's behaviour, an immediate retry.
	//
	// The wait is spent by re-queueing the task with a §11 admission hold,
	// never by sleeping in the actor — a sleeping actor holds its
	// concurrency slot for the whole wait, and with max_parallel_tasks slots
	// held that way nothing runs at all (task 003's reasoning, applied to the
	// general case). The attempt is still `failed` and still consumes a
	// retry, which is what separates it from a `usage_limit` hold.
	RetryBackoff *config.Duration `yaml:"retry_backoff"`
	Timeout      *config.Duration `yaml:"timeout"`

	// If guards the step (§7.7, task 015). It renders against the §8.4
	// context and must produce exactly `true` or `false`. On an ordinary
	// step false means *skip this step and carry on*; on a `condition` step
	// it is the condition itself, and false ends the sequence. On a
	// `parallel` sub-step it means *do not start this member*, because a
	// group is a set with no "later" to skip to (decision 3).
	//
	// Empty means unguarded, which is why it is a plain string rather than a
	// pointer: an empty template renders to an empty string, which is not
	// `true`, so "unset" and "set to nothing" must be the same thing and are.
	If string `yaml:"if,omitempty"`
	// AllowFailure turns the failures the step itself produced into an
	// advance instead of a block (§7.2, task 015 decision 5) — the field
	// that gives a guard something a run discovered to read. It is
	// orthogonal to the retry budget: the step retries as it always did, and
	// this decides only what happens when the budget is spent.
	AllowFailure bool `yaml:"allow_failure,omitempty"`

	// agent steps
	Prompt         string           `yaml:"prompt"`
	Agent          string           `yaml:"agent"`
	Model          string           `yaml:"model"`
	Effort         string           `yaml:"effort"`
	PermissionMode string           `yaml:"permission_mode"`
	OnInput        string           `yaml:"on_input"`
	InputTimeout   *config.Duration `yaml:"input_timeout"`

	// agent and command steps
	Check        string           `yaml:"check"`
	CheckTimeout *config.Duration `yaml:"check_timeout"`

	// command steps
	Run   string            `yaml:"run"`
	Shell string            `yaml:"shell"`
	Env   map[string]string `yaml:"env"`

	// manual steps
	Instructions string `yaml:"instructions"`

	// parallel steps (task 014). Steps carries the sub-steps; MaxParallel
	// bounds how many run at once, falling back to the daemon's
	// `parallel.max_parallel`. Both are omitempty so a marshalled snapshot
	// does not sprout an empty group on every other step type — the rest of
	// this struct writes its zero values out deliberately (see Marshal).
	Steps       []Step `yaml:"steps,omitempty"`
	MaxParallel *int   `yaml:"max_parallel,omitempty"`

	// fan_out steps (task 014). Lanes is the statically declared list; Lane
	// is the single template a *derived* fan-out renders once per ForEach
	// item instead (§7.6, task 080). Exactly one of the two is set.
	//
	// A derived lane's `workflow:` stays static — only `id`, `needs`,
	// `fields` and `if` may vary per item — which is what keeps §5.3 true:
	// the registry is still read exactly once, at task creation, by
	// ResolveTree.
	Lanes []Lane `yaml:"lanes,omitempty"`
	Lane  *Lane  `yaml:"lane,omitempty"`
	// MaxLanes bounds a derived lane list, defaulting to `fan_out.max_tasks`.
	// A static list is counted at creation and needs none (task 080
	// decision 6).
	MaxLanes *int   `yaml:"max_lanes,omitempty"`
	Merge    *Merge `yaml:"merge,omitempty"`

	// loop steps (task 016). Steps above carries the body — a loop and a
	// group are the two structure steps, and one field for "the steps inside
	// me" is what keeps allSteps, the catalog check and the snapshot
	// marshaller from growing a second case each.
	//
	// Exactly one of Count and ForEach is set: the driver (decision 2).
	// MaxIterations bounds the loop, defaulting to `loop.max_iterations`.
	Count         *int    `yaml:"count,omitempty"`
	ForEach       ForEach `yaml:"for_each,omitempty"`
	MaxIterations *int    `yaml:"max_iterations,omitempty"`

	// include steps (task 019). Workflow names the registry workflow to
	// splice in, resolved through the usual builtin < global < project
	// shadowing at **task creation**.
	//
	// Unlike a lane's `workflow:`, which is moved to `resolved_from:` beside
	// the steps it resolved to, this field does not survive resolution at
	// all: the step it sits on is *replaced* by those steps.
	Workflow string `yaml:"workflow,omitempty"`
	// ResolvedFrom is the chain of workflow names a spliced step came
	// through, outermost first — `[outer, inner]` for a step that workflow
	// `outer` included from `inner`. Empty for a step the caller wrote
	// itself. Written by Expand at task creation, never by hand
	// (decision 6).
	//
	// A chain rather than the single name `Lane.ResolvedFrom` carries,
	// because a nested include otherwise cannot say how its steps got where
	// they are. It is unambiguous because a given callee appears at most once
	// in one expansion: a second appearance is a duplicate step id, which is
	// a 400 (decision 5).
	ResolvedFrom []string `yaml:"resolved_from,omitempty"`
}

// ForEach is a `loop` step's item list: a YAML sequence of templates, or a
// single scalar template. Both spellings decode to the same slice, and both
// are rendered the same way at run time — each entry is rendered, trimmed and
// split on newlines with empty lines dropped (§7.8) — so a command's
// multi-line output and a hand-written list are one mechanism, not two.
//
// The scalar spelling exists because the motivating case is
// `for_each: '{{ .Steps.changed.Result }}'`, a list a step discovered at run
// time, and wrapping that in a one-element sequence would be ceremony.
type ForEach []string

// UnmarshalYAML accepts either spelling. goccy hands the raw node bytes, so
// the sequence attempt is tried first and a scalar falls out of its failure.
func (f *ForEach) UnmarshalYAML(b []byte) error {
	var list []string
	if err := yaml.Unmarshal(b, &list); err == nil {
		*f = list
		return nil
	}
	var scalar string
	if err := yaml.Unmarshal(b, &scalar); err != nil {
		return fmt.Errorf("for_each must be a string or a list of strings: %w", err)
	}
	*f = ForEach{scalar}
	return nil
}

// LaneNeeds is a lane's `needs:` list: a YAML sequence of lane ids, or a
// single scalar. Both spellings decode to the same slice, for the reason
// ForEach accepts both — the motivating derived case is
// `needs: '{{ .Item.needs }}'`, one template producing the whole list, and
// wrapping that in a one-element sequence would be ceremony.
//
// A derived list's entries are rendered and then split by SplitNeeds, which is
// what makes a JSON array's Go rendering — `[api db]` — read as two ids.
type LaneNeeds []string

// UnmarshalYAML accepts either spelling, the same way ForEach does.
func (n *LaneNeeds) UnmarshalYAML(b []byte) error {
	var list []string
	if err := yaml.Unmarshal(b, &list); err == nil {
		*n = list
		return nil
	}
	var scalar string
	if err := yaml.Unmarshal(b, &scalar); err != nil {
		return fmt.Errorf("needs must be a string or a list of strings: %w", err)
	}
	*n = LaneNeeds{scalar}
	return nil
}

// Lane is one branch of a `fan_out` step: a named registry workflow or inline
// steps, which become a real child task's own flat snapshot (§7.6,
// decision 4).
//
// Exactly one of Workflow and Steps is set. Nesting lives here and only here
// — a lane's workflow may itself fan out, to any depth, bounded by
// `fan_out.max_depth` at creation (decision 5).
type Lane struct {
	ID string `yaml:"id"`
	// If guards the lane (§7.6, task 015 decision 11): false means this lane
	// is not spawned, while its siblings still run and the join still
	// happens. Evaluated when the `fan_out` step runs, in the parent's
	// context, so a lane can depend on what an earlier step found — which is
	// why the creation-time `max_depth`/`max_tasks` limits count guarded
	// lanes too.
	If string `yaml:"if,omitempty"`
	// Workflow names a registry workflow, resolved through the usual
	// builtin < global < project shadowing at **task-creation** time.
	Workflow string `yaml:"workflow,omitempty"`
	// Steps is an inline workflow body, used when Workflow is empty.
	Steps []Step `yaml:"steps,omitempty"`
	// Needs names sibling lanes of the same `fan_out` step this lane depends
	// on (§7.6, task 080). The lane is eligible to spawn only once every lane
	// it names is `done` **and merged into the parent's branch**, which is
	// what makes the dependency mean something: a round merges before it
	// spawns, so a dependent lane's worktree is cut from a branch that
	// already carries its dependencies' commits.
	//
	// Absent or empty means eligible immediately — every lane's behaviour
	// before this field existed, and still a flat list's.
	//
	// It is *happens-after*, not isolation: there is one parent branch, so a
	// lane naming `[a, b]` also sees `c` when `c` merged in the same round.
	// Dependencies are satisfied at least, never exactly (§7.6).
	Needs LaneNeeds `yaml:"needs,omitempty"`
	// ResolvedFrom records the registry workflow a named lane was resolved
	// from, and is written by ResolveTree at task creation — never by hand.
	//
	// Resolution moves the name here and fills Steps, rather than leaving
	// Workflow set beside them: `workflow` and `steps` are mutually exclusive
	// in an authored file, and a resolved snapshot that carried both would no
	// longer parse. The name is still needed, as the child task's
	// workflow_name (decision 4).
	ResolvedFrom string `yaml:"resolved_from,omitempty"`
	// Fields are merged over the parent task's, this lane winning
	// (decision 29).
	Fields map[string]string `yaml:"fields,omitempty"`
	// Agent, Model, Effort and Priority override the root's inherited values
	// for this lane's whole subtree (decision 10). Empty inherits.
	Agent    string `yaml:"agent,omitempty"`
	Model    string `yaml:"model,omitempty"`
	Effort   string `yaml:"effort,omitempty"`
	Priority *int   `yaml:"priority,omitempty"`
}

// Merge is how a `fan_out` step joins its lanes back (§7.6, decisions 7, 8).
// Lanes are merged `--no-ff` one at a time in declared order, stopping at the
// first conflict.
//
// The two fields are one setting split in two because YAML unions are worse
// than the split: `on_conflict` is the policy, and `agent` carries the step
// that policy runs. Validation keeps them consistent, so neither can be set
// without the other making sense.
type Merge struct {
	// OnConflict is `block` (the default) or `agent`.
	OnConflict string `yaml:"on_conflict,omitempty"`
	// Agent is a full agent Step — same fields, same validation, same
	// executor — run in the parent's worktree over the conflicted files
	// (decision 24). Required by, and only by, `on_conflict: agent`.
	Agent *Step `yaml:"agent,omitempty"`
}

// ConflictPolicy resolves a fan_out step's conflict policy, defaulting to
// `block` for a step that declares no `merge:` at all.
func (s Step) ConflictPolicy() string {
	if s.Merge == nil || s.Merge.OnConflict == "" {
		return ConflictBlock
	}
	return s.Merge.OnConflict
}

// placedStep is a step together with the YAML path it was found at, so a
// finding about a sub-step reports `steps[2].steps[0].model` rather than the
// group's own path (task 014).
type placedStep struct {
	Path string
	Step Step
}

// allSteps flattens a workflow into every step it contains, groups first and
// then their sub-steps, in declaration order. Nesting is one level deep by
// construction: validateSubStep rejects a `parallel` inside a `parallel`.
func allSteps(wf *Workflow) []placedStep {
	out := make([]placedStep, 0, len(wf.Steps))
	for i, step := range wf.Steps {
		base := fmt.Sprintf("steps[%d]", i)
		out = append(out, placedStep{Path: base, Step: step})
		for j, sub := range step.Steps {
			out = append(out, placedStep{Path: fmt.Sprintf("%s.steps[%d]", base, j), Step: sub})
		}
	}
	return out
}

// DisplayName is the step's display name, falling back to its id (§8.2).
func (s Step) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// Options tune validation. KnownAgents is the set of registered adapter
// names; when empty the `agent` field is not checked against it.
type Options struct {
	KnownAgents []string
	// Catalogs supplies adapter → option catalog for the §8.2 cross-catalog
	// check of every agent step's resolved (agent, model, effort) triple.
	// Nil disables catalog validation. The daemon wires the catalog cache's
	// never-probing view here (T2.11).
	Catalogs func() agent.Catalogs
	// MaxIterations supplies `loop.max_iterations` — the ceiling a `count:`
	// is checked against at load, so `count: 5000` is refused in front of the
	// person typing (§7.8, task 016 decision 5).
	//
	// A func rather than an int, for the reason Catalogs is one: the registry
	// captures these Options once and re-parses files for the rest of the
	// daemon's life, so a plain value would pin the ceiling to whatever
	// config said at startup and a hot reload (§12.3) would reach the engine
	// without reaching validation. Nil disables the check, which is what a
	// caller wanting only structural validation — a snapshot re-parse, a test
	// — gets by leaving it unset.
	MaxIterations func() int
}

// Error is a single validation failure, located in the source file when the
// offending node can be found (spec §8.2, T2.1).
type Error struct {
	// Path is the YAML path of the offending node, e.g. "steps[1].timeout".
	Path string `json:"path,omitempty"`
	// Line is the 1-based source line, or 0 when it could not be resolved.
	Line int `json:"line,omitempty"`
	// Message describes the problem.
	Message string `json:"message"`
}

func (e Error) String() string {
	switch {
	case e.Line > 0 && e.Path != "":
		return fmt.Sprintf("line %d: %s: %s", e.Line, e.Path, e.Message)
	case e.Line > 0:
		return fmt.Sprintf("line %d: %s", e.Line, e.Message)
	case e.Path != "":
		return fmt.Sprintf("%s: %s", e.Path, e.Message)
	default:
		return e.Message
	}
}

// Errors is the list of validation failures for one workflow file.
type Errors []Error

// Error implements the error interface, joining every failure.
func (es Errors) Error() string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, e.String())
	}
	return strings.Join(parts, "; ")
}

// Parse decodes and validates a workflow definition. Decoding is strict:
// unknown keys are errors, to catch typos (§8.2). A non-nil error is always
// of type Errors. The middle return carries §8.2 catalog warnings —
// non-fatal findings a valid workflow may still surface (T2.11).
func Parse(src []byte, opts Options) (*Workflow, Errors, error) {
	var wf Workflow
	if err := yaml.UnmarshalWithOptions(src, &wf, yaml.DisallowUnknownField()); err != nil {
		return nil, nil, Errors{{Line: lineOfDecodeError(err), Message: cleanDecodeError(err)}}
	}
	loc := newLocator(src)
	errs, warns := validate(&wf, opts, loc)
	if len(errs) > 0 {
		return nil, warns, errs
	}
	return &wf, warns, nil
}

// Marshal re-encodes a workflow as YAML. It exists for `edit + retry`, which
// rewrites one step inside a task's own snapshot (spec §5.3, §6).
//
// The output is canonical rather than faithful: comments and field order
// from the original file are lost, and fields left unset are written out
// empty. That is acceptable because a snapshot is machine-owned — only Parse
// ever reads it — and validation judges a step by the values it carries, not
// by which keys are present.
func Marshal(wf *Workflow) ([]byte, error) {
	out, err := yaml.Marshal(wf)
	if err != nil {
		return nil, fmt.Errorf("encode workflow: %w", err)
	}
	return out, nil
}

// asErrors extracts the validation failures from an error returned by Parse.
func asErrors(err error, target *Errors) bool { return errors.As(err, target) }

// yamlUnmarshalLenient decodes without strict field checking, for probing a
// file that already failed validation.
func yamlUnmarshalLenient(src []byte, v any) error {
	if err := yaml.Unmarshal(src, v); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	return nil
}

// validate applies the §8.2 constraints. Every check reports its own error
// so one file surfaces all its problems in a single pass; catalog findings
// split into errors and warnings per the cross-catalog rule.
func validate(wf *Workflow, opts Options, loc *locator) (Errors, Errors) {
	var errs, warns Errors
	add := func(path, format string, args ...any) {
		errs = append(errs, Error{Path: path, Line: loc.line(path), Message: fmt.Sprintf(format, args...)})
	}
	addWarn := func(path, format string, args ...any) {
		warns = append(warns, Error{Path: path, Line: loc.line(path), Message: fmt.Sprintf(format, args...)})
	}

	if wf.Name == "" {
		add("name", "name is required")
	} else if strings.ContainsAny(wf.Name, " \t/\\") {
		add("name", "name %q must not contain whitespace or path separators", wf.Name)
	}
	validatePlatforms(wf, add)
	validateFieldDefinitions(wf, add)
	validateDefaults(wf, opts, add)

	if len(wf.Steps) == 0 {
		add("steps", "steps must not be empty")
	}
	// Ids are unique across the whole workflow, sub-steps and loop bodies
	// included: a member of either shares its structure step's step_index and
	// is told apart from its siblings by step_id alone (task 014 decision 16,
	// task 016 decision 7), which is also what names its transcript file.
	validateBody(wf, wf.Steps, "steps", make(map[string]string, len(wf.Steps)), opts, add, addWarn)
	warns = append(warns, validateCatalogs(wf, opts, loc, add)...)
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })
	sort.SliceStable(warns, func(i, j int) bool { return warns[i].Line < warns[j].Line })
	return errs, warns
}

// validateCatalogs applies the §8.2 cross-catalog check to every agent
// step's resolved triple (no task override exists at load time). A finding
// is attributed to the level that supplied the value: the step's own field,
// or defaults — where identical findings from many steps collapse to one.
func validateCatalogs(wf *Workflow, opts Options, loc *locator, add func(string, string, ...any)) Errors {
	if opts.Catalogs == nil {
		return nil
	}
	catalogs := opts.Catalogs()
	var warns Errors
	seen := map[string]bool{}
	// Sub-steps of a `parallel` group are ordinary agent steps and get the
	// same cross-catalog check; skipping them would let a group hide a model
	// name that fails everywhere else in the file (task 014).
	for _, placed := range allSteps(wf) {
		base, step := placed.Path, placed.Step
		if step.Type != StepAgent {
			continue
		}
		sel := agent.Resolve(
			agent.Level{Agent: step.Agent, Model: step.Model, Effort: step.Effort},
			agent.Level{},
			agent.Level{Agent: wf.Defaults.Agent, Model: wf.Defaults.Model, Effort: wf.Defaults.Effort},
		)
		// The §8.2 capability check (task 013): a step that requires mid-run
		// input on an adapter that can never provide it is broken on every
		// host, forever, and is decidable here — InputSupport is curated, so
		// no probe is involved. claude's version gate is *not* judged: only a
		// probe can answer it, and this path must not spawn.
		if wf.StepRequiresInput(step) && !catalogs.InputEverPossible(sel.Agent) {
			if path, dup := findingPath(step, "agent", base, seen); !dup {
				add(path, "agent %q can never take mid-run input, which step %q requires (on_input: %s)",
					sel.Agent, step.DisplayName(), InputRequire)
			}
		}
		cerrs, cwarns := catalogs.Check(sel)
		for _, f := range cerrs {
			path, dup := findingPath(step, f.Field, base, seen)
			if dup {
				continue
			}
			add(path, "%s", f.Message)
		}
		for _, f := range cwarns {
			path, dup := findingPath(step, f.Field, base, seen)
			if dup {
				continue
			}
			warns = append(warns, Error{Path: path, Line: loc.line(path), Message: f.Message})
		}
	}
	return warns
}

// findingPath locates a catalog finding: the step's own field when the step
// set the value, else the defaults field — reported once however many steps
// inherit it.
func findingPath(step Step, field string, base string, seen map[string]bool) (string, bool) {
	var stepValue string
	switch field {
	case "effort":
		stepValue = step.Effort
	case "agent":
		stepValue = step.Agent
	default:
		stepValue = step.Model
	}
	if stepValue != "" {
		return base + "." + field, false
	}
	path := "defaults." + field
	if seen[path] {
		return path, true
	}
	seen[path] = true
	return path, false
}

func validateDefaults(wf *Workflow, opts Options, add func(string, string, ...any)) {
	d := wf.Defaults
	if d.Agent != "" && !knownAgent(d.Agent, opts.KnownAgents) {
		add("defaults.agent", "unknown agent %q (known: %s)", d.Agent, strings.Join(opts.KnownAgents, ", "))
	}
	if d.PermissionMode != "" && !isPermissionMode(d.PermissionMode) {
		add("defaults.permission_mode", "permission_mode must be %q or %q, got %q",
			PermissionFullAuto, PermissionRestricted, d.PermissionMode)
	}
	if d.OnInput != "" && !isInputPolicy(d.OnInput) {
		add("defaults.on_input", "on_input must be %q, %q or %q, got %q",
			InputWait, InputDeny, InputRequire, d.OnInput)
	}
	if d.MaxRetries != nil && *d.MaxRetries < 0 {
		add("defaults.max_retries", "max_retries must not be negative, got %d", *d.MaxRetries)
	}
	// Zero is legal and is the default: it means an immediate retry, which is
	// what every workflow written before task 028 gets.
	if d.RetryBackoff != nil && *d.RetryBackoff < 0 {
		add("defaults.retry_backoff", "retry_backoff must not be negative, got %s", d.RetryBackoff)
	}
	if d.Timeout != nil && *d.Timeout <= 0 {
		add("defaults.timeout", "timeout must be positive, got %s", d.Timeout)
	}
	if d.InputTimeout != nil && *d.InputTimeout <= 0 {
		add("defaults.input_timeout", "input_timeout must be positive, got %s", d.InputTimeout)
	}
	validateContainerDefaults(wf, add)
}

// validateContainerDefaults applies the two container checks load-time
// validation can actually make (task 061 decision 8).
//
// The block itself is always checkable. The `shell:` refusal is not: a
// containerized `run:` body executes under the container's /bin/sh — the
// inverse of §8.3 — and containerization also resolves from `config.yaml`,
// which is hot-reloadable and which a workflow being parsed knows nothing
// about. So the refusal lands here only when the workflow pins its own image,
// the one case load time can judge; every other case is refused at task
// creation, where the config-level and workflow-level images resolve together.
func validateContainerDefaults(wf *Workflow, add func(string, string, ...any)) {
	c := wf.Defaults.Container
	if c == nil {
		return
	}
	if err := (config.Container{Runtime: derefString(c.Runtime), ExtraMounts: c.ExtraMounts}).Validate(); err != nil {
		add("defaults.container", "%s", err.Error())
	}
	if c.Image == nil || strings.TrimSpace(*c.Image) == "" {
		return
	}
	for _, f := range ContainerShellConflicts(wf) {
		add(f.Path, "%s", f.Message)
	}
}

// ContainerShellConflicts lists the steps a containerized run cannot honour:
// a `shell:` pin of pwsh or cmd, neither of which exists in the Linux image a
// container runs (§8.3, task 061 decision 8). It is exported because task
// creation makes the same judgement on the resolved image, and one definition
// is what keeps the load-time and creation-time messages identical.
func ContainerShellConflicts(wf *Workflow) []Error {
	var out []Error
	WalkSteps(wf.Steps, func(step Step, path string) {
		if step.Shell == ShellPwsh || step.Shell == ShellCmd {
			out = append(out, Error{
				Path: path + ".shell",
				Message: fmt.Sprintf(
					"step %q pins shell %q, which a containerized run cannot provide: "+
						"a container's run bodies execute under the image's /bin/sh (§8.3)",
					step.ID, step.Shell),
			})
		}
	})
	return out
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// validateStep checks one step: its type, the fields that type requires, the
// fields that do not belong to it, and every template it carries.
func validateStep(step Step, base string, opts Options, add func(string, string, ...any)) {
	switch step.Type {
	case "":
		add(base+".type", "type is required (one of %s)", stepTypeList)
	case StepAgent:
		if step.Prompt == "" {
			add(base+".prompt", "agent steps require a prompt")
		}
		if step.Agent != "" && !knownAgent(step.Agent, opts.KnownAgents) {
			add(base+".agent", "unknown agent %q (known: %s)", step.Agent, strings.Join(opts.KnownAgents, ", "))
		}
		if step.PermissionMode != "" && !isPermissionMode(step.PermissionMode) {
			add(base+".permission_mode", "permission_mode must be %q or %q, got %q",
				PermissionFullAuto, PermissionRestricted, step.PermissionMode)
		}
		if step.OnInput != "" && !isInputPolicy(step.OnInput) {
			add(base+".on_input", "on_input must be %q, %q or %q, got %q",
				InputWait, InputDeny, InputRequire, step.OnInput)
		}
		rejectFields(step, base, add, "run", "shell", "env", "instructions",
			"steps", "max_parallel", "lanes", "merge")
	case StepCommand:
		if step.Run == "" {
			add(base+".run", "command steps require a run command")
		}
		if step.Shell != "" && !isShell(step.Shell) {
			add(base+".shell", "shell must be one of %s, %s, %s; got %q",
				ShellSh, ShellPwsh, ShellCmd, step.Shell)
		}
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "instructions",
			"steps", "max_parallel", "lanes", "merge")
	case StepManual:
		if step.Instructions == "" {
			add(base+".instructions", "manual steps require instructions")
		}
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env", "steps", "max_parallel", "lanes", "merge",
			"allow_failure")
	case StepParallel:
		if len(step.Steps) == 0 {
			add(base+".steps", "parallel steps require at least one sub-step")
		}
		if step.MaxParallel != nil && *step.MaxParallel < 1 {
			add(base+".max_parallel", "max_parallel must be at least 1, got %d", *step.MaxParallel)
		}
		// A group carries no work of its own: only `timeout` and
		// `max_retries`, checked below for every type, bound the group itself.
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env", "instructions", "lanes", "merge", "allow_failure")
	case StepFanOut:
		validateFanOutShape(step, base, add)
		validateMerge(step, base, opts, add)
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env", "instructions", "steps", "max_parallel",
			"allow_failure")
	case StepLoop:
		validateLoop(step, base, add, opts)
	case StepBreak:
		// `break` is `condition` with the enclosing loop supplying the
		// consequence, so it carries the same fields and rejects the same
		// ones: it starts no process, so there is nothing to time out, retry
		// or allow to fail (decision 3).
		if step.If == "" {
			add(base+".if", "break steps require an if expression")
		}
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env", "instructions", "steps", "max_parallel",
			"lanes", "merge", "allow_failure", "max_retries", "retry_backoff", "timeout")
	case StepCondition:
		// The guard *is* the body. `if:` may not also act as a skip-guard
		// here: "skip the check that decides whether to continue" has two
		// readings and neither is worth having (task 015 decision 7).
		if step.If == "" {
			add(base+".if", "condition steps require an if expression")
		}
		// Everything else goes, `timeout`, `max_retries`, `retry_backoff` and
		// `allow_failure` included: a condition step starts no process, so it
		// cannot time out, has nothing to retry, nothing to pace a retry of,
		// and no failure of its own to allow.
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env", "instructions", "steps", "max_parallel",
			"lanes", "merge", "allow_failure", "max_retries", "retry_backoff", "timeout")
	case StepInclude:
		if step.Workflow == "" {
			add(base+".workflow", "include steps require the name of the workflow to include")
		}
		// An include is resolved away at task creation, so it owns no
		// step_runs row, no attempt and no process — which is what rejects
		// `timeout`, `max_retries`, `retry_backoff`, `allow_failure` and
		// `check`: there is nothing for them to bind to (decision 11).
		//
		// `if` goes with them, and that one had a real alternative. Honouring
		// a guard here would mean distributing it onto every spliced step,
		// which reads correctly until one of them is a `condition` or a
		// `break` — both of which carry their own `if:` and reject every
		// other field, so the two would have to be combined by rewriting Go
		// template text. Guard the callee's own steps instead, or put a
		// `condition` in front of the include.
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env", "instructions", "steps", "max_parallel",
			"lanes", "merge", "if", "allow_failure", "max_retries", "retry_backoff",
			"timeout")
	default:
		add(base+".type", "unknown step type %q (one of %s)", step.Type, stepTypeList)
	}
	// `count`, `for_each` and `max_iterations` belong to a loop and to
	// nothing else. Rejecting them here rather than in each arm keeps the
	// eight arms above from each growing the same three strings. `workflow`
	// is the same shape for `include`.
	switch step.Type {
	case StepLoop:
	case StepFanOut:
		// `for_each` is shared with a derived fan-out (task 080): one
		// spelling of "a list a step discovered", rendered the same way.
		// `count` and `max_iterations` stay a loop's alone.
		rejectFields(step, base, add, "count", "max_iterations")
	default:
		rejectFields(step, base, add, "count", "for_each", "max_iterations")
	}
	// `lane` and `max_lanes` are a derived fan-out's, and nothing else's.
	if step.Type != StepFanOut {
		rejectFields(step, base, add, "lane", "max_lanes")
	}
	if step.Type != StepInclude {
		rejectFields(step, base, add, "workflow")
	}
	// resolved_from is written by Expand at task creation. A hand-written
	// file carrying one is claiming a provenance nothing produced, and a
	// snapshot re-parse is the only caller that should ever see it — which is
	// why this is checked against the field being *set*, not against the step
	// type (decision 6).
	if len(step.ResolvedFrom) > 0 && step.Workflow != "" {
		add(base+".resolved_from", "resolved_from is set by task creation, not by hand")
	}

	if step.MaxRetries != nil && *step.MaxRetries < 0 {
		add(base+".max_retries", "max_retries must not be negative, got %d", *step.MaxRetries)
	}
	if step.RetryBackoff != nil && *step.RetryBackoff < 0 {
		add(base+".retry_backoff", "retry_backoff must not be negative, got %s", step.RetryBackoff)
	}
	if step.Timeout != nil && *step.Timeout <= 0 {
		add(base+".timeout", "timeout must be positive, got %s", step.Timeout)
	}
	if step.CheckTimeout != nil && *step.CheckTimeout <= 0 {
		add(base+".check_timeout", "check_timeout must be positive, got %s", step.CheckTimeout)
	}
	if step.InputTimeout != nil && *step.InputTimeout <= 0 {
		add(base+".input_timeout", "input_timeout must be positive, got %s", step.InputTimeout)
	}
	for field, text := range map[string]string{
		"prompt": step.Prompt, "run": step.Run, "check": step.Check,
		"instructions": step.Instructions, "if": step.If,
	} {
		if text == "" {
			continue
		}
		if _, err := template.New(field).Parse(text); err != nil {
			add(base+"."+field, "template does not parse: %v", err)
		}
	}
}

// validateMerge checks a fan_out step's join settings: the policy is known,
// and the agent step is present exactly when the policy calls for it.
func validateMerge(step Step, base string, opts Options, add func(string, string, ...any)) {
	m := step.Merge
	if m == nil {
		return // `block`, the default (§7.6)
	}
	switch m.OnConflict {
	case "", ConflictBlock:
		if m.Agent != nil {
			add(base+".merge.agent",
				"merge.agent is only used by on_conflict: %s; this step blocks on a conflict", ConflictAgent)
		}
	case ConflictAgent:
		if m.Agent == nil {
			add(base+".merge.agent", "on_conflict: %s requires merge.agent", ConflictAgent)
			return
		}
	default:
		add(base+".merge.on_conflict", "on_conflict must be %q or %q, got %q",
			ConflictBlock, ConflictAgent, m.OnConflict)
	}
	if m.Agent == nil {
		return
	}
	// A full agent step, judged by the ordinary rules (decision 24) — its
	// prompt, check, timeout and agent selection all mean what they mean
	// everywhere else. `type:` is implied rather than written out.
	resolver := *m.Agent
	if resolver.Type == "" {
		resolver.Type = StepAgent
	}
	if resolver.Type != StepAgent {
		add(base+".merge.agent.type", "merge.agent must be an %s step, got %q", StepAgent, resolver.Type)
		return
	}
	if resolver.ID == "" {
		// Ids matter here for the same reason they do anywhere: this step
		// gets step_runs rows of its own.
		add(base+".merge.agent.id", "merge.agent requires an id")
	}
	if m.Agent.OnInput == InputRequire {
		// The resolver runs while the task holds its slot mid-join; a
		// mid-run question is answerable, but the conflict it is resolving
		// is a worktree state no human can inspect through the request.
		add(base+".merge.agent.on_input", "on_input: %s is not valid on a merge resolver", InputRequire)
	}
	validateStep(resolver, base+".merge.agent", opts, add)
}

// validateLoop checks a `loop` step: its body, its single driver, and the
// ceiling the driver is bounded by (§7.8, task 016 decisions 2, 5, 11).
//
// The ceiling is the step's own `max_iterations:` when it declares one, else
// `loop.max_iterations` from config. Checking `count:` against it at load is
// the whole of decision 5's "bounded by construction": a loop that could
// never have finished is a workflow bug, and this is the last moment it can
// be reported to the person who wrote it.
func validateLoop(step Step, base string, add func(string, string, ...any), opts Options) {
	if len(step.Steps) == 0 {
		add(base+".steps", "loop steps require at least one body step")
	}
	switch {
	case step.Count == nil && len(step.ForEach) == 0:
		add(base, "a loop needs exactly one driver: %s or %s", "count", "for_each")
	case step.Count != nil && len(step.ForEach) > 0:
		add(base, "a loop has either count or for_each, not both")
	}
	var ceiling int
	if opts.MaxIterations != nil {
		ceiling = opts.MaxIterations()
	}
	if step.MaxIterations != nil {
		if *step.MaxIterations < 1 {
			add(base+".max_iterations", "max_iterations must be at least 1, got %d", *step.MaxIterations)
		} else {
			ceiling = *step.MaxIterations
		}
	}
	if step.Count != nil {
		switch {
		case *step.Count < 1:
			add(base+".count", "count must be at least 1, got %d", *step.Count)
		case ceiling > 0 && *step.Count > ceiling:
			add(base+".count",
				"count %d exceeds the %d-iteration ceiling; raise max_iterations on this step "+
					"or loop.max_iterations in config", *step.Count, ceiling)
		}
	}
	for i, item := range step.ForEach {
		if _, err := template.New("for_each").Parse(item); err != nil {
			add(fmt.Sprintf("%s.for_each[%d]", base, i), "template does not parse: %v", err)
		}
	}
	// No `max_retries`, no `retry_backoff` and no `allow_failure`
	// (decision 11): a loop has no attempt of its own to retry or to pace,
	// and "allow" on a loop could only mean "a loop_limit block advances
	// anyway", which is the silent success decision 5 refused. All three live
	// on the body steps, where they already do the useful thing.
	rejectFields(step, base, add, "prompt", "agent", "model", "effort",
		"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
		"run", "shell", "env", "instructions", "lanes", "merge", "max_parallel",
		"allow_failure", "max_retries", "retry_backoff")
}

// validateBodyStep checks one member of a `loop` body. Four step types cannot
// appear there and one field cannot be set on one, all for the reason §7.5
// gives about a group: a loop's position is *derived from its rows*
// (decision 7), and each of these needs a state no row can express — "this
// task is parked in iteration 3" (decisions 10).
func validateBodyStep(wf *Workflow, body Step, base string, opts Options, add func(string, string, ...any)) {
	switch body.Type {
	case StepManual:
		add(base+".type", "manual steps are not valid inside a loop body: "+
			"a gate ends the actor goroutine and releases the slot, and no state says which iteration is gated")
	case StepFanOut:
		add(base+".type", "fan_out steps are not valid inside a loop body: "+
			"the task parks in awaiting_children, and a parked parent has no row to say which iteration it parked in")
	case StepParallel:
		add(base+".type", "parallel groups are not valid inside a loop body: "+
			"a loop's position is derived from its rows, and that derivation stays affordable one level deep")
	case StepLoop:
		add(base+".type", "loops do not nest: a loop's position is derived from its rows, "+
			"and that derivation stays affordable one level deep")
	}
	// Resolved, not literal, exactly as a group sub-step is: `defaults.on_input:
	// require` reaches a body step that says nothing, and is just as unrunnable
	// there (§7.4).
	if wf.StepRequiresInput(body) {
		field := base + ".on_input"
		if body.OnInput == "" {
			field = "defaults.on_input"
		}
		add(field, "on_input: %s is not valid inside a loop body: "+
			"%s holds one pending request for the whole task", InputRequire, "awaiting_input")
	}
	validateStep(body, base, opts, add)
}

// validateLanes checks a fan_out step's lanes. Lane step ids live in their
// own namespace — each lane becomes a **separate child task** with its own
// flat snapshot (decision 4) — so they are checked against a fresh set rather
// than the parent workflow's.
//
// A derived step's single `lane:` template is checked here too, with the one
// difference a template forces: its `id` is rendered per item, so it cannot be
// held to the slug rule at load. Uniqueness and the slug rule move to spawn
// time for it (task 080 decisions 5, 6).
func validateLanes(wf *Workflow, step Step, base string, opts Options,
	add func(string, string, ...any), addWarn func(string, string, ...any),
) {
	if step.Lane != nil {
		validateLane(wf, *step.Lane, base+".lane", true, opts, add, addWarn)
		return
	}
	seen := make(map[string]string, len(step.Lanes))
	for i, lane := range step.Lanes {
		lanePath := fmt.Sprintf("%s.lanes[%d]", base, i)
		switch {
		case lane.ID == "":
			add(lanePath+".id", "id is required")
		case !isSlug(lane.ID):
			add(lanePath+".id", "lane id %q must be a slug (lowercase letters, digits, '-', '_', '.')", lane.ID)
		default:
			if prev, dup := seen[lane.ID]; dup {
				add(lanePath+".id", "duplicate lane id %q (first used by %s)", lane.ID, prev)
			}
			seen[lane.ID] = lanePath
		}
		validateLane(wf, lane, lanePath, false, opts, add, addWarn)
	}
	// The `needs:` graph is checked as a whole, once the ids are known: an
	// unknown id and a cycle are both statements about the *set*, and neither
	// can be judged from one lane. For a derived step the same check runs at
	// spawn, where the ids first exist (task 080 decision 6).
	for _, problem := range LaneGraphProblems(step.Lanes) {
		add(fmt.Sprintf("%s.lanes[%d].needs", base, problem.Index), "%s", problem.Message)
	}
}

// validateLane checks one lane. templated marks the single `lane:` template of
// a derived fan-out, whose id and needs are rendered per item and so cannot be
// judged here.
func validateLane(wf *Workflow, lane Lane, lanePath string, templated bool, opts Options,
	add func(string, string, ...any), addWarn func(string, string, ...any),
) {
	if templated {
		if lane.ID == "" {
			add(lanePath+".id", "id is required")
		} else if _, err := template.New("id").Parse(lane.ID); err != nil {
			add(lanePath+".id", "template does not parse: %v", err)
		}
		for i, need := range lane.Needs {
			if _, err := template.New("needs").Parse(need); err != nil {
				add(fmt.Sprintf("%s.needs[%d]", lanePath, i), "template does not parse: %v", err)
			}
		}
	}
	switch {
	case lane.Workflow == "" && len(lane.Steps) == 0:
		add(lanePath, "a lane needs either a workflow name or inline steps")
	case lane.Workflow != "" && len(lane.Steps) > 0:
		add(lanePath, "a lane has either a workflow name or inline steps, not both")
	}
	if lane.ResolvedFrom != "" && lane.Workflow != "" {
		// resolved_from is machine-written; a hand-written file carrying
		// both is describing two different sources for one lane.
		add(lanePath+".resolved_from", "resolved_from is set by task creation, not by hand")
	}
	if lane.Workflow != "" && strings.ContainsAny(lane.Workflow, " \t/\\") {
		add(lanePath+".workflow",
			"workflow %q must not contain whitespace or path separators", lane.Workflow)
	}
	if lane.Agent != "" && !knownAgent(lane.Agent, opts.KnownAgents) {
		add(lanePath+".agent", "unknown agent %q (known: %s)",
			lane.Agent, strings.Join(opts.KnownAgents, ", "))
	}
	if lane.If != "" {
		if _, err := template.New("if").Parse(lane.If); err != nil {
			add(lanePath+".if", "template does not parse: %v", err)
		}
	}
	// Inline steps are a workflow body in their own right, so they are
	// validated like one: their own id namespace, and their own right to
	// contain a fan_out (decision 5). What they may not contain is a
	// gate-free assumption anyone else's steps do not also carry.
	validateLaneSteps(wf, lane, lanePath, opts, add, addWarn)
}

// validateLaneSteps validates one lane's inline steps as the workflow body
// they will become.
func validateLaneSteps(wf *Workflow, lane Lane, base string, opts Options,
	add func(string, string, ...any), addWarn func(string, string, ...any),
) {
	if len(lane.Steps) == 0 {
		return
	}
	// A lane's inline steps *are* a workflow body — they become a child
	// task's own flat snapshot — so they go through the same walk the
	// top-level steps do, down to the trailing-`condition` warning. Their id
	// namespace is their own, which is the one thing that differs and is why
	// the map is made here (decision 4).
	validateBody(wf, lane.Steps, base+".steps", make(map[string]string, len(lane.Steps)), opts, add, addWarn)
}

// validateBody validates one workflow body: the top-level steps, or a
// fan-out lane's inline steps. ids is the namespace step ids are unique
// within, shared with every structure step's members.
func validateBody(wf *Workflow, steps []Step, base string, ids map[string]string, opts Options,
	add func(string, string, ...any), addWarn func(string, string, ...any),
) {
	for i, step := range steps {
		stepPath := fmt.Sprintf("%s[%d]", base, i)
		checkStepID(step, stepPath, ids, add)
		if step.Type == StepBreak {
			// `break` names the loop it ends, and there is none here.
			// Symmetric with `condition` being rejected inside a group: one
			// word, one meaning, supplied by what it is attached to
			// (decision 3).
			add(stepPath+".type", "break steps are only valid inside a loop body: "+
				"there is no loop here for one to end")
		}
		validateStep(step, stepPath, opts, add)
		switch step.Type {
		case StepParallel:
			for j, sub := range step.Steps {
				subPath := fmt.Sprintf("%s.steps[%d]", stepPath, j)
				checkStepID(sub, subPath, ids, add)
				validateSubStep(wf, sub, subPath, opts, add)
			}
		case StepFanOut:
			validateLanes(wf, step, stepPath, opts, add, addWarn)
		case StepLoop:
			for j, body := range step.Steps {
				bodyPath := fmt.Sprintf("%s.steps[%d]", stepPath, j)
				checkStepID(body, bodyPath, ids, add)
				validateBodyStep(wf, body, bodyPath, opts, add)
			}
		}
	}
	warnTrailingCondition(steps, base, addWarn)
}

// checkStepID validates one step id and records it in the body's namespace.
func checkStepID(step Step, base string, ids map[string]string, add func(string, string, ...any)) {
	switch {
	case step.ID == "":
		add(base+".id", "id is required")
	case !isSlug(step.ID):
		add(base+".id", "id %q must be a slug (lowercase letters, digits, '-', '_', '.')", step.ID)
	default:
		if prev, dup := ids[step.ID]; dup {
			add(base+".id", "duplicate step id %q (first used by %s)", step.ID, prev)
		}
		ids[step.ID] = base
	}
}

// warnTrailingCondition reports a `condition` step in last position. It is a
// warning rather than an error because a file being edited toward its final
// shape should still load: what it says is that the step cannot do anything a
// missing step would not, since continuing past the last step and stopping at
// it are the same outcome — the task is `done` either way (task 015).
func warnTrailingCondition(steps []Step, base string, addWarn func(string, string, ...any)) {
	if n := len(steps); n > 0 && steps[n-1].Type == StepCondition {
		addWarn(fmt.Sprintf("%s[%d].type", base, n-1),
			"a %s step in last position has no effect: the task is done whether it continues or stops",
			StepCondition)
	}
}

// validateSubStep checks one member of a `parallel` group. Beyond everything
// validateStep already checks, two step types cannot appear inside a group and
// one field cannot be set on a member of one — all three for the same reason:
// a group runs inside a single admission of a single task, and each of them
// would need a task state that says "one sub-step of one group is waiting",
// which §6 has no room for (task 014 decisions 18, 19).
func validateSubStep(wf *Workflow, sub Step, base string, opts Options, add func(string, string, ...any)) {
	switch sub.Type {
	case StepManual:
		add(base+".type", "manual steps are not valid inside a parallel group: "+
			"a gate ends the actor goroutine and releases the slot")
	case StepParallel:
		add(base+".type", "parallel groups do not nest; a group's sub-steps are %s or %s steps",
			StepAgent, StepCommand)
	case StepFanOut:
		// A fan_out parks its task in awaiting_children and releases the
		// slot, which is the same thing a gate does and the same reason
		// `manual` is refused here.
		add(base+".type", "fan_out steps are not valid inside a parallel group: "+
			"the task parks while its lanes run, and a group cannot park half a task")
	case StepCondition:
		// A group is a set, not a sequence, so "end the sequence" has
		// nothing to name here (task 015 decision 7). Subsetting a group is
		// what `if:` on a sub-step already does.
		add(base+".type", "condition steps are not valid inside a parallel group: "+
			"a group has no later steps to stop; guard the sub-step with `if:` instead")
	case StepLoop:
		// A loop's position is derived from its rows, which are keyed by the
		// structure step's index; nesting one inside a group would put two
		// derivations on one index (task 016 decision 10).
		add(base+".type", "loop steps are not valid inside a parallel group: "+
			"both derive their position from rows sharing one step_index")
	case StepBreak:
		add(base+".type", "break steps are only valid inside a loop body: "+
			"there is no loop here for one to end")
	}
	// Resolved, not literal: `defaults.on_input: require` reaches a sub-step
	// that says nothing, and it is just as unrunnable there (§7.4).
	if wf.StepRequiresInput(sub) {
		field := base + ".on_input"
		if sub.OnInput == "" {
			field = "defaults.on_input"
		}
		add(field, "on_input: %s is not valid inside a parallel group: "+
			"%s holds one pending request for the whole task", InputRequire, "awaiting_input")
	}
	validateStep(sub, base, opts, add)
}

// rejectFields reports the named fields as not allowed for this step's type.
// Strict decoding catches unknown keys; this catches keys that are known but
// belong to a different step type (§8.2).
func rejectFields(step Step, base string, add func(string, string, ...any), fields ...string) {
	set := map[string]bool{
		"prompt": step.Prompt != "", "agent": step.Agent != "", "model": step.Model != "",
		"effort": step.Effort != "", "permission_mode": step.PermissionMode != "",
		"on_input": step.OnInput != "", "input_timeout": step.InputTimeout != nil,
		"check": step.Check != "", "check_timeout": step.CheckTimeout != nil,
		"run": step.Run != "", "shell": step.Shell != "", "env": len(step.Env) > 0,
		"instructions": step.Instructions != "",
		"steps":        len(step.Steps) > 0, "max_parallel": step.MaxParallel != nil,
		"lanes": len(step.Lanes) > 0, "merge": step.Merge != nil,
		"lane": step.Lane != nil, "max_lanes": step.MaxLanes != nil,
		"if": step.If != "", "allow_failure": step.AllowFailure,
		"max_retries": step.MaxRetries != nil, "timeout": step.Timeout != nil,
		"retry_backoff": step.RetryBackoff != nil,
		"count":         step.Count != nil, "for_each": len(step.ForEach) > 0,
		"max_iterations": step.MaxIterations != nil,
		"workflow":       step.Workflow != "",
	}
	for _, f := range fields {
		if set[f] {
			add(base+"."+f, "%s is not valid on a %s step", f, step.Type)
		}
	}
}

func knownAgent(name string, known []string) bool {
	if len(known) == 0 {
		return true
	}
	for _, k := range known {
		if k == name {
			return true
		}
	}
	return false
}

func isPermissionMode(s string) bool { return s == PermissionFullAuto || s == PermissionRestricted }

func isInputPolicy(s string) bool {
	return s == InputWait || s == InputDeny || s == InputRequire
}

func isShell(s string) bool { return s == ShellSh || s == ShellPwsh || s == ShellCmd }

// isSlug reports whether s is a step id: lowercase alphanumerics plus
// '-', '_' and '.', starting with an alphanumeric.
func isSlug(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return s != ""
}

// locator maps YAML paths to source line numbers so semantic errors can be
// reported at the offending line, the way decode errors already are.
type locator struct {
	file *ast.File
}

func newLocator(src []byte) *locator {
	f, err := parser.ParseBytes(src, 0)
	if err != nil {
		return &locator{}
	}
	return &locator{file: f}
}

// line resolves a dotted/indexed path such as "steps[1].timeout" to its
// source line, walking up to the nearest ancestor that exists (a missing
// `prompt` key still points at its step). Returns 0 when nothing matches.
func (l *locator) line(path string) int {
	if l == nil || l.file == nil {
		return 0
	}
	for p := path; p != ""; p = parentPath(p) {
		yp, err := yaml.PathString("$." + p)
		if err != nil {
			continue
		}
		node, err := yp.FilterFile(l.file)
		if err != nil || node == nil {
			continue
		}
		if tok := node.GetToken(); tok != nil {
			return tok.Position.Line
		}
	}
	return 0
}

// decodeErrorPos matches the "[line:column]" prefix goccy puts on decode
// errors, which is how a strict-decoding failure reports its location.
var decodeErrorPos = regexp.MustCompile(`^\[(\d+):(\d+)\]\s*`)

func lineOfDecodeError(err error) int {
	m := decodeErrorPos.FindStringSubmatch(firstLine(err.Error()))
	if m == nil {
		return 0
	}
	line, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return 0
	}
	return line
}

// cleanDecodeError reduces a goccy error to its first line without the
// position prefix; the position travels in Error.Line instead.
func cleanDecodeError(err error) string {
	return decodeErrorPos.ReplaceAllString(firstLine(err.Error()), "")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// parentPath drops the last path element: "steps[1].timeout" → "steps[1]",
// "steps[1]" → "steps", "steps" → "".
func parentPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	if i := strings.LastIndex(path, "["); i > 0 {
		return path[:i]
	}
	return ""
}

// WalkSteps visits every step in a workflow body, descending into a
// `parallel` group's and a `loop`'s sub-steps and into a resolved `fan_out`
// lane's, and reports the dotted path of each. It exists because more than
// one check needs "every step, wherever it lives" and each of them had been
// re-deriving the nesting rules.
func WalkSteps(steps []Step, fn func(step Step, path string)) {
	walkSteps(steps, "steps", fn)
}

func walkSteps(steps []Step, base string, fn func(step Step, path string)) {
	for i, step := range steps {
		path := fmt.Sprintf("%s[%d]", base, i)
		fn(step, path)
		walkSteps(step.Steps, path+".steps", fn)
		for j, lane := range step.Lanes {
			walkSteps(lane.Steps, fmt.Sprintf("%s.lanes[%d].steps", path, j), fn)
		}
		if step.Lane != nil {
			walkSteps(step.Lane.Steps, path+".lane.steps", fn)
		}
	}
}
