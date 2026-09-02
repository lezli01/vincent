package api

import (
	"context"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
)

// quotaResponse is one adapter's usage-window block, carried by both
// `GET /v1/agents` and `GET /v1/info` (§9.6, task 026; amended by task 082).
//
// It rides both because the board header reads /v1/info while the new-task
// and repair forms read /v1/agents: a client should not need a second fetch to
// render a badge, and two endpoints describing the same adapter must not be
// able to disagree about it.
//
// `null` — not a zeroed block — is what an adapter with nothing observed and
// nothing reported says. A zero here would read as "empty quota", which is the
// opposite of what it means.
type quotaResponse struct {
	// Spent is derived per request, and *how* depends on the source. An
	// observation is spent while `now < resets_at`: a lapsed reset does not
	// delete the row, so `spent: false` with the observation intact is how
	// "this window closed at 14:20 and has since reopened" is said.
	//
	// A reported reading is spent at `used_percent >= 100`, and the
	// difference is not cosmetic. A reported window's `resets_at` is always
	// in the *future* — that is what a window still open means — so the
	// observed derivation would answer true for every reading vincent ever
	// receives and light the board's badge permanently for everyone running
	// codex (task 082).
	Spent bool `json:"spent"`
	// UsedPercent and Window shipped as permanent nulls under task 026 and no
	// longer are: they carry the *tightest* window of a reported reading —
	// highest `used_percent`, ties broken by the earliest reset. They stay
	// null for an observation, which measures nothing.
	//
	// The scalars are filled rather than repurposed (task 082 decision 3): a
	// client written against task 026's shape keeps working unchanged and
	// gets the one number that matters, while a client that wants the whole
	// reading reads Windows.
	UsedPercent *float64 `json:"used_percent"`
	Window      *string  `json:"window"`
	// ObservedAt is when the daemon learned this: the moment of the
	// `usage_limit` stop for an observation, the moment of the reading for a
	// report.
	ObservedAt string `json:"observed_at"`
	ResetsAt   string `json:"resets_at"`
	// ResetsAtReported is the difference between a fact and an estimate:
	// true when the CLI named the reset, false when
	// `usage_limit_recheck_interval` supplied it (§12.3). A reported window
	// that named no reset is false with a zero `resets_at`, which is the same
	// statement — do not render it as a time.
	ResetsAtReported bool `json:"resets_at_reported"`
	// Source is `observed` for a window this daemon watched close, or the
	// reporting source for a reading (`codex_app_server`,
	// `claude_status_line`). It is the seam task 026 left for exactly this,
	// and it is what tells a client which `spent` derivation it is looking at.
	Source string `json:"source"`
	// Windows is every window the source named. Always an array, never null —
	// the same normalization handleAgents applies to `models` and `efforts`,
	// so a client can range over it without a nil check. Empty for an
	// observation, which knows of no windows at all.
	Windows []quotaWindow `json:"windows"`
}

// quotaWindow is one usage window inside a reported reading (task 082).
type quotaWindow struct {
	// Name is the source's own name for the window — "primary"/"secondary"
	// for codex, "five_hour"/"seven_day" for claude. It is passed through
	// rather than normalized: two vendors' windows are not the same thing.
	Name        string  `json:"name"`
	UsedPercent float64 `json:"used_percent"`
	// Window is the human label ("5h", "7d"), empty when the source named
	// none.
	Window string `json:"window"`
	// ResetsAt is the zero time when the source did not name one, which
	// ResetsAtReported reports as false.
	ResetsAt         time.Time `json:"resets_at"`
	ResetsAtReported bool      `json:"resets_at_reported"`
}

// catalogEntry pairs an adapter name with the cache entry a handler read for
// it, so /v1/agents and /v1/info can walk the catalog *once* and hand that one
// read to agentQuotas. The two endpoints must not be able to disagree about an
// adapter (§9.6), and a second Entry call under `?refresh=true` would not just
// disagree — it would probe the adapter twice.
type catalogEntry struct {
	name  string
	entry agent.CatalogEntry
}

// catalogEntries reads every adapter's entry once. refresh forces a re-probe,
// which also forces the reported-quota reading to be taken again.
func (s *Server) catalogEntries(ctx context.Context, refresh bool) []catalogEntry {
	if s.deps.Catalog == nil {
		return nil
	}
	names := s.deps.Catalog.Names()
	out := make([]catalogEntry, 0, len(names))
	for _, name := range names {
		e, ok := s.deps.Catalog.Entry(ctx, name, refresh)
		if !ok {
			continue
		}
		out = append(out, catalogEntry{name: name, entry: e})
	}
	return out
}

// agentQuotas merges the two things the daemon can know about an adapter's
// usage into the one block both endpoints serve, keyed by adapter.
//
// The reported reading wins where there is one: it measures a window still
// open, while an observation only records a wall already hit. The observation
// is the fallback — no reading, or a reading whose windows have all reopened
// since — because a stale percentage is worse than an honest older fact.
//
// A failed store read degrades to "nothing observed" with a log line, for the
// same reason the /v1/info orphan count does: a display fact must not take the
// endpoint every client polls down with it. A nil Store (tests without
// persistence) answers the same way, and a reported reading still lands.
func (s *Server) agentQuotas(ctx context.Context, entries []catalogEntry) map[string]*quotaResponse {
	now := time.Now()
	out := make(map[string]*quotaResponse, len(entries))
	if s.deps.Store != nil {
		rows, err := s.deps.Store.ListAgentQuota(ctx)
		if err != nil {
			s.deps.Logger.Warn("list agent quota", "error", err)
		}
		for _, q := range rows {
			out[q.Agent] = newQuotaResponse(q, now)
		}
	}
	for _, e := range entries {
		if r := reportedQuotaResponse(e.entry.Quota, now); r != nil {
			out[e.name] = r
		}
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
		Windows:          []quotaWindow{},
	}
}

// reportedQuotaResponse renders a reported reading, or nil when there is
// nothing to render and the observation should stand: no reading, a reading
// naming no windows, or one whose windows have all elapsed.
func reportedQuotaResponse(q *agent.ReportedQuota, now time.Time) *quotaResponse {
	if q == nil || len(q.Windows) == 0 || elapsedQuota(q, now) {
		return nil
	}
	t := tightestWindow(q.Windows)
	used := t.UsedPercent
	resp := &quotaResponse{
		// The `used_percent >= 100` split, not `now < resets_at` — see Spent.
		Spent:       used >= 100,
		UsedPercent: &used,
		ObservedAt:  q.ReportedAt.UTC().Format(time.RFC3339),
		// A window with no named reset formats as the zero time rather than
		// as an empty string: `resets_at` has been an RFC3339 string since
		// task 026 and a client parsing it must not be handed something that
		// is not one. `resets_at_reported` is what says it means nothing.
		ResetsAt:         t.ResetsAt.UTC().Format(time.RFC3339),
		ResetsAtReported: !t.ResetsAt.IsZero(),
		Source:           q.Source,
		Windows:          make([]quotaWindow, 0, len(q.Windows)),
	}
	if t.Window != "" {
		label := t.Window
		resp.Window = &label
	}
	for _, w := range q.Windows {
		resp.Windows = append(resp.Windows, quotaWindow{
			Name:             w.Name,
			UsedPercent:      w.UsedPercent,
			Window:           w.Window,
			ResetsAt:         w.ResetsAt.UTC(),
			ResetsAtReported: !w.ResetsAt.IsZero(),
		})
	}
	return resp
}

// elapsedQuota reports a reading every dated window of which has since
// reopened, which is when the older observation is the better answer.
//
// Windows that named no reset are skipped, and a reading made entirely of
// those never elapses: nothing in it says when it stops being true, so the
// only alternative would be inventing an expiry the source did not state.
// quotaTTL is what bounds those, by asking again.
func elapsedQuota(q *agent.ReportedQuota, now time.Time) bool {
	dated := 0
	for _, w := range q.Windows {
		if w.ResetsAt.IsZero() {
			continue
		}
		dated++
		if now.Before(w.ResetsAt) {
			return false
		}
	}
	return dated > 0
}

// tightestWindow picks the window the block's scalars carry: the one closest
// to stopping work. Highest `used_percent` wins, and a tie goes to whichever
// reopens soonest — a window that named no reset cannot claim to be sooner,
// because the zero time is "unknown", not "the year 1".
func tightestWindow(ws []agent.ReportedWindow) agent.ReportedWindow {
	best := ws[0]
	for _, w := range ws[1:] {
		switch {
		case w.UsedPercent > best.UsedPercent:
			best = w
		case w.UsedPercent < best.UsedPercent, w.ResetsAt.IsZero():
		case best.ResetsAt.IsZero(), w.ResetsAt.Before(best.ResetsAt):
			best = w
		}
	}
	return best
}
