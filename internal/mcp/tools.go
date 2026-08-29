package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Route is one HTTP route, as this package needs to know it: a method, a
// pattern in net/http's ServeMux syntax, and the tool it becomes.
//
// It is deliberately a copy of the route table rather than a reference to it.
// internal/api may not be imported here (doc.go), so parity is asserted by a
// test in internal/api — which imports both — rather than obtained by
// construction. That is the point: a route added later cannot be silently
// unexposed *or* silently exposed, because the test fails either way.
type Route struct {
	Method string
	Path   string
	// Tool is the MCP tool name. Empty for a route that is not a tool.
	Tool string
	// Description is what the model reads when it decides to call this.
	Description string
}

// Excluded is the destructive-admin surface that is deliberately not a tool
// (task 057 decision 4, spec §13.4). An agent must not be able to stop,
// garbage-collect or reconfigure the daemon supervising it — least of all one
// running as a vincent step. These stay CLI-and-curl only.
//
// The two SSE routes are absent for a different reason and are listed
// separately: they are not excluded on principle, they are replaced. See
// Streaming.
var Excluded = []Route{
	{Method: http.MethodPost, Path: "/v1/daemon/stop"},
	{Method: http.MethodPost, Path: "/v1/daemon/backup"},
	{Method: http.MethodDelete, Path: "/v1/projects/{id}"},
	{Method: http.MethodPost, Path: "/v1/maintenance/gc"},
	{Method: http.MethodPost, Path: "/v1/doctor/fix"},
}

// Streaming lists the §13.3 SSE routes. They are not tools because a tool call
// is a request/response and an event stream is not; WaitTool is what replaces
// them for an MCP client.
var Streaming = []Route{
	{Method: http.MethodGet, Path: "/v1/events"},
	{Method: http.MethodGet, Path: "/v1/tasks/{id}/events"},
}

// WaitTool is the one tool with no route behind it (task 057 decision 5): a
// bounded blocking wait on a task, fed by the event broker server-side.
const WaitTool = "task_wait"

// routes is the tool surface: §13.2's route table minus Excluded and
// Streaming. Order is the route table's, so a reader can diff the two lists by
// eye.
var routes = []Route{
	{http.MethodGet, "/v1/health", "health", "Daemon liveness and version."},
	{http.MethodGet, "/v1/info", "info", "Daemon identity: version, pid, uptime, listen address, agent availability, orphan count and database size."},
	{http.MethodGet, "/v1/config", "config_get", "The daemon's effective configuration (§12.3), as the daemon currently reads it."},
	{http.MethodGet, "/v1/agents", "agent_list", "The agent adapters and their selectable models and effort levels (§9.6)."},
	{http.MethodGet, "/v1/doctor", "doctor", "The diagnostic report: directories, log, database figures and detected problems."},
	{http.MethodGet, "/v1/maintenance/orphans", "orphan_list", "Data-root directories no task row claims (§10)."},
	{http.MethodGet, "/v1/projects", "project_list", "List registered projects."},
	{http.MethodPost, "/v1/projects", "project_create", "Register a git repository as a project. Body: {path, name?, branch_template?}."},
	{http.MethodGet, "/v1/projects/{id}", "project_get", "One project by id."},
	{http.MethodPatch, "/v1/projects/{id}", "project_patch", "Update a project's mutable fields. Body is the subset to change."},
	{http.MethodGet, "/v1/projects/{id}/github", "project_github", "Whether this project's origin is a reachable GitHub repository (§12.3)."},
	{http.MethodGet, "/v1/projects/{id}/github/issues", "project_github_issues", "Open GitHub issues for this project's origin repository."},
	{http.MethodGet, "/v1/projects/{id}/github/pulls", "project_github_pulls", "Open GitHub pull requests for this project's origin repository."},
	{http.MethodGet, "/v1/workflows", "workflow_list", "The workflow registry (§5.2): built-in, global and project workflows after shadowing."},
	{http.MethodPost, "/v1/workflows/validate", "workflow_validate", "Validate workflow YAML without running it (§8.2). Body: {source}."},
	{http.MethodGet, "/v1/workflows/definition", "workflow_definition", "One workflow's parsed definition, by name and scope."},
	{http.MethodPost, "/v1/resolve", "resolve", "Resolve a path to the project that owns it."},
	{http.MethodGet, "/v1/tasks", "task_list", "List tasks, filtered by the query parameters the API documents."},
	{http.MethodPost, "/v1/tasks", "task_create", "Create a task. Body: {project_id, workflow, prompt, ...} as documented for POST /v1/tasks."},
	{http.MethodGet, "/v1/tasks/{id}", "task_get", "One task by id, with its current state and block reason."},
	{http.MethodPatch, "/v1/tasks/{id}", "task_patch", "Update a task's mutable fields (priority, agent override)."},
	{http.MethodPost, "/v1/tasks/{id}/cancel", "task_cancel", "Cancel a task (§6). Kills its live agent process if it has one."},
	{http.MethodPost, "/v1/tasks/{id}/pause", "task_pause", "Request a pause at the next step boundary (§6)."},
	{http.MethodPost, "/v1/tasks/{id}/resume", "task_resume", "Resume a paused task (§6)."},
	{http.MethodPost, "/v1/tasks/{id}/retry", "task_retry", "Retry a blocked task's failed step (§6)."},
	{http.MethodPost, "/v1/tasks/{id}/repair", "task_repair", "Re-run a blocked task's step with a repair prompt (§6). Body: {prompt}."},
	{http.MethodPost, "/v1/tasks/{id}/skip", "task_skip", "Skip a blocked task's failed step and continue (§6)."},
	{http.MethodPost, "/v1/tasks/{id}/approve", "task_approve", "Approve a task waiting at a human gate (§7.3)."},
	{http.MethodPost, "/v1/tasks/{id}/reject", "task_reject", "Reject a task waiting at a human gate (§7.3). Body: {reason?}."},
	{http.MethodPost, "/v1/tasks/{id}/answer", "task_answer", "Answer a task's pending mid-run input request (§7.4). Body: {answers} or {response}."},
	{http.MethodPost, "/v1/tasks/{id}/archive", "task_archive", "Archive a settled task: removes its worktree, and may delete an empty branch under delete_empty_branch_on_archive (§10)."},
	{http.MethodPost, "/v1/tasks/{id}/follow_up", "task_follow_up", "Queue follow-up work on a finished task's branch (§7.10). Body: {prompt, ...}."},
	{http.MethodGet, "/v1/tasks/{id}/workflow", "task_workflow", "The workflow snapshot the task is running, as it was at creation."},
	{http.MethodGet, "/v1/tasks/{id}/steps", "task_steps", "The task's steps and their step runs."},
	{http.MethodPost, "/v1/tasks/{id}/steps/{step_id}/status", "step_status", "Set the step-authored status line a board renders (task 036). Body: {status}."},
	{http.MethodGet, "/v1/tasks/{id}/steps/{run_id}/transcript", "task_transcript", "One step run's transcript. Query: format=raw|normalized, offset, limit."},
	{http.MethodGet, "/v1/tasks/{id}/diff", "task_diff", "The task branch's diff against its recorded base."},
	{http.MethodGet, "/v1/tasks/{id}/github/pull", "task_github_pull", "The pull request this task is linked to, if any (task 052)."},
	{http.MethodPost, "/v1/tasks/{id}/github/pull", "task_github_pull_link", "Link this task to a pull request. Body: {number}."},
	{http.MethodDelete, "/v1/tasks/{id}/github/pull", "task_github_pull_unlink", "Unlink this task's pull request. A human unlink is sticky (decision record row 27): the reconciler never re-applies it, so this suppresses the link permanently."},
}

// Routes returns the tool surface: one entry per `/v1` route that is a tool,
// in route-table order. WaitTool is not among them — it has no route.
func Routes() []Route {
	out := make([]Route, len(routes))
	copy(out, routes)
	return out
}

// Names returns every tool name this package serves, sorted, including
// WaitTool. It is what a test asserts the five exclusions absent from.
func Names() []string {
	out := make([]string, 0, len(routes)+1)
	for _, r := range routes {
		out = append(out, r.Tool)
	}
	out = append(out, WaitTool)
	sort.Strings(out)
	return out
}

// pathParams returns the `{name}` segments of a ServeMux pattern, in order.
func pathParams(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, seg[1:len(seg)-1])
		}
	}
	return out
}

// inputSchema builds one route's JSON Schema mechanically: its path segments
// become required properties, and the request carries either a `body` object
// (methods with one) or a `query` object of string parameters.
//
// The bodies are deliberately unconstrained beyond "object". The route's own
// handler validates them — that is the whole reason a tool replays rather than
// reimplements (decision 3) — and mirroring 40 request shapes here would be a
// second definition of the wire format to keep in step with the first. What
// the model needs to fill one in is in Description and on the API page.
func inputSchema(r Route) json.RawMessage {
	props := map[string]any{}
	required := []string{}
	for _, p := range pathParams(r.Path) {
		desc := fmt.Sprintf("the %s path parameter of %s %s", p, r.Method, r.Path)
		if p == "id" {
			props[p] = map[string]any{"type": "integer", "description": desc}
		} else {
			props[p] = map[string]any{"type": "string", "description": desc}
		}
		required = append(required, p)
	}
	if hasBody(r.Method) {
		props["body"] = map[string]any{
			"type":        "object",
			"description": "the JSON request body for " + r.Method + " " + r.Path,
		}
		if takesIdempotencyKey(r) {
			props["idempotency_key"] = map[string]any{
				"type": "string",
				"description": "optional replay-protection key: re-sending the same " +
					"arguments with the same key returns the original result instead of " +
					"creating a second task",
			}
		}
	} else {
		props["query"] = map[string]any{
			"type":                 "object",
			"description":          "query parameters for " + r.Method + " " + r.Path,
			"additionalProperties": map[string]any{"type": "string"},
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	// Marshaling a map[string]any of plain values cannot fail.
	b, _ := json.Marshal(schema) //nolint:errchkjson // see above
	return b
}

// takesIdempotencyKey reports whether a route honours §13.1's
// `Idempotency-Key`. Only `POST /v1/tasks` does, and the schema says so on
// that tool alone rather than everywhere — an argument a route ignores is a
// worse lie than a missing one.
func takesIdempotencyKey(r Route) bool {
	return r.Method == http.MethodPost && r.Path == "/v1/tasks"
}

// hasBody reports whether a method carries a request body on this API.
// DELETE never does here: the one DELETE that is a tool takes its target
// entirely from the path.
func hasBody(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut
}
