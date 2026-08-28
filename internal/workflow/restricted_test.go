package workflow

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestPermissionModeResolution pins the §9.4 precedence the API's gate and the
// engine now share: the step's own field, then defaults, then full-auto.
func TestPermissionModeResolution(t *testing.T) {
	src := `
name: careful
defaults:
  permission_mode: restricted
steps:
  - id: locked
    type: agent
    prompt: read only
  - id: open
    type: agent
    permission_mode: full-auto
    prompt: do anything
  - id: build
    type: command
    run: git status
`
	wf, _, err := Parse([]byte(src), Options{KnownAgents: []string{"claude"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := wf.PermissionMode(wf.Steps[0]); got != PermissionRestricted {
		t.Errorf("PermissionMode(inherited) = %q, want %q", got, PermissionRestricted)
	}
	if got := wf.PermissionMode(wf.Steps[1]); got != PermissionFullAuto {
		t.Errorf("PermissionMode(step override) = %q, want %q", got, PermissionFullAuto)
	}
	if !wf.StepRequiresRestricted(wf.Steps[0]) {
		t.Error("inherited restricted step does not require restriction")
	}
	if wf.StepRequiresRestricted(wf.Steps[1]) {
		t.Error("step with permission_mode: full-auto requires restriction; the step must win over defaults")
	}
	// `permission_mode` is what an agent CLI is launched with; a command
	// step's shell has never consulted it, so it cannot make the gate fire.
	if wf.StepRequiresRestricted(wf.Steps[2]) {
		t.Error("command step counts as restricted; only agent steps launch an adapter")
	}
	// A step with nothing set anywhere is full-auto (§16).
	var bare *Workflow
	if got := bare.PermissionMode(Step{Type: StepAgent}); got != PermissionFullAuto {
		t.Errorf("PermissionMode(nil workflow) = %q, want %q", got, PermissionFullAuto)
	}
}

// TestRestrictedMismatch covers the gate itself: which step's agent the
// refusal is judged against, and the case that must *not* refuse — a step that
// pins a capable agent of its own is not constrained by the task-level pick,
// the same rule task 013 decision 6 records for input.
func TestRestrictedMismatch(t *testing.T) {
	cannot := func(name string) bool { return name == "cursor" }
	tests := []struct {
		name     string
		src      string
		override agent.Level
		want     string
	}{
		{
			name: "restricted step on an incapable task-level agent",
			src: `
name: careful
steps:
  - id: locked
    type: agent
    permission_mode: restricted
    prompt: read only
`,
			override: agent.Level{Agent: "cursor"},
			want:     `step "locked" runs restricted and agent "cursor" cannot restrict on this host`,
		},
		{
			name: "restricted step pinning a capable agent is not constrained",
			src: `
name: careful
steps:
  - id: locked
    type: agent
    agent: claude
    permission_mode: restricted
    prompt: read only
`,
			override: agent.Level{Agent: "cursor"},
		},
		{
			name: "full-auto step on an incapable agent is fine",
			src: `
name: open
steps:
  - id: go
    type: agent
    prompt: do anything
`,
			override: agent.Level{Agent: "cursor"},
		},
		{
			name: "defaults reach a step that says nothing",
			src: `
name: careful
defaults:
  permission_mode: restricted
steps:
  - id: locked
    type: agent
    prompt: read only
`,
			override: agent.Level{Agent: "cursor"},
			want:     `step "locked" runs restricted and agent "cursor" cannot restrict on this host`,
		},
		{
			name: "workflow default agent is judged when the task picks nothing",
			src: `
name: careful
defaults:
  agent: cursor
  permission_mode: restricted
steps:
  - id: locked
    type: agent
    prompt: read only
`,
			want: `step "locked" runs restricted and agent "cursor" cannot restrict on this host`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, _, err := Parse([]byte(tt.src), Options{KnownAgents: []string{"claude", "cursor"}})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := wf.RestrictedMismatch(tt.override, cannot)
			if got != tt.want {
				t.Errorf("RestrictedMismatch = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRestrictedMismatchNilInputs pins the two ways a caller can ask nothing:
// a daemon with no catalog passes a nil predicate, and a nil workflow reaches
// here from a snapshot that did not parse. Neither may refuse a task.
func TestRestrictedMismatchNilInputs(t *testing.T) {
	var wf *Workflow
	if got := wf.RestrictedMismatch(agent.Level{}, func(string) bool { return true }); got != "" {
		t.Errorf("nil workflow refused: %q", got)
	}
	src := "name: x\nsteps:\n  - id: a\n    type: agent\n    permission_mode: restricted\n    prompt: p\n"
	parsed, _, err := Parse([]byte(src), Options{KnownAgents: []string{"claude"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := parsed.RestrictedMismatch(agent.Level{Agent: "claude"}, nil); got != "" {
		t.Errorf("nil predicate refused: %q", got)
	}
	// The clause is one sentence a 400 body embeds verbatim; a newline in it
	// would break the error envelope's single-line shape.
	msg := parsed.RestrictedMismatch(agent.Level{Agent: "claude"}, func(string) bool { return true })
	if msg == "" || strings.Contains(msg, "\n") {
		t.Errorf("mismatch clause = %q, want one non-empty line", msg)
	}
}
