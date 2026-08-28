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
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Path          string `json:"path,omitempty"`
	Version       string `json:"version,omitempty"`
	SupportsInput bool   `json:"supports_input"`
	// InputVerdict is the daemon's answer to whether this adapter may back a
	// step declaring `on_input: require` (§7.4, task 013): supported,
	// unsupported, or unknown. It is not derivable from supports_input alone —
	// a false there is "no" for an installed binary and "nobody can say" for
	// an absent one — so the daemon publishes the verdict its own gate uses
	// rather than leaving each client to re-derive the asymmetry.
	InputVerdict string `json:"input_verdict"`
	// VersionVerdict is what vincent knows about this build: "tested",
	// "untested", "incompatible", or "" when nothing is installed to judge
	// (task 041). It is advisory — nothing refuses a run on account of it —
	// and "untested" is the normal answer for a user on a current CLI.
	VersionVerdict string `json:"version_verdict"`
	// TestedVersions is the build list version_verdict was judged against,
	// so a client can say what "untested" is untested against.
	TestedVersions string `json:"tested_versions,omitempty"`
	// RestrictedVerdict is whether this adapter can run a `permission_mode:
	// restricted` step on this host: "supported", "unsupported" or "unknown"
	// (§9.4, §9.7, task 041). Unlike the others it needs no installed
	// binary — it is a fact about the adapter and the OS — and it is the one
	// verdict here that refuses anything: task creation rejects a restricted
	// step bound for an adapter reported "unsupported".
	//
	// Model-catalog health is not a fourth field: it is `probe_error`, which
	// §9.6 already defines as exactly "the option probe failed and you are
	// reading the curated catalog".
	RestrictedVerdict string         `json:"restricted_verdict"`
	LoggedIn          *bool          `json:"logged_in"` // null = the adapter cannot tell (§9.5)
	Error             string         `json:"error,omitempty"`
	Models            []agent.Option `json:"models"`
	Efforts           []agent.Option `json:"efforts"`
	DefaultModel      string         `json:"default_model"`
	DefaultEffort     string         `json:"default_effort"`
	ProbedAt          string         `json:"probed_at"`
	ProbeError        *string        `json:"probe_error"`
	// Quota is what the daemon has watched happen to this adapter's usage
	// window (task 026); null when nothing has ever been observed for it.
	// There is no probe behind it — no CLI vincent ships can report remaining
	// quota without a real run (§9.2, §9.3, §9.7) — so this is the durable
	// form of the `usage_limit` stops the engine already recognizes.
	Quota *quotaResponse `json:"quota"`
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
	quotas := s.agentQuotas(r.Context())
	out := make([]agentResponse, 0, len(s.deps.Catalog.Names()))
	for _, name := range s.deps.Catalog.Names() {
		e, ok := s.deps.Catalog.Entry(r.Context(), name, refresh)
		if !ok {
			continue
		}
		resp := agentResponse{
			Name:              name,
			Available:         e.Availability.Found,
			Path:              e.Availability.Path,
			Version:           e.Availability.Version,
			SupportsInput:     e.Availability.SupportsInput,
			InputVerdict:      string(e.InputVerdict()),
			VersionVerdict:    string(e.Availability.VersionVerdict),
			TestedVersions:    e.Availability.TestedVersions,
			RestrictedVerdict: string(e.RestrictedVerdict()),
			LoggedIn:          e.Availability.LoggedIn,
			Error:             e.Availability.Error,
			Models:            e.Options.Models,
			Efforts:           e.Options.Efforts,
			DefaultModel:      e.Options.DefaultModel,
			DefaultEffort:     e.Options.DefaultEffort,
			ProbedAt:          e.ProbedAt.UTC().Format(time.RFC3339),
			Quota:             quotas[name],
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
