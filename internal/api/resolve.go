package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// resolveRequest is the POST /v1/resolve body (spec §13.2): a workflow, the
// project whose scope resolves its name, and the task-level override triple
// as it stands right now — which is the part no GET can serve, because those
// overrides exist only in the form the user is still filling in.
type resolveRequest struct {
	Workflow  string `json:"workflow"`
	ProjectID *int64 `json:"project_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
}

// resolvedField is one resolved §8.6 value plus the level that supplied it.
// An empty value with source "adapter" is meaningful and honest: the adapter
// reported no default of its own, so the CLI decides at run time.
type resolvedField struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// resolvedStepResponse carries one step's resolution. Non-agent steps keep
// their place in the list — indexes match the workflow listing — with null
// fields, since agent/model/effort mean nothing for a command or a gate.
type resolvedStepResponse struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Agent  *resolvedField `json:"agent"`
	Model  *resolvedField `json:"model"`
	Effort *resolvedField `json:"effort"`
}

type resolveResponse struct {
	Workflow string                 `json:"workflow"`
	Steps    []resolvedStepResponse `json:"steps"`
}

// handleResolve serves POST /v1/resolve: §8.6 applied to every step of a
// workflow under a candidate task-level override.
//
// It exists so no client re-implements the precedence. The registry listing
// answers "what does this step say", which is a different question — a step
// naming no agent resolves to level 4, and only the daemon holds both the
// adapter catalog and the resolver needed to name it (T4.7 decision; the PR L
// decision that resolution stays server-side is honored, not relitigated).
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Workflow)
	if name == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "workflow is required")
		return
	}
	var projectID int64
	if req.ProjectID != nil {
		if *req.ProjectID < 1 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				"project_id must be a positive integer")
			return
		}
		if _, err := s.deps.Store.GetProject(r.Context(), *req.ProjectID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, CodeNotFound,
					fmt.Sprintf("project %d not found", *req.ProjectID))
				return
			}
			s.internalError(w, "resolve: get project", err)
			return
		}
		projectID = *req.ProjectID
	}
	entry, ok := s.deps.Workflows.Lookup(projectID, name)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("workflow %q not found for project %d", name, projectID))
		return
	}
	if entry.Workflow == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("workflow %q is invalid: %s", name, entry.Errors.Error()))
		return
	}

	override := agent.Level{
		Agent:  strings.TrimSpace(req.Agent),
		Model:  strings.TrimSpace(req.Model),
		Effort: strings.TrimSpace(req.Effort),
	}
	defaults := agent.Level{
		Agent:  entry.Workflow.Defaults.Agent,
		Model:  entry.Workflow.Defaults.Model,
		Effort: entry.Workflow.Defaults.Effort,
	}
	out := resolveResponse{Workflow: entry.Name, Steps: []resolvedStepResponse{}}
	for _, st := range entry.Workflow.Steps {
		row := resolvedStepResponse{ID: st.ID, Name: st.DisplayName(), Type: st.Type}
		if st.Type == workflow.StepAgent {
			sel, src := agent.ResolveWithSources(
				agent.Level{Agent: st.Agent, Model: st.Model, Effort: st.Effort},
				override, defaults,
			)
			model, effort := s.adapterDefaults(r, sel)
			row.Agent = &resolvedField{Value: sel.Agent, Source: string(src.Agent)}
			row.Model = &resolvedField{Value: firstNonBlank(sel.Model, model), Source: string(src.Model)}
			row.Effort = &resolvedField{Value: firstNonBlank(sel.Effort, effort), Source: string(src.Effort)}
		}
		out.Steps = append(out.Steps, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// adapterDefaults names §8.6 level 4 for the resolved agent: the model and
// effort the adapter itself reports. It reads the binary-identity cache
// without refreshing, so resolving a workflow never spawns a probe — this is
// called on every keystroke-driven refresh of the new-task form.
//
// Both returns are "" when the adapter is unknown or reports no default of
// its own, which stays a truthful level-4 answer: the CLI decides.
func (s *Server) adapterDefaults(r *http.Request, sel agent.Selection) (model, effort string) {
	if s.deps.Catalog == nil {
		return "", ""
	}
	e, ok := s.deps.Catalog.Entry(r.Context(), sel.Agent, false)
	if !ok {
		return "", ""
	}
	return e.Options.DefaultModel, e.Options.DefaultEffort
}
