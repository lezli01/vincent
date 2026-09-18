package trigger

import "github.com/lezli01/vincent/internal/workflow"

// The trigger schema is the §8.2 descriptor's sibling (task 065 decision 3,
// applied to triggers by task 096 decision 19): served by
// GET /v1/triggers/schema so the TUI form renders from it rather than
// re-deriving the validator, and walked against the validator by
// TestTriggerSchemaMatchesValidation in both directions. The source and
// action types 096.3–096.5 added reach the form as variants here, with no
// client change.
//
// Scalar controls reuse workflow's vocabulary (ControlString, ControlEnum,
// …) so one form renderer draws both documents. The ones below are the
// nested bodies and pickers a trigger has and a workflow does not.
const (
	// ControlSource, ControlAction, ControlLimits and ControlSignature are
	// descents: a form opens a sub-form on them rather than typing into them.
	// Source and action descend into the variant their `type` picks.
	ControlSource    = "source"
	ControlAction    = "action"
	ControlLimits    = "limits"
	ControlSignature = "signature"
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
	// Events are the `action` values a GitHub source synthesizes, and Trusted
	// the subset a trigger may match without allowed_actors (decision 31F).
	Events  []string `json:"events,omitempty"`
	Trusted []string `json:"trusted,omitempty"`
}

// Schema is the whole descriptor GET /v1/triggers/schema serves.
type Schema struct {
	TopLevel  []SchemaField   `json:"top_level"`
	Sources   []SchemaVariant `json:"sources"`
	Actions   []SchemaVariant `json:"actions"`
	Limits    []SchemaField   `json:"limits"`
	Signature []SchemaField   `json:"signature"`
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
	warnCancelPrevious = "overrun: cancel_previous lets an inbound event destroy in-flight agent work: every " +
		"unfinished task in the group is cancelled before this event fires; name allowed_actors on a GitHub source"
)

// SchemaDescriptor returns the served descriptor. It is built rather than
// stored so enum members come from the constants the validator checks.
func SchemaDescriptor() Schema {
	sourceType := SchemaField{Name: "type", Control: workflow.ControlEnum, Values: SourceTypes(), Required: true}
	project := SchemaField{Name: "project", Control: ControlProject, Required: true, Help: "the project tasks are created in, or whose branches a reaction acts on"}
	actionType := SchemaField{Name: "type", Control: workflow.ControlEnum, Values: ActionTypes(), Required: true}
	target := SchemaField{Name: "target", Control: workflow.ControlEnum, Values: []string{TargetBranch}, Required: true, Help: "the task whose branch_name the event names"}
	branch := SchemaField{Name: "branch", Control: workflow.ControlTemplate, Required: true, Help: "template over .Event rendering the branch"}
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
			{Name: "allowed_actors", Control: workflow.ControlList, Help: "GitHub sources: the issue or pull request authors to accept; required to match an untrusted event"},
			{Name: "action", Control: ControlAction, Required: true, Values: ActionTypes(), Help: "what an event that passes does"},
			{
				Name: "on_fire", Control: workflow.ControlEnum, Values: []string{OnFirePropose, OnFireCreate},
				Default: OnFirePropose, Help: "propose creates or re-runs the task paused for you to admit; create starts it",
				Dangerous: []DangerousValue{{Value: OnFireCreate, Warning: warnCreate}},
			},
			{Name: "dedupe_key", Control: workflow.ControlTemplate, Default: "{{ .Event.id }}", Help: "an event whose key already fired is skipped"},
			{
				Name: "overrun", Control: workflow.ControlEnum, Values: Overruns(), Default: OverrunParallel,
				Help:      "what to do when this event's group already has unfinished work; an unreviewed on_fire: propose task holds its group",
				Dangerous: []DangerousValue{{Value: OverrunCancelPrevious, Warning: warnCancelPrevious}},
			},
			{Name: "concurrency_key", Control: workflow.ControlTemplate, Default: "the trigger id, or the target task for a reaction", Help: "overrun only: template over .Event naming the group"},
			{Name: "limits", Control: ControlLimits, Help: "per-trigger bounds"},
			{
				Name: "permission", Control: workflow.ControlEnum, Values: []string{PermissionRestricted, PermissionWorkflow},
				Default: PermissionRestricted, Help: "create_task only: restricted clamps every agent step; workflow runs it as written",
				Dangerous: []DangerousValue{{Value: PermissionWorkflow, Warning: warnWorkflow}},
			},
		},
		Sources: []SchemaVariant{
			{
				Type: SourceCommand,
				Help: "run an argv on an interval and read NDJSON events from its stdout",
				Fields: []SchemaField{
					sourceType, project,
					{Name: "poll_interval", Control: workflow.ControlDuration, Required: true, Help: "at least " + MinPollInterval.String()},
					{Name: "command", Control: workflow.ControlList, Required: true, Help: "argv, run directly — never through a shell"},
				},
			},
			{
				Type:   SourceGitHubIssues,
				Help:   "diff the project's GitHub issues on the github.poll_interval tick; no actor, only the author",
				Fields: []SchemaField{sourceType, project},
				Events: GitHubEvents(SourceGitHubIssues), Trusted: trustedList(SourceGitHubIssues),
			},
			{
				Type:   SourceGitHubPRs,
				Help:   "diff the project's GitHub pull requests on the github.poll_interval tick; no actor, only the author",
				Fields: []SchemaField{sourceType, project},
				Events: GitHubEvents(SourceGitHubPRs), Trusted: trustedList(SourceGitHubPRs),
			},
			{
				Type: SourceSchedule,
				Help: "fire on a clock: a cron expression, or a fixed interval counted from the moment the trigger was enabled",
				Fields: []SchemaField{
					sourceType, project,
					{Name: "cron", Control: workflow.ControlString, Help: CronGrammar},
					{
						Name: "every", Control: workflow.ControlDuration,
						Help: "instead of cron: fire this long after being enabled and then every interval; at least " + MinPollInterval.String(),
					},
					{
						Name: "timezone", Control: workflow.ControlString,
						Help: "IANA zone the cron expression is read in, such as Europe/Budapest; defaults to the daemon host's zone",
					},
				},
			},
			{
				Type: SourceHTTP,
				Help: "accept a signed event on POST /v1/triggers/{id}/events; the caller also needs the daemon's bearer token",
				Fields: []SchemaField{
					sourceType, project,
					{Name: "signature", Control: ControlSignature, Required: true, Help: "how a pushed event proves its sender"},
				},
			},
		},
		Actions: []SchemaVariant{
			{
				Type: ActionCreateTask,
				Help: "create a task, as POST /v1/tasks would for a person",
				Fields: []SchemaField{
					actionType,
					{Name: "workflow", Control: workflow.ControlWorkflow, Help: "defaults to the project's default workflow"},
					{Name: "title", Control: workflow.ControlTemplate, Required: true, Help: "template over .Event"},
					{Name: "description", Control: workflow.ControlText, Help: "template over .Event"},
					{Name: "fields", Control: workflow.ControlMap, Help: "workflow field → template over .Event"},
					{Name: "github_issue", Control: workflow.ControlTemplate, Help: "renders to an issue number, or nothing"},
					{Name: "github_pull", Control: workflow.ControlTemplate, Help: "renders to a pull request number, or nothing"},
				},
			},
			{
				Type: ActionFollowUp,
				Help: "run follow-up work on the finished task whose branch the event names",
				Fields: []SchemaField{
					actionType, target, branch,
					{Name: "prompt", Control: workflow.ControlText, Required: true, Help: "template over .Event: what the follow-up is told"},
				},
			},
			{
				Type: ActionRetry,
				Help: "retry the blocked task whose branch the event names",
				Fields: []SchemaField{
					actionType, target, branch,
					{Name: "prompt", Control: workflow.ControlText, Help: "template over .Event: overrides the failed step's prompt"},
				},
			},
			{
				Type:   ActionCancel,
				Help:   "cancel the task whose branch the event names; requires on_fire: create",
				Fields: []SchemaField{actionType, target, branch},
			},
		},
		Limits: []SchemaField{
			{Name: "max_per_hour", Control: workflow.ControlInt, Help: "fires in a trailing hour; 0 is no cap"},
			{Name: "max_task_cost_usd", Control: ControlNumber, Help: "create_task only: each created task's cost cap; 0 sends none"},
		},
		Signature: []SchemaField{
			{Name: "scheme", Control: workflow.ControlEnum, Values: SignatureSchemes(), Required: true, Help: "github_hmac_sha256 is X-Hub-Signature-256"},
			{Name: "secret_env", Control: workflow.ControlString, Required: true, Help: "the daemon environment variable holding the secret; never the secret itself"},
		},
	}
}
