package workflow

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// loopOpts is the validation surface a loop is judged against: a known agent
// and the `loop.max_iterations` ceiling decision 5 validates `count:` on.
func loopOpts(ceiling int) Options {
	return Options{
		KnownAgents:   []string{"claude"},
		MaxIterations: func() int { return ceiling },
	}
}

// TestParseLoopStep covers the shape §7.8 exists to allow, and the two things
// the engine leans on: the body keeps declaration order (it is a sequence),
// and a loop survives Marshal — `edit + retry` rewrites the snapshot through
// it, and decision 12 says such an edit applies to every remaining iteration.
func TestParseLoopStep(t *testing.T) {
	src := strings.TrimSpace(`
name: converge
steps:
  - id: green
    name: Fix until green
    type: loop
    count: 5
    timeout: 90m
    steps:
      - {id: suite, type: command, run: go test ./..., allow_failure: true, max_retries: 0}
      - {id: passed, type: break, if: '{{ eq (index .Steps "suite").ExitCode 0 }}'}
      - {id: repair, type: agent, prompt: "The suite is red: {{ (index .Steps \"suite\").Result }}"}
`) + "\n"
	wf, _, err := Parse([]byte(src), loopOpts(10))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	loop := wf.Steps[0]
	if loop.Type != StepLoop {
		t.Fatalf("steps[0].type = %q, want %q", loop.Type, StepLoop)
	}
	if loop.Count == nil || *loop.Count != 5 {
		t.Errorf("count = %v, want 5", loop.Count)
	}
	if loop.Timeout == nil || loop.Timeout.Std() != 90*time.Minute {
		t.Errorf("loop timeout = %v, want 90m", loop.Timeout)
	}
	gotIDs := make([]string, 0, len(loop.Steps))
	for _, body := range loop.Steps {
		gotIDs = append(gotIDs, body.ID)
	}
	if want := []string{"suite", "passed", "repair"}; !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("body ids = %v, want %v in declaration order", gotIDs, want)
	}
	if !loop.Steps[0].AllowFailure {
		t.Error("allow_failure on a body step was dropped; it is how a red probe becomes data a break can read")
	}

	out, err := Marshal(wf)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, _, err := Parse(out, loopOpts(10))
	if err != nil {
		t.Fatalf("re-parse marshalled workflow: %v", err)
	}
	rt := back.Steps[0]
	if rt.Count == nil || *rt.Count != 5 {
		t.Errorf("round-tripped count = %v, want 5", rt.Count)
	}
	rtIDs := make([]string, 0, len(rt.Steps))
	for _, body := range rt.Steps {
		rtIDs = append(rtIDs, body.ID)
	}
	if !reflect.DeepEqual(rtIDs, gotIDs) {
		t.Errorf("round-tripped body ids = %v, want %v", rtIDs, gotIDs)
	}
	if rt.Steps[1].If != loop.Steps[1].If {
		t.Errorf("round-tripped break guard = %q, want %q", rt.Steps[1].If, loop.Steps[1].If)
	}
}

// TestParseForEachSpellings pins that a scalar and a sequence decode to the
// same slice: they are one mechanism, and the engine renders both the same
// way (§7.8).
func TestParseForEachSpellings(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want ForEach
	}{
		{
			name: "scalar template",
			yaml: `for_each: '{{ .Steps.changed.Result }}'`,
			want: ForEach{"{{ .Steps.changed.Result }}"},
		},
		{
			name: "inline sequence",
			yaml: `for_each: [api, web, cli]`,
			want: ForEach{"api", "web", "cli"},
		},
		{
			name: "block sequence",
			yaml: "for_each:\n      - api\n      - web",
			want: ForEach{"api", "web"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := strings.TrimSpace(`
name: each
steps:
  - id: review-each
    type: loop
    `+tt.yaml+`
    steps:
      - {id: read, type: command, run: 'echo {{ .Loop.Item }}'}
`) + "\n"
			wf, _, err := Parse([]byte(src), loopOpts(10))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := wf.Steps[0].ForEach; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("for_each = %#v, want %#v", got, tt.want)
			}
			// The snapshot goes through Marshal, so the canonical sequence
			// spelling has to come back as the same list.
			out, err := Marshal(wf)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			back, _, err := Parse(out, loopOpts(10))
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			if got := back.Steps[0].ForEach; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("round-tripped for_each = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestValidateLoop(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "no driver",
			src: `
  - id: spin
    type: loop
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "exactly one driver",
		},
		{
			name: "both drivers",
			src: `
  - id: spin
    type: loop
    count: 2
    for_each: [a, b]
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "either count or for_each, not both",
		},
		{
			name: "empty body",
			src: `
  - id: spin
    type: loop
    count: 2
    steps: []`,
			want: "at least one body step",
		},
		{
			name: "count over the ceiling",
			src: `
  - id: spin
    type: loop
    count: 5000
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "count 5000 exceeds the 10-iteration ceiling",
		},
		{
			name: "count over its own raised ceiling",
			src: `
  - id: spin
    type: loop
    count: 30
    max_iterations: 25
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "count 30 exceeds the 25-iteration ceiling",
		},
		{
			name: "count under a raised ceiling is fine",
			src: `
  - id: spin
    type: loop
    count: 20
    max_iterations: 25
    steps:
      - {id: work, type: command, run: "true"}`,
		},
		{
			name: "zero count",
			src: `
  - id: spin
    type: loop
    count: 0
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "count must be at least 1",
		},
		{
			name: "zero max_iterations",
			src: `
  - id: spin
    type: loop
    for_each: [a]
    max_iterations: 0
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "max_iterations must be at least 1",
		},
		{
			name: "a loop has no retry budget of its own",
			src: `
  - id: spin
    type: loop
    count: 2
    max_retries: 3
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "max_retries is not valid on a loop step",
		},
		{
			name: "allow_failure on a loop would be a silent loop_limit",
			src: `
  - id: spin
    type: loop
    count: 2
    allow_failure: true
    steps:
      - {id: work, type: command, run: "true"}`,
			want: "allow_failure is not valid on a loop step",
		},
		{
			name: "manual in a body",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - {id: gate, type: manual, instructions: look}`,
			want: "manual steps are not valid inside a loop body",
		},
		{
			name: "fan_out in a body",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - id: spread
        type: fan_out
        lanes:
          - {id: one, steps: [{id: inner, type: command, run: "true"}]}`,
			want: "fan_out steps are not valid inside a loop body",
		},
		{
			name: "parallel in a body",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - id: both
        type: parallel
        steps:
          - {id: a, type: command, run: "true"}`,
			want: "parallel groups are not valid inside a loop body",
		},
		{
			name: "loops do not nest",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - id: inner
        type: loop
        count: 2
        steps:
          - {id: work, type: command, run: "true"}`,
			want: "loops do not nest",
		},
		{
			name: "on_input require in a body",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - {id: ask, type: agent, prompt: hi, on_input: require}`,
			want: "is not valid inside a loop body",
		},
		{
			name: "condition is permitted in a body",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - {id: enough, type: condition, if: '{{ true }}'}
      - {id: work, type: command, run: "true"}`,
		},
		{
			name: "body ids collide with the rest of the workflow",
			src: `
  - id: build
    type: command
    run: "true"
  - id: spin
    type: loop
    count: 2
    steps:
      - {id: build, type: command, run: "true"}`,
			want: `duplicate step id "build"`,
		},
		{
			name: "count is not a field of an ordinary step",
			src: `
  - id: build
    type: command
    run: "true"
    count: 3`,
			want: "count is not valid on a command step",
		},
		{
			name: "for_each is not a field of a group",
			src: `
  - id: both
    type: parallel
    for_each: [a]
    steps:
      - {id: a, type: command, run: "true"}`,
			want: "for_each is not valid on a parallel step",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "name: t\nsteps:" + tt.src + "\n"
			_, _, err := Parse([]byte(src), loopOpts(10))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Parse accepted %s, want an error mentioning %q", tt.name, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestValidateBreak(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "break needs a guard",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - {id: stop, type: break}`,
			want: "break steps require an if expression",
		},
		{
			name: "break carries nothing else",
			src: `
  - id: spin
    type: loop
    count: 2
    steps:
      - {id: stop, type: break, if: '{{ true }}', timeout: 5m, run: "true"}`,
			want: "is not valid on a break step",
		},
		{
			name: "break at the top level",
			src: `
  - id: stop
    type: break
    if: '{{ true }}'`,
			want: "break steps are only valid inside a loop body",
		},
		{
			name: "break inside a parallel group",
			src: `
  - id: both
    type: parallel
    steps:
      - {id: stop, type: break, if: '{{ true }}'}`,
			want: "break steps are only valid inside a loop body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "name: t\nsteps:" + tt.src + "\n"
			_, _, err := Parse([]byte(src), loopOpts(10))
			if err == nil {
				t.Fatalf("Parse accepted %s, want an error mentioning %q", tt.name, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestLoopCeilingUnsetSkipsTheCheck pins that a nil MaxIterations disables the
// count check rather than treating the ceiling as zero — every snapshot
// re-parse in the daemon goes through Options{}, and a snapshot that blew a
// ceiling apart would otherwise become unrunnable rather than merely bounded.
func TestLoopCeilingUnsetSkipsTheCheck(t *testing.T) {
	src := strings.TrimSpace(`
name: t
steps:
  - id: spin
    type: loop
    count: 500
    max_iterations: 500
    steps:
      - {id: work, type: command, run: "true"}
`) + "\n"
	if _, _, err := Parse([]byte(src), Options{}); err != nil {
		t.Fatalf("Parse with no ceiling: %v", err)
	}
}
