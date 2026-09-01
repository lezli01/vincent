package workflow

// This file is §8.2 as data (task 065 decision 3). It exists because the
// forms a client draws have to know which fields are legal on which step type
// — including the context-sensitive rules: a `parallel` sub-step may not be
// `manual`, `parallel`, `fan_out`, `condition` or `loop`; `break` is valid
// only inside a loop body; `condition` and `include` reject nearly every
// other field; `merge.agent` belongs to `on_conflict: agent`.
//
// PR L recorded that re-deriving the daemon's checks in the TUI is how the two
// drift, so the descriptor is served rather than reimplemented, and a drift
// test in this package walks it against validateStep itself: every field the
// descriptor offers for a type must survive that type's rejectFields call, and
// every field it withholds must be rejected by it. A field added to Parse
// without a schema entry fails, in the style of task 060's config drift test.
//
// Agent, model and effort value sets are deliberately absent. They are host
// state, not schema: GET /v1/agents (§9.6) serves them, and the new-task form
// already consumes it.

// Schema control kinds. They say what a client should draw, not what the
// value's Go type is — every one of them is a line of YAML by the time it
// reaches Edit.
const (
	// ControlString is a one-line text value.
	ControlString = "string"
	// ControlText is a multi-line value: a `|` block scalar on save.
	ControlText = "text"
	// ControlEnum is a closed set, published in Values (task 058's enum rows).
	ControlEnum = "enum"
	// ControlBool, ControlInt and ControlDuration are scalars with their own
	// input rules; a duration is a Go duration string (§12.1).
	ControlBool     = "bool"
	ControlInt      = "int"
	ControlDuration = "duration"
	// ControlList is a list of strings, written as a flow sequence.
	ControlList = "list"
	// ControlMap is a string→string mapping, written as a flow mapping.
	ControlMap = "map"
	// ControlAgent, ControlModel and ControlEffort are closed sets whose
	// members come from GET /v1/agents rather than from here.
	ControlAgent  = "agent"
	ControlModel  = "model"
	ControlEffort = "effort"
	// ControlSteps, ControlLanes, ControlMerge and ControlContainer are the
	// nested bodies a client descends into rather than typing into.
	ControlSteps = "steps"
	ControlLanes = "lanes"
	// ControlLane is the *single* lane template a derived `fan_out` renders
	// once per `for_each` item (§7.6, task 080). Its rows are the same Lane
	// descriptor ControlLanes descends into; only the arity differs.
	ControlLane  = "lane"
	ControlMerge = "merge"
	// ControlContainer is `defaults.container` (§8.6, task 061): the
	// workflow level of the containerization precedence chain. Every key is
	// a pointer in `config.ContainerOverride` because unset and set-to-zero
	// are different answers, so a form must be able to leave a row alone.
	ControlContainer = "container"
	// ControlWorkflow names another registry entry.
	ControlWorkflow = "workflow"
	// ControlFields is the §8.1.2 field declaration list.
	ControlFields = "fields"
	// ControlTemplate is a Go template rendered against the §8.4 context: an
	// `if:` guard or a `for_each` driver.
	ControlTemplate = "template"
)

// SchemaField is one editable row.
type SchemaField struct {
	Name    string `json:"name"`
	Control string `json:"control"`
	// Values are the members of a ControlEnum row.
	Values []string `json:"values,omitempty"`
	// Required marks a field the type cannot validate without.
	Required bool `json:"required,omitempty"`
	// Help is the one-line explanation a form shows beside the row. It is
	// the spec's own wording, condensed.
	Help string `json:"help,omitempty"`
}

// SchemaStepType is one step type and the fields it accepts beyond the common
// ones. Contexts names every place the type may appear, which is what makes
// the nesting rules renderable: a client offering a type a context forbids is
// offering a 400.
type SchemaStepType struct {
	Type   string        `json:"type"`
	Fields []SchemaField `json:"fields"`
	// Common lists the common step fields this type accepts; a type that
	// starts no process accepts almost none of them.
	Common []string `json:"common"`
	// Contexts is a subset of SchemaContexts.
	Contexts []string `json:"contexts"`
	Help     string   `json:"help,omitempty"`
}

// Step contexts: the four places a step can be written.
const (
	// ContextBody is the top level, and a fan-out lane's inline steps —
	// both are ordinary sequences with a "later" to skip to.
	ContextBody = "body"
	// ContextParallel is a `parallel` group's members (§7.5).
	ContextParallel = "parallel"
	// ContextLoop is a `loop` body (§7.8), which is a body plus `break`.
	ContextLoop = "loop"
	// ContextMerge is a `fan_out` step's merge resolver, which is one agent
	// step and nothing else (§7.6).
	ContextMerge = "merge"
)

// Schema is the whole descriptor GET /v1/workflows/schema serves.
type Schema struct {
	TopLevel []SchemaField    `json:"top_level"`
	Defaults []SchemaField    `json:"defaults"`
	Field    []SchemaField    `json:"field"`
	Common   []SchemaField    `json:"common"`
	Steps    []SchemaStepType `json:"steps"`
	Lane     []SchemaField    `json:"lane"`
	Merge    []SchemaField    `json:"merge"`
	// Container is the shape of `defaults.container` (§8.6), served for the
	// same reason Merge and Lane are: a nested block a client must be able
	// to build a form for without re-deriving it from the spec.
	Container []SchemaField `json:"container"`
	Contexts  []string      `json:"contexts"`
}

// commonStepFields are the fields every step type shares in principle; each
// type then lists in Common the ones it actually accepts.
func commonStepFields() []SchemaField {
	return []SchemaField{
		{Name: "id", Control: ControlString, Required: true, Help: "unique slug within the workflow"},
		{Name: "name", Control: ControlString, Help: "display name; falls back to the id"},
		{Name: "if", Control: ControlTemplate, Help: "guard: render true to run this step (§7.7)"},
		{Name: "allow_failure", Control: ControlBool, Help: "advance instead of blocking when the retry budget is spent"},
		{Name: "max_retries", Control: ControlInt, Help: "attempts after the first, before the step blocks"},
		{Name: "retry_backoff", Control: ControlDuration, Help: "wait before the next attempt"},
		{Name: "timeout", Control: ControlDuration, Help: "kill the step after this long"},
	}
}

// allCommon is every common field name; a type that accepts all of them says
// so with this rather than by repeating the list.
func allCommon() []string {
	out := make([]string, 0, 7)
	for _, f := range commonStepFields() {
		out = append(out, f.Name)
	}
	return out
}

// structureCommon is what a step that starts no process accepts: `condition`,
// `break` and `include` have nothing to time out, retry, pace or allow to
// fail, which is exactly what validateStep rejects on them.
var structureCommon = []string{"id", "name"}

// SchemaDescriptor returns the served §8.2 descriptor. It is built rather
// than stored so the enum members come from the same constants Parse checks
// against, and a new permission mode or shell reaches every client with the
// change that introduced it.
func SchemaDescriptor() Schema {
	agentFields := []SchemaField{
		{Name: "prompt", Control: ControlText, Required: true, Help: "the instruction the agent runs, rendered against §8.4"},
		{Name: "agent", Control: ControlAgent, Help: "adapter to run; defaults to defaults.agent"},
		{Name: "model", Control: ControlModel},
		{Name: "effort", Control: ControlEffort},
		{Name: "permission_mode", Control: ControlEnum, Values: []string{PermissionFullAuto, PermissionRestricted}},
		{
			Name: "on_input", Control: ControlEnum, Values: []string{InputWait, InputDeny, InputRequire},
			Help: "what to do when the agent asks a question mid-run (§7.4)",
		},
		{Name: "input_timeout", Control: ControlDuration},
		{Name: "check", Control: ControlString, Help: "command that must exit 0 for the step to pass"},
		{Name: "check_timeout", Control: ControlDuration},
	}
	return Schema{
		TopLevel: []SchemaField{
			{Name: "name", Control: ControlString, Required: true, Help: "the registry key this workflow is addressed by (§5.2)"},
			{Name: "description", Control: ControlString},
			{
				Name: "platforms", Control: ControlList, Values: platformTokens,
				Help: "hosts this workflow may run on; empty means anywhere (§8.1.1)",
			},
			{Name: "fields", Control: ControlFields, Help: "declared task inputs (§8.1.2)"},
			{Name: "defaults", Control: ControlMerge, Help: "workflow-level fallbacks for step fields"},
			{Name: "steps", Control: ControlSteps, Required: true},
		},
		Defaults: []SchemaField{
			{Name: "agent", Control: ControlAgent},
			{Name: "model", Control: ControlModel},
			{Name: "effort", Control: ControlEffort},
			{Name: "permission_mode", Control: ControlEnum, Values: []string{PermissionFullAuto, PermissionRestricted}},
			{Name: "on_input", Control: ControlEnum, Values: []string{InputWait, InputDeny, InputRequire}},
			{Name: "input_timeout", Control: ControlDuration},
			{Name: "max_retries", Control: ControlInt},
			{Name: "retry_backoff", Control: ControlDuration},
			{Name: "timeout", Control: ControlDuration},
			{
				Name: "container", Control: ControlContainer,
				Help: "run this workflow's steps in a container; beats config.yaml (§8.6)",
			},
		},
		Field: []SchemaField{
			{Name: "name", Control: ControlString, Required: true},
			{Name: "label", Control: ControlString},
			{Name: "description", Control: ControlString},
			{
				Name: "type", Control: ControlEnum, Required: true,
				Values: []string{FieldString, FieldInteger, FieldNumber, FieldBoolean, FieldEnum},
			},
			{Name: "required", Control: ControlBool},
			{Name: "pattern", Control: ControlString, Help: "regular expression; string fields only"},
			{Name: "values", Control: ControlList, Help: "the members of an enum field"},
			{Name: "multiple", Control: ControlBool, Help: "an enum field accepting several members"},
			{Name: "default", Control: ControlString, Help: "the value that applies when the key is omitted"},
		},
		Common: commonStepFields(),
		Steps: []SchemaStepType{
			{
				Type: StepAgent, Fields: agentFields, Common: allCommon(),
				Contexts: []string{ContextBody, ContextParallel, ContextLoop, ContextMerge},
				Help:     "run an agent CLI in the task's worktree",
			},
			{
				Type: StepCommand, Common: allCommon(),
				Contexts: []string{ContextBody, ContextParallel, ContextLoop},
				Help:     "run a shell command in the task's worktree",
				Fields: []SchemaField{
					{Name: "run", Control: ControlText, Required: true},
					{Name: "shell", Control: ControlEnum, Values: []string{ShellSh, ShellPwsh, ShellCmd}},
					{Name: "env", Control: ControlMap},
					{Name: "check", Control: ControlString},
					{Name: "check_timeout", Control: ControlDuration},
				},
			},
			{
				Type: StepManual, Common: []string{"id", "name", "if", "max_retries", "retry_backoff", "timeout"},
				Contexts: []string{ContextBody},
				Help:     "stop and wait for a person (§7.3)",
				Fields:   []SchemaField{{Name: "instructions", Control: ControlText, Required: true}},
			},
			{
				Type: StepParallel, Common: []string{"id", "name", "if", "max_retries", "retry_backoff", "timeout"},
				Contexts: []string{ContextBody},
				Help:     "run sub-steps concurrently in one admission (§7.5)",
				Fields: []SchemaField{
					{Name: "steps", Control: ControlSteps, Required: true},
					{Name: "max_parallel", Control: ControlInt},
				},
			},
			{
				Type: StepFanOut, Common: []string{"id", "name", "if", "max_retries", "retry_backoff", "timeout"},
				Contexts: []string{ContextBody},
				Help:     "spawn a child task per lane and merge them back (§7.6)",
				Fields: []SchemaField{
					{Name: "lanes", Control: ControlLanes, Help: "the lanes, or a lane: template with for_each:"},
					{Name: "lane", Control: ControlLane, Help: "one lane template, rendered once per for_each item"},
					{Name: "for_each", Control: ControlList, Help: "the item list a lane: template derives lanes from"},
					{Name: "max_lanes", Control: ControlInt, Help: "ceiling on a derived lane list"},
					{Name: "merge", Control: ControlMerge},
				},
			},
			{
				Type: StepCondition, Common: structureCommon,
				Contexts: []string{ContextBody, ContextLoop},
				Help:     "end the sequence unless its if: renders true (§7.7)",
				Fields:   []SchemaField{{Name: "if", Control: ControlTemplate, Required: true}},
			},
			// A loop has no attempt of its own to retry or to pace, so it
			// carries neither retry field (task 016 decision 11).
			{
				Type: StepLoop, Common: []string{"id", "name", "if", "timeout"},
				Contexts: []string{ContextBody},
				Help:     "repeat a body a fixed number of times or once per item (§7.8)",
				Fields: []SchemaField{
					{Name: "steps", Control: ControlSteps, Required: true},
					{Name: "count", Control: ControlInt, Help: "one of count or for_each is the driver"},
					{Name: "for_each", Control: ControlList},
					{Name: "max_iterations", Control: ControlInt},
				},
			},
			{
				Type: StepBreak, Common: structureCommon,
				Contexts: []string{ContextLoop},
				Help:     "end the enclosing loop when its if: renders true (§7.8)",
				Fields:   []SchemaField{{Name: "if", Control: ControlTemplate, Required: true}},
			},
			{
				Type: StepInclude, Common: structureCommon,
				Contexts: []string{ContextBody, ContextLoop},
				Help:     "splice another workflow's steps in at task creation (§7.9)",
				Fields:   []SchemaField{{Name: "workflow", Control: ControlWorkflow, Required: true}},
			},
		},
		Lane: []SchemaField{
			{Name: "id", Control: ControlString, Required: true},
			{Name: "if", Control: ControlTemplate},
			{Name: "needs", Control: ControlList, Help: "sibling lanes that must be done and merged first (§7.6)"},
			{Name: "workflow", Control: ControlWorkflow, Help: "a registry workflow, or inline steps — not both"},
			{Name: "steps", Control: ControlSteps},
			{Name: "fields", Control: ControlMap, Help: "task fields handed to the child task"},
			{Name: "agent", Control: ControlAgent},
			{Name: "model", Control: ControlModel},
			{Name: "effort", Control: ControlEffort},
			{Name: "priority", Control: ControlInt},
		},
		Merge: []SchemaField{
			{Name: "on_conflict", Control: ControlEnum, Values: []string{ConflictBlock, ConflictAgent}},
			{
				Name: "agent", Control: ControlMerge,
				Help: "the resolving agent step; required by and only valid with on_conflict: agent",
			},
		},
		Container: []SchemaField{
			{Name: "image", Control: ControlString, Help: "the image to run in; empty runs on the host (§16)"},
			{Name: "runtime", Control: ControlString, Help: "the container CLI, e.g. docker or podman"},
			{Name: "mount_agent_config", Control: ControlBool, Help: "mount the agent CLI's credentials into the container"},
			{Name: "network", Control: ControlBool, Help: "give the container a network"},
			{Name: "extra_mounts", Control: ControlList, Help: "additional host paths to mount"},
		},
		Contexts: []string{ContextBody, ContextParallel, ContextLoop, ContextMerge},
	}
}

// SchemaStep returns the descriptor for one step type.
func SchemaStep(typ string) (SchemaStepType, bool) {
	for _, s := range SchemaDescriptor().Steps {
		if s.Type == typ {
			return s, true
		}
	}
	return SchemaStepType{}, false
}

// StepTypesFor returns the step types legal in a context, which is the answer
// a form needs to fill its `type` row without re-deriving validateSubStep.
func StepTypesFor(context string) []string {
	var out []string
	for _, s := range SchemaDescriptor().Steps {
		for _, c := range s.Contexts {
			if c == context {
				out = append(out, s.Type)
				break
			}
		}
	}
	return out
}
