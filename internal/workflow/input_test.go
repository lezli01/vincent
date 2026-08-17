package workflow

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestRequirePolicyParses covers the §7.4 third value on every level that may
// carry it, and its rejection on the step types that may not.
func TestRequirePolicyParses(t *testing.T) {
	src := `
name: interactive
defaults:
  on_input: require
steps:
  - id: ask
    type: agent
    prompt: what should I build?
  - id: quiet
    type: agent
    on_input: deny
    prompt: build it
`
	wf, _, err := Parse([]byte(src), Options{KnownAgents: []string{"claude"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !wf.StepRequiresInput(wf.Steps[0]) {
		t.Error("step inheriting defaults.on_input: require does not require input")
	}
	// Decision 7: a step's own policy wins over the default, so `deny` inside
	// a `require` workflow leaves that one step unattended.
	if wf.StepRequiresInput(wf.Steps[1]) {
		t.Error("step with on_input: deny requires input; the step must win over defaults")
	}
	if got := wf.InputPolicy(wf.Steps[1]); got != InputDeny {
		t.Errorf("InputPolicy = %q, want %q", got, InputDeny)
	}
}

func TestRequireRejectedOnNonAgentSteps(t *testing.T) {
	src := `
name: bad
steps:
  - id: build
    type: command
    on_input: require
    run: make
`
	if _, _, err := Parse([]byte(src), Options{}); err == nil {
		t.Fatal("on_input accepted on a command step")
	}
}

func TestUnknownInputPolicyNamesAllThree(t *testing.T) {
	src := `
name: bad
steps:
  - id: ask
    type: agent
    on_input: sometimes
    prompt: hi
`
	_, _, err := Parse([]byte(src), Options{})
	if err == nil {
		t.Fatal("unknown on_input value accepted")
	}
	for _, want := range []string{InputWait, InputDeny, InputRequire} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not offer %q", err, want)
		}
	}
}

// TestRequireOnIncapableAgentIsALoadError is the §8.2 half of the gate: a
// requiring step pinned to an adapter with no control channel is broken on
// every host, and is decidable without probing.
func TestRequireOnIncapableAgentIsALoadError(t *testing.T) {
	src := `
name: bad
steps:
  - id: ask
    type: agent
    agent: codex
    on_input: require
    prompt: what should I build?
`
	_, _, err := Parse([]byte(src), catalogOpts())
	if err == nil {
		t.Fatal("require on codex parsed clean")
	}
	var errs Errors
	if !asErrors(err, &errs) {
		t.Fatalf("error is not workflow.Errors: %T", err)
	}
	if errs[0].Path != "steps[0].agent" {
		t.Errorf("path = %q, want steps[0].agent — the field to change", errs[0].Path)
	}
	if !strings.Contains(errs[0].Message, "codex") {
		t.Errorf("message %q does not name the agent", errs[0].Message)
	}
}

// The finding is attributed to whichever level supplied the agent, and a
// default inherited by many steps is reported once.
func TestRequireFindingAttributedToDefaults(t *testing.T) {
	src := `
name: bad
defaults:
  agent: codex
  on_input: require
steps:
  - id: one
    type: agent
    prompt: a
  - id: two
    type: agent
    prompt: b
`
	_, _, err := Parse([]byte(src), catalogOpts())
	if err == nil {
		t.Fatal("require on a codex default parsed clean")
	}
	var errs Errors
	if !asErrors(err, &errs) {
		t.Fatalf("error is not workflow.Errors: %T", err)
	}
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1 collapsed onto defaults: %v", len(errs), errs)
	}
	if errs[0].Path != "defaults.agent" {
		t.Errorf("path = %q, want defaults.agent", errs[0].Path)
	}
}

// claude's support is a version question only a probe can answer, so §8.2
// must not judge it.
func TestRequireOnDetectedAgentIsNotALoadError(t *testing.T) {
	src := `
name: fine
steps:
  - id: ask
    type: agent
    agent: claude
    on_input: require
    prompt: what should I build?
`
	if _, _, err := Parse([]byte(src), catalogOpts()); err != nil {
		t.Fatalf("require on claude rejected at load: %v", err)
	}
}

// TestRequiresInputDerivation covers decision 6: only a requiring step that
// leaves its agent to the task constrains the task's agent choice.
func TestRequiresInputDerivation(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"unpinned requiring step constrains", `
name: w
steps:
  - id: ask
    type: agent
    on_input: require
    prompt: a
`, true},
		{"pinned requiring step does not", `
name: w
steps:
  - id: ask
    type: agent
    agent: claude
    on_input: require
    prompt: a
`, false},
		{"no requiring step", `
name: w
steps:
  - id: ask
    type: agent
    prompt: a
`, false},
		{"a pinned requiring step does not excuse an unpinned one", `
name: w
steps:
  - id: pinned
    type: agent
    agent: claude
    on_input: require
    prompt: a
  - id: loose
    type: agent
    on_input: require
    prompt: b
`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf, _, err := Parse([]byte(tc.src), Options{KnownAgents: []string{"claude"}})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := wf.RequiresInput(); got != tc.want {
				t.Errorf("RequiresInput() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInputMismatchResolvesThroughOverrides is the clause the API's 400 and
// the engine's pre-flight both embed: it must follow §8.6 resolution, not a
// conservative approximation of it.
func TestInputMismatchResolvesThroughOverrides(t *testing.T) {
	src := `
name: w
defaults:
  agent: claude
steps:
  - id: build
    type: agent
    agent: codex
    prompt: build
  - id: ask
    type: agent
    on_input: require
    prompt: ask
`
	wf, _, err := Parse([]byte(src), Options{KnownAgents: []string{"claude", "codex"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	incapable := func(name string) bool { return name == "codex" }

	// No override: the requiring step inherits defaults.agent (claude).
	if got := wf.InputMismatch(agent.Level{}, incapable); got != "" {
		t.Errorf("mismatch with a capable default = %q, want none", got)
	}
	// The task override reaches the unpinned step and breaks it — while the
	// pinned codex step, which never asks, stays irrelevant.
	got := wf.InputMismatch(agent.Level{Agent: "codex"}, incapable)
	if got == "" {
		t.Fatal("codex override on a requiring step reported no mismatch")
	}
	for _, want := range []string{"ask", "codex"} {
		if !strings.Contains(got, want) {
			t.Errorf("mismatch %q does not name %q", got, want)
		}
	}
	// An unknown verdict is not evidence: nothing is refused on it.
	if got := wf.InputMismatch(agent.Level{Agent: "codex"}, func(string) bool { return false }); got != "" {
		t.Errorf("mismatch with an unknown verdict = %q, want none", got)
	}
}
