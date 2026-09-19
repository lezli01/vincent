package agenttest

import (
	"context"
	"slices"
	"sync"

	"github.com/lezli01/vincent/internal/agent"
)

// The two skill stubs (task 124 decision 15). They are separate from
// StubNonResuming on purpose: that stub's whole behaviour under test is
// `SupportsResume() == false`, and hanging a second refusal on it would blur
// which refusal a test was proving.
//
// As with StubNonResuming, nothing registers either in the daemon's real
// registry — shipping a fake adapter in production would cut against the
// §9.1 property these stubs exist to defend.

// NoSkillsName is the adapter name StubNoSkills registers under. It is
// deliberately not the name of any shipped CLI: the refusal must outlive
// whichever adapters happen to lack the capability today.
const NoSkillsName = "stubnoskills"

// StubNoSkills is an adapter that implements neither agent.SkillLister nor
// agent.SkillInvoker, and does nothing else at all (§9.1, task 124).
//
// It is what proves CanListSkills and CanInvokeSkills false. cursor cannot
// list today (§9.7) and would answer the same, but a refusal pinned to a
// shipped CLI inverts itself the day that CLI gains the capability — the
// lesson recorded on agent.Resumer.
type StubNoSkills struct{ inert }

// Name implements agent.Adapter.
func (StubNoSkills) Name() string { return NoSkillsName }

// Detect implements agent.Adapter: present enough to be listed, so a test
// reaches the capability rather than an "agent not found".
func (StubNoSkills) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: NoSkillsName, Version: "0"}, nil
}

// Path implements agent.Adapter.
func (StubNoSkills) Path() (string, error) { return NoSkillsName, nil }

// SkillsName is the adapter name StubSkills registers under.
const SkillsName = "stubskills"

// StubSkills is an adapter that lists skills and invokes them (§9.1, task
// 124): ListSkills answers whatever Script last set, and SkillSyntax answers
// Syntax. It is the one stub a listing cache and a client inserting an
// invocation can both be tested against.
//
// It records every SkillQuery it receives and counts its calls, both safe
// under concurrent ListSkills, so a test can assert which directory was asked
// about and how often a cache went to the CLI.
type StubSkills struct {
	inert

	// Syntax is what SkillSyntax answers, and the sigil Invocation prefixes.
	// Set it before the stub is shared; it is not guarded.
	Syntax agent.SkillSyntax

	mu      sync.Mutex
	list    agent.SkillList
	err     error
	queries []agent.SkillQuery
}

var (
	_ agent.SkillLister  = (*StubSkills)(nil)
	_ agent.SkillInvoker = (*StubSkills)(nil)
)

// Script sets what every later ListSkills answers: list, or err when err is
// non-nil. Script agent.ErrSkillsUnsupported for a build that can never
// list, and any other error for a probe that failed.
func (s *StubSkills) Script(list agent.SkillList, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list, s.err = list, err
}

// Calls is how many times ListSkills has been called.
func (s *StubSkills) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

// Queries is every SkillQuery ListSkills received, in call order.
func (s *StubSkills) Queries() []agent.SkillQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queries)
}

// Name implements agent.Adapter.
func (*StubSkills) Name() string { return SkillsName }

// Detect implements agent.Adapter: present enough to be listed.
func (*StubSkills) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: SkillsName, Version: "0"}, nil
}

// Path implements agent.Adapter.
func (*StubSkills) Path() (string, error) { return SkillsName, nil }

// ListSkills implements agent.SkillLister with the scripted answer.
func (s *StubSkills) ListSkills(_ context.Context, q agent.SkillQuery) (agent.SkillList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, q)
	if s.err != nil {
		return agent.SkillList{}, s.err
	}
	return s.list, nil
}

// SkillSyntax implements agent.SkillInvoker.
func (s *StubSkills) SkillSyntax() agent.SkillSyntax { return s.Syntax }

// Invocation implements agent.SkillInvoker: the sigil and the name, verbatim.
func (s *StubSkills) Invocation(sk agent.Skill, _ []agent.Skill) string {
	return s.Syntax.Sigil + sk.Name
}

// inert is the part of agent.Adapter the skill stubs never exercise: nothing
// to select, nothing to parse, and nothing to start.
type inert struct{}

// Options implements agent.Adapter: nothing to select.
func (inert) Options(context.Context) (agent.Options, error) { return agent.Options{}, nil }

// Curated implements agent.Adapter.
func (inert) Curated() agent.Options { return agent.Options{} }

// NewLineParser implements agent.Adapter: every line is unknown, since the
// stub never writes one.
func (inert) NewLineParser() agent.LineParser {
	return func(raw []byte) agent.Event {
		return agent.Event{Type: agent.EventUnknown, Raw: raw}
	}
}

// Start implements agent.Adapter: nothing under test starts a run.
func (inert) Start(context.Context, agent.RunSpec) (agent.RunHandle, error) {
	return nil, errStub
}
