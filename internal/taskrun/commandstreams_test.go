package taskrun

// The two channels a command step's output feeds are not the same channel
// (§8.4, issue #311): `result_summary` is what a human reads — the board, the
// detail view, the repair prompt — and carries **both** streams, while the
// stdout tail is the value `.Steps.<id>.Result` hands a template. Splitting
// them is the fix; keeping `result_summary` whole is half of it, because a
// step that failed with a stderr-only diagnostic must not summarize as blank.

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

func TestCommandStepKeepsBothStreamsInResultSummary(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := "name: streams\nsteps:\n" +
		commandStep("emit", noisyEmit("to-stderr", "to-stdout"), "max_retries: 0")
	task := h.createTask(t, snapshot)

	if done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked); done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1", len(runs))
	}
	run := runs[0]
	for _, want := range []string{"to-stdout", "to-stderr"} {
		if !strings.Contains(run.ResultSummary, want) {
			t.Errorf("result_summary = %q, want it to carry %q — it is what a human reads on a failure",
				run.ResultSummary, want)
		}
	}
	if run.StdoutTail == nil {
		t.Fatalf("stdout_tail is nil on a command attempt; §8.4's `.Result` has nothing to render from")
	}
	if got := strings.TrimSpace(*run.StdoutTail); got != "to-stdout" {
		t.Errorf("stdout_tail = %q, want %q", got, "to-stdout")
	}
}

// A command that writes nothing to stdout has an **empty** `.Result`, not a
// missing one: the tail is recorded, so the fallback to `result_summary` — the
// pre-0025 path — is not reached and the stderr text does not leak back in.
func TestCommandStepRecordsEmptyStdoutTail(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := "name: quiet\nsteps:\n" +
		commandStep("emit", script(
			"printf '%s\\n' 'only-stderr' >&2",
			"[Console]::Error.WriteLine('only-stderr')"), "max_retries: 0")
	task := h.createTask(t, snapshot)

	if done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked); done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1", len(runs))
	}
	if runs[0].StdoutTail == nil {
		t.Fatalf("stdout_tail is nil; a command that wrote no stdout still recorded a tail")
	}
	if got := strings.TrimSpace(*runs[0].StdoutTail); got != "" {
		t.Errorf("stdout_tail = %q, want empty", got)
	}
}
