package apiclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// The client half of the workflow write routes (§13.2, task 065). The wire
// carries edit operations, never YAML: a client that had to send a whole
// document would have had to discard the file's comments to build it, and the
// daemon would then have nothing to preserve (decision 2).

// Workflow edit operation kinds.
const (
	WorkflowOpSet    = "set"
	WorkflowOpInsert = "insert"
	WorkflowOpRemove = "remove"
	WorkflowOpMove   = "move"
)

// WorkflowOp is one edit. Path is dotted with list indices —
// "steps[2].prompt", "steps[3].lanes[0].merge.on_conflict", "fields[1].values".
type WorkflowOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value,omitempty"`
	// Block writes Value as a `|` block scalar, which is what a prompt, a
	// multi-line run: or an instructions body needs.
	Block bool `json:"block,omitempty"`
	// Item is the entry an insert writes, as ordered keys; the daemon
	// renders the YAML.
	Item []WorkflowOpField `json:"item,omitempty"`
	// To is a move's destination index within the same sequence.
	To int `json:"to,omitempty"`
}

// WorkflowOpField is one key of an inserted sequence entry.
type WorkflowOpField struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
	Block bool   `json:"block,omitempty"`
}

// CreateWorkflowRequest is the POST /v1/workflows body. From forks an
// existing entry — including a built-in, which is the only way to change one
// — by copying its bytes verbatim; the fork keeps the source's own `name:`,
// because keeping it is what makes the copy shadow the original (§5.2).
type CreateWorkflowRequest struct {
	Scope         string `json:"scope"`
	ProjectID     int64  `json:"project_id,omitempty"`
	Name          string `json:"name"`
	From          string `json:"from,omitempty"`
	FromProjectID *int64 `json:"from_project_id,omitempty"`
}

// WorkflowWriteResult is what both writes answer with.
type WorkflowWriteResult struct {
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	File    string `json:"file"`
	Version string `json:"version"`
	// Errors and Warnings are the parse verdict on the bytes now on disk.
	Errors   []WorkflowFinding `json:"errors"`
	Warnings []WorkflowFinding `json:"warnings"`
}

// WorkflowSchema is the served §8.2 descriptor (task 065 decision 3): which
// fields are legal on which step type, and where each type may be nested. A
// client renders its forms from this rather than re-deriving the daemon's
// checks, which is how the two used to drift (PR L).
type WorkflowSchema struct {
	TopLevel []WorkflowSchemaField    `json:"top_level"`
	Defaults []WorkflowSchemaField    `json:"defaults"`
	Field    []WorkflowSchemaField    `json:"field"`
	Common   []WorkflowSchemaField    `json:"common"`
	Steps    []WorkflowSchemaStepType `json:"steps"`
	Lane     []WorkflowSchemaField    `json:"lane"`
	Merge    []WorkflowSchemaField    `json:"merge"`
	Contexts []string                 `json:"contexts"`
}

// WorkflowSchemaField is one editable row. Control says what to draw, not
// what the value's Go type is: every one of them is a line of YAML by the
// time the daemon writes it.
type WorkflowSchemaField struct {
	Name     string   `json:"name"`
	Control  string   `json:"control"`
	Values   []string `json:"values,omitempty"`
	Required bool     `json:"required,omitempty"`
	Help     string   `json:"help,omitempty"`
}

// WorkflowSchemaStepType is one step type: the fields it accepts beyond the
// common ones, which common fields it accepts, and every context it may
// appear in.
type WorkflowSchemaStepType struct {
	Type     string                `json:"type"`
	Fields   []WorkflowSchemaField `json:"fields"`
	Common   []string              `json:"common"`
	Contexts []string              `json:"contexts"`
	Help     string                `json:"help,omitempty"`
}

// Control kinds a client branches on. Anything else is drawn as a text row,
// which is what keeps an older client usable against a newer daemon.
const (
	WorkflowControlString   = "string"
	WorkflowControlText     = "text"
	WorkflowControlEnum     = "enum"
	WorkflowControlBool     = "bool"
	WorkflowControlInt      = "int"
	WorkflowControlDuration = "duration"
	WorkflowControlList     = "list"
	WorkflowControlMap      = "map"
	WorkflowControlAgent    = "agent"
	WorkflowControlModel    = "model"
	WorkflowControlEffort   = "effort"
	WorkflowControlSteps    = "steps"
	WorkflowControlLanes    = "lanes"
	WorkflowControlMerge    = "merge"
	WorkflowControlWorkflow = "workflow"
	WorkflowControlFields   = "fields"
	WorkflowControlTemplate = "template"
)

// Step contexts (§8.2's nesting rules).
const (
	WorkflowContextBody     = "body"
	WorkflowContextParallel = "parallel"
	WorkflowContextLoop     = "loop"
	WorkflowContextMerge    = "merge"
)

// WorkflowSchema fetches the served §8.2 descriptor.
func (c *Client) WorkflowSchema(ctx context.Context) (WorkflowSchema, error) {
	var out WorkflowSchema
	if err := c.get(ctx, "/v1/workflows/schema", &out); err != nil {
		return WorkflowSchema{}, err
	}
	return out, nil
}

// CreateWorkflow writes a new workflow file in the requested scope. The
// daemon chooses the path and the starting bytes, so no YAML travels either
// way. A name the target scope already declares comes back as a 409, which is
// the §5.2 duplicate it would otherwise become.
func (c *Client) CreateWorkflow(ctx context.Context, req CreateWorkflowRequest) (WorkflowWriteResult, error) {
	var out WorkflowWriteResult
	if err := c.post(ctx, "/v1/workflows", req, &out); err != nil {
		return WorkflowWriteResult{}, err
	}
	return out, nil
}

// PatchWorkflow applies ops to the named workflow. Version is the token the
// read handed back; a file that moved underneath comes back as a 409 carrying
// the current token in details, which is the caller's cue to offer a reload.
func (c *Client) PatchWorkflow(ctx context.Context, name string, projectID int64,
	version string, ops []WorkflowOp,
) (WorkflowWriteResult, error) {
	q := url.Values{"name": {name}}
	if projectID != 0 {
		q.Set("project_id", strconv.FormatInt(projectID, 10))
	}
	body := struct {
		Version string       `json:"version"`
		Ops     []WorkflowOp `json:"ops"`
	}{Version: version, Ops: ops}
	var out WorkflowWriteResult
	if err := c.send(ctx, http.MethodPatch, "/v1/workflows?"+q.Encode(), body, &out); err != nil {
		return WorkflowWriteResult{}, err
	}
	return out, nil
}
