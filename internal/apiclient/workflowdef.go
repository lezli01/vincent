package apiclient

import (
	"context"
	"net/url"
	"strconv"
)

// WorkflowDefinition is GET /v1/workflows/definition (§13.2, task 017): one
// workflow's whole structure, which the registry list discards. Its shape
// mirrors the server DTO field for field — the two are one contract, and
// keeping them apart from internal/workflow is what lets the parser's model
// change without moving the wire.
//
// Definition is nil when the file did not parse. That is a successful
// response, not an error: the findings are in Errors, the same way the list
// shows a broken file rather than hiding it.
type WorkflowDefinition struct {
	Name              string            `json:"name"`
	Scope             string            `json:"scope"`
	ProjectID         *int64            `json:"project_id"`
	File              string            `json:"file,omitempty"`
	Platforms         []string          `json:"platforms,omitempty"`
	PlatformSupported bool              `json:"platform_supported"`
	RequiresInput     bool              `json:"requires_input"`
	Errors            []WorkflowFinding `json:"errors,omitempty"`
	Warnings          []WorkflowFinding `json:"warnings,omitempty"`
	Error             *string           `json:"error"`
	Definition        *WorkflowBody     `json:"definition"`
}

// WorkflowBody is the workflow as its file declares it. Defaults stay their
// own block: a step's empty Agent means the file set none there, and what it
// would inherit is Defaults.Agent — a distinction §8.6 rests on and one that
// folding the defaults into each step would destroy.
type WorkflowBody struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Platforms   []string          `json:"platforms,omitempty"`
	Defaults    WorkflowDefaults  `json:"defaults"`
	Steps       []WorkflowStepDef `json:"steps"`
}

// WorkflowDefaults is the §8.1 defaults block. An absent field means the file
// set nothing, and the daemon's own default applies at run time.
type WorkflowDefaults struct {
	Agent          string  `json:"agent,omitempty"`
	Model          string  `json:"model,omitempty"`
	Effort         string  `json:"effort,omitempty"`
	PermissionMode string  `json:"permission_mode,omitempty"`
	OnInput        string  `json:"on_input,omitempty"`
	InputTimeout   *string `json:"input_timeout,omitempty"`
	MaxRetries     *int    `json:"max_retries,omitempty"`
	Timeout        *string `json:"timeout,omitempty"`
}

// WorkflowStepDef is one step as authored, flat across every type the way the
// YAML is (§8.2). Durations are the string forms §13.2 uses everywhere
// ("60m"), and a nil pointer is "unset" rather than zero.
type WorkflowStepDef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Type string `json:"type"`

	MaxRetries   *int    `json:"max_retries,omitempty"`
	Timeout      *string `json:"timeout,omitempty"`
	If           string  `json:"if,omitempty"`
	AllowFailure bool    `json:"allow_failure,omitempty"`

	Prompt         string  `json:"prompt,omitempty"`
	Agent          string  `json:"agent,omitempty"`
	Model          string  `json:"model,omitempty"`
	Effort         string  `json:"effort,omitempty"`
	PermissionMode string  `json:"permission_mode,omitempty"`
	OnInput        string  `json:"on_input,omitempty"`
	InputTimeout   *string `json:"input_timeout,omitempty"`

	Check        string  `json:"check,omitempty"`
	CheckTimeout *string `json:"check_timeout,omitempty"`

	Run   string            `json:"run,omitempty"`
	Shell string            `json:"shell,omitempty"`
	Env   map[string]string `json:"env,omitempty"`

	Instructions string `json:"instructions,omitempty"`

	// Steps is a parallel group's members or a loop's body — one field for
	// "the steps inside me", as the workflow model has it.
	Steps       []WorkflowStepDef `json:"steps,omitempty"`
	MaxParallel *int              `json:"max_parallel,omitempty"`

	Lanes []WorkflowLaneDef `json:"lanes,omitempty"`
	Merge *WorkflowMergeDef `json:"merge,omitempty"`

	// Exactly one of Count and ForEach drives a loop. ForEach is templates,
	// not values: what it iterates is discovered when the loop runs, so a
	// definition reader cannot know the iteration count and must draw the
	// body once.
	Count         *int     `json:"count,omitempty"`
	ForEach       []string `json:"for_each,omitempty"`
	MaxIterations *int     `json:"max_iterations,omitempty"`
}

// WorkflowLaneDef is one fan_out lane: a named registry workflow or inline
// steps, never both in an authored file.
type WorkflowLaneDef struct {
	ID           string            `json:"id"`
	If           string            `json:"if,omitempty"`
	Workflow     string            `json:"workflow,omitempty"`
	ResolvedFrom string            `json:"resolved_from,omitempty"`
	Steps        []WorkflowStepDef `json:"steps,omitempty"`
	Fields       map[string]string `json:"fields,omitempty"`
	Agent        string            `json:"agent,omitempty"`
	Model        string            `json:"model,omitempty"`
	Effort       string            `json:"effort,omitempty"`
	Priority     *int              `json:"priority,omitempty"`
}

// WorkflowMergeDef is a fan_out step's join.
type WorkflowMergeDef struct {
	OnConflict string           `json:"on_conflict,omitempty"`
	Agent      *WorkflowStepDef `json:"agent,omitempty"`
}

// Valid reports whether the workflow parsed. An invalid one has findings and
// no body, which is a 200 by design.
func (d WorkflowDefinition) Valid() bool { return len(d.Errors) == 0 && d.Definition != nil }

// DisplayName is the label for a step: its authored name, else its id. The
// DTO carries the authored name so a caller can tell the two apart; every
// caller that only wants something to print uses this.
func (s WorkflowStepDef) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// ConflictPolicy resolves the join's policy, defaulting to `block` for a
// merge the file left unspelled — the same default §7.6 gives a fan_out step
// that declares no merge at all.
func (m WorkflowMergeDef) ConflictPolicy() string {
	if m.OnConflict == "" {
		return "block"
	}
	return m.OnConflict
}

// GetWorkflowDefinition fetches one workflow's full structure as it applies to
// a project, with §5.2 shadowing already resolved: the project's own entry
// wins, else global, else the built-in. Passing 0 looks in global scope only.
//
// The name travels in the query string because a registry name is neither
// URL-safe nor unique — a file that fails to parse is listed under a name that
// was never validated (task 017 decision 10).
func (c *Client) GetWorkflowDefinition(ctx context.Context, projectID int64, name string) (WorkflowDefinition, error) {
	q := url.Values{"name": {name}}
	if projectID != 0 {
		q.Set("project_id", strconv.FormatInt(projectID, 10))
	}
	var out WorkflowDefinition
	if err := c.get(ctx, "/v1/workflows/definition?"+q.Encode(), &out); err != nil {
		return WorkflowDefinition{}, err
	}
	return out, nil
}
