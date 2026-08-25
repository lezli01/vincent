package workflow

import (
	"errors"
	"reflect"
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
			// Zero is legal and is the default: it means an immediate retry.
			// Negative is the only thing there is to refuse (task 028).
			name:     "negative retry_backoff",
			src:      "name: x\nsteps:\n  - {id: a, type: command, run: \"true\", retry_backoff: -30s}\n",
			wantSub:  "retry_backoff must not be negative",
			wantPath: "steps[0].retry_backoff",
		},
		{
			name:     "negative retry_backoff in defaults",
			src:      "name: x\ndefaults: {retry_backoff: -1s}\nsteps:\n  - {id: a, type: command, run: \"true\"}\n",
			wantSub:  "retry_backoff must not be negative",
			wantPath: "defaults.retry_backoff",
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
		// task 014 — `type: parallel`.
		{
			name:     "parallel group with no sub-steps",
			src:      "name: x\nsteps:\n  - {id: g, type: parallel, steps: []}\n",
			wantSub:  "parallel steps require at least one sub-step",
			wantPath: "steps[0].steps",
		},
		{
			name: "parallel group with max_parallel below one",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    max_parallel: 0\n" +
				"    steps:\n      - {id: t, type: command, run: go test ./...}\n",
			wantSub:  "max_parallel must be at least 1",
			wantPath: "steps[0].max_parallel",
		},
		{
			name: "manual sub-step",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n" +
				"    steps:\n      - {id: m, type: manual, instructions: hi}\n",
			wantSub:  "manual steps are not valid inside a parallel group",
			wantPath: "steps[0].steps[0].type",
		},
		{
			name: "nested parallel group",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    steps:\n" +
				"      - id: h\n        type: parallel\n" +
				"        steps:\n          - {id: t, type: command, run: ls}\n",
			wantSub:  "parallel groups do not nest",
			wantPath: "steps[0].steps[0].type",
		},
		{
			name: "sub-step requiring mid-run input",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    steps:\n" +
				"      - {id: a, type: agent, prompt: hi, on_input: require}\n",
			wantSub:  "not valid inside a parallel group",
			wantPath: "steps[0].steps[0].on_input",
		},
		{
			// Resolved, not literal: the sub-step names no policy at all, and
			// the requirement reaches it from `defaults:` — which is where the
			// error must point, since that is the line to change.
			name: "sub-step inheriting require from defaults",
			src: "name: x\ndefaults:\n  on_input: require\nsteps:\n  - id: g\n    type: parallel\n" +
				"    steps:\n      - {id: a, type: agent, prompt: hi}\n",
			wantSub:  "not valid inside a parallel group",
			wantPath: "defaults.on_input",
		},
		{
			// Sub-steps are validated like any other step of their type.
			name: "sub-step failing its own type's rules",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n" +
				"    steps:\n      - {id: c, type: command}\n",
			wantSub:  "command steps require a run command",
			wantPath: "steps[0].steps[0].run",
		},
		{
			// Ids are unique workflow-wide, not merely within a group: a
			// sub-step shares its group's step_index and is told apart by id.
			name: "sub-step id colliding with a top-level step",
			src: "name: x\nsteps:\n  - {id: a, type: command, run: ls}\n  - id: g\n    type: parallel\n" +
				"    steps:\n      - {id: a, type: command, run: ls}\n",
			wantSub:  "duplicate step id",
			wantPath: "steps[1].steps[0].id",
		},
		{
			name: "group carrying a field of another type",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    prompt: hi\n" +
				"    steps:\n      - {id: t, type: command, run: ls}\n",
			wantSub:  "prompt is not valid on a parallel step",
			wantPath: "steps[0].prompt",
		},
		{
			name:     "sub-steps on a step that is not a group",
			src:      "name: x\nsteps:\n  - {id: a, type: command, run: ls, steps: [{id: b, type: command, run: ls}]}\n",
			wantSub:  "steps is not valid on a command step",
			wantPath: "steps[0].steps",
		},
		// task 014 — `type: fan_out`.
		{
			name:     "fan_out with no lanes",
			src:      "name: x\nsteps:\n  - {id: f, type: fan_out, lanes: []}\n",
			wantSub:  "fan_out steps require at least one lane",
			wantPath: "steps[0].lanes",
		},
		{
			name: "lane with neither a workflow nor steps",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n" +
				"    lanes:\n      - {id: api}\n",
			wantSub:  "either a workflow name or inline steps",
			wantPath: "steps[0].lanes[0]",
		},
		{
			name: "lane with both a workflow and steps",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n    lanes:\n" +
				"      - id: api\n        workflow: build\n" +
				"        steps: [{id: s, type: command, run: ls}]\n",
			wantSub:  "not both",
			wantPath: "steps[0].lanes[0]",
		},
		{
			name: "duplicate lane id",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n    lanes:\n" +
				"      - {id: api, workflow: build}\n      - {id: api, workflow: docs}\n",
			wantSub:  "duplicate lane id",
			wantPath: "steps[0].lanes[1].id",
		},
		{
			name: "inline lane step failing its own rules",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n    lanes:\n" +
				"      - id: api\n        steps: [{id: s, type: agent}]\n",
			wantSub:  "agent steps require a prompt",
			wantPath: "steps[0].lanes[0].steps[0].prompt",
		},
		{
			name: "on_conflict: agent without a resolver",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n" +
				"    merge: {on_conflict: agent}\n    lanes: [{id: api, workflow: build}]\n",
			wantSub:  "requires merge.agent",
			wantPath: "steps[0].merge.agent",
		},
		{
			name: "a resolver the policy never runs",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n    merge:\n      agent:\n" +
				"        id: resolve\n        prompt: fix it\n    lanes: [{id: api, workflow: build}]\n",
			wantSub:  "only used by on_conflict",
			wantPath: "steps[0].merge.agent",
		},
		{
			name: "unknown conflict policy",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n" +
				"    merge: {on_conflict: yolo}\n    lanes: [{id: api, workflow: build}]\n",
			wantSub:  "on_conflict must be",
			wantPath: "steps[0].merge.on_conflict",
		},
		{
			// The resolver is a full agent step, so it is judged by the
			// ordinary agent rules rather than a private subset.
			name: "resolver failing the ordinary agent rules",
			src: "name: x\nsteps:\n  - id: f\n    type: fan_out\n    merge:\n      on_conflict: agent\n" +
				"      agent: {id: resolve, prompt: fix it, agent: gemini}\n" +
				"    lanes: [{id: api, workflow: build}]\n",
			wantSub:  `unknown agent "gemini"`,
			wantPath: "steps[0].merge.agent.agent",
		},
		{
			name: "fan_out inside a parallel group",
			src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    steps:\n" +
				"      - id: f\n        type: fan_out\n        lanes: [{id: api, workflow: build}]\n",
			wantSub:  "not valid inside a parallel group",
			wantPath: "steps[0].steps[0].type",
		},
		{
			name:     "lanes on a step that is not a fan_out",
			src:      "name: x\nsteps:\n  - {id: a, type: command, run: ls, lanes: [{id: l, workflow: w}]}\n",
			wantSub:  "lanes is not valid on a command step",
			wantPath: "steps[0].lanes",
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
// TestParseParallelGroup covers the shape task 014 exists to allow, and the
// two properties the engine leans on: sub-steps decode in declaration order,
// and a group's own `steps:` survives a Marshal round-trip — `edit + retry`
// rewrites a snapshot through Marshal, and a group that lost its members
// there would come back as an empty group.
func TestParseParallelGroup(t *testing.T) {
	src := strings.TrimSpace(`
name: verify
steps:
  - id: build
    type: command
    run: go build ./...
  - id: verify
    type: parallel
    max_parallel: 2
    timeout: 30m
    steps:
      - {id: test, type: command, run: go test ./...}
      - {id: lint, type: command, run: golangci-lint run}
      - {id: review, type: agent, prompt: "Review {{.Task.Title}}.", check: go vet ./...}
`) + "\n"
	wf, _, err := Parse([]byte(src), Options{KnownAgents: []string{"claude"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	group := wf.Steps[1]
	if group.Type != StepParallel {
		t.Fatalf("steps[1].type = %q, want %q", group.Type, StepParallel)
	}
	if group.MaxParallel == nil || *group.MaxParallel != 2 {
		t.Errorf("max_parallel = %v, want 2", group.MaxParallel)
	}
	if group.Timeout == nil || group.Timeout.Std() != 30*time.Minute {
		t.Errorf("group timeout = %v, want 30m", group.Timeout)
	}
	gotIDs := make([]string, 0, len(group.Steps))
	for _, sub := range group.Steps {
		gotIDs = append(gotIDs, sub.ID)
	}
	if want := []string{"test", "lint", "review"}; !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("sub-step ids = %v, want %v in declaration order", gotIDs, want)
	}
	if group.Steps[2].Check != "go vet ./..." {
		t.Errorf("sub-step check = %q, want it preserved", group.Steps[2].Check)
	}

	out, err := Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, _, err := Parse(out, Options{KnownAgents: []string{"claude"}})
	if err != nil {
		t.Fatalf("re-parse marshalled workflow: %v", err)
	}
	// Not a whole-struct DeepEqual: Marshal is canonical rather than
	// faithful, so an unset `env:` comes back as an empty map rather than a
	// nil one. What matters here is that the group kept its members.
	rt := back.Steps[1]
	rtIDs := make([]string, 0, len(rt.Steps))
	for _, sub := range rt.Steps {
		rtIDs = append(rtIDs, sub.ID)
	}
	if want := []string{"test", "lint", "review"}; !reflect.DeepEqual(rtIDs, want) {
		t.Errorf("round-tripped sub-step ids = %v, want %v", rtIDs, want)
	}
	if rt.MaxParallel == nil || *rt.MaxParallel != 2 {
		t.Errorf("round-tripped max_parallel = %v, want 2", rt.MaxParallel)
	}
	if rt.Steps[2].Prompt != group.Steps[2].Prompt {
		t.Errorf("round-tripped sub-step prompt = %q, want %q", rt.Steps[2].Prompt, group.Steps[2].Prompt)
	}
}

// TestParseFanOutStep covers the shape phase 2 exists to allow, including the
// two things the engine leans on: lane order is declaration order (it is the
// merge order, decision 7), and lanes survive Marshal — the snapshot a child
// is cut from goes through it.
//
// It also pins decision 5: a lane's inline steps may themselves fan out, to
// any depth. The bound on that is a creation-time check against
// `fan_out.max_depth`, not a parse-time ban.
func TestParseFanOutStep(t *testing.T) {
	src := strings.TrimSpace(`
name: build-all
steps:
  - id: build
    type: fan_out
    merge:
      on_conflict: agent
      agent:
        id: resolve
        prompt: "Resolve the conflict in {{.Task.Title}}."
        check: go build ./...
    lanes:
      - id: api
        workflow: implement-module
        fields: { module: api }
        model: opus
        priority: 5
      - id: docs
        steps:
          - id: write
            type: agent
            prompt: Document the API.
          - id: deep
            type: fan_out
            lanes:
              - { id: inner, workflow: proofread }
`) + "\n"
	wf, _, err := Parse([]byte(src), Options{KnownAgents: []string{"claude"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	step := wf.Steps[0]
	if step.Type != StepFanOut {
		t.Fatalf("type = %q, want %q", step.Type, StepFanOut)
	}
	if got := []string{step.Lanes[0].ID, step.Lanes[1].ID}; got[0] != "api" || got[1] != "docs" {
		t.Errorf("lane order = %v, want [api docs] as declared", got)
	}
	if step.Lanes[0].Fields["module"] != "api" {
		t.Errorf("lane fields = %v, want module=api", step.Lanes[0].Fields)
	}
	if step.Lanes[0].Model != "opus" || step.Lanes[0].Priority == nil || *step.Lanes[0].Priority != 5 {
		t.Errorf("lane overrides not decoded: model=%q priority=%v",
			step.Lanes[0].Model, step.Lanes[0].Priority)
	}
	if step.ConflictPolicy() != ConflictAgent || step.Merge.Agent == nil {
		t.Fatalf("conflict policy = %q, resolver = %v", step.ConflictPolicy(), step.Merge.Agent)
	}
	if step.Merge.Agent.Check != "go build ./..." {
		t.Errorf("resolver check = %q, want it preserved", step.Merge.Agent.Check)
	}
	// Depth 2: a lane's inline steps fan out again.
	if inner := step.Lanes[1].Steps[1]; inner.Type != StepFanOut || inner.Lanes[0].ID != "inner" {
		t.Errorf("nested fan_out not parsed: %+v", inner)
	}

	out, err := Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, _, err := Parse(out, Options{KnownAgents: []string{"claude"}})
	if err != nil {
		t.Fatalf("re-parse marshalled workflow: %v", err)
	}
	rt := back.Steps[0]
	if len(rt.Lanes) != 2 || rt.Lanes[0].ID != "api" || rt.Lanes[1].ID != "docs" {
		t.Errorf("round-tripped lanes = %+v, want both in order", rt.Lanes)
	}
	if rt.Lanes[1].Steps[1].Lanes[0].ID != "inner" {
		t.Error("the depth-2 lane did not survive the round trip")
	}
	if rt.ConflictPolicy() != ConflictAgent || rt.Merge.Agent == nil ||
		rt.Merge.Agent.Prompt != step.Merge.Agent.Prompt {
		t.Errorf("round-tripped merge = %+v, want the resolver preserved", rt.Merge)
	}
}

// TestDefaultConflictPolicyIsBlock: a fan_out that says nothing about
// conflicts blocks on one. An agent resolving a semantic conflict silently is
// the outcome decision 8 refuses to make the default.
func TestDefaultConflictPolicyIsBlock(t *testing.T) {
	src := "name: x\nsteps:\n  - id: f\n    type: fan_out\n    lanes: [{id: api, workflow: build}]\n"
	wf, _, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := wf.Steps[0].ConflictPolicy(); got != ConflictBlock {
		t.Errorf("default conflict policy = %q, want %q", got, ConflictBlock)
	}
}

// TestMarshalOmitsEmptyGroupFields: `steps:` and `max_parallel:` are omitempty
// so a marshalled snapshot does not give every agent and command step an empty
// group — which would then have to be ignored by everything reading it.
func TestMarshalOmitsEmptyGroupFields(t *testing.T) {
	wf, _, err := Parse([]byte("name: x\nsteps:\n  - {id: a, type: command, run: ls}\n"), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"steps:\n    -", "max_parallel"} {
		if strings.Contains(string(out), key) {
			t.Errorf("marshalled a command step with %q:\n%s", key, out)
		}
	}
}

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

func TestBuiltinCreateWorkflowIsValid(t *testing.T) {
	e, ok := builtins()[CreateWorkflowName]
	if !ok {
		t.Fatalf("builtins() has no %q entry", CreateWorkflowName)
	}
	if !e.Valid() {
		t.Fatalf("built-in create-workflow is invalid: %v", e.Errors)
	}
	if e.Scope != ScopeBuiltin || e.File != "" {
		t.Errorf("scope = %q, file = %q; want builtin scope and no file", e.Scope, e.File)
	}
	if len(e.Workflow.Steps) != 1 || e.Workflow.Steps[0].Type != StepAgent {
		t.Fatalf("create-workflow steps = %+v, want one agent step", e.Workflow.Steps)
	}
	if mr := e.Workflow.Steps[0].MaxRetries; mr == nil || *mr != 0 {
		t.Errorf("create-workflow max_retries = %v, want 0 (a replay would find the first file)", mr)
	}
	// The prompt tells the agent it may stop and ask; the YAML has to agree,
	// and say so rather than leaning on the engine's fallback (decision 9).
	if got := e.Workflow.Steps[0].OnInput; got != InputWait {
		t.Errorf("create-workflow on_input = %q, want %q", got, InputWait)
	}
	if len(e.Workflow.Fields) != 2 {
		t.Fatalf("fields = %+v, want the name and the destination switch", e.Workflow.Fields)
	}
	name := e.Workflow.Fields[0]
	if name.Name != "workflow_name" || name.Type != FieldString || !name.Required {
		t.Errorf("field[0] = %+v, want a required string named workflow_name", name)
	}
	if f := e.Workflow.Fields[1]; f.Name != "global" || f.Type != FieldBoolean || f.Required {
		t.Errorf("field[1] = %+v, want an optional boolean named global", f)
	}

	// The name is also a file name, so the pattern has to reject what a file
	// name cannot carry while still accepting the whole slug vocabulary.
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"release", true},
		{"feature-pr", true},
		{"release.v2", true},
		{"deploy_prod", true},
		{"Release", false},
		{"feature pr", false},
		{"../escape", false},
		{"a/b", false},
		{"-leading", false},
		{"", false},
	} {
		errs := e.Workflow.ValidateTaskFields(map[string]string{"workflow_name": tc.value})
		if got := len(errs) == 0; got != tc.want {
			t.Errorf("workflow_name %q accepted = %v, want %v (%v)", tc.value, got, tc.want, errs)
		}
	}
}

// The skill is prose that the step renders as a template, so a "{{" arriving
// in it must not become an action. Nothing in the skill opens one today; this
// is the guard for the day one does (task 024 decision 8).
func TestCreateWorkflowSkillSplicingIsTemplateSafe(t *testing.T) {
	const src = "---\nname: x\ndescription: y\n---\n\nUse {{.Task.Title}} and a lone }} here.\n"

	body := skillInstructions(src)
	if strings.HasPrefix(body, "---") {
		t.Fatalf("front matter survived: %q", body)
	}

	indented := indentBlock(EscapeTemplate(body), promptIndent)
	for _, line := range strings.Split(indented, "\n") {
		if line != "" && !strings.HasPrefix(line, promptIndent) {
			t.Errorf("line %q is not indented into the block scalar", line)
		}
	}

	got, err := Render("prompt", indented, RenderContext{})
	if err != nil {
		t.Fatalf("Render() error = %v; an escaped skill must never execute", err)
	}
	if !strings.Contains(got, "{{.Task.Title}}") || !strings.Contains(got, "lone }} here") {
		t.Errorf("rendered = %q, want the braces back as literal text", got)
	}
}

// The destination is the whole point of the `global` field, so both branches
// are rendered rather than eyeballed. An unset field renders as the project
// branch, which is what makes the switch safe to leave blank (task 024).
func TestBuiltinCreateWorkflowDestinationBranches(t *testing.T) {
	prompt := builtins()[CreateWorkflowName].Workflow.Steps[0].Prompt

	for _, tc := range []struct {
		name       string
		fields     map[string]string
		wantSubstr string
		notSubstr  string
	}{
		{
			name:       "unset renders the project registry",
			fields:     map[string]string{"workflow_name": "release"},
			wantSubstr: "/repo/root/.vincent/workflows",
			notSubstr:  "vincent doctor --json",
		},
		{
			name:       "false renders the project registry",
			fields:     map[string]string{"workflow_name": "release", "global": "false"},
			wantSubstr: "/repo/root/.vincent/workflows",
			notSubstr:  "vincent doctor --json",
		},
		{
			name:       "true renders the global registry",
			fields:     map[string]string{"workflow_name": "release", "global": "true"},
			wantSubstr: "vincent doctor --json",
			notSubstr:  "/repo/root/.vincent/workflows",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := RenderContext{
				Task:    TaskContext{Title: "add a release workflow", Fields: tc.fields},
				Project: ProjectContext{Name: "vincent", Path: "/repo/root"},
			}
			got, err := Render("prompt", prompt, rc)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("rendered prompt does not mention %q:\n%s", tc.wantSubstr, got)
			}
			if strings.Contains(got, tc.notSubstr) {
				t.Errorf("rendered prompt mentions the other branch (%q):\n%s", tc.notSubstr, got)
			}
			// The skill is spliced in, not summarized, and its front
			// matter is not.
			if !strings.Contains(got, "Choose the cheapest correct primitive") {
				t.Errorf("rendered prompt does not carry the embedded skill:\n%s", got)
			}
			if strings.Contains(got, "name: vincent-workflows") {
				t.Errorf("rendered prompt still carries the skill's front matter:\n%s", got)
			}
			// The declared name reaches the prompt, so the agent is told
			// which name to use rather than inventing one.
			if !strings.Contains(got, "use release\nverbatim") {
				t.Errorf("rendered prompt does not carry workflow_name:\n%s", got)
			}
		})
	}
}
