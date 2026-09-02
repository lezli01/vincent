package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// This file parses the `account/rateLimits/read` result into the vocabulary
// §9.6 already has (agent.ReportedQuota). It is deliberately separate from the
// transport in appserver.go: the shape is pinned to a captured response
// (testdata/app_server_ratelimits_0.150.1.json) and is table-tested against it
// with no subprocess anywhere near, which is the same split the JSONL stream
// parsing already uses.

// limitID is the entry this reads out of `rateLimitsByLimitId`. codex keys
// that map by product, and the only product vincent runs through this adapter
// is codex itself.
const limitID = "codex"

// rateLimitsResult is the result payload, narrowed to the two windows.
//
// `credits`, `planType`, `spendControlReached`, `individualLimit` and
// `rateLimitReachedType` are present on the wire and deliberately absent here
// — see appserver.go's header for why a plan tier is not a quota.
type rateLimitsResult struct {
	// ByLimitID is the current shape and the preferred one.
	ByLimitID map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
	// RateLimits is the older flat shape. 0.150.1 sends both, carrying
	// identical numbers; builds before it send only this one, which is why it
	// is a fallback rather than dead weight.
	RateLimits *rateLimitSnapshot `json:"rateLimits"`
}

type rateLimitSnapshot struct {
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
}

type rateLimitWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	// WindowDurationMins is how long the window is, in minutes: 300 for the
	// five-hour window, 10080 for the seven-day one.
	WindowDurationMins int `json:"windowDurationMins"`
	// ResetsAt is UNIX epoch **seconds**, not an RFC3339 string — 1788371363,
	// as captured. A reader that assumed a timestamp string would silently
	// decode nothing and report a window that never resets.
	ResetsAt int64 `json:"resetsAt"`
}

// parseRateLimits turns one rate-limits result into a reported reading.
//
// now is passed rather than read so the parse is a pure function of the
// captured bytes: ReportedAt is when we asked, and a test that could not fix
// it could not compare a whole reading at once.
func parseRateLimits(result json.RawMessage, now time.Time) (*agent.ReportedQuota, error) {
	var parsed rateLimitsResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, fmt.Errorf("parse codex rate limits: %w", err)
	}
	snap, ok := parsed.snapshot()
	if !ok {
		return nil, errors.New("codex rate limits carry no windows")
	}
	q := &agent.ReportedQuota{
		Source:     agent.QuotaSourceCodexAppServer,
		ReportedAt: now,
	}
	for _, w := range []struct {
		name string
		win  *rateLimitWindow
	}{
		{"primary", snap.Primary},
		{"secondary", snap.Secondary},
	} {
		if w.win == nil {
			// A build that names only one window reports one. An absent
			// window is not a window at zero.
			continue
		}
		q.Windows = append(q.Windows, agent.ReportedWindow{
			Name:        w.name,
			UsedPercent: w.win.UsedPercent,
			Window:      windowLabel(w.win.WindowDurationMins),
			ResetsAt:    resetTime(w.win.ResetsAt),
		})
	}
	if len(q.Windows) == 0 {
		return nil, errors.New("codex rate limits carry no windows")
	}
	return q, nil
}

// snapshot picks which of the two shapes to read: the keyed one when it holds
// our limit, the flat one otherwise. A snapshot naming neither window is
// treated as absent, so an empty `rateLimitsByLimitId.codex` still falls
// through to `rateLimits` rather than winning with nothing.
func (r rateLimitsResult) snapshot() (rateLimitSnapshot, bool) {
	if s, ok := r.ByLimitID[limitID]; ok && s.named() {
		return s, true
	}
	if r.RateLimits != nil && r.RateLimits.named() {
		return *r.RateLimits, true
	}
	return rateLimitSnapshot{}, false
}

func (s rateLimitSnapshot) named() bool { return s.Primary != nil || s.Secondary != nil }

// windowLabel renders a duration in minutes as the short human label the wire
// carries: the largest unit that divides it exactly, and minutes when none
// does. 300 → "5h", 10080 → "7d", 90 → "90m".
//
// Weeks are not a unit here even though 10080 is exactly one: "7d" is what the
// vendor's own UI says, and a label exists to be recognized, not to be minimal.
func windowLabel(mins int) string {
	switch {
	case mins <= 0:
		// The source named a window without saying how long it is, which
		// agent.ReportedWindow spells as an empty label.
		return ""
	case mins%(60*24) == 0:
		return fmt.Sprintf("%dd", mins/(60*24))
	case mins%60 == 0:
		return fmt.Sprintf("%dh", mins/60)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// resetTime converts epoch seconds to a UTC time, mapping a missing or
// nonsensical value to the zero time — which agent.ReportedWindow defines as
// "the source did not name one", a different statement from "it resets now".
func resetTime(epoch int64) time.Time {
	if epoch <= 0 {
		return time.Time{}
	}
	return time.Unix(epoch, 0).UTC()
}
