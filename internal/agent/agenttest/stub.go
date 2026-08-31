package agenttest

import (
	"context"
	"errors"

	"github.com/lezli01/vincent/internal/agent"
)

// NonResumingName is the adapter name StubNonResuming registers under. It is
// deliberately not the name of any shipped CLI: the point of the stub is that
// the refusal outlives the three adapters that happen to exist today.
const NonResumingName = "stubnoresume"

// StubNonResuming is an adapter that cannot resume a prior session, and does
// nothing else at all.
//
// It exists because the `agent_cannot_resume` refusal (§5.5, task 063
// decision 3) has to keep being proven after task 070 taught codex and cursor
// to resume, and nothing shipped answers false any more. Pointing those tests
// at a shipped adapter is what made them assert the opposite of the truth the
// day the capability landed; pointing them here is what stops that recurring
// for the next adapter.
//
// It is a stub rather than a fakeagent dialect on purpose: `SupportsResume()
// == false` is the entire behaviour under test, and no subprocess can make
// that statement any truer. Nothing registers it in the daemon's real
// registry — shipping a fake adapter in production would cut against the same
// §9.1 property this test is defending.
type StubNonResuming struct{}

var errStub = errors.New("stub adapter: never started")

// Name implements agent.Adapter.
func (StubNonResuming) Name() string { return NonResumingName }

// Detect implements agent.Adapter: present enough to be listed, so a test can
// reach the refusal rather than an "agent not found".
func (StubNonResuming) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: NonResumingName, Version: "0"}, nil
}

// Options implements agent.Adapter: nothing to select.
func (StubNonResuming) Options(context.Context) (agent.Options, error) {
	return agent.Options{}, nil
}

// Path implements agent.Adapter.
func (StubNonResuming) Path() (string, error) { return NonResumingName, nil }

// Curated implements agent.Adapter.
func (StubNonResuming) Curated() agent.Options { return agent.Options{} }

// NewLineParser implements agent.Adapter: every line is unknown, since this
// adapter never writes one.
func (StubNonResuming) NewLineParser() agent.LineParser {
	return func(raw []byte) agent.Event {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
}

// Start implements agent.Adapter, in the negative: a chat on this adapter is
// refused before anything is started, so reaching here is the bug.
func (StubNonResuming) Start(context.Context, agent.RunSpec) (agent.RunHandle, error) {
	return nil, errStub
}

// SupportsResume implements agent.Resumer, in the negative — the one thing
// this type is for.
func (StubNonResuming) SupportsResume() bool { return false }
