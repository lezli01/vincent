package workflow

import (
	"errors"
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	rc := RenderContext{
		Task:  TaskContext{Title: "t", Fields: map[string]string{"ship": "yes"}},
		Steps: map[string]StepResult{"probe": {Status: "failed", ExitCode: 3}},
		Host:  HostContext{OS: "linux", Arch: "amd64"},
	}
	tests := []struct {
		name    string
		expr    string
		want    bool
		wantErr string
	}{
		{name: "literal true", expr: "{{ true }}", want: true},
		{name: "literal false", expr: "{{ false }}", want: false},
		{name: "surrounding whitespace is trimmed", expr: "  {{ true }}\n", want: true},
		{name: "field comparison", expr: `{{ eq (index .Task.Fields "ship") "yes" }}`, want: true},
		{name: "exit code of an allowed failure", expr: `{{ ne (index .Steps "probe").ExitCode 0 }}`, want: true},
		{name: "host", expr: `{{ eq .Host.OS "linux" }}`, want: true},
		{
			name: "a number is not a verdict", expr: "{{ 7 }}",
			wantErr: `must render to "true" or "false"`,
		},
		{
			// The trap a truthiness table would fall into: this is what a
			// guard reading a field that is not there renders to.
			name: "empty is not false", expr: "",
			wantErr: "an empty string",
		},
		{
			name: "yes is not true", expr: "yes",
			wantErr: `got "yes"`,
		},
		{
			name: "missing key is a render error", expr: `{{ index .Task.Fields "absent" }}`,
			wantErr: "render",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate("if", tt.expr, rc)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Evaluate(%q) = %v, want an error", tt.expr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Evaluate(%q): %v", tt.expr, err)
			}
			if got != tt.want {
				t.Errorf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestParseConditionValidation(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		wantSub  string
		wantPath string
	}{
		{
			name:     "condition without an if",
			src:      "name: x\nsteps:\n  - {id: a, type: condition}\n",
			wantSub:  "condition steps require an if expression",
			wantPath: "steps[0].if",
		},
		{
			name: "condition carrying a body",
			src: "name: x\nsteps:\n  - {id: a, type: condition, if: \"{{ true }}\", run: echo hi}\n" +
				"  - {id: b, type: manual, instructions: hi}\n",
			wantSub:  "run is not valid on a condition step",
			wantPath: "steps[0].run",
		},
		{
			name: "condition carrying a retry budget",
			src: "name: x\nsteps:\n  - {id: a, type: condition, if: \"{{ true }}\", max_retries: 2}\n" +
				"  - {id: b, type: manual, instructions: hi}\n",
			wantSub:  "max_retries is not valid on a condition step",
			wantPath: "steps[0].max_retries",
		},
		{
			name:     "allow_failure on a manual gate",
			src:      "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi, allow_failure: true}\n",
			wantSub:  "allow_failure is not valid on a manual step",
			wantPath: "steps[0].allow_failure",
		},
		{
			name: "condition inside a parallel group",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    steps:\n" +
				"      - {id: c, type: condition, if: \"{{ true }}\"}\n",
			wantSub:  "condition steps are not valid inside a parallel group",
			wantPath: "steps[0].steps[0].type",
		},
		{
			name:     "guard that does not parse",
			src:      "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi, if: \"{{ oops\"}\n",
			wantSub:  "template does not parse",
			wantPath: "steps[0].if",
		},
		{
			name: "lane guard that does not parse",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n    lanes:\n" +
				"      - {id: l, workflow: other, if: \"{{ oops\"}\n",
			wantSub:  "template does not parse",
			wantPath: "steps[0].lanes[0].if",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tt.src), Options{})
			if err == nil {
				t.Fatal("Parse succeeded, want a validation error")
			}
			var errs Errors
			if !errors.As(err, &errs) {
				t.Fatalf("error is %T, want Errors", err)
			}
			for _, e := range errs {
				if strings.Contains(e.Message, tt.wantSub) &&
					(tt.wantPath == "" || e.Path == tt.wantPath) {
					return
				}
			}
			t.Fatalf("no finding matched %q at %q; got %v", tt.wantSub, tt.wantPath, errs)
		})
	}
}

func TestParseConditionAccepted(t *testing.T) {
	src := `name: conditional
steps:
  - id: probe
    type: command
    run: git diff --quiet
    allow_failure: true
  - id: gate
    type: condition
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'
  - id: fix
    type: agent
    if: '{{ eq .Host.OS "linux" }}'
    prompt: Fix it.
`
	wf, warns, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %v, want none", warns)
	}
	if !wf.Steps[0].AllowFailure {
		t.Error("allow_failure did not survive the round trip")
	}
	if wf.Steps[1].Type != StepCondition || !wf.Steps[1].Guarded() {
		t.Errorf("step 1 = %+v, want a guarded condition step", wf.Steps[1])
	}
	if !wf.Steps[2].Guarded() {
		t.Error("the agent step's guard was dropped")
	}
	// A snapshot round-trips: the engine re-parses it on every admission.
	out, err := Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	again, _, err := Parse(out, Options{})
	if err != nil {
		t.Fatalf("re-parse of the marshalled snapshot: %v", err)
	}
	if again.Steps[1].If != wf.Steps[1].If {
		t.Errorf("guard after round trip = %q, want %q", again.Steps[1].If, wf.Steps[1].If)
	}
}

func TestParseWarnsOnTrailingCondition(t *testing.T) {
	src := "name: x\nsteps:\n  - {id: a, type: manual, instructions: hi}\n" +
		"  - {id: last, type: condition, if: \"{{ true }}\"}\n"
	_, warns, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v — a trailing condition is a warning, not an error", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "has no effect") {
		t.Fatalf("warnings = %v, want one about a trailing condition having no effect", warns)
	}
	if warns[0].Path != "steps[1].type" {
		t.Errorf("warning path = %q, want steps[1].type", warns[0].Path)
	}
}
