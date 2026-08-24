package tui

import (
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// eventAgentQuotaChanged is the durable event the daemon writes when what it
// knows about an adapter's usage window changes (§13.3, task 026). Its payload
// names the adapter, but the board renders every adapter's badge from
// `GET /v1/info`, so the honest reaction is to refetch that rather than to
// patch one entry from an event body.
const eventAgentQuotaChanged = "agent.quota_changed"

// quotaMark is the board header's third badge (§15, task 026): an adapter that
// is installed, authenticated, and out of quota until a stated time. It is a
// glyph rather than a colour because the board's other two badges are, and
// because "why is nothing running" must survive a monochrome terminal.
const quotaMark = "⏳"

// quotaReset renders the reset time with the provenance the daemon recorded.
//
// The arrow is the point. `→ 14:20` is what the CLI said; `≈ 14:20` is
// `now + usage_limit_recheck_interval` (§12.3) because the CLI said nothing,
// and showing a computed 15-minute guess as a stated fact is how a human ends
// up waiting on a number vincent invented. It is the same `15:04` local-time
// form the board's `queued → 14:20` cell already uses.
func quotaReset(q *apiclient.AgentQuota) string {
	if q == nil {
		return ""
	}
	arrow := "≈ "
	if q.ResetsAtReported {
		arrow = "→ "
	}
	return arrow + q.ResetsAt.Local().Format("15:04")
}

// quotaBadge is the compact board-header form: `⏳14:20`, or "" for an adapter
// with no spent window right now. It deliberately drops the arrow — the header
// has one line for every adapter and the distinction it carries belongs where
// there is room to explain it, which is the daemon view.
func quotaBadge(q *apiclient.AgentQuota, now time.Time) string {
	if !q.SpentAt(now) {
		return ""
	}
	return quotaMark + q.ResetsAt.Local().Format("15:04")
}
