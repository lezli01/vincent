package workflow

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// retryFieldsCases carries `max_retries` or `retry_backoff` on a `parallel`
// and a `manual` step — the documents issue #374 is about.
var retryFieldsCases = []struct {
	name, src, field, typ string
}{
	{
		name: "parallel max_retries",
		src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    max_retries: 3\n" +
			"    steps:\n      - {id: t, type: command, run: go test ./...}\n",
		field: "max_retries", typ: StepParallel,
	},
	{
		name: "parallel retry_backoff",
		src: "name: x\nsteps:\n  - id: g\n    type: parallel\n    retry_backoff: 30s\n" +
			"    steps:\n      - {id: t, type: command, run: go test ./...}\n",
		field: "retry_backoff", typ: StepParallel,
	},
	{
		name:  "manual max_retries",
		src:   "name: x\nsteps:\n  - {id: m, type: manual, instructions: hi, max_retries: 3}\n",
		field: "max_retries", typ: StepManual,
	},
	{
		name:  "manual retry_backoff",
		src:   "name: x\nsteps:\n  - {id: m, type: manual, instructions: hi, retry_backoff: 30s}\n",
		field: "retry_backoff", typ: StepManual,
	},
}

// TestRetryFieldsRejectedOnStepsWithoutAnAttempt pins issue #374. A
// `parallel` group owns no attempt of its own (task 014 decisions 17, 18) and
// a `manual` gate is entered once and decided by a human (§6) — neither
// reaches runStepWithRetries, so `max_retries` and `retry_backoff` on either
// bind to nothing. §8.2 already rejects them on a `loop` for exactly that
// reason; an authored document must be refused the same way rather than
// accepted and ignored.
func TestRetryFieldsRejectedOnStepsWithoutAnAttempt(t *testing.T) {
	for _, tt := range retryFieldsCases {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tt.src), Options{Authored: true})
			if err == nil {
				t.Fatalf("Parse accepted %s on a %s step, which owns no attempt for it to bind to", tt.field, tt.typ)
			}
			var errs Errors
			if !errors.As(err, &errs) {
				t.Fatalf("Parse error = %T %v, want Errors", err, err)
			}
			want := tt.field + " is not valid on a " + tt.typ + " step"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("errors = %q, want a message containing %q", err.Error(), want)
			}
			if !hasPath(errs, "steps[0]."+tt.field) {
				t.Errorf("errors = %v, want one at path %q", errs, "steps[0]."+tt.field)
			}
		})
	}
}

// TestSnapshotWithRetryFieldsOnStepsWithoutAnAttemptStillParses is the
// compatibility half of issue #374. A task created before the rejection may
// carry either field on a `parallel` or `manual` step — typed by hand, or
// written there by include expansion — and every path that re-reads a
// snapshot parses it with Options{}. The rejection is for documents a person
// writes (Options.Authored, the task 080 decision 5 mechanism), so such a
// task must keep loading, with the value as ignored as it always was.
func TestSnapshotWithRetryFieldsOnStepsWithoutAnAttemptStillParses(t *testing.T) {
	for _, tt := range retryFieldsCases {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Parse([]byte(tt.src), Options{}); err != nil {
				t.Fatalf("snapshot parse refused %s on a %s step: %v", tt.field, tt.typ, err)
			}
		})
	}
}

// TestExpandKeepsRetryDefaultsOffStepsWithoutAnAttempt is the include half of
// issue #374. materialise writes a callee's `defaults:` onto its steps, and it
// must not write `max_retries` or `retry_backoff` onto a `parallel` or
// `manual` step — that would produce a snapshot §8.2 rejects, the way it
// already declines to for `loop`. `timeout` still binds to both, and a
// group's sub-steps still inherit all three.
func TestExpandKeepsRetryDefaultsOffStepsWithoutAnAttempt(t *testing.T) {
	lookup := registry(t, map[string]string{
		"strict": "name: strict\ndefaults: {max_retries: 0, timeout: 30m, retry_backoff: 45s}\n" +
			"steps:\n" +
			"  - id: group\n    type: parallel\n" +
			"    steps:\n      - {id: sub, type: command, run: make}\n" +
			"  - {id: gate, type: manual, instructions: look}\n",
	})
	got, err := Expand(mustParse(t, `
name: root
steps:
  - {id: c, type: include, workflow: strict}
`), expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	for _, step := range got.Steps {
		if step.MaxRetries != nil {
			t.Errorf("%s step %q max_retries = %d, want unset: it owns no attempt", step.Type, step.ID, *step.MaxRetries)
		}
		if step.RetryBackoff != nil {
			t.Errorf("%s step %q retry_backoff = %s, want unset: it owns no attempt", step.Type, step.ID, step.RetryBackoff)
		}
		if step.Timeout == nil || step.Timeout.Std() != 30*time.Minute {
			t.Errorf("%s step %q timeout = %v, want the callee's 30m", step.Type, step.ID, step.Timeout)
		}
	}
	sub := got.Steps[0].Steps[0]
	if sub.MaxRetries == nil || *sub.MaxRetries != 0 {
		t.Errorf("sub-step max_retries = %v, want the callee's 0", sub.MaxRetries)
	}
	if sub.RetryBackoff == nil || sub.RetryBackoff.Std() != 45*time.Second {
		t.Errorf("sub-step retry_backoff = %v, want the callee's 45s", sub.RetryBackoff)
	}
}
