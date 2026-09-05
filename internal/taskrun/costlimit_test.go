package taskrun

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// fakeAgentCostUSD is what the fake agent's claude dialect reports on its
// terminal result line (cmd/fakeagent's emitSuccessResult). Every cap in this
// file is chosen against it, which is why no new scenario is needed: the
// money is already real as far as the engine is concerned.
const fakeAgentCostUSD = 0.0123

// costCappedWorkflow is an agent step that spends, followed by a command step
// that must not run once the cap has tripped.
const costCappedWorkflow = `name: pricey
steps:
  - id: think
    type: agent
    max_retries: 0
    prompt: spend some money
  - id: after
    type: command
    max_retries: 0
    run: |
      exit 0
`

// TestEngineCostCapBlocksTheTask is task 033's done-when: a task that has
// spent past `max_task_cost_usd` blocks `cost_limit` at the next attempt
// boundary, and nothing further runs (§12.3, §17, §18).
//
// The step row is the whole "block, not fail" claim: it stays `succeeded`
// with no failure reason, because a budget overrun is a policy stop rather
// than something the step did wrong.
func TestEngineCostCapBlocksTheTask(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTaskCostUSD = fakeAgentCostUSD / 2 // one attempt is already over
	})
	task := h.createTask(t, costCappedWorkflow)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked {
		t.Fatalf("task = %s (%s), want blocked", final.State, final.BlockReason)
	}
	if final.BlockReason != ReasonCostLimit {
		t.Errorf("block reason = %q, want %q", final.BlockReason, ReasonCostLimit)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1 — the step after the cap must not run: %+v", len(runs), runs)
	}
	if runs[0].State != store.StepSucceeded || runs[0].FailureReason != "" {
		t.Errorf("step run = %s/%q, want succeeded with no failure reason — the cap blocks, it does not fail",
			runs[0].State, runs[0].FailureReason)
	}
	if runs[0].CostUSD == nil || *runs[0].CostUSD != fakeAgentCostUSD {
		t.Errorf("recorded cost = %v, want %v — the cap must be reading the attempt's own spend",
			runs[0].CostUSD, fakeAgentCostUSD)
	}
	// The cursor stays where it is, which is what lets a retry resume the
	// step this task is blocked at rather than skipping it.
	if final.CurrentStep != 0 {
		t.Errorf("current_step = %d, want 0 — a blocked task stays at the step it is on", final.CurrentStep)
	}
}

// TestEngineCostCapOffOrGenerousRunsToDone pins the other half: a cap the task
// stays under changes nothing, and the default — unset, zero — is genuinely
// off rather than a cap of nothing.
func TestEngineCostCapOffOrGenerousRunsToDone(t *testing.T) {
	for name, capUSD := range map[string]float64{
		"unset is off":        0,
		"cap above the spend": fakeAgentCostUSD * 100,
		"cap equal the spend": fakeAgentCostUSD, // the check is `>`, not `>=`
	} {
		t.Run(name, func(t *testing.T) {
			h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxTaskCostUSD = capUSD })
			task := h.createTask(t, costCappedWorkflow)
			h.start(t)

			final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
			if final.State != store.TaskDone {
				t.Fatalf("task = %s (%s), want done", final.State, final.BlockReason)
			}
			if runs := h.stepRuns(t, task.ID); len(runs) != 2 {
				t.Fatalf("step runs = %d, want 2 — both steps run: %+v", len(runs), runs)
			}
		})
	}
}

// TestEngineCostCapConsumesNoRetry covers §7.2's budget and the escape hatch
// in one: the block costs the step no retry, and a `retry` that does not
// raise the cap advances by exactly one attempt before blocking here again.
//
// That is the whole answer to the issue's open question. `resume` is valid
// only from `paused`; from `blocked` the human actions are retry, repair,
// skip and cancel, and because the cap is checked only *after* an attempt
// finishes, a retry always makes one attempt of progress. No exemption flag,
// no second kind of invalid action.
func TestEngineCostCapConsumesNoRetry(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTaskCostUSD = fakeAgentCostUSD / 2
	})
	task := h.createTask(t, costCappedWorkflow)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonCostLimit {
		t.Fatalf("task = %s (%s), want blocked cost_limit", blocked.State, blocked.BlockReason)
	}
	ref := store.StepRef{TaskID: task.ID, StepIndex: 0, StepID: "think"}
	attempts, err := h.store.CountStepAttempts(t.Context(), ref, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Last != 1 || attempts.Failed != 0 {
		t.Fatalf("attempts = %+v, want one attempt and no failure — the cap consumes no retry", attempts)
	}

	if _, _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	reblocked := h.waitForState(t, task.ID, store.TaskBlocked)
	if reblocked.BlockReason != ReasonCostLimit {
		t.Errorf("block reason after retry = %q, want %q", reblocked.BlockReason, ReasonCostLimit)
	}
	attempts, err = h.store.CountStepAttempts(t.Context(), ref, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Last != 2 {
		t.Errorf("attempts after retry = %d, want 2 — a retry buys exactly one attempt", attempts.Last)
	}
	if runs := h.stepRuns(t, task.ID); len(runs) != 2 {
		t.Errorf("step runs = %d, want 2 — the step after the cap still must not run", len(runs))
	}
}

// TestEngineCostCapBeatsAStepFailureWithBudgetLeft: when an attempt fails
// with retries remaining *and* the task is over its cap, the cap wins. The
// due retry never runs — retrying spends more money to arrive at the same
// wall — and task 028's paced re-queue is pre-empted with it, because a paced
// retry is still a retry.
//
// The failed row keeps its own state and its own reason, so the timeline
// still says what broke while `block_reason` says why nothing further was
// tried.
func TestEngineCostCapBeatsAStepFailureWithBudgetLeft(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTaskCostUSD = fakeAgentCostUSD / 2
	})
	// The scenario exits 3 *after* reporting its terminal result line, so the
	// attempt fails and still spends — which is the only way to reach this
	// boundary with budget left and a rollup already over.
	t.Setenv("FAKEAGENT_SCENARIO", "nonzero-exit")
	task := h.createTask(t, `name: failing-and-pricey
steps:
  - id: think
    type: agent
    max_retries: 2
    retry_backoff: 30s
    prompt: fail expensively
`)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked {
		t.Fatalf("task = %s (%s), want blocked", final.State, final.BlockReason)
	}
	if final.BlockReason != ReasonCostLimit {
		t.Errorf("block reason = %q, want %q — the cap outranks the failure", final.BlockReason, ReasonCostLimit)
	}
	if final.QueuedReason != "" {
		t.Errorf("queued reason = %q, want none — no retry_backoff hold may be taken", final.QueuedReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1 — the due retry must not run: %+v", len(runs), runs)
	}
	if runs[0].State != store.StepFailed || runs[0].FailureReason != ReasonNonzeroExit {
		t.Errorf("step run = %s/%q, want failed/nonzero_exit — the row keeps what actually broke",
			runs[0].State, runs[0].FailureReason)
	}
	if hasEvent(h.eventTypes(t, task.ID), eventStepRetrying) {
		t.Error("a step.retrying event was emitted; the retry was announced and then not taken")
	}
}

// TestEngineCostCapFiresInsideALoop is the test that distinguishes the chosen
// enforcement point from the rejected one. `runSteps` walks top-level
// positions only, so a check there would let a whole loop run before the cap
// was consulted once — the overshoot would be every remaining iteration
// rather than the one attempt the cap promises.
func TestEngineCostCapFiresInsideALoop(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTaskCostUSD = fakeAgentCostUSD / 2
	})
	task := h.createTask(t, `name: looping-and-pricey
steps:
  - id: grind
    type: loop
    count: 3
    steps:
      - id: think
        type: agent
        max_retries: 0
        prompt: iterate expensively
`)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked {
		t.Fatalf("task = %s (%s), want blocked", final.State, final.BlockReason)
	}
	if final.BlockReason != ReasonCostLimit {
		t.Errorf("block reason = %q, want %q", final.BlockReason, ReasonCostLimit)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1 — the loop must stop mid-flight, not after: %+v", len(runs), runs)
	}
	if runs[0].Iteration != 1 {
		t.Errorf("blocked on iteration %d, want 1", runs[0].Iteration)
	}
}

// TestEngineCostCapIsInertWithoutReportedCost: a task run entirely on an
// adapter that reports no cost never blocks on the cap, whatever it is set to
// (§9.3, §9.7). The guard is store.TaskRollup.HasCost rather than arithmetic,
// so "unreported" and "free" stay different facts and nothing is estimated
// from tokens.
func TestEngineCostCapIsInertWithoutReportedCost(t *testing.T) {
	for _, tc := range []struct{ agent, model string }{
		{"codex", "gpt-5.6-sol"},
		{"cursor", "claude-sonnet-5-thinking-high"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			h := newEngineHarnessWith(t, func(c *config.Config) {
				c.MaxTaskCostUSD = 0.000001 // as tight as a cap can be
			})
			task := h.createTask(t, "name: costless\nsteps:\n"+
				"  - id: think\n    type: agent\n"+
				"    agent: "+tc.agent+"\n    model: "+tc.model+"\n"+
				"    max_retries: 0\n    prompt: spend nothing measurable\n")
			h.start(t)

			final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
			if final.State != store.TaskDone {
				t.Fatalf("task = %s (%s), want done — %s reports no cost, so the cap is inert",
					final.State, final.BlockReason, tc.agent)
			}
			rollups, err := h.store.TaskRollups(t.Context(), []int64{task.ID})
			if err != nil {
				t.Fatalf("TaskRollups: %v", err)
			}
			if rollups[task.ID].HasCost {
				t.Errorf("rollup reports a cost for %s; an unreported cost must not become $0.00", tc.agent)
			}
		})
	}
}

// TestEngineCostCapRaisedByAReloadLetsARetryFinish is the documented remedy,
// end to end: raise the cap and retry. The engine reads the cap per check
// rather than caching it, so a hot reload (§12.3) reaches a daemon that is
// already running — no restart, and no state on the task to clear.
func TestEngineCostCapRaisedByAReloadLetsARetryFinish(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxTaskCostUSD = fakeAgentCostUSD / 2
	})
	task := h.createTask(t, costCappedWorkflow)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonCostLimit {
		t.Fatalf("task = %s (%s), want blocked cost_limit", blocked.State, blocked.BlockReason)
	}

	h.reload(func(c *config.Config) { c.MaxTaskCostUSD = fakeAgentCostUSD * 100 })
	if _, _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done once the cap was raised", final.State, final.BlockReason)
	}
}
