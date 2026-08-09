package apiclient

import (
	"context"
	"time"
)

// AgentOption is one model or effort value with where it came from:
// "cli" when the adapter's own CLI listed it, "curated" when it comes from
// vincent's built-in catalog. The distinction matters to a human choosing:
// a curated value may be stale, a CLI value cannot be.
type AgentOption struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// Option sources as GET /v1/agents reports them (§9.6).
const (
	OptionSourceCLI     = "cli"
	OptionSourceCurated = "curated"
)

// Agent is one adapter from GET /v1/agents: the §9.5 availability plus the
// provenance-tagged option catalog the override pickers render.
type Agent struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Path          string `json:"path,omitempty"`
	Version       string `json:"version,omitempty"`
	SupportsInput bool   `json:"supports_input"`
	// Error explains an unavailable adapter (not found, unusable version).
	Error string `json:"error,omitempty"`

	Models        []AgentOption `json:"models"`
	Efforts       []AgentOption `json:"efforts"`
	DefaultModel  string        `json:"default_model"`
	DefaultEffort string        `json:"default_effort"`

	ProbedAt time.Time `json:"probed_at"`
	// ProbeError is set when the live probe failed and the response fell back
	// to the curated catalog. The entry is still usable; the options are just
	// not first-hand.
	ProbeError *string `json:"probe_error"`
}

// Agents is the adapter catalog indexed by name, in the daemon's
// registration order.
type Agents []Agent

// Find returns the named adapter.
func (a Agents) Find(name string) (Agent, bool) {
	for _, ag := range a {
		if ag.Name == name {
			return ag, true
		}
	}
	return Agent{}, false
}

// Unavailable reports whether name is a known adapter that is not usable
// right now. An unknown name is not "unavailable" — the catalog simply has
// nothing to say about it, and neither should a warning badge. An empty name
// means "the adapter default" (§8.6 level 4), which the registry cannot
// resolve for us, so it is never flagged either.
func (a Agents) Unavailable(name string) bool {
	if name == "" {
		return false
	}
	ag, ok := a.Find(name)
	return ok && !ag.Available
}

// ListAgents fetches the adapter catalog. refresh forces a live re-probe;
// without it the daemon answers from its binary-identity cache, which costs
// a path resolution and one stat per adapter and re-probes on its own when a
// binary actually changed — live enough for a picker, and it does not spawn
// an adapter subprocess every time a form opens.
func (c *Client) ListAgents(ctx context.Context, refresh bool) (Agents, error) {
	path := "/v1/agents"
	if refresh {
		path += "?refresh=true"
	}
	var body struct {
		Agents Agents `json:"agents"`
	}
	if err := c.get(ctx, path, &body); err != nil {
		return nil, err
	}
	return body.Agents, nil
}
