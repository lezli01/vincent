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
	Errors      []workflow.Error       `json:"errors,omitempty"`
	Error       *string                `json:"error"`
}

type workflowStepResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Agent string `json:"agent,omitempty"`
}

func toWorkflowResponse(e workflow.Entry) workflowResponse {
	out := workflowResponse{
		Name:  e.Name,
		Scope: string(e.Scope),
		File:  e.File,
		Steps: []workflowStepResponse{},
	}
	if e.ProjectID != 0 {
		id := e.ProjectID
		out.ProjectID = &id
	}
	if e.Workflow != nil {
		out.Description = e.Workflow.Description
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
	return out
}

// agentOf reports the agent a step would use before task-level overrides
// (§8.6 levels 1 and 3); the full resolution engine lands in T2.11.
func agentOf(wf *workflow.Workflow, st workflow.Step) string {
	if st.Type != workflow.StepAgent {
		return ""
	}
	if st.Agent != "" {
		return st.Agent
	}
	return wf.Defaults.Agent
}

// handleWorkflowList serves GET /v1/workflows?project_id= — the merged
// registry view with §5.2 shadowing applied.
func (s *Server) handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	var projectID int64
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				"project_id must be a positive integer")
			return
		}
		if _, err := s.deps.Store.GetProject(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("project %d not found", id))
				return
			}
			s.internalError(w, "list workflows: get project", err)
			return
		}
		projectID = id
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

// validateResponse mirrors §13.2: { valid, errors[] }.
type validateResponse struct {
	Valid  bool             `json:"valid"`
	Name   string           `json:"name,omitempty"`
	Errors []workflow.Error `json:"errors"`
}

// handleWorkflowValidate serves POST /v1/workflows/validate. A workflow that
// fails validation is a valid API response, not an API error: the body
// reports the failures with their source lines.
func (s *Server) handleWorkflowValidate(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.YAML == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "yaml is required")
		return
	}
	wf, err := workflow.Parse([]byte(req.YAML), s.deps.Workflows.Options())
	if err != nil {
		var errs workflow.Errors
		if !errors.As(err, &errs) {
			errs = workflow.Errors{{Message: err.Error()}}
		}
		writeJSON(w, http.StatusOK, validateResponse{Valid: false, Errors: errs})
		return
	}
	writeJSON(w, http.StatusOK, validateResponse{Valid: true, Name: wf.Name, Errors: []workflow.Error{}})
}
