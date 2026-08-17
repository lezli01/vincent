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
)

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
	Defaults  Defaults `yaml:"defaults"`
	Steps     []Step   `yaml:"steps"`
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
	Timeout        *config.Duration `yaml:"timeout"`
}

// Step is one workflow step. The struct is flat across all three step types;
// fields that do not belong to a step's type are rejected by validation
// (spec §8.2), which keeps the YAML shape and its errors simple.
type Step struct {
	ID         string           `yaml:"id"`
	Name       string           `yaml:"name"`
	Type       string           `yaml:"type"`
	MaxRetries *int             `yaml:"max_retries"`
	Timeout    *config.Duration `yaml:"timeout"`

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

	// fan_out steps (task 014)
	Lanes []Lane `yaml:"lanes,omitempty"`
	Merge *Merge `yaml:"merge,omitempty"`
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
	// Workflow names a registry workflow, resolved through the usual
	// builtin < global < project shadowing at **task-creation** time.
	Workflow string `yaml:"workflow,omitempty"`
	// Steps is an inline workflow body, used when Workflow is empty.
	Steps []Step `yaml:"steps,omitempty"`
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

	if wf.Name == "" {
		add("name", "name is required")
	} else if strings.ContainsAny(wf.Name, " \t/\\") {
		add("name", "name %q must not contain whitespace or path separators", wf.Name)
	}
	validatePlatforms(wf, add)
	validateDefaults(wf, opts, add)

	if len(wf.Steps) == 0 {
		add("steps", "steps must not be empty")
	}
	// Ids are unique across the whole workflow, sub-steps included: a
	// sub-step shares its group's step_index and is told apart from its
	// siblings by step_id alone (task 014 decision 16), which is also what
	// names its transcript file.
	seen := make(map[string]string, len(wf.Steps))
	checkID := func(step Step, base string) {
		switch {
		case step.ID == "":
			add(base+".id", "id is required")
		case !isSlug(step.ID):
			add(base+".id", "id %q must be a slug (lowercase letters, digits, '-', '_', '.')", step.ID)
		default:
			if prev, dup := seen[step.ID]; dup {
				add(base+".id", "duplicate step id %q (first used by %s)", step.ID, prev)
			}
			seen[step.ID] = base
		}
	}
	for i, step := range wf.Steps {
		base := fmt.Sprintf("steps[%d]", i)
		checkID(step, base)
		validateStep(step, base, opts, add)
		switch step.Type {
		case StepParallel:
			for j, sub := range step.Steps {
				subBase := fmt.Sprintf("%s.steps[%d]", base, j)
				checkID(sub, subBase)
				validateSubStep(wf, sub, subBase, opts, add)
			}
		case StepFanOut:
			validateLanes(wf, step, base, opts, add)
		}
	}
	warns = validateCatalogs(wf, opts, loc, add)
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
	if d.Timeout != nil && *d.Timeout <= 0 {
		add("defaults.timeout", "timeout must be positive, got %s", d.Timeout)
	}
	if d.InputTimeout != nil && *d.InputTimeout <= 0 {
		add("defaults.input_timeout", "input_timeout must be positive, got %s", d.InputTimeout)
	}
}

// validateStep checks one step: its type, the fields that type requires, the
// fields that do not belong to it, and every template it carries.
func validateStep(step Step, base string, opts Options, add func(string, string, ...any)) {
	switch step.Type {
	case "":
		add(base+".type", "type is required (one of %s, %s, %s, %s, %s)",
			StepAgent, StepCommand, StepManual, StepParallel, StepFanOut)
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
			"run", "shell", "env", "steps", "max_parallel", "lanes", "merge")
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
			"run", "shell", "env", "instructions", "lanes", "merge")
	case StepFanOut:
		if len(step.Lanes) == 0 {
			add(base+".lanes", "fan_out steps require at least one lane")
		}
		validateMerge(step, base, opts, add)
		rejectFields(step, base, add, "prompt", "agent", "model", "effort",
			"permission_mode", "on_input", "input_timeout", "check", "check_timeout",
			"run", "shell", "env", "instructions", "steps", "max_parallel")
	default:
		add(base+".type", "unknown step type %q (one of %s, %s, %s, %s, %s)",
			step.Type, StepAgent, StepCommand, StepManual, StepParallel, StepFanOut)
	}

	if step.MaxRetries != nil && *step.MaxRetries < 0 {
		add(base+".max_retries", "max_retries must not be negative, got %d", *step.MaxRetries)
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
		"prompt": step.Prompt, "run": step.Run, "check": step.Check, "instructions": step.Instructions,
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

// validateLanes checks a fan_out step's lanes. Lane step ids live in their
// own namespace — each lane becomes a **separate child task** with its own
// flat snapshot (decision 4) — so they are checked against a fresh set rather
// than the parent workflow's.
func validateLanes(wf *Workflow, step Step, base string, opts Options, add func(string, string, ...any)) {
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
		switch {
		case lane.Workflow == "" && len(lane.Steps) == 0:
			add(lanePath, "a lane needs either a workflow name or inline steps")
		case lane.Workflow != "" && len(lane.Steps) > 0:
			add(lanePath, "a lane has either a workflow name or inline steps, not both")
		}
		if lane.Workflow != "" && strings.ContainsAny(lane.Workflow, " \t/\\") {
			add(lanePath+".workflow",
				"workflow %q must not contain whitespace or path separators", lane.Workflow)
		}
		if lane.Agent != "" && !knownAgent(lane.Agent, opts.KnownAgents) {
			add(lanePath+".agent", "unknown agent %q (known: %s)",
				lane.Agent, strings.Join(opts.KnownAgents, ", "))
		}
		// Inline steps are a workflow body in their own right, so they are
		// validated like one: their own id namespace, and their own right to
		// contain a fan_out (decision 5). What they may not contain is a
		// gate-free assumption anyone else's steps do not also carry.
		validateLaneSteps(wf, lane, lanePath, opts, add)
	}
}

// validateLaneSteps validates one lane's inline steps as the workflow body
// they will become.
func validateLaneSteps(wf *Workflow, lane Lane, base string, opts Options, add func(string, string, ...any)) {
	if len(lane.Steps) == 0 {
		return
	}
	seen := make(map[string]string, len(lane.Steps))
	for i, step := range lane.Steps {
		stepPath := fmt.Sprintf("%s.steps[%d]", base, i)
		switch {
		case step.ID == "":
			add(stepPath+".id", "id is required")
		case !isSlug(step.ID):
			add(stepPath+".id", "id %q must be a slug (lowercase letters, digits, '-', '_', '.')", step.ID)
		default:
			if prev, dup := seen[step.ID]; dup {
				add(stepPath+".id", "duplicate step id %q (first used by %s)", step.ID, prev)
			}
			seen[step.ID] = stepPath
		}
		validateStep(step, stepPath, opts, add)
		switch step.Type {
		case StepParallel:
			for j, sub := range step.Steps {
				validateSubStep(wf, sub, fmt.Sprintf("%s.steps[%d]", stepPath, j), opts, add)
			}
		case StepFanOut:
			validateLanes(wf, step, stepPath, opts, add)
		}
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
