package trigger

import "github.com/lezli01/vincent/internal/workflow"

// The trigger schema is the §8.2 descriptor's sibling (task 065 decision 3,
// applied to triggers by task 096 decision 19): served by
// GET /v1/triggers/schema so the TUI form renders from it rather than
// re-deriving the validator, and walked against the validator by
// TestTriggerSchemaMatchesValidation in both directions. The source and
// action types 096.3–096.5 add reach the form as new variants here, with no
// client change.
//
// Scalar controls reuse workflow's vocabulary (ControlString, ControlEnum,
// …) so one form renderer draws both documents. The ones below are the
// nested bodies and pickers a trigger has and a workflow does not.
const (
	// ControlSource, ControlAction and ControlLimits are descents: a form
	// opens a sub-form on them rather than typing into them. Source and
	// action descend into the variant their `type` picks.
	ControlSource = "source"
	ControlAction = "action"
	ControlLimits = "limits"
	// ControlMatch is `match:`, a descent into a free map of dotted event
	// path → expected value (a scalar, or a flow list meaning any of).
	ControlMatch = "match"
	// ControlProject is a project id, picked from GET /v1/projects rather
	// than from the descriptor — host state, as the agent pickers are.
	ControlProject = "project"
	// ControlNumber is a non-negative decimal, such as a dollar amount.
	ControlNumber = "number"
)

// SchemaField is one editable row.
type SchemaField struct {
	Name    string `json:"name"`
	Control string `json:"control"`
	// Values are the members of a workflow.ControlEnum row, and the variant
	// types a ControlSource or ControlAction descent can pick.
	Values   []string `json:"values,omitempty"`
	Required bool     `json:"required,omitempty"`
	// Default is what an absent key means, when that is not the zero value
	// a form would otherwise assume.
	Default string `json:"default,omitempty"`
	Help    string `json:"help,omitempty"`
	// Dangerous marks the values a client must confirm before committing
	// (decision 19), whichever control or key produced them.
	Dangerous []DangerousValue `json:"dangerous,omitempty"`
}

// DangerousValue is one value that asks first, with the warning to show.
type DangerousValue struct {
	Value   string `json:"value"`
	Warning string `json:"warning"`
}

// SchemaVariant is one `type` of a source or action and the fields it takes.
// Fields includes `type` itself.
type SchemaVariant struct {
	Type   string        `json:"type"`
	Fields []SchemaField `json:"fields"`
	Help   string        `json:"help,omitempty"`
}

// Schema is the whole descriptor GET /v1/triggers/schema serves.
type Schema struct {
	TopLevel []SchemaField   `json:"top_level"`
	Sources  []SchemaVariant `json:"sources"`
	Actions  []SchemaVariant `json:"actions"`
	Limits   []SchemaField   `json:"limits"`
}

// Warnings for the three dangerous values (decision 19). The enable warning
// is generic here; a client that knows the trigger's on_fire states the
// consequence for it, as the TUI does.
const (
	warnEnable = "an enabled trigger polls its source and acts on every event that passes its filter, " +
		"with no keypress, while triggers.enabled is on"
	warnCreate = "on_fire: create starts agents on this machine for every event, with no keypress; " +
		"propose holds each task for you to admit"
	warnWorkflow = "permission: workflow lifts the restricted clamp: agent steps run as the workflow wrote " +
		"them, full-auto included"
)

// SchemaDescriptor returns the served descriptor. It is built rather than
// stored so enum members come from the constants the validator checks.
func SchemaDescriptor() Schema {
	return Schema{
		TopLevel: []SchemaField{
			{Name: "id", Control: workflow.ControlString, Required: true, Help: "the trigger's name; the file is {id}.yaml"},
			{
				Name: "enabled", Control: workflow.ControlBool, Default: "false",
				Help:      "poll and fire; off by default, and inert while triggers.enabled is off",
				Dangerous: []DangerousValue{{Value: "true", Warning: warnEnable}},
			},
			{Name: "source", Control: ControlSource, Required: true, Values: SourceTypes(), Help: "where events come from"},
			{Name: "match", Control: ControlMatch, Help: "prefilter: event path → value it must have"},
			{Name: "if", Control: workflow.ControlTemplate, Help: "guard over .Event: render true to act (§7.7)"},
			{Name: "action", Control: ControlAction, Required: true, Values: ActionTypes(), Help: "what an event that passes does"},
			{
				Name: "on_fire", Control: workflow.ControlEnum, Values: []string{OnFirePropose, OnFireCreate},
				Default: OnFirePropose, Help: "propose creates the task paused for you to admit; create starts it",
				Dangerous: []DangerousValue{{Value: OnFireCreate, Warning: warnCreate}},
			},
			{Name: "dedupe_key", Control: workflow.ControlTemplate, Default: "{{ .Event.id }}", Help: "an event whose key already fired is skipped"},
			{Name: "limits", Control: ControlLimits, Help: "per-trigger bounds"},
			{
				Name: "permission", Control: workflow.ControlEnum, Values: []string{PermissionRestricted, PermissionWorkflow},
				Default: PermissionRestricted, Help: "restricted clamps every agent step; workflow runs it as written",
				Dangerous: []DangerousValue{{Value: PermissionWorkflow, Warning: warnWorkflow}},
			},
		},
		Sources: []SchemaVariant{{
			Type: SourceCommand,
			Help: "run an argv on an interval and read NDJSON events from its stdout",
			Fields: []SchemaField{
				{Name: "type", Control: workflow.ControlEnum, Values: SourceTypes(), Required: true},
				{Name: "project", Control: ControlProject, Required: true, Help: "the project tasks are created in"},
				{Name: "poll_interval", Control: workflow.ControlDuration, Required: true, Help: "at least " + MinPollInterval.String()},
				{Name: "command", Control: workflow.ControlList, Required: true, Help: "argv, run directly — never through a shell"},
			},
		}},
		Actions: []SchemaVariant{{
			Type: ActionCreateTask,
			Help: "create a task, as POST /v1/tasks would for a person",
			Fields: []SchemaField{
				{Name: "type", Control: workflow.ControlEnum, Values: ActionTypes(), Required: true},
				{Name: "workflow", Control: workflow.ControlWorkflow, Help: "defaults to the project's default workflow"},
				{Name: "title", Control: workflow.ControlTemplate, Required: true, Help: "template over .Event"},
				{Name: "description", Control: workflow.ControlText, Help: "template over .Event"},
				{Name: "fields", Control: workflow.ControlMap, Help: "workflow field → template over .Event"},
				{Name: "github_issue", Control: workflow.ControlTemplate, Help: "renders to an issue number, or nothing"},
				{Name: "github_pull", Control: workflow.ControlTemplate, Help: "renders to a pull request number, or nothing"},
			},
		}},
		Limits: []SchemaField{
			{Name: "max_per_hour", Control: workflow.ControlInt, Help: "fires in a trailing hour; 0 is no cap"},
			{Name: "max_task_cost_usd", Control: ControlNumber, Help: "each created task's cost cap; 0 sends none"},
		},
	}
}
