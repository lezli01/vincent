package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// agentResponse is one adapter in GET /v1/agents (spec §9.6): the §9.5
// availability plus the provenance-tagged option catalog.
type agentResponse struct {
	Name          string         `json:"name"`
	Available     bool           `json:"available"`
	Path          string         `json:"path,omitempty"`
	Version       string         `json:"version,omitempty"`
	SupportsInput bool           `json:"supports_input"`
	Error         string         `json:"error,omitempty"`
	Models        []agent.Option `json:"models"`
	Efforts       []agent.Option `json:"efforts"`
	DefaultModel  string         `json:"default_model"`
	DefaultEffort string         `json:"default_effort"`
	ProbedAt      string         `json:"probed_at"`
	ProbeError    *string        `json:"probe_error"`
}

// handleAgents serves GET /v1/agents from the binary-identity cache: a
// request costs a path resolution and one stat per adapter unless the binary
// actually changed; ?refresh=true forces a re-probe (§9.6). A probe failure
// degrades to the curated catalog with probe_error set, never an API error.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Catalog == nil {
		s.internalError(w, "agent catalog", errors.New("no agent catalog is configured"))
		return
	}
	refresh := r.URL.Query().Has("refresh") &&
		r.URL.Query().Get("refresh") != "false" && r.URL.Query().Get("refresh") != "0"
	out := make([]agentResponse, 0, len(s.deps.Catalog.Names()))
	for _, name := range s.deps.Catalog.Names() {
		e, ok := s.deps.Catalog.Entry(r.Context(), name, refresh)
		if !ok {
			continue
		}
		resp := agentResponse{
			Name:          name,
			Available:     e.Availability.Found,
			Path:          e.Availability.Path,
			Version:       e.Availability.Version,
			SupportsInput: e.Availability.SupportsInput,
			Error:         e.Availability.Error,
			Models:        e.Options.Models,
			Efforts:       e.Options.Efforts,
			DefaultModel:  e.Options.DefaultModel,
			DefaultEffort: e.Options.DefaultEffort,
			ProbedAt:      e.ProbedAt.UTC().Format(time.RFC3339),
		}
		if resp.Models == nil {
			resp.Models = []agent.Option{}
		}
		if resp.Efforts == nil {
			resp.Efforts = []agent.Option{}
		}
		if e.ProbeError != "" {
			pe := e.ProbeError
			resp.ProbeError = &pe
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}
