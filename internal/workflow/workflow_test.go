package workflow

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const validSource = `name: feature-pr
description: Implement, test, review.
defaults:
  agent: claude
  permission_mode: full-auto
  on_input: wait
  input_timeout: 24h
  max_retries: 1
  timeout: 60m
steps:
  - id: implement
    name: Implement the change
    type: agent
    prompt: |
      Do {{.Task.Title}}
    check: go test ./...
    max_retries: 2
    timeout: 45m
  - id: gate-review
    type: manual
    instructions: Inspect the diff for #{{.Task.ID}}.
  - id: publish
    type: command
    run: git push -u origin {{.Task.BranchName}}
    shell: sh
    env:
      GIT_TERMINAL_PROMPT: "0"
    timeout: 5m
    max_retries: 0
`

func TestParseValid(t *testing.T) {
	wf, _, err := Parse([]byte(validSource), Options{KnownAgents: []string{"claude", "codex"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wf.Name != "feature-pr" || len(wf.Steps) != 3 {
		t.Fatalf("name = %q, steps = %d; want feature-pr / 3", wf.Name, len(wf.Steps))
	}
	if got := wf.Steps[0].DisplayName(); got != "Implement the change" {
		t.Errorf("DisplayName = %q, want the declared name", got)
	}
	if got := wf.Steps[1].DisplayName(); got != "gate-review" {
		t.Errorf("DisplayName without name = %q, want the id", got)
	}
	if wf.Defaults.Timeout == nil || wf.Defaults.Timeout.Std() != time.Hour {
		t.Errorf("defaults.timeout = %v, want 60m", wf.Defaults.Timeout)
	}
	if wf.Steps[0].MaxRetries == nil || *wf.Steps[0].MaxRetries != 2 {
		t.Errorf("step max_retries not decoded: %v", wf.Steps[0].MaxRetries)
	}
	if wf.Steps[2].Env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("env not decoded: %v", wf.Steps[2].Env)
	}
	// Absent optional values stay nil so the daemon default applies.
	if wf.Steps[1].Timeout != nil {
		t.Errorf("absent timeout = %v, want nil", wf.Steps[1].Timeout)
	}
}

func TestParseValidation(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		wantSub  string // substring of the reported message
		wantPath string
	}{
		{
			name:    "unknown top-level key",
			src:     "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi}\nbogus: 1\n",
			wantSub: `bogus`,
		},
		{
			name:    "unknown step key",
			src:     "name: x\nsteps:\n  - id: a\n    type: manual\n    instructions: hi\n    retries: 3\n",
			wantSub: `retries`,
		},
		{
			name:     "missing name",
			src:      "steps:\n  - {id: a, type: manual, instructions: hi}\n",
			wantSub:  "name is required",
			wantPath: "name",
		},
		{
			name:     "no steps",
			src:      "name: x\nsteps: []\n",
			wantSub:  "steps must not be empty",
			wantPath: "steps",
		},
		{
			name:     "duplicate step id",
			src:      "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi}\n  - {id: a, type: manual, instructions: ho}\n",
			wantSub:  "duplicate step id",
			wantPath: "steps[1].id",
		},
		{
			name:     "missing step type",
			src:      "name: x\nsteps:\n  - {id: a, instructions: hi}\n",
			wantSub:  "type is required",
			wantPath: "steps[0].type",
		},
		{
			name:     "unknown step type",
			src:      "name: x\nsteps:\n  - {id: a, type: wizard}\n",
			wantSub:  "unknown step type",
			wantPath: "steps[0].type",
		},
		{
			name:     "agent step without prompt",
			src:      "name: x\nsteps:\n  - {id: a, type: agent}\n",
			wantSub:  "agent steps require a prompt",
			wantPath: "steps[0].prompt",
		},
		{
			name:     "command step without run",
			src:      "name: x\nsteps:\n  - {id: a, type: command}\n",
			wantSub:  "command steps require a run command",
			wantPath: "steps[0].run",
		},
		{
			name:     "manual step without instructions",
			src:      "name: x\nsteps:\n  - {id: a, type: manual}\n",
			wantSub:  "manual steps require instructions",
			wantPath: "steps[0].instructions",
		},
		{
			name:     "field from another step type",
			src:      "name: x\nsteps:\n  - {id: a, type: agent, prompt: hi, run: make}\n",
			wantSub:  "run is not valid on a agent step",
			wantPath: "steps[0].run",
		},
		{
			name:     "manual step with check",
			src:      "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi, check: go test ./...}\n",
			wantSub:  "check is not valid on a manual step",
			wantPath: "steps[0].check",
		},
		{
			name:     "unknown agent",
			src:      "name: x\nsteps:\n  - {id: a, type: agent, prompt: hi, agent: gemini}\n",
			wantSub:  `unknown agent "gemini"`,
			wantPath: "steps[0].agent",
		},
		{
			name:     "bad permission mode",
			src:      "name: x\nsteps:\n  - {id: a, type: agent, prompt: hi, permission_mode: yolo}\n",
			wantSub:  "permission_mode must be",
			wantPath: "steps[0].permission_mode",
		},
		{
			name:     "bad on_input",
			src:      "name: x\nsteps:\n  - {id: a, type: agent, prompt: hi, on_input: maybe}\n",
			wantSub:  "on_input must be",
			wantPath: "steps[0].on_input",
		},
		{
			name:     "bad shell",
			src:      "name: x\nsteps:\n  - {id: a, type: command, run: ls, shell: fish}\n",
			wantSub:  "shell must be one of",
			wantPath: "steps[0].shell",
		},
		{
			name:    "unparsable duration",
			src:     "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi, timeout: soon}\n",
			wantSub: "invalid duration",
		},
		{
			name:     "non-positive duration",
			src:      "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi, timeout: 0s}\n",
			wantSub:  "timeout must be positive",
			wantPath: "steps[0].timeout",
		},
		{
			name:     "negative max_retries",
			src:      "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi, max_retries: -1}\n",
			wantSub:  "max_retries must not be negative",
			wantPath: "steps[0].max_retries",
		},
		{
			name:     "template does not parse",
			src:      "name: x\nsteps:\n  - {id: a, type: agent, prompt: \"{{.Task.Title\"}\n",
			wantSub:  "template does not parse",
			wantPath: "steps[0].prompt",
		},
		{
			name:     "bad step id",
			src:      "name: x\nsteps:\n  - {id: \"Step One\", type: manual, instructions: hi}\n",
			wantSub:  "must be a slug",
			wantPath: "steps[0].id",
		},
		{
			name:     "name with spaces",
			src:      "name: my workflow\nsteps:\n  - {id: a, type: manual, instructions: hi}\n",
			wantSub:  "must not contain whitespace",
			wantPath: "name",
		},
		{
			name:     "defaults reject unknown agent",
			src:      "name: x\ndefaults:\n  agent: gemini\nsteps:\n  - {id: a, type: manual, instructions: hi}\n",
			wantSub:  `unknown agent "gemini"`,
			wantPath: "defaults.agent",
		},
	}

	opts := Options{KnownAgents: []string{"claude", "codex"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tt.src), opts)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error, want validation failure", tt.name)
			}
			var errs Errors
			if !errors.As(err, &errs) {
				t.Fatalf("error type = %T, want Errors", err)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("errors = %q, want a message containing %q", err.Error(), tt.wantSub)
			}
			if tt.wantPath != "" && !hasPath(errs, tt.wantPath) {
				t.Errorf("errors = %v, want one at path %q", errs, tt.wantPath)
			}
		})
	}
}

func hasPath(errs Errors, path string) bool {
	for _, e := range errs {
		if e.Path == path {
			return true
		}
	}
	return false
}

// TestParseReportsLines pins the file/line reporting T2.1 promises: both a
// strict-decoding failure and a semantic failure point at the offending line.
func TestParseReportsLines(t *testing.T) {
	semantic := "name: x\nsteps:\n  - id: a\n    type: agent\n    timeout: 0s\n    prompt: hi\n"
	_, _, err := Parse([]byte(semantic), Options{})
	var errs Errors
	if !errors.As(err, &errs) || len(errs) == 0 {
		t.Fatalf("Parse = %v, want validation errors", err)
	}
	if errs[0].Line != 5 {
		t.Errorf("semantic error line = %d, want 5 (%s)", errs[0].Line, errs[0])
	}

	strict := "name: x\nsteps:\n  - id: a\n    type: manual\n    instructions: hi\n    nope: 1\n"
	_, _, err = Parse([]byte(strict), Options{})
	if !errors.As(err, &errs) || len(errs) == 0 {
		t.Fatalf("Parse = %v, want a decode error", err)
	}
	if errs[0].Line != 6 {
		t.Errorf("decode error line = %d, want 6 (%s)", errs[0].Line, errs[0])
	}
}

// TestParseCollectsEveryFailure proves one pass reports all problems rather
// than stopping at the first.
func TestParseCollectsEveryFailure(t *testing.T) {
	src := "name: x\nsteps:\n  - {id: a, type: agent}\n  - {id: a, type: bogus}\n"
	_, _, err := Parse([]byte(src), Options{})
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("error type = %T, want Errors", err)
	}
	if len(errs) < 3 {
		t.Fatalf("errors = %v, want at least 3 (missing prompt, duplicate id, unknown type)", errs)
	}
}

func TestBuiltinAdhocIsValid(t *testing.T) {
	entries := builtins()
	e, ok := entries[AdhocName]
	if !ok {
		t.Fatalf("builtins() has no %q entry", AdhocName)
	}
	if !e.Valid() {
		t.Fatalf("built-in adhoc is invalid: %v", e.Errors)
	}
	if e.Scope != ScopeBuiltin || e.File != "" {
		t.Errorf("scope = %q, file = %q; want builtin scope and no file", e.Scope, e.File)
	}
	if len(e.Workflow.Steps) != 1 || e.Workflow.Steps[0].Type != StepAgent {
		t.Fatalf("adhoc steps = %+v, want one agent step", e.Workflow.Steps)
	}
	if mr := e.Workflow.Steps[0].MaxRetries; mr == nil || *mr != 0 {
		t.Errorf("adhoc max_retries = %v, want 0 (fail fast)", mr)
	}
}
