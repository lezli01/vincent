package taskrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestParallelGroupWaitSubStepsRunAsDeny is issue #965. `awaiting_input`
// holds one pending request for the whole task (§7.4), which is why task 014
// decision 19 rejects `on_input: require` inside a group — and the same holds
// for `wait`, the default. Two sub-steps that both ask would race on the one
// task state: the first parks the task, the second's RequestInput is invalid
// from awaiting_input, is logged and dropped, and that agent idles until its
// timeout. A sub-step resolving to `wait` runs as `deny` instead: each
// question is auto-answered, the task never leaves running, and both
// sub-steps finish.
//
// One sub-step leaves on_input unset (the default) and one says `wait`
// explicitly; both are clamped.
func TestParallelGroupWaitSubStepsRunAsDeny(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	h := newEngineHarness(t)
	h.start(t)
	snapshot := groupSnapshot("max_parallel: 2",
		agentStep("left", "max_retries: 0"),
		agentStep("right", "max_retries: 0", "on_input: wait"),
	)
	task := h.createTask(t, snapshot)

	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked, store.TaskAwaitingInput)
	if final.State != store.TaskDone {
		t.Fatalf("task reached %s (block_reason %q, pending_input %q), want done: "+
			"a group sub-step under on_input: wait must run as deny",
			final.State, final.BlockReason, final.PendingInputJSON)
	}
	if p := awaitingInputPayload(h.statePayloads(t, task.ID)); p != nil {
		t.Errorf("a group sub-step parked the whole task in awaiting_input: %v", p)
	}
	byStep := subRuns(h.stepRuns(t, task.ID))
	for _, id := range []string{"left", "right"} {
		runs := byStep[id]
		if len(runs) != 1 || runs[0].State != store.StepSucceeded {
			t.Errorf("sub-step %s runs = %+v, want one succeeded attempt", id, runs)
			continue
		}
		if !containsAll(runs[0].ResultSummary, "no user is available") {
			t.Errorf("sub-step %s result %q does not echo the deny-mode canned answer", id, runs[0].ResultSummary)
		}
		if runs[0].InputWaitMS != 0 {
			t.Errorf("sub-step %s input_wait_ms = %d, want 0 (never parked)", id, runs[0].InputWaitMS)
		}
	}
}
