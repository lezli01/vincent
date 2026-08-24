package api

import (
	"context"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// quotaResponse is one adapter's usage-window block, carried by both
// `GET /v1/agents` and `GET /v1/info` (§9.6, task 026).
//
// It rides both because the board header reads /v1/info while the new-task
// and repair forms read /v1/agents: a client should not need a second fetch to
// render a badge, and two endpoints describing the same adapter must not be
// able to disagree about it.
//
// `null` — not a zeroed block — is what an adapter with nothing observed
// reports. A zero here would read as "empty quota", which is the opposite of
// what it means.
type quotaResponse struct {
	// Spent is derived per request (now < resets_at) rather than stored: a
	// lapsed reset does not delete the row, so `spent: false` with the
	// observation intact is how "this window closed at 14:20 and has since
	// reopened" is said.
	Spent bool `json:"spent"`
	// UsedPercent and Window ship as permanent nulls. No CLI vincent
	// supports has a non-interactive quota surface (§9.2, §9.3, §9.7), so
	// nothing can fill either — they are here so clients are written once
	// against the final shape, and fill in the day a vendor ships one.
	UsedPercent *float64 `json:"used_percent"`
	Window      *string  `json:"window"`
	ObservedAt  string   `json:"observed_at"`
	ResetsAt    string   `json:"resets_at"`
	// ResetsAtReported is the difference between a fact and an estimate:
	// true when the CLI named the reset, false when
	// `usage_limit_recheck_interval` supplied it (§12.3).
	ResetsAtReported bool `json:"resets_at_reported"`
	// Source is `observed` for everything written today — a window this
	// daemon watched close. It is the seam a probe-sourced value would fill
	// without a second shape.
	Source string `json:"source"`
}

// agentQuotas reads every recorded observation once per request, keyed by
// adapter. A failed read degrades to "nothing observed" with a log line, for
// the same reason the /v1/info orphan count does: a display fact must not take
// the endpoint every client polls down with it. A nil Store (tests without
// persistence) answers the same way.
func (s *Server) agentQuotas(ctx context.Context) map[string]*quotaResponse {
	if s.deps.Store == nil {
		return nil
	}
	rows, err := s.deps.Store.ListAgentQuota(ctx)
	if err != nil {
		s.deps.Logger.Warn("list agent quota", "error", err)
		return nil
	}
	now := time.Now()
	out := make(map[string]*quotaResponse, len(rows))
	for _, q := range rows {
		out[q.Agent] = newQuotaResponse(q, now)
	}
	return out
}

func newQuotaResponse(q store.AgentQuota, now time.Time) *quotaResponse {
	return &quotaResponse{
		Spent:            q.Spent(now),
		ObservedAt:       q.ObservedAt.UTC().Format(time.RFC3339),
		ResetsAt:         q.ResetsAt.UTC().Format(time.RFC3339),
		ResetsAtReported: q.ResetsAtReported,
		Source:           q.Source,
	}
}
