package workflow

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// testCatalogs mirrors the shipped curated catalogs (§9.3): claude curates
// models and efforts, codex curates efforts only.
func testCatalogs() agent.Catalogs {
	opt := func(vs ...string) []agent.Option {
		out := make([]agent.Option, 0, len(vs))
		for _, v := range vs {
			out = append(out, agent.Option{Value: v, Source: agent.SourceCurated})
		}
		return out
	}
	return agent.Catalogs{
		"claude": {
			Models: opt("sonnet", "opus", "haiku"), Efforts: opt("low", "medium", "high", "xhigh", "max"),
			InputSupport: agent.InputDetected,
		},
		"codex": {
			Efforts:      opt("minimal", "low", "medium", "high", "xhigh"),
			InputSupport: agent.InputNever,
		},
	}
}

func catalogOpts() Options {
	return Options{
		KnownAgents: []string{"claude", "codex"},
		Catalogs:    testCatalogs,
	}
}

func TestCatalogValidationValid(t *testing.T) {
	src := `
name: ok
defaults:
  agent: claude
  model: sonnet
  effort: max
steps:
  - id: one
    type: agent
    prompt: p
  - id: two
    type: agent
    agent: codex
    effort: xhigh
    prompt: p
`
	wf, warns, err := Parse([]byte(src), catalogOpts())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wf == nil || len(warns) != 0 {
		t.Errorf("got warnings %v, want a clean pass (xhigh is valid on codex too)", warns)
	}
}

func TestCatalogValidationCrossCatalogError(t *testing.T) {
	// A claude model reaching a codex step is a validation error located at
	// the step's own field (§8.2).
	src := `
name: bad
defaults:
  agent: claude
steps:
  - id: one
    type: agent
    agent: codex
    model: sonnet
    prompt: p
`
	_, _, err := Parse([]byte(src), catalogOpts())
	if err == nil {
		t.Fatal("Parse accepted a claude model on a codex step")
	}
	var errs Errors
	if !asErrors(err, &errs) || len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", err)
	}
	if errs[0].Path != "steps[0].model" || !strings.Contains(errs[0].Message, "claude") {
		t.Errorf("error = %+v, want steps[0].model naming claude's catalog", errs[0])
	}
	if errs[0].Line == 0 {
		t.Error("error line = 0, want the located source line")
	}
}

func TestCatalogValidationDefaultsEffortError(t *testing.T) {
	// A codex-only effort inherited from defaults by a claude step errors at
	// defaults.effort — once, however many steps inherit it.
	src := `
name: bad-defaults
defaults:
  agent: claude
  effort: minimal
steps:
  - id: one
    type: agent
    prompt: p
  - id: two
    type: agent
    prompt: p
`
	_, _, err := Parse([]byte(src), catalogOpts())
	if err == nil {
		t.Fatal("Parse accepted a codex-only effort on claude steps")
	}
	var errs Errors
	if !asErrors(err, &errs) || len(errs) != 1 {
		t.Fatalf("errors = %v, want the defaults finding deduped to one", err)
	}
	if errs[0].Path != "defaults.effort" {
		t.Errorf("error path = %q, want defaults.effort", errs[0].Path)
	}
}

func TestCatalogValidationUnknownWarns(t *testing.T) {
	// Values no catalog knows pass with a warning: the CLI is the final
	// authority (§8.2). The workflow stays valid.
	src := `
name: warned
defaults:
  agent: codex
  model: gpt-5.6-sol
steps:
  - id: one
    type: agent
    prompt: p
  - id: two
    type: agent
    prompt: p
  - id: three
    type: agent
    effort: turbo
    prompt: p
`
	wf, warns, err := Parse([]byte(src), catalogOpts())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wf == nil {
		t.Fatal("workflow nil despite warnings-only findings")
	}
	// One deduped defaults.model warning + one step-level effort warning.
	if len(warns) != 2 {
		t.Fatalf("warnings = %v, want 2 (deduped defaults.model + steps[2].effort)", warns)
	}
	if warns[0].Path != "defaults.model" || warns[1].Path != "steps[2].effort" {
		t.Errorf("warning paths = %q, %q; want defaults.model, steps[2].effort", warns[0].Path, warns[1].Path)
	}
}

func TestCatalogValidationSkipsWithoutCatalogs(t *testing.T) {
	// Nil Catalogs (taskrun snapshot parses, tests) disables the check.
	src := `
name: unchecked
steps:
  - id: one
    type: agent
    model: sonnet
    agent: codex
    prompt: p
`
	_, warns, err := Parse([]byte(src), Options{KnownAgents: []string{"claude", "codex"}})
	if err != nil || len(warns) != 0 {
		t.Errorf("Parse without catalogs: err=%v warns=%v, want silent acceptance", err, warns)
	}
}
