package chatrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// ptrEq reports whether p holds want, with nil meaning "not reported" and
// matching only want < 0.
func ptrEq(p *int64, want int64) bool {
	if want < 0 {
		return p == nil
	}
	return p != nil && *p == want
}

func fmtPtr(p *int64) any {
	if p == nil {
		return "nil"
	}
	return *p
}

// checkContext compares a turn's four task 156 columns; -1 is "nil".
func checkContext(t *testing.T, i int, turn *store.ChatTurn, cr, cw, ctx, win int64) {
	t.Helper()
	if !ptrEq(turn.CacheReadTokens, cr) || !ptrEq(turn.CacheCreationTokens, cw) ||
		!ptrEq(turn.ContextTokens, ctx) || !ptrEq(turn.ContextWindow, win) {
		t.Errorf("turn %d cache/context = %v/%v %v/%v, want %d/%d %d/%d", i+1,
			fmtPtr(turn.CacheReadTokens), fmtPtr(turn.CacheCreationTokens),
			fmtPtr(turn.ContextTokens), fmtPtr(turn.ContextWindow), cr, cw, ctx, win)
	}
}

// TestTurnRecordsContextFigures: a per-run adapter's cache counts and
// context figures land on the turn as reported, and a figure the run did not
// report stays nil rather than becoming zero (task 156).
func TestTurnRecordsContextFigures(t *testing.T) {
	h := newHarness(t)
	h.perRun.setResults(
		agent.RunResult{
			SessionID: "s1", InputTokens: 10, OutputTokens: 5, CacheReadTokens: 900,
			CacheCreationTokens: 90, ContextTokens: 1000, ContextWindow: 200000, ResultText: "ok",
		},
		agent.RunResult{SessionID: "s1", InputTokens: 10, OutputTokens: 5, ResultText: "ok"},
	)
	c := h.chatOn(t, perRunAgentName)
	want := [][4]int64{{900, 90, 1000, 200000}, {-1, -1, -1, -1}}
	for i, w := range want {
		turn := h.sendAndWait(t, c.ID, "turn")
		h.waitIdle(t, c.ID)
		if turn.State != chatstate.TurnDone {
			t.Fatalf("turn %d = %s (%s)", i+1, turn.State, turn.ErrorMessage)
		}
		checkContext(t, i, turn, w[0], w[1], w[2], w[3])
	}
}

// TestCacheInInputRecordsNoCache: an adapter whose input already counts its
// cache (codex) records no cache counts, so the heading cannot count the
// cache twice.
func TestCacheInInputRecordsNoCache(t *testing.T) {
	h := newHarness(t)
	h.perRun.cacheInInput = true
	h.perRun.setResults(agent.RunResult{
		SessionID: "s1", InputTokens: 1000, OutputTokens: 5,
		CacheReadTokens: 900, ResultText: "ok",
	})
	c := h.chatOn(t, perRunAgentName)
	turn := h.sendAndWait(t, c.ID, "turn")
	h.waitIdle(t, c.ID)
	checkContext(t, 0, turn, -1, -1, -1, -1)
	if turn.InputTokens != 1000 {
		t.Errorf("input = %d, want the run's 1000", turn.InputTokens)
	}
}

// TestSessionCacheTotalsBecomeTurnShares is task 156 decision 3 under task
// 138.11's rules: on a session-totals adapter the cache counts take the same
// share subtraction as the input and output — raw on the first turn and on a
// new session, as they are after a reset, and measured from the last turn
// that reported.
func TestSessionCacheTotalsBecomeTurnShares(t *testing.T) {
	h := newHarness(t)
	run := func(session string, in, out, cr, cw int64) agent.RunResult {
		return agent.RunResult{
			SessionID: session, InputTokens: in, OutputTokens: out,
			CacheReadTokens: cr, CacheCreationTokens: cw, ResultText: "ok",
		}
	}
	h.totals.setResults(
		run("s1", 100, 40, 1000, 10), // first turn: raw is the turn's
		run("s1", 250, 90, 3000, 30), // resumed: its own share
		run("s1", 30, 10, 500, 5),    // reset: raw rather than negative
		run("s2", 500, 200, 800, 8),  // a fresh session
		run("s2", 0, 0, 0, 0),        // no usage: nothing recorded
		run("s2", 600, 260, 950, 9),  // measured from turn 4
	)
	c := h.chatOn(t, totalsAgentName)
	want := [][2]int64{{1000, 10}, {2000, 20}, {500, 5}, {800, 8}, {-1, -1}, {150, 1}}
	for i, w := range want {
		turn := h.sendAndWait(t, c.ID, "turn")
		h.waitIdle(t, c.ID)
		if turn.State != chatstate.TurnDone {
			t.Fatalf("turn %d = %s (%s)", i+1, turn.State, turn.ErrorMessage)
		}
		checkContext(t, i, turn, w[0], w[1], -1, -1)
	}
}
