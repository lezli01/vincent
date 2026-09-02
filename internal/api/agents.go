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
	// SupportsResume is whether this adapter can resume its own session, and
	// so whether it may hold a chat (§5.5, decision row 29). It comes from
	// the same `agent.CanResume` the chat-creation gate consults, so a picker
	// built on it and the `agent_cannot_resume` refusal cannot disagree.
	//
	// Like `restricted_verdict` it needs no installed binary — it is a fact
	// about the adapter — but it is null rather than false when there is no
	// registry to ask, because "nobody can say" and "no" are different
	// answers and only the second may filter anything out.
	SupportsResume *bool `json:"supports_resume"`
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
	// Quota is this adapter's usage window: a reading a source reported, or
	// failing that a window the daemon watched close (task 026, task 082).
	// null when neither exists, which is the normal state and must render as
	// "unknown", never as "fine".
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
	// One read of the catalog, shared with agentQuotas: /v1/agents and
	// /v1/info must not be able to disagree about an adapter, and under
	// `?refresh=true` a second Entry call would probe every adapter twice.
	entries := s.catalogEntries(r.Context(), refresh)
	quotas := s.agentQuotas(r.Context(), entries)
	out := make([]agentResponse, 0, len(entries))
	for _, ce := range entries {
		name, e := ce.name, ce.entry
		resp := agentResponse{
			Name:              name,
			Available:         e.Availability.Found,
			Path:              e.Availability.Path,
			Version:           e.Availability.Version,
			SupportsInput:     e.Availability.SupportsInput,
			SupportsResume:    s.resumeSupport(name),
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

// resumeSupport answers `supports_resume` for one adapter, or nil when there
// is no registry to ask.
func (s *Server) resumeSupport(name string) *bool {
	if s.deps.Agents == nil {
		return nil
	}
	a, ok := s.deps.Agents.Get(name)
	if !ok {
		return nil
	}
	can := agent.CanResume(a)
	return &can
}
