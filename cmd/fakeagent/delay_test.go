package main_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestDelayStretchesSuccessfulRun covers FAKEAGENT_DELAY_MS in both dialects.
// The knob exists so the M3 gate (T3.8) can put tasks on the board that are
// still running when a human looks, and an untested delay fails silently as
// "the gate felt fast" — which is indistinguishable from passing.
func TestDelayStretchesSuccessfulRun(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"claude", []string{"-p", "--output-format", "stream-json"}},
		{"codex", []string{"exec", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// 1200ms spends one full tick and one partial, so the ticking
			// loop is exercised rather than a single terminal sleep.
			start := time.Now()
			out := runAgent(t, bin, tc.args, "FAKEAGENT_DELAY_MS=1200")
			elapsed := time.Since(start)

			if elapsed < 1200*time.Millisecond {
				t.Errorf("run finished in %s, want at least 1.2s", elapsed)
			}
			if got := strings.Count(out, "still working"); got != 2 {
				t.Errorf("progress lines = %d, want 2 (one per tick)\n%s", got, out)
			}
			// The delay must not cost the run its normal ending: a task that
			// never reports success is a failed step, not a slow one.
			if !strings.Contains(out, "done: ") {
				t.Errorf("delayed run did not complete normally:\n%s", out)
			}
		})
	}
}

// TestDelayAbsentOrInvalidIsNoDelay pins the fail-open rule: this is test
// scaffolding, so a typo in a gate script must not hang or fail a suite.
func TestDelayAbsentOrInvalidIsNoDelay(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"unset", nil},
		{"empty", []string{"FAKEAGENT_DELAY_MS="}},
		{"not a number", []string{"FAKEAGENT_DELAY_MS=soon"}},
		{"negative", []string{"FAKEAGENT_DELAY_MS=-5000"}},
		{"zero", []string{"FAKEAGENT_DELAY_MS=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Asserted on the output, not the clock: an upper bound on wall
			// time measures how loaded the machine is (process spawn alone
			// can exceed a second under a parallel ./... run), while the
			// progress lines are the delay's only observable — no ticks
			// emitted means no ticks slept.
			out := runAgent(t, bin, []string{"-p", "--output-format", "stream-json"}, tc.env...)
			if strings.Contains(out, "still working") {
				t.Errorf("emitted progress lines with no delay configured:\n%s", out)
			}
		})
	}
}

// runAgent runs fakeagent to completion with the prompt on stdin and returns
// its stdout.
func runAgent(t *testing.T, bin string, args []string, env ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader("do a thing")
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run fakeagent %v: %v", args, err)
	}
	return string(out)
}
