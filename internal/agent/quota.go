package agent

import (
	"context"
	"time"
)

// This file is the seam a *reported* quota reading arrives through (§9.6,
// task 082).
//
// Task 026 wrote the quota block observation-only and named its own expiry
// condition: `used_percent` and `window` "fill in the day a vendor ships a
// surface, at which point `source` changes from `observed`". Two of the three
// adapters now have one — codex answers an app-server request, claude reports
// through its status line — and this is that clause being taken up.
//
// QuotaReporter is deliberately **not** a method on Adapter. cursor has no
// quota surface at all (§9.7), and the standing rule is that a capability an
// adapter lacks is stated in §9.x and ignored at run time, never emulated: a
// method on Adapter would force cursor to grow a "cannot report" stub, which
// is exactly the interface change task 026 decision 1 refused. An optional
// interface says the same thing by being unsatisfied.

// Quota sources beyond `observed` (§9.6, task 082). `observed` itself is
// store.QuotaSourceObserved: it is a fact the *engine* wrote, not something an
// adapter reports, so it does not belong in this catalogue.
const (
	// QuotaSourceCodexAppServer is a reading codex's app-server answered on
	// request — a live probe, run by the catalog cache.
	QuotaSourceCodexAppServer = "codex_app_server"
	// QuotaSourceClaudeStatusLine is a reading claude *pushed*: the CLI runs
	// `vincent statusline` to render its status line and the payload it hands
	// that process carries the usage windows. Nothing here probes for it —
	// there is no request to make — so it arrives at CatalogCache.SetQuota
	// through `POST /v1/agents/{name}/quota` instead.
	QuotaSourceClaudeStatusLine = "claude_status_line"
)

// ReportedQuota is a usage reading something *reported*, as opposed to a
// window vincent watched close (store.AgentQuota). The difference is not
// cosmetic: an observation is evidence of a wall already hit, while a reading
// is a percentage of a window still open, and only the second can say how much
// is left before anything stops.
type ReportedQuota struct {
	// Source names who said so — one of the constants above. It reaches the
	// wire as the quota block's `source`, replacing `observed`.
	Source string
	// ReportedAt is when the reading was taken, not when the window opened.
	ReportedAt time.Time
	// Windows is every window the source named. A source that tracks two
	// (codex's primary/secondary, claude's five-hour/seven-day) reports both;
	// the wire carries all of them and the block's scalars carry the tightest.
	Windows []ReportedWindow
}

// ReportedWindow is one usage window inside a reading.
type ReportedWindow struct {
	// Name is the source's own name for the window: "primary"/"secondary"
	// for codex, "five_hour"/"seven_day" for claude. It is passed through
	// rather than normalized — two vendors' windows are not the same thing,
	// and inventing a shared vocabulary for them would claim they are.
	Name        string
	UsedPercent float64
	// Window is the human label — "5h", "7d". Empty when the source named a
	// window without saying how long it is.
	Window string
	// ResetsAt is when the window reopens. The zero value means the source
	// did not name one, which is a different statement from "it never
	// resets" and must not render as a time.
	ResetsAt time.Time
}

// sameReading reports whether two readings say the same thing to a client.
//
// ReportedAt is deliberately excluded. A status line re-renders on every
// prompt, so a source can push an identical reading many times a minute; what
// changed then is when we were told, not what we were told. This is the rule
// store.UpsertAgentQuota already applies to an unchanged observation — "the
// row's observed_at moves but nothing a client renders does, so no event" —
// and it is what keeps the push route from waking every subscriber on the wire
// with news they already have.
func (q *ReportedQuota) sameReading(other *ReportedQuota) bool {
	switch {
	case q == nil || other == nil:
		return q == other
	case q.Source != other.Source || len(q.Windows) != len(other.Windows):
		return false
	}
	for i, w := range q.Windows {
		o := other.Windows[i]
		if w.Name != o.Name || w.UsedPercent != o.UsedPercent ||
			w.Window != o.Window || !w.ResetsAt.Equal(o.ResetsAt) {
			return false
		}
	}
	return true
}

// QuotaReporter is the optional capability an adapter may satisfy: a CLI that
// can be asked how much of its usage window is left, without a real run
// (§9.6, task 082).
//
// An adapter that cannot is simply not one — there is no "unsupported"
// return, because there is no call to make. The catalog cache type-asserts for
// it and asks nobody else, so cursor costs no subprocess and reports nothing.
//
// Failure is silent by contract: an error, a timeout or a garbage answer
// degrades to the observation-only behaviour task 026 shipped, and fails no
// probe. Returning (nil, nil) is the same as an error without the noise — "I
// have nothing to say right now" — and leaves the previous reading standing.
type QuotaReporter interface {
	Quota(ctx context.Context) (*ReportedQuota, error)
}
