package apiclient

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Workflow field types as GET /v1/workflows reports them (§8.1.2).
const (
	WorkflowFieldString  = "string"
	WorkflowFieldInteger = "integer"
	WorkflowFieldNumber  = "number"
	WorkflowFieldBoolean = "boolean"
	// WorkflowFieldEnum carries its members in Values (task 058). A client
	// that predates it sees an unknown type, falls through to a free-text
	// row and runs no local check; the daemon still gates the value.
	WorkflowFieldEnum = "enum"
)

// WorkflowFieldSeparator joins the members of a `multiple: true` enum, in the
// workflow's declared order (§8.1.2, task 058).
const WorkflowFieldSeparator = ","

// WorkflowEntry is one row of GET /v1/workflows (§13.2): the merged registry
// with §5.2 shadowing already applied. A file that failed to parse is listed
// with its Errors rather than omitted, so a picker can show the human what
// broke instead of silently losing a workflow they just edited.
type WorkflowEntry struct {
	Name        string              `json:"name"`
	Scope       string              `json:"scope"`
	ProjectID   *int64              `json:"project_id"`
	File        string              `json:"file,omitempty"`
	Description string              `json:"description"`
	Fields      []WorkflowField     `json:"fields"`
	Steps       []WorkflowEntryStep `json:"steps"`

	// Platforms is the workflow's §8.1.1 platform restriction, empty when it
	// runs anywhere. PlatformSupported is the daemon's verdict for its own
	// host; nil means the daemon predates the field, which is
	// indistinguishable from "unrestricted" and treated as such.
	Platforms         []string `json:"platforms,omitempty"`
	PlatformSupported *bool    `json:"platform_supported,omitempty"`

	// RequiresInput reports that some step needs an agent able to stop and
	// ask mid-run, and leaves the choice of agent to the task (§7.4, task
	// 013). Absent means the daemon predates the field, which is
	// indistinguishable from "no such step" and treated as such.
	RequiresInput bool `json:"requires_input,omitempty"`

	// Includes names the workflows this one splices in (§7.9, task 019).
	// Absent means the daemon predates the field, which is indistinguishable
	// from "includes nothing" and treated as such.
	Includes []string `json:"includes,omitempty"`

	Errors []WorkflowFinding `json:"errors,omitempty"`
	// Warnings are non-fatal §8.2 catalog findings; the entry stays valid.
	Warnings []WorkflowFinding `json:"warnings,omitempty"`
	Error    *string           `json:"error"`
}

// WorkflowField is one ordered public task input declared by a workflow
// (§8.1.2, task 022). Values remain strings; Type tells a client how to edit
// and validate a declared name. Additional task fields are still accepted.
type WorkflowField struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Pattern     string `json:"pattern,omitempty"`

	// Values are an `enum` field's members in declared order, Multiple says
	// it accepts more than one of them, and Default is the value that applies
	// when the key is omitted. Absent means the daemon predates the field,
	// which is indistinguishable from "declares none" and treated as such.
	Values   []string `json:"values,omitempty"`
	Multiple bool     `json:"multiple,omitempty"`
	Default  string   `json:"default,omitempty"`
}

// DisplayLabel is the presentation label, falling back to the field name.
func (f WorkflowField) DisplayLabel() string {
	if f.Label != "" {
		return f.Label
	}
	return f.Name
}

// WorkflowEntryStep is one step as the registry reports it. Agent is the
// §8.6 resolution of levels 1 and 3 only — the step's own field, else the
// workflow's defaults. It is empty when neither names one, which means "the
// adapter default" and not "no agent".
type WorkflowEntryStep struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Agent string `json:"agent,omitempty"`
}

// WorkflowFinding is one validation error or warning with its source line.
type WorkflowFinding struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Valid reports whether this entry can back a task. An invalid entry is
// listed for display only: POST /v1/tasks rejects it.
func (e WorkflowEntry) Valid() bool { return len(e.Errors) == 0 }

// RunsHere reports whether the daemon's host satisfies this workflow's
// platform restriction (§8.1.1). A workflow that does not run here is listed
// but cannot back a task: POST /v1/tasks rejects it the way it rejects an
// invalid one.
func (e WorkflowEntry) RunsHere() bool {
	return e.PlatformSupported == nil || *e.PlatformSupported
}

// PlatformNote describes the restriction for one picker row: "needs linux,
// darwin". It is empty when the workflow declares none.
func (e WorkflowEntry) PlatformNote() string {
	if len(e.Platforms) == 0 {
		return ""
	}
	return "needs " + strings.Join(e.Platforms, ", ")
}

// FirstError returns the leading validation failure, which is what a picker
// row has room for. It is empty for a valid entry.
func (e WorkflowEntry) FirstError() string {
	if len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0].Message
}

// ListWorkflows fetches the registry as it applies to one project: global
// entries plus that project's own, with project scope shadowing global.
// Passing 0 lists global entries only.
func (c *Client) ListWorkflows(ctx context.Context, projectID int64) ([]WorkflowEntry, error) {
	path := "/v1/workflows"
	if projectID != 0 {
		path += "?" + url.Values{"project_id": {strconv.FormatInt(projectID, 10)}}.Encode()
	}
	var body struct {
		Workflows []WorkflowEntry `json:"workflows"`
	}
	if err := c.get(ctx, path, &body); err != nil {
		return nil, err
	}
	return body.Workflows, nil
}
