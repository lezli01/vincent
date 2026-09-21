package agenttest

import (
	"context"

	"github.com/lezli01/vincent/internal/agent"
)

// The file-mention stub (task 126 decision 25). It is separate from
// StubNoSkills for the reason StubNoSkills is separate from StubNonResuming
// (task 124 decision 15): a stub carrying refusals from two unrelated
// capability families blurs which refusal a failing test was proving, and
// StubNoSkills' own comment — "implements neither ... and does nothing else
// at all" — stays true only while it does nothing else.
//
// As with the others, nothing registers it in the daemon's real registry.

// NoMentionsName is the adapter name StubNoMentions registers under. It is
// deliberately not the name of any shipped CLI: the refusal must outlive
// whichever adapters happen to lack the capability today.
const NoMentionsName = "stubnomentions"

// StubNoMentions is an adapter that does not implement agent.FileMentioner,
// and does nothing else at all (§9.1, task 126).
//
// It is what proves CanMentionFiles false. All three shipped adapters
// implement the capability today, and a refusal pinned to a shipped CLI
// inverts itself the day that CLI changes — the lesson recorded on
// agent.Resumer.
type StubNoMentions struct{ inert }

// Name implements agent.Adapter.
func (StubNoMentions) Name() string { return NoMentionsName }

// Detect implements agent.Adapter: present enough to be listed, so a test
// reaches the capability rather than an "agent not found".
func (StubNoMentions) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: NoMentionsName, Version: "0"}, nil
}

// Path implements agent.Adapter.
func (StubNoMentions) Path() (string, error) { return NoMentionsName, nil }
