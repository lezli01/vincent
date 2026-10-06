package taskrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// A step's agent ends its turn on work it left in the background just as a
// chat's does (task 133): the step's result is what it said once the work
// finished, not the "I'll report back" it said before.
func TestAgentStepWaitsForBackgroundWork(t *testing.T) {
	h := newEngineHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "background")
	task := h.createTask(t, `name: suite
steps:
  - id: verify
    type: agent
    prompt: run the suite
`)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", final.State, final.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].ResultSummary != "the suite passed" {
		t.Fatalf("step runs = %+v, want one whose result is the answer after the suite", runs)
	}
}
