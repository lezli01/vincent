package agent_test

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestSkillCapabilitiesComeFromTheInterfaces pins §9.1's two skill
// capabilities (task 124). Both legs are proven against agenttest stubs and
// neither against a shipped adapter: cursor cannot list today, but a refusal
// pinned to it would invert itself the day it can — the lesson recorded on
// agent.Resumer.
func TestSkillCapabilitiesComeFromTheInterfaces(t *testing.T) {
	for _, tc := range []struct {
		a          agent.Adapter
		list, call bool
	}{
		{agenttest.StubNoSkills{}, false, false},
		{&agenttest.StubSkills{}, true, true},
	} {
		if got := agent.CanListSkills(tc.a); got != tc.list {
			t.Errorf("CanListSkills(%s) = %v, want %v", tc.a.Name(), got, tc.list)
		}
		if got := agent.CanInvokeSkills(tc.a); got != tc.call {
			t.Errorf("CanInvokeSkills(%s) = %v, want %v", tc.a.Name(), got, tc.call)
		}
	}
}

// TestErrSkillsUnsupportedIsTheOnlyPositiveNo holds task 124 decision 16: the
// sentinel survives wrapping, and an ordinary probe failure never reads as it.
// The chat skills route maps the first to `unsupported` and the second to
// `unknown`, so the two must stay distinguishable by errors.Is alone.
func TestErrSkillsUnsupportedIsTheOnlyPositiveNo(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the sentinel", agent.ErrSkillsUnsupported, true},
		{"the sentinel wrapped", fmt.Errorf("claude 2.0.1: %w", agent.ErrSkillsUnsupported), true},
		{"a probe that timed out", errors.New("initialize: context deadline exceeded"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &agenttest.StubSkills{}
			s.Script(agent.SkillList{Skills: []agent.Skill{{Name: "never-returned"}}}, tc.err)
			list, err := s.ListSkills(t.Context(), agent.SkillQuery{WorkDir: "wt"})
			if err == nil {
				t.Fatal("the scripted error was not returned")
			}
			if got := errors.Is(err, agent.ErrSkillsUnsupported); got != tc.want {
				t.Errorf("errors.Is(%v, ErrSkillsUnsupported) = %v, want %v", err, got, tc.want)
			}
			if len(list.Skills) != 0 {
				t.Errorf("an error came with skills: %+v", list)
			}
		})
	}
}

// TestStubSkillsRecordsWhatItWasAsked is the listing stub's own contract, the
// one a listing cache's tests rest on: the scripted list comes back verbatim —
// repeated names and all, since nothing is synthesized — and every query is
// counted and kept, safely under concurrent calls.
func TestStubSkillsRecordsWhatItWasAsked(t *testing.T) {
	want := agent.SkillList{
		Skills: []agent.Skill{
			{Name: "review", Description: "Review a diff (project)", ArgumentHint: "[path]"},
			{Name: "review", Scope: "user", Path: "/home/u/.codex/skills/review/SKILL.md"},
			{Name: "fmt:check", Plugin: "fmt"},
		},
		Problems: []agent.SkillProblem{{Path: "/x/SKILL.md", Message: "missing name"}},
	}
	s := &agenttest.StubSkills{Syntax: agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere}}
	s.Script(want, nil)

	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			got, err := s.ListSkills(t.Context(), agent.SkillQuery{WorkDir: fmt.Sprintf("wt-%d", i)})
			if err != nil {
				t.Errorf("ListSkills: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ListSkills = %+v, want the scripted %+v", got, want)
			}
		})
	}
	wg.Wait()

	if got := s.Calls(); got != n {
		t.Errorf("Calls() = %d, want %d", got, n)
	}
	dirs := map[string]bool{}
	for _, q := range s.Queries() {
		dirs[q.WorkDir] = true
	}
	for i := range n {
		if !dirs[fmt.Sprintf("wt-%d", i)] {
			t.Errorf("Queries() lost the query for wt-%d: %v", i, dirs)
		}
	}

	if got := s.Invocation(want.Skills[2], want.Skills); got != "$fmt:check" {
		t.Errorf("Invocation = %q, want the configured sigil and the name verbatim", got)
	}
}
