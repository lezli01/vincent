package chatrun

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
)

// totalsAgentName and perRunAgentName are the two halves of totalsAdapter:
// one reports session totals (copilot's shape, agent.CumulativeUsage), the
// other per-run figures (claude's, codex's and cursor's).
const (
	totalsAgentName = "totalsstub"
	perRunAgentName = "perrunstub"
)

// totalsRun is what one scripted run reports.
type totalsRun struct {
	session string
	in, out int64
}

// totalsAdapter answers each Start with the next scripted RunResult and no
// events, so a test states the figures a turn's agent reported and reads the
// row chatrun wrote from them.
type totalsAdapter struct {
	name       string
	cumulative bool

	mu     sync.Mutex
	script []totalsRun
}

func (a *totalsAdapter) Name() string { return a.name }
func (a *totalsAdapter) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: a.name, Version: "0"}, nil
}

func (a *totalsAdapter) Options(context.Context) (agent.Options, error) { return agent.Options{}, nil }
func (a *totalsAdapter) Path() (string, error)                          { return a.name, nil }
func (a *totalsAdapter) Curated() agent.Options                         { return agent.Options{} }
func (a *totalsAdapter) NewLineParser() agent.LineParser {
	return func(raw []byte) agent.Event { return agent.Event{Type: agent.EventOutput, Raw: raw} }
}
func (a *totalsAdapter) SupportsResume() bool      { return true }
func (a *totalsAdapter) UsageIsSessionTotal() bool { return a.cumulative }

func (a *totalsAdapter) set(script ...totalsRun) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.script = script
}

func (a *totalsAdapter) Start(context.Context, agent.RunSpec) (agent.RunHandle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.script) == 0 {
		return nil, errors.New("totals stub: no scripted run left")
	}
	next := a.script[0]
	a.script = a.script[1:]
	ch := make(chan agent.Event)
	close(ch)
	return &totalsHandle{events: ch, res: agent.RunResult{
		SessionID: next.session, InputTokens: next.in, OutputTokens: next.out, ResultText: "ok",
	}}, nil
}

type totalsHandle struct {
	events chan agent.Event
	res    agent.RunResult
}

func (h *totalsHandle) Events() <-chan agent.Event        { return h.events }
func (h *totalsHandle) Wait() (agent.RunResult, error)    { return h.res, nil }
func (h *totalsHandle) Respond(agent.InputResponse) error { return errors.New("non-interactive") }
func (h *totalsHandle) Terminate() error                  { return nil }
func (h *totalsHandle) Kill() error                       { return nil }
func (h *totalsHandle) PID() int                          { return 0 }
func (h *totalsHandle) Argv() []string                    { return []string{totalsAgentName} }

// TestSessionTotalsBecomeTurnShares is task 138.11 decision 1: on an adapter
// reporting session totals, a turn records its own spend — the totals minus
// the same session's previous recorded totals — while the raw totals are kept
// beside it. A new session starts from its raw figures, totals that shrank
// are taken as they are, and a turn that reported nothing is skipped over.
func TestSessionTotalsBecomeTurnShares(t *testing.T) {
	h := newHarness(t)
	h.totals.set(
		totalsRun{"s1", 100, 40},  // first turn: raw is the turn's
		totalsRun{"s1", 250, 90},  // resumed: 150/50 of its own
		totalsRun{"s1", 30, 10},   // shrank: reset, raw rather than negative
		totalsRun{"s2", 500, 200}, // a fresh session after session_lost
		totalsRun{"s2", 0, 0},     // no usage line: no totals recorded
		totalsRun{"s2", 600, 260}, // measured from turn 4, not turn 5
	)
	c := h.chatOn(t, totalsAgentName)
	want := []struct {
		in, out       int64
		rawIn, rawOut int64
		raw           bool
	}{
		{100, 40, 100, 40, true},
		{150, 50, 250, 90, true},
		{30, 10, 30, 10, true},
		{500, 200, 500, 200, true},
		{0, 0, 0, 0, false},
		{100, 60, 600, 260, true},
	}
	for i, w := range want {
		turn := h.sendAndWait(t, c.ID, "turn")
		h.waitIdle(t, c.ID)
		if turn.State != chatstate.TurnDone {
			t.Fatalf("turn %d = %s (%s)", i+1, turn.State, turn.ErrorMessage)
		}
		if turn.InputTokens != w.in || turn.OutputTokens != w.out {
			t.Errorf("turn %d tokens = %d/%d, want %d/%d", i+1, turn.InputTokens, turn.OutputTokens, w.in, w.out)
		}
		switch {
		case !w.raw && (turn.SessionInputTokens != nil || turn.SessionOutputTokens != nil):
			t.Errorf("turn %d recorded session totals for a run that reported none", i+1)
		case w.raw && (turn.SessionInputTokens == nil || turn.SessionOutputTokens == nil):
			t.Errorf("turn %d recorded no session totals", i+1)
		case w.raw && (*turn.SessionInputTokens != w.rawIn || *turn.SessionOutputTokens != w.rawOut):
			t.Errorf("turn %d session totals = %d/%d, want %d/%d", i+1,
				*turn.SessionInputTokens, *turn.SessionOutputTokens, w.rawIn, w.rawOut)
		}
	}
}

// TestPerRunUsageIsUntouched: an adapter that is not agent.CumulativeUsage
// keeps reporting exactly what its run reported, with no totals stored.
func TestPerRunUsageIsUntouched(t *testing.T) {
	h := newHarness(t)
	h.perRun.set(totalsRun{"s1", 100, 40}, totalsRun{"s1", 250, 90})
	c := h.chatOn(t, perRunAgentName)
	for i, w := range [][2]int64{{100, 40}, {250, 90}} {
		turn := h.sendAndWait(t, c.ID, "turn")
		h.waitIdle(t, c.ID)
		if turn.InputTokens != w[0] || turn.OutputTokens != w[1] {
			t.Errorf("turn %d tokens = %d/%d, want %d/%d", i+1, turn.InputTokens, turn.OutputTokens, w[0], w[1])
		}
		if turn.SessionInputTokens != nil || turn.SessionOutputTokens != nil {
			t.Errorf("turn %d stored session totals for a per-run adapter", i+1)
		}
	}
}
