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
	// InputVerdict is the daemon's verdict on backing an `on_input: require`
	// step (§7.4, task 013): "supported", "unsupported" or "unknown". Empty
	// means the daemon predates the field, which is treated as unknown —
	// nothing is refused on the strength of a field that was never sent.
	InputVerdict string `json:"input_verdict,omitempty"`
	// VersionVerdict is the daemon's reading of this build: "tested",
	// "untested" or "incompatible" (task 040). Empty means the daemon
	// predates the field or has nothing installed to judge — either way, no
	// judgement, which is what a renderer must show. It is advisory
	// everywhere: no client should refuse anything on the strength of it.
	VersionVerdict string `json:"version_verdict,omitempty"`
	// TestedVersions is the build list the verdict was judged against, for a
	// renderer that wants to say what "untested" means here.
	TestedVersions string `json:"tested_versions,omitempty"`
	// RestrictedVerdict is the daemon's verdict on backing a
	// `permission_mode: restricted` step: "supported", "unsupported" or
	// "unknown" (§9.4, task 040). Unlike the others it holds without an
	// installed binary, and it is the one the daemon refuses task creation
	// on.
	RestrictedVerdict string `json:"restricted_verdict,omitempty"`
	// LoggedIn is nil when the adapter cannot cheaply tell (§9.5); false
	// means installed but unauthenticated, which fails every run.
	LoggedIn *bool `json:"logged_in"`
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
	// Quota is the adapter's usage window as the daemon last observed it
	// (task 026), or nil when nothing has ever been observed for it — which
	// is the normal state and must render as "unknown", never as "empty".
	Quota *AgentQuota `json:"quota"`
}

// AgentQuota is what the daemon knows about one adapter's usage window
// (task 026, §9.6). It is an *observation*, not a measurement: no CLI vincent
// ships can report remaining quota without a real run, so this is the durable
// record of the `usage_limit` stops the daemon has watched happen.
type AgentQuota struct {
	// Spent is the daemon's answer as of the response: the observed window
	// has not reset yet. False with the rest of the block populated means
	// "this adapter last ran out at ObservedAt and has since recovered".
	Spent bool `json:"spent"`
	// UsedPercent and Window are permanently null today — nothing can fill
	// them (§9.2, §9.3, §9.7). They are declared so a client written now
	// against this shape keeps working the day a CLI ships a quota surface.
	UsedPercent *float64  `json:"used_percent"`
	Window      *string   `json:"window"`
	ObservedAt  time.Time `json:"observed_at"`
	ResetsAt    time.Time `json:"resets_at"`
	// ResetsAtReported separates a fact from an estimate: true when the CLI
	// named the reset time, false when the daemon's
	// `usage_limit_recheck_interval` supplied it. A renderer must not show a
	// computed 15-minute guess as something the CLI stated.
	ResetsAtReported bool `json:"resets_at_reported"`
	// Source is "observed" for everything written today.
	Source string `json:"source"`
}

// QuotaSourceObserved is a window the daemon watched close, as opposed to one
// a probe reported. It is the only source anything sends today.
const QuotaSourceObserved = "observed"

// SpentAt reports whether the observed window is still shut as of now.
//
// It re-derives the answer from ResetsAt rather than trusting the wire's
// Spent, which is the daemon's answer *at fetch time*. Nothing is emitted when
// a window merely lapses — there is no sweeper and no timer on the daemon side
// (task 026) — so a client that trusted Spent would keep a badge on screen
// long after the window reopened. Spent stays on the wire because a
// non-subscribing client (curl, a script) wants the daemon's reading, not a
// clock comparison it has to write itself.
func (q *AgentQuota) SpentAt(now time.Time) bool {
	return q != nil && now.Before(q.ResetsAt)
}

// QuotaSpent reports an adapter whose usage window is still shut as of now. An
// adapter with no observation answers false: never having run out is not the
// same as being fine, but it is the only honest default, and a badge on it
// would be a fabrication.
func (a Agent) QuotaSpent(now time.Time) bool { return a.Quota.SpentAt(now) }

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

// NotAuthenticated reports an adapter that is installed and probes cleanly
// but will fail every run for lack of a login. It is deliberately false when
// LoggedIn is nil: an adapter that cannot answer must never be accused (§9.5).
func (a Agent) NotAuthenticated() bool {
	return a.Available && a.LoggedIn != nil && !*a.LoggedIn
}

// Unusable reports an adapter that will not complete a step — missing, or
// present but unauthenticated. The two are one question for a caller deciding
// whether to warn, and two different sentences when it explains why.
func (a Agent) Unusable() bool { return !a.Available || a.NotAuthenticated() }

// InputVerdict values as GET /v1/agents reports them (§7.4, task 013).
const (
	InputVerdictSupported   = "supported"
	InputVerdictUnsupported = "unsupported"
	InputVerdictUnknown     = "unknown"
)

// CannotTakeInput reports an adapter the daemon would refuse for a step
// declaring `on_input: require`. Only a positive verdict counts: unknown, and
// a daemon too old to send one, both answer false, exactly as the daemon's own
// gate does.
func (a Agent) CannotTakeInput() bool { return a.InputVerdict == InputVerdictUnsupported }

// VersionVerdict values as GET /v1/agents reports them (task 040).
const (
	VersionVerdictTested       = "tested"
	VersionVerdictUntested     = "untested"
	VersionVerdictIncompatible = "incompatible"
)

// RestrictedVerdict values as GET /v1/agents reports them (§9.4, task 040).
const (
	RestrictedVerdictSupported   = "supported"
	RestrictedVerdictUnsupported = "unsupported"
	RestrictedVerdictUnknown     = "unknown"
)

// CannotRestrict reports an adapter the daemon would refuse for a step
// running `permission_mode: restricted`. Only a positive verdict counts:
// unknown, and a daemon too old to send one, both answer false, exactly as
// the daemon's own gate does.
func (a Agent) CannotRestrict() bool { return a.RestrictedVerdict == RestrictedVerdictUnsupported }

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
	return ok && ag.Unusable()
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
