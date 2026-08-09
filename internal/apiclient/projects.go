package apiclient

import (
	"context"
	"time"
)

// Project is one row of GET /v1/projects (§13.2). DefaultWorkflow and
// MaxParallelTasks are pointers because "unset" and "set to the zero value"
// are different: an unset default workflow falls through to the built-in
// adhoc, and an unset cap falls through to the global one.
type Project struct {
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	Path             string    `json:"path"`
	DefaultBranch    string    `json:"default_branch"`
	DefaultWorkflow  *string   `json:"default_workflow"`
	MaxParallelTasks *int      `json:"max_parallel_tasks"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Workflow reports the workflow a new task in this project gets when the
// caller names none: the project default, else the built-in adhoc, which is
// the same fallback handleTaskCreate applies server-side.
func (p Project) Workflow() string {
	if p.DefaultWorkflow != nil && *p.DefaultWorkflow != "" {
		return *p.DefaultWorkflow
	}
	return AdhocWorkflow
}

// AdhocWorkflow is the built-in single-step workflow every project can run
// without registering anything (§8.1).
const AdhocWorkflow = "adhoc"

// ListProjects fetches every registered project. The endpoint has no filters
// and no pagination — the list is human-sized by construction.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out []Project
	if err := c.get(ctx, "/v1/projects", &out); err != nil {
		return nil, err
	}
	return out, nil
}
