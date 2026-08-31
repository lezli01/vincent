package chatrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
)

// floodAgentName is the adapter newHarness registers alongside the three real
// ones for this test. It is a stub rather than a fakeagent dialect because
// what is under test is a handover between goroutines, not an argv: no
// subprocess can make a blocked channel send any more blocked than this one.
const floodAgentName = "floodstub"

// floodEvents is more events than the runner can want. The cap is reached on
// the first, so every one after it is a send nobody has asked for — which is
// the only condition under which the deadlock this file guards can happen.
const floodEvents = 8

// floodSendTimeout bounds an abandoned handover. Without it a regression is a
// hung package (`panic: test timed out after 10m0s`, and a goroutine dump to
// read); with it the run gives up, the turn finishes, and the assertion below
// says in one line what went wrong.
const floodSendTimeout = 10 * time.Second

// floodAdapter is an agent whose stream outlives the consumer's interest in
// it: it hands events over on an *unbuffered* channel and its Wait does not
// return until the goroutine doing the handing over is finished.
//
// That is the shape of every shipped adapter — internal/agent/claude's
// readLoop feeds a mux which feeds the events channel, and Wait blocks on
// readerDone — with the buffers removed. The buffers are the only reason the
// real adapters hid this: 128 events fit between the CLI and the loop, so a
// consumer that stopped reading deadlocked only when the agent had got that
// far ahead before the kill landed.
type floodAdapter struct {
	mu  sync.Mutex
	run *floodRun
}

// Name implements agent.Adapter.
func (a *floodAdapter) Name() string { return floodAgentName }

// Detect implements agent.Adapter.
func (a *floodAdapter) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: floodAgentName, Version: "0"}, nil
}

// Options implements agent.Adapter.
func (a *floodAdapter) Options(context.Context) (agent.Options, error) {
	return agent.Options{}, nil
}

// Path implements agent.Adapter.
func (a *floodAdapter) Path() (string, error) { return floodAgentName, nil }

// Curated implements agent.Adapter.
func (a *floodAdapter) Curated() agent.Options { return agent.Options{} }

// NewLineParser implements agent.Adapter.
func (a *floodAdapter) NewLineParser() agent.LineParser {
	return func(raw []byte) agent.Event { return agent.Event{Type: agent.EventOutput, Raw: raw} }
}

// SupportsResume implements agent.Resumer, so a chat on this adapter is not
// refused at creation before it can reach the loop.
func (a *floodAdapter) SupportsResume() bool { return true }

// Start implements agent.Adapter.
func (a *floodAdapter) Start(context.Context, agent.RunSpec) (agent.RunHandle, error) {
	r := &floodRun{events: make(chan agent.Event), done: make(chan struct{})}
	a.mu.Lock()
	a.run = r
	a.mu.Unlock()
	go r.emit()
	return r, nil
}

// started reports the run Start handed out, or nil if nothing ran.
func (a *floodAdapter) started() *floodRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.run
}

// floodRun is one run of floodAdapter.
type floodRun struct {
	events  chan agent.Event
	done    chan struct{}
	mu      sync.Mutex
	drained bool
}

func (r *floodRun) emit() {
	defer close(r.done)
	defer close(r.events)
	// Each line is over the 256-byte cap on its own, so the runner reaches
	// the cap on the first event and stops wanting the rest.
	line := fmt.Sprintf(`{"type":"assistant","text":%q}`, strings.Repeat("flooding ", 40))
	for range floodEvents {
		select {
		case r.events <- agent.Event{Type: agent.EventOutput, Text: line, Raw: []byte(line)}:
		case <-time.After(floodSendTimeout):
			// Nobody is reading. The real adapters have no such escape and
			// would sit here for as long as the daemon lives.
			return
		}
	}
	r.mu.Lock()
	r.drained = true
	r.mu.Unlock()
}

// wasDrained reports whether every event was taken before the stream closed.
func (r *floodRun) wasDrained() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drained
}

// Events implements agent.RunHandle.
func (r *floodRun) Events() <-chan agent.Event { return r.events }

// Wait implements agent.RunHandle: it blocks until the goroutine that owns
// the stream is finished, which is what makes an undrained stream a hang
// rather than a lost line.
func (r *floodRun) Wait() (agent.RunResult, error) {
	<-r.done
	return agent.RunResult{ExitCode: 0, SessionID: "flood-1"}, nil
}

// Respond implements agent.RunHandle.
func (r *floodRun) Respond(agent.InputResponse) error {
	return errors.New("the flood stub is non-interactive")
}

// Terminate implements agent.RunHandle.
func (r *floodRun) Terminate() error { return nil }

// Kill implements agent.RunHandle.
func (r *floodRun) Kill() error { return nil }

// PID implements agent.RunHandle.
func (r *floodRun) PID() int { return 0 }

// Argv implements agent.RunHandle.
func (r *floodRun) Argv() []string { return []string{floodAgentName} }

// TestTurnDrainsTheStreamItAbandons is the regression test for a hang, not a
// wrong answer. Every early exit from consume — cancel, either clock, the
// transcript cap — leaves an agent that is still talking, and the adapter's
// Wait does not return until that agent's reader goroutine is finished. A
// consumer that simply returned parked the reader on a channel nobody empties
// and runTurn on a Wait that never returns: the turn stayed non-terminal, the
// chat never came back to idle, and its §11 slot was held until the daemon
// restarted. Runner.Stop then blocked too, so a shutdown hung with it.
//
// It is asserted through the transcript cap because that is the exit reached
// soonest and most deterministically; all four share drain.
func TestTurnDrainsTheStreamItAbandons(t *testing.T) {
	h := newHarness(t)
	h.cfg.TranscriptMaxBytes = 256
	c := h.chatOn(t, floodAgentName)

	turn := h.sendAndWait(t, c.ID, "say a lot")
	if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonTranscriptLimit {
		t.Fatalf("turn = %s/%s, want failed/%s", turn.State, turn.FailReason, ReasonTranscriptLimit)
	}
	run := h.flood.started()
	if run == nil {
		t.Fatal("the flood adapter was never started")
	}
	if !run.wasDrained() {
		t.Fatal("the abandoned stream was never drained: a real adapter's reader would still be " +
			"blocked handing over an event, and its Wait with it")
	}
	// And the chat is usable again, which is the half a human notices.
	h.waitIdle(t, c.ID)
}
