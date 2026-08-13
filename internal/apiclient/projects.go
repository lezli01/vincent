package apiclient

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Project is one row of GET /v1/projects (§13.2). DefaultWorkflow and
// MaxParallelTasks are pointers because "unset" and "set to the zero value"
// are different: an unset default workflow falls through to the built-in
// adhoc, and an unset cap means the project has no cap of its own.
//
// The two admission caps are independent, not a fallback chain: the global
// max_parallel_tasks always binds, and a project's cap binds additionally
// when it is set (scheduler.admit). So a nil MaxParallelTasks does not mean
// "capped at the global figure" — it means this project may take as many of
// the global slots as it can get.
type Project struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Path             string  `json:"path"`
	DefaultBranch    string  `json:"default_branch"`
	DefaultWorkflow  *string `json:"default_workflow"`
	MaxParallelTasks *int    `json:"max_parallel_tasks"`
	// BranchTemplate is this project's branch convention; nil inherits the one in
	// config.yaml, and an unset config means the built-in name (task 001).
	BranchTemplate *string   `json:"branch_template"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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

// CreateProjectRequest is the POST /v1/projects body (§13.2). Only Path is
// required: the daemon derives the name from the directory and detects the
// default branch itself, so an omitted field is a deliberate "you decide"
// rather than a value the caller has to invent.
type CreateProjectRequest struct {
	Path             string  `json:"path"`
	Name             *string `json:"name,omitempty"`
	DefaultBranch    *string `json:"default_branch,omitempty"`
	DefaultWorkflow  *string `json:"default_workflow,omitempty"`
	MaxParallelTasks *int    `json:"max_parallel_tasks,omitempty"`
	BranchTemplate   *string `json:"branch_template,omitempty"`
}

// PatchProjectRequest is the PATCH /v1/projects/{id} body. Every field is an
// Opt so absent, null and set stay distinguishable end to end; name, path and
// default_branch reject null server-side, which is left to the daemon to say.
type PatchProjectRequest struct {
	Name             Opt[string] `json:"name,omitzero"`
	Path             Opt[string] `json:"path,omitzero"`
	DefaultBranch    Opt[string] `json:"default_branch,omitzero"`
	DefaultWorkflow  Opt[string] `json:"default_workflow,omitzero"`
	MaxParallelTasks Opt[int]    `json:"max_parallel_tasks,omitzero"`
	// BranchTemplate set to null or "" makes the project inherit config.yaml
	// again, which is how a convention is removed (task 001).
	BranchTemplate Opt[string] `json:"branch_template,omitzero"`
}

// CreateProject registers a repository. The daemon validates the path is a
// git repository, that no other project already names the same repo, and
// that the branch resolves — all of which need filesystem access, so the
// errors come back as *Error for the caller to render, not to pre-empt.
func (c *Client) CreateProject(ctx context.Context, req CreateProjectRequest) (Project, error) {
	var out Project
	if err := c.post(ctx, "/v1/projects", req, &out); err != nil {
		return Project{}, err
	}
	return out, nil
}

// PatchProject updates the fields the request sets and returns the project as
// the daemon now holds it.
func (c *Client) PatchProject(ctx context.Context, id int64, req PatchProjectRequest) (Project, error) {
	var out Project
	path := "/v1/projects/" + strconv.FormatInt(id, 10)
	if err := c.send(ctx, http.MethodPatch, path, req, &out); err != nil {
		return Project{}, err
	}
	return out, nil
}

// DeleteProject removes the project and cascades to its task rows.
//
// It answers 409 in two situations that look alike and are not. A project
// holding non-archived tasks is refused until force is set — force is the
// confirmation, the same shape as a dirty archive (§6). A project holding a
// *running* task is refused whether or not force is set: the caller has to
// cancel it first, so re-issuing with force can only fail again.
func (c *Client) DeleteProject(ctx context.Context, id int64, force bool) error {
	path := "/v1/projects/" + strconv.FormatInt(id, 10)
	if force {
		path += "?force"
	}
	return c.send(ctx, http.MethodDelete, path, nil, nil)
}
