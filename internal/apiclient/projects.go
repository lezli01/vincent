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
	BranchTemplate *string `json:"branch_template"`
	// SlotsUsed is how many of this project's tasks hold a concurrency slot
	// right now (§11): `running` or `awaiting_input`, fan-out lanes included.
	// The daemon counts it against MaxParallelTasks, so it is 0 — not absent
	// — for a project holding none.
	SlotsUsed int       `json:"slots_used"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Stats is the project's counts, served only when ListProjects was asked
	// for them with WithStats (task 132.1). Nil when not asked, and nil too
	// when the daemon could not count — it degrades to null rather than
	// failing the list.
	Stats *ProjectStats `json:"stats,omitempty"`
}

// ProjectStats is the `stats` object of GET /v1/projects?stats=true (§13.2).
//
// The task figures count every non-archived task row, fan-out lanes
// included, so they line up with SlotsUsed. Tasks.Active is therefore not
// Issues.Active: an issue is active through an unsettled *root* task.
type ProjectStats struct {
	Tasks struct {
		// ByState omits zero states and never carries archived.
		ByState   map[string]int `json:"by_state"`
		Active    int            `json:"active"`
		Attention int            `json:"attention"`
	} `json:"tasks"`
	Issues struct {
		Open         int `json:"open"`
		OpenImported int `json:"open_imported"`
		Active       int `json:"active"`
		// Lanes counts issues per board lane, closed ones as done.
		Lanes struct {
			Open       int `json:"open"`
			InProgress int `json:"in_progress"`
			HandOff    int `json:"hand_off"`
			Done       int `json:"done"`
		} `json:"lanes"`
	} `json:"issues"`
	// Chats is kept apart from Tasks: chat attention is never part of the
	// task attention count.
	Chats struct {
		Live          int `json:"live"`
		AwaitingInput int `json:"awaiting_input"`
	} `json:"chats"`
	// IssueSync is the stored sync health; GET .../issues/sync is the full
	// status.
	IssueSync struct {
		Enabled      bool       `json:"enabled"`
		OK           bool       `json:"ok"`
		Reason       string     `json:"reason"`
		LastSyncedAt *time.Time `json:"last_synced_at"`
	} `json:"issue_sync"`
	// LastActivityAt is the newest change to the project's tasks, issues or
	// chats; nil when it has none.
	LastActivityAt *time.Time `json:"last_activity_at"`
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

// ListProjectsOption shapes a ListProjects request.
type ListProjectsOption func(*listProjectsOptions)

type listProjectsOptions struct{ stats bool }

// WithStats asks the daemon for each project's Stats (task 132.1). It costs
// the daemon a fixed handful of GROUP BY statements, so it is opt-in rather
// than on every refresh.
func WithStats() ListProjectsOption {
	return func(o *listProjectsOptions) { o.stats = true }
}

// ListProjects fetches every registered project. The endpoint has no filters
// and no pagination — the list is human-sized by construction.
func (c *Client) ListProjects(ctx context.Context, opts ...ListProjectsOption) ([]Project, error) {
	var o listProjectsOptions
	for _, opt := range opts {
		opt(&o)
	}
	path := "/v1/projects"
	if o.stats {
		path += "?stats=true"
	}
	var out []Project
	if err := c.get(ctx, path, &out); err != nil {
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

// Branch is one local branch of a project, as GET
// /v1/projects/{id}/branches reports it (task 125).
type Branch struct {
	Name string `json:"name"`
	// CheckedOutIn is the working tree holding the branch, or "" when none
	// does.
	CheckedOutIn string `json:"checked_out_in,omitempty"`
	// MainCheckout says CheckedOutIn is the project's own path, so a task or
	// chat adopting this branch runs *there* rather than in a worktree (§10,
	// task 125 decision 3). The daemon computes it: git and the project
	// record may spell one directory two ways.
	MainCheckout bool `json:"main_checkout,omitempty"`
	// Current marks the branch the project's main checkout has at HEAD.
	Current bool `json:"current,omitempty"`
}

type branchListResponse struct {
	Branches []Branch `json:"branches"`
}

// ListBranches returns the project's local branches, which is what the
// new-task and new-chat forms offer as a picker (task 125). Free text is
// still accepted on both: a name that is not in this list is the ordinary
// cut-a-new-branch mode.
func (c *Client) ListBranches(ctx context.Context, projectID int64) ([]Branch, error) {
	var out branchListResponse
	path := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/branches"
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Branches, nil
}
