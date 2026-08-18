package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// workflowResponse is one registry entry (spec §13.2). A broken file is
// listed with its errors instead of being hidden, so the TUI can show it.
type workflowResponse struct {
	Name        string                 `json:"name"`
	Scope       string                 `json:"scope"`
	ProjectID   *int64                 `json:"project_id"`
	File        string                 `json:"file,omitempty"`
	Description string                 `json:"description"`
	Steps       []workflowStepResponse `json:"steps"`
	// Platforms is the §8.1.1 restriction as the file declares it; empty
	// means the workflow runs anywhere. PlatformSupported is the daemon's verdict
	// on its own host — the client never re-derives it, because the daemon is
	// the process that would run the steps (task 010).
	Platforms         []string `json:"platforms,omitempty"`
	PlatformSupported bool     `json:"platform_supported"`
	// RequiresInput reports that some step declares `on_input: require` and
	// leaves its agent to the task, so the agent picked for a task must be one
	// that can stop and ask (§7.4, task 013). Derived by the daemon for the
	// same reason platform_supported is: the process that would run the steps
	// is the one that says what they need.
	RequiresInput bool             `json:"requires_input"`
	Errors        []workflow.Error `json:"errors,omitempty"`
	// Warnings are non-fatal §8.2 catalog findings; the entry stays valid.
	Warnings []workflow.Error `json:"warnings,omitempty"`
	Error    *string          `json:"error"`
}

type workflowStepResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Agent string `json:"agent,omitempty"`
}

func toWorkflowResponse(e workflow.Entry) workflowResponse {
	out := workflowResponse{
		Name:              e.Name,
		Scope:             string(e.Scope),
		File:              e.File,
		Steps:             []workflowStepResponse{},
		PlatformSupported: e.RunsHere(),
		RequiresInput:     e.NeedsInputAgent(),
	}
	if e.ProjectID != 0 {
		id := e.ProjectID
		out.ProjectID = &id
	}
	if e.Workflow != nil {
		out.Description = e.Workflow.Description
		out.Platforms = e.Workflow.Platforms
		for _, st := range e.Workflow.Steps {
			out.Steps = append(out.Steps, workflowStepResponse{
				ID:    st.ID,
				Name:  st.DisplayName(),
				Type:  st.Type,
				Agent: agentOf(e.Workflow, st),
			})
		}
	}
	if len(e.Errors) > 0 {
		out.Errors = e.Errors
		msg := e.Errors.Error()
		out.Error = &msg
	}
	if len(e.Warnings) > 0 {
		out.Warnings = e.Warnings
	}
	return out
}

// agentOf reports the agent a step *names* — §8.6 levels 1 and 3 only, with
// no override applied and level 4 left empty. That is deliberately the
// registry's question ("what does this file say"); the resolved answer,
// including the adapter default, is what POST /v1/resolve serves (T4.7).
func agentOf(wf *workflow.Workflow, st workflow.Step) string {
	if st.Type != workflow.StepAgent {
		return ""
	}
	if st.Agent != "" {
		return st.Agent
	}
	return wf.Defaults.Agent
}

// projectIDParam reads the optional project_id scoping a registry request,
// writing the §13.1 error envelope and reporting false when it cannot. A
// missing parameter is 0, which both registry calls read as "global scope
// only" — the project must exist to scope by, so an unknown id is a 404
// rather than a silently global answer.
func projectIDParam(s *Server, w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("project_id")
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"project_id must be a positive integer")
		return 0, false
	}
	if _, err := s.deps.Store.GetProject(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("project %d not found", id))
			return 0, false
		}
		s.internalError(w, "workflows: get project", err)
		return 0, false
	}
	return id, true
}

// handleWorkflowList serves GET /v1/workflows?project_id= — the merged
// registry view with §5.2 shadowing applied.
func (s *Server) handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDParam(s, w, r)
	if !ok {
		return
	}
	entries := s.deps.Workflows.List(projectID)
	out := make([]workflowResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, toWorkflowResponse(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": out})
}

// validateRequest is the POST /v1/workflows/validate body.
type validateRequest struct {
	YAML string `json:"yaml"`
}

// validateResponse mirrors §13.2: { valid, errors[], warnings[] }.
type validateResponse struct {
	Valid    bool             `json:"valid"`
	Name     string           `json:"name,omitempty"`
	Errors   []workflow.Error `json:"errors"`
	Warnings []workflow.Error `json:"warnings"`
}

// handleWorkflowValidate serves POST /v1/workflows/validate. A workflow that
// fails validation is a valid API response, not an API error: the body
// reports the failures with their source lines, plus any non-fatal §8.2
// catalog warnings.
func (s *Server) handleWorkflowValidate(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.YAML == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "yaml is required")
		return
	}
	wf, warns, err := workflow.Parse([]byte(req.YAML), s.deps.Workflows.Options())
	if warns == nil {
		warns = workflow.Errors{}
	}
	if err != nil {
		var errs workflow.Errors
		if !errors.As(err, &errs) {
			errs = workflow.Errors{{Message: err.Error()}}
		}
		writeJSON(w, http.StatusOK, validateResponse{Valid: false, Errors: errs, Warnings: warns})
		return
	}
	writeJSON(w, http.StatusOK, validateResponse{
		Valid: true, Name: wf.Name, Errors: []workflow.Error{}, Warnings: warns,
	})
}
