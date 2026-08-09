package apiclient

import (
	"context"
	"net/url"
	"strconv"
)

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
	Steps       []WorkflowEntryStep `json:"steps"`

	Errors []WorkflowFinding `json:"errors,omitempty"`
	// Warnings are non-fatal §8.2 catalog findings; the entry stays valid.
	Warnings []WorkflowFinding `json:"warnings,omitempty"`
	Error    *string           `json:"error"`
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
