package apiclient

import (
	"context"
	"net/url"
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
	// SupportsResume is whether this adapter can resume its own session, and
	// so whether it may hold a chat (§5.5, decision row 29). It is a pointer
	// for the same reason LoggedIn is: nil is "no judgement" — a daemon that
	// predates the field, or one with no adapter registry to ask — and
	// nothing may be refused on the strength of a field that was never sent.
	SupportsResume *bool `json:"supports_resume"`
	// InputVerdict is the daemon's verdict on backing an `on_input: require`
	// step (§7.4, task 013): "supported", "unsupported" or "unknown". Empty
	// means the daemon predates the field, which is treated as unknown —
	// nothing is refused on the strength of a field that was never sent.
	InputVerdict string `json:"input_verdict,omitempty"`
	// VersionVerdict is the daemon's reading of this build: "tested",
	// "untested" or "incompatible" (task 041). Empty means the daemon
	// predates the field or has nothing installed to judge — either way, no
	// judgement, which is what a renderer must show. It is advisory
	// everywhere: no client should refuse anything on the strength of it.
	VersionVerdict string `json:"version_verdict,omitempty"`
	// TestedVersions is the build list the verdict was judged against, for a
	// renderer that wants to say what "untested" means here.
	TestedVersions string `json:"tested_versions,omitempty"`
	// RestrictedVerdict is the daemon's verdict on backing a
	// `permission_mode: restricted` step: "supported", "unsupported" or
	// "unknown" (§9.4, task 041). Unlike the others it holds without an
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
	// Quota is the adapter's usage window: a reading a source reported, or
	// failing that one the daemon observed (task 026, task 082). nil when
	// neither exists — the normal state, which must render as "unknown",
	// never as "empty".
	Quota *AgentQuota `json:"quota"`
}

// AgentQuota is what the daemon knows about one adapter's usage window
// (task 026, §9.6, task 082). It is one of two different things and Source
// says which: an *observation* — the durable record of a `usage_limit` stop
// the daemon watched happen — or a *reading* an adapter's own surface
// reported, which is a measurement of a window still open.
type AgentQuota struct {
	// Spent is the daemon's answer as of the response. For an observation:
	// the window has not reset yet, and false with the rest of the block
	// populated means "this adapter last ran out at ObservedAt and has since
	// recovered". For a reading: UsedPercent has reached 100.
	Spent bool `json:"spent"`
	// UsedPercent and Window carry the tightest window of a reported reading
	// — the highest percentage, ties broken by the earliest reset. They are
	// null for an observation, which measures nothing.
	UsedPercent *float64 `json:"used_percent"`
	Window      *string  `json:"window"`
	// ObservedAt is when the daemon learned this: the `usage_limit` stop, or
	// the moment of the reading.
	ObservedAt time.Time `json:"observed_at"`
	ResetsAt   time.Time `json:"resets_at"`
	// ResetsAtReported separates a fact from an estimate: true when the CLI
	// named the reset time, false when the daemon's
	// `usage_limit_recheck_interval` supplied it, and false with a zero
	// ResetsAt when a reported window named none at all. A renderer must not
	// show a computed 15-minute guess as something the CLI stated, and must
	// not show the zero time as a time.
	ResetsAtReported bool `json:"resets_at_reported"`
	// Source is "observed", or the reporting source of a reading. It is what
	// tells a renderer which of the two blocks above it is holding — and
	// which SpentAt derivation applies.
	Source string `json:"source"`
	// Windows is every window a reported reading named; empty for an
	// observation. The daemon always sends an array, never null, so it can be
	// ranged over without a nil check.
	Windows []AgentQuotaWindow `json:"windows"`
}

// AgentQuotaWindow is one usage window of a reported reading (task 082).
// Name is the source's own vocabulary — "primary"/"secondary" for codex,
// "five_hour"/"seven_day" for claude — and is not normalized across vendors,
// because two vendors' windows are not the same thing.
type AgentQuotaWindow struct {
	Name        string  `json:"name"`
	UsedPercent float64 `json:"used_percent"`
	// Window is the human label ("5h", "7d"), empty when the source named
	// none.
	Window string `json:"window"`
	// ResetsAt is the zero time when the source named no reset, which
	// ResetsAtReported reports as false.
	ResetsAt         time.Time `json:"resets_at"`
	ResetsAtReported bool      `json:"resets_at_reported"`
}

// Quota sources as GET /v1/agents reports them (§9.6, task 026, task 082).
const (
	// QuotaSourceObserved is a window the daemon watched close: an agent step
	// its CLI stopped with `usage_limit`.
	QuotaSourceObserved = "observed"
	// QuotaSourceCodexAppServer is a reading codex's app-server answered.
	QuotaSourceCodexAppServer = "codex_app_server"
	// QuotaSourceClaudeStatusLine is a reading claude's status line pushed.
	QuotaSourceClaudeStatusLine = "claude_status_line"
)

// SpentAt reports whether the window is shut as of now.
//
// It re-derives the answer rather than trusting the wire's Spent, which is the
// daemon's answer *at fetch time*. Nothing is emitted when a window merely
// lapses — there is no sweeper and no timer on the daemon side (task 026) — so
// a client that trusted Spent would keep a badge on screen long after the
// window reopened. Spent stays on the wire because a non-subscribing client
// (curl, a script) wants the daemon's reading, not a clock comparison it has
// to write itself.
//
// The derivation splits on Source, and getting that wrong is the one way this
// feature breaks the board (task 082). An observation is spent while its reset
// is in the future. A *reading* is spent at 100%, and its reset is always in
// the future — that is what a window still open means — so applying the
// observation's clock comparison to one would light the badge permanently for
// every user whose adapter reports. A reading with no percentage at all is not
// spent: absent evidence is not a wall.
func (q *AgentQuota) SpentAt(now time.Time) bool {
	switch {
	case q == nil:
		return false
	case q.Source != "" && q.Source != QuotaSourceObserved:
		return q.UsedPercent != nil && *q.UsedPercent >= 100
	default:
		return now.Before(q.ResetsAt)
	}
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

// CannotResume reports an adapter the daemon would refuse a chat on (§5.5,
// decision row 29). Only a positive no counts: a nil SupportsResume — an
// older daemon, or one that cannot say — answers false, exactly as the
// input and restricted verdicts do.
func (a Agent) CannotResume() bool { return a.SupportsResume != nil && !*a.SupportsResume }

// VersionVerdict values as GET /v1/agents reports them (task 041).
const (
	VersionVerdictTested       = "tested"
	VersionVerdictUntested     = "untested"
	VersionVerdictIncompatible = "incompatible"
)

// RestrictedVerdict values as GET /v1/agents reports them (§9.4, task 041).
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
	// refresh is the same subprocess-per-adapter walk /v1/doctor?probe=true
	// makes, and is bounded the same way.
	if err := c.getVia(ctx, c.probeClient(refresh), path, &body); err != nil {
		return nil, err
	}
	return body.Agents, nil
}

// AgentQuotaReport is the body of `POST /v1/agents/{name}/quota` (§9.6,
// task 082): a usage reading a source pushes, because the daemon has no way to
// go and fetch it.
//
// claude is why it exists. Its usage windows arrive through the status line —
// the CLI runs `vincent statusline` to draw itself and hands that process the
// numbers — so the reading turns up at a moment the daemon did not choose.
type AgentQuotaReport struct {
	Source  string                   `json:"source"`
	Windows []AgentQuotaReportWindow `json:"windows"`
}

// AgentQuotaReportWindow is one window of a pushed reading.
type AgentQuotaReportWindow struct {
	Name        string  `json:"name"`
	UsedPercent float64 `json:"used_percent"`
	Window      string  `json:"window"`
	// ResetsAt is omitted when the source did not name a reset. It is a
	// pointer rather than a bare time.Time because "no reset was named" and
	// "the reset is the zero time" are different statements, and only the
	// first is ever true.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// ReportAgentQuota pushes a usage reading for one adapter (204 No Content).
//
// The daemon holds it in memory only — a reading is exactly as durable as the
// daemon (task 082 decision 4) — so a caller that renders repeatedly should
// keep pushing rather than assume one report sticks across a restart. Nothing
// is emitted when the reading has not changed, so pushing an identical one
// costs a request and wakes nobody.
func (c *Client) ReportAgentQuota(ctx context.Context, name string, r AgentQuotaReport) error {
	return c.post(ctx, "/v1/agents/"+url.PathEscape(name)+"/quota", r, nil)
}
