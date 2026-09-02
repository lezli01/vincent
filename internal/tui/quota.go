package tui

import (
	"math"
	"strconv"
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
//
// A reported block may carry no reset at all — a source that named a spent
// window and no time for it — and that answers empty rather than the zero
// clock: `≈ 00:00` is a time, and one nobody is waiting for (task 082).
func quotaReset(q *apiclient.AgentQuota) string {
	if q == nil || q.ResetsAt.IsZero() {
		return ""
	}
	arrow := "≈ "
	if q.ResetsAtReported {
		arrow = "→ "
	}
	return arrow + q.ResetsAt.Local().Format("15:04")
}

// quotaWindowReset is quotaReset for one window of a reported reading
// (task 082). It carries the same distinction for the same reason — `→ 14:20`
// is a time the source stated, `≈ 14:20` is one vincent worked out — and adds
// the third case a window has and the block's scalars do not: a source that
// named a window and no reset at all, which renders as no time rather than as
// the zero clock.
func quotaWindowReset(w apiclient.AgentQuotaWindow) string {
	if w.ResetsAt.IsZero() {
		return ""
	}
	arrow := "≈ "
	if w.ResetsAtReported {
		arrow = "→ "
	}
	return arrow + w.ResetsAt.Local().Format("15:04")
}

// quotaWindowLabel is what to call a window on screen. The source's own human
// label wins — "5h" is what codex and claude both say — and the wire name
// ("five_hour") is the fallback for a source that named none, because a
// percentage attached to nothing is not a reading anybody can act on.
func quotaWindowLabel(w apiclient.AgentQuotaWindow) string {
	if w.Window != "" {
		return w.Window
	}
	return w.Name
}

// quotaPercent renders a used-percentage at one decimal, trimmed. A reading
// of 28 is "28%" and not "28.0%"; one of 28.47 is "28.5%" rather than a
// precision the source did not really have.
func quotaPercent(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + "%"
}

// quotaSourceLabel turns a wire source into prose. The constants are
// snake_case identifiers meant for a JSON body, and "quota codex_app_server"
// reads as a leaked field name on a line a human is meant to read. An
// unrecognised source prints as it arrived: inventing prose for a source this
// build has never heard of would hide it.
func quotaSourceLabel(source string) string {
	switch source {
	case apiclient.QuotaSourceCodexAppServer:
		return "codex app-server"
	case apiclient.QuotaSourceClaudeStatusLine:
		return "claude status line"
	default:
		return source
	}
}

// quotaBadge is the compact board-header form: `⏳14:20`, or "" for an adapter
// with no spent window right now. It deliberately drops the arrow — the header
// has one line for every adapter and the distinction it carries belongs where
// there is room to explain it, which is the daemon view.
func quotaBadge(q *apiclient.AgentQuota, now time.Time) string {
	if !q.SpentAt(now) {
		return ""
	}
	if q.ResetsAt.IsZero() {
		// A reported window that is spent and named no reset: the glyph is
		// the whole fact, and `⏳00:00` would be a time vincent made up.
		return quotaMark
	}
	return quotaMark + q.ResetsAt.Local().Format("15:04")
}
