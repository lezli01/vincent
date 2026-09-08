package taskrun

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// agentStepSnapshot is a one-step agent workflow with its retry budget stated
// rather than defaulted, so both halves of task 003 are measured against a
// number this file chose: a usage limit must leave all of it, and an auth
// failure must spend all of it.
const agentStepSnapshot = `name: quota
steps:
  - id: implement
    type: agent
    max_retries: 1
    prompt: "Do the work"
`

// waitForHold polls until the task is queued carrying an admission hold.
func (h *engineHarness) waitForHold(t *testing.T, id int64) *store.Task {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last *store.Task
	for time.Now().Before(deadline) {
		task, err := h.store.GetTask(t.Context(), id)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		last = task
		if task.State == store.TaskQueued && task.AdmitNotBefore != nil {
			return task
		}
		if task.State == store.TaskBlocked || task.State == store.TaskDone {
			t.Fatalf("task %d reached %s (%q); want queued with an admission hold",
				id, task.State, task.BlockReason)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d never picked up an admission hold (state %s)", id, last.State)
	return nil
}

// TestUsageLimitReQueuesAndConsumesNoRetry is the core claim of task 003: a
// quota stop is recorded as an interrupted attempt, spends none of the retry
// budget, and parks the task on an admission hold instead of blocking it.
func TestUsageLimitReQueuesAndConsumesNoRetry(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	h := newEngineHarnessWith(t, func(c *config.Config) {
		// Long enough that the hold cannot expire during the test, so what is
		// asserted is the hold itself and not a race with the 5 s tick.
		c.UsageLimitRecheckInterval = config.Duration(time.Hour)
	})
	task := h.createTask(t, agentStepSnapshot)
	before := time.Now()
	h.start(t)

	held := h.waitForHold(t, task.ID)
	if held.QueuedReason != ReasonUsageLimit {
		t.Errorf("queued_reason = %q, want %s", held.QueuedReason, ReasonUsageLimit)
	}
	if held.BlockReason != "" {
		t.Errorf("block_reason = %q; a held task is not blocked", held.BlockReason)
	}
	// No reset time was reported, so the interval decided.
	if got := held.AdmitNotBefore.Sub(before); got < 55*time.Minute || got > 65*time.Minute {
		t.Errorf("admit_not_before is %s away, want about the 1h recheck interval", got)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("attempts = %d, want 1 — a quota stop must not burn the budget", len(runs))
	}
	if runs[0].State != store.StepInterrupted {
		t.Errorf("attempt state = %s, want interrupted", runs[0].State)
	}
	if runs[0].FailureReason != ReasonUsageLimit {
		t.Errorf("failure_reason = %q, want %s", runs[0].FailureReason, ReasonUsageLimit)
	}

	// The budget itself, as the engine counts it: interrupted attempts are
	// excluded, so the step still has its full max_retries for a real failure.
	attempts, err := h.store.CountStepAttempts(t.Context(), store.StepRef{TaskID: task.ID, StepID: "implement"}, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Failed != 0 {
		t.Errorf("failed attempts = %d, want 0", attempts.Failed)
	}
}

// TestUsageLimitHonoursTheReportedResetTime: when the CLI names a reset, that
// timestamp wins over the configured interval.
func TestUsageLimitHonoursTheReportedResetTime(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	t.Setenv("FAKEAGENT_USAGE_LIMIT_RESET", "1800") // 30 minutes from now
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.UsageLimitRecheckInterval = config.Duration(4 * time.Hour)
	})
	task := h.createTask(t, agentStepSnapshot)
	before := time.Now()
	h.start(t)

	held := h.waitForHold(t, task.ID)
	got := held.AdmitNotBefore.Sub(before)
	if got < 25*time.Minute || got > 35*time.Minute {
		t.Errorf("admit_not_before is %s away, want ~30m from the CLI rather than the 4h interval", got)
	}
}

// TestUsageLimitReleasesTheSlot: the whole reason the wait is not a sleep in
// the actor. With one slot, a quota-walled task must not keep the queue from
// moving.
func TestUsageLimitReleasesTheSlot(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxParallelTasks = 1
		c.UsageLimitRecheckInterval = config.Duration(time.Hour)
	})
	// The walled task is older, so §11 admits it first.
	walled := h.createTask(t, agentStepSnapshot)
	other := h.createTask(t, "name: other\nsteps:\n"+
		commandStep("work", script("echo second task ran", "Write-Output 'second task ran'")))
	h.start(t)

	held := h.waitForHold(t, walled.ID)
	if held.QueuedReason != ReasonUsageLimit {
		t.Fatalf("queued_reason = %q, want %s", held.QueuedReason, ReasonUsageLimit)
	}
	done := h.waitForState(t, other.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("second task = %s (%s), want done — the held task kept its slot",
			done.State, done.BlockReason)
	}
}

// TestUsageLimitRecoversWithoutAHuman is the end-to-end promise: the window
// reopens, the scheduler re-admits, and the task finishes with nobody
// pressing anything. The fake CLI owns the "has the window reopened" state via
// a marker file, so the recovery is observed rather than staged.
func TestUsageLimitRecoversWithoutAHuman(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		// Effectively "as soon as the next tick comes round": the hold is
		// what is being released here, not waited on.
		c.UsageLimitRecheckInterval = config.Duration(time.Millisecond)
	})
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	t.Setenv("FAKEAGENT_USAGE_LIMIT_MARKER", filepath.Join(t.TempDir(), "window-spent"))

	task := h.createTask(t, agentStepSnapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done with no human action", done.State, done.BlockReason)
	}
	if done.QueuedReason != "" || done.AdmitNotBefore != nil {
		t.Errorf("hold survived the run: %q / %v", done.QueuedReason, done.AdmitNotBefore)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("attempts = %d, want 2 (the walled one, then the successful re-run)", len(runs))
	}
	if runs[0].State != store.StepInterrupted || runs[0].FailureReason != ReasonUsageLimit {
		t.Errorf("first attempt = %s/%q, want interrupted/%s",
			runs[0].State, runs[0].FailureReason, ReasonUsageLimit)
	}
	if runs[1].State != store.StepSucceeded {
		t.Errorf("second attempt = %s (%s), want succeeded", runs[1].State, runs[1].FailureReason)
	}
}

// TestUnauthenticatedBlocksUnderTheNormalBudget is the auth half, which
// deliberately changes nothing but the reason: the step still runs, the
// attempts still fail, the §7.2 budget still applies, and the task still ends
// up blocked — now saying what to fix instead of pointing at a transcript.
func TestUnauthenticatedBlocksUnderTheNormalBudget(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "unauthenticated")
	h := newEngineHarness(t)
	task := h.createTask(t, agentStepSnapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}
	if blocked.BlockReason != ReasonAgentUnauthenticated {
		t.Fatalf("block_reason = %q, want %s", blocked.BlockReason, ReasonAgentUnauthenticated)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("attempts = %d, want 2 (max_retries 1) — the budget must not be short-circuited", len(runs))
	}
	for _, run := range runs {
		if run.State != store.StepFailed || run.FailureReason != ReasonAgentUnauthenticated {
			t.Errorf("attempt %d = %s/%q, want failed/%s",
				run.Attempt, run.State, run.FailureReason, ReasonAgentUnauthenticated)
		}
	}
}

// quotaModeSnapshot names claude — which adapter the observation is filed
// under is asserted below — and states a retry budget, so "a quota stop
// spends none of it" is measured against a number rather than a default.
const quotaModeSnapshot = `name: quota-mode
defaults:
  agent: claude
steps:
  - id: implement
    type: agent
    max_retries: 1
    prompt: "Do the work"
`

// waitForBlock polls until the task is blocked, failing loudly if it settles
// on an admission hold instead — the mistake this file's blocking half is
// there to catch.
func (h *engineHarness) waitForBlock(t *testing.T, id int64) *store.Task {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last *store.Task
	for time.Now().Before(deadline) {
		task, err := h.store.GetTask(t.Context(), id)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		last = task
		if task.State == store.TaskBlocked {
			return task
		}
		if task.State == store.TaskDone {
			t.Fatalf("task %d reached done; want blocked on the quota wall", id)
		}
		if task.State == store.TaskQueued && task.AdmitNotBefore != nil {
			t.Fatalf("task %d picked up an admission hold (%q, until %s); "+
				"usage_limit_auto_continue said to block",
				id, task.QueuedReason, task.AdmitNotBefore)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d never blocked (state %s)", id, last.State)
	return nil
}

// TestUsageLimitAutoContinueTruthTable walks all six squares of the key: three
// modes × whether the CLI named a reset. `always` holds either way,
// `reported_only` holds only where the reset is the CLI's own word, `never`
// blocks either way.
//
// Every square asserts the observation too, including the blocking ones. The
// board badge and the `agent.quota_changed` event are how an operator learns
// *why* a task just blocked (task 026, decision 2), so a mode that suppressed
// the recording would leave the board disagreeing with itself.
func TestUsageLimitAutoContinueTruthTable(t *testing.T) {
	const (
		interval = 4 * time.Hour
		// Seconds the fake CLI reports as its reset, well clear of the
		// interval so which one won is never in doubt.
		reportedIn = 30 * time.Minute
	)
	cases := []struct {
		mode     string
		reported bool
		hold     bool
	}{
		{config.UsageLimitAlways, true, true},
		{config.UsageLimitAlways, false, true},
		{config.UsageLimitReportedOnly, true, true},
		{config.UsageLimitReportedOnly, false, false},
		{config.UsageLimitNever, true, false},
		{config.UsageLimitNever, false, false},
	}
	for _, tc := range cases {
		leg := "estimated"
		if tc.reported {
			leg = "reported"
		}
		t.Run(tc.mode+"/"+leg, func(t *testing.T) {
			t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
			if tc.reported {
				t.Setenv("FAKEAGENT_USAGE_LIMIT_RESET", "1800")
			}
			h := newEngineHarnessWith(t, func(c *config.Config) {
				// Long enough that a hold cannot expire mid-test, so what is
				// asserted is the decision and not a race with the tick.
				c.UsageLimitRecheckInterval = config.Duration(interval)
				c.UsageLimitAutoContinue = tc.mode
			})
			task := h.createTask(t, quotaModeSnapshot)
			before := time.Now()
			h.start(t)

			if tc.hold {
				held := h.waitForHold(t, task.ID)
				if held.QueuedReason != ReasonUsageLimit {
					t.Errorf("queued_reason = %q, want %s", held.QueuedReason, ReasonUsageLimit)
				}
				if held.BlockReason != "" {
					t.Errorf("block_reason = %q; a held task is not blocked", held.BlockReason)
				}
			} else {
				blocked := h.waitForBlock(t, task.ID)
				if blocked.BlockReason != ReasonUsageLimit {
					t.Errorf("block_reason = %q, want %s", blocked.BlockReason, ReasonUsageLimit)
				}
				// The cursor stays where it is: a blocked task is blocked
				// *at* a step, which is what lets a retry re-run it.
				if blocked.CurrentStep != 0 {
					t.Errorf("current_step = %d, want 0 — a block does not advance the cursor",
						blocked.CurrentStep)
				}
				if blocked.QueuedReason != "" || blocked.AdmitNotBefore != nil {
					t.Errorf("blocked task carries a hold: %q / %v",
						blocked.QueuedReason, blocked.AdmitNotBefore)
				}
			}

			// The same in either branch, and the point of taking the block
			// inside the interrupted arm: the attempt row still says what the
			// step did, and the budget is untouched.
			runs := h.stepRuns(t, task.ID)
			if len(runs) != 1 {
				t.Fatalf("attempts = %d, want 1 — a quota stop must not burn the budget", len(runs))
			}
			if runs[0].State != store.StepInterrupted {
				t.Errorf("attempt state = %s, want interrupted", runs[0].State)
			}
			if runs[0].FailureReason != ReasonUsageLimit {
				t.Errorf("failure_reason = %q, want %s", runs[0].FailureReason, ReasonUsageLimit)
			}
			attempts, err := h.store.CountStepAttempts(t.Context(),
				store.StepRef{TaskID: task.ID, StepID: "implement"}, time.Time{})
			if err != nil {
				t.Fatalf("CountStepAttempts: %v", err)
			}
			if attempts.Failed != 0 {
				t.Errorf("failed attempts = %d, want 0 — no retry is consumed in any mode",
					attempts.Failed)
			}

			q := claudeQuota(t, h)
			if q.ResetsAtReported != tc.reported {
				t.Errorf("resets_at_reported = %v, want %v", q.ResetsAtReported, tc.reported)
			}
			if q.Source != store.QuotaSourceObserved {
				t.Errorf("source = %q, want %q", q.Source, store.QuotaSourceObserved)
			}
			want := interval
			if tc.reported {
				want = reportedIn
			}
			if got := q.ResetsAt.Sub(before); got < want-5*time.Minute || got > want+5*time.Minute {
				t.Errorf("resets_at is %s away, want about %s", got, want)
			}
		})
	}
}

// TestUsageLimitModeIsReadAtTheStop is the hot-reload claim, mirroring the one
// the recheck interval already carries: the mode is read when the wall is hit,
// never cached, so a change reaches a running daemon.
//
// It also pins the other half of that: a hold already in flight is *not*
// converted. Switching to `never` does not reach back and block the held task
// — it is re-admitted once, walks into the same wall, and blocks there.
func TestUsageLimitModeIsReadAtTheStop(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	h := newEngineHarnessWith(t, func(c *config.Config) {
		// Short, so the hold this starts with expires promptly and the next
		// stop — the one that meets the new mode — happens without waiting.
		c.UsageLimitRecheckInterval = config.Duration(time.Millisecond)
		c.UsageLimitAutoContinue = config.UsageLimitAlways
	})
	task := h.createTask(t, quotaModeSnapshot)
	h.start(t)

	held := h.waitForHold(t, task.ID)
	if held.QueuedReason != ReasonUsageLimit {
		t.Fatalf("queued_reason = %q, want %s", held.QueuedReason, ReasonUsageLimit)
	}

	h.reload(func(c *config.Config) { c.UsageLimitAutoContinue = config.UsageLimitNever })

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonUsageLimit {
		t.Fatalf("task = %s (%q), want blocked/%s once the mode changed",
			blocked.State, blocked.BlockReason, ReasonUsageLimit)
	}
	// At least two: the stop that held under `always`, then the one that
	// blocked under `never`. More is legal — the hold is a millisecond long,
	// so the task can be re-admitted a few times before the reload lands —
	// and every one of them is still an interruption that spent no retry.
	runs := h.stepRuns(t, task.ID)
	if len(runs) < 2 {
		t.Fatalf("attempts = %d, want at least 2 (a hold, then a block): %+v", len(runs), runs)
	}
	for _, run := range runs {
		if run.State != store.StepInterrupted || run.FailureReason != ReasonUsageLimit {
			t.Errorf("attempt %d = %s/%q, want interrupted/%s",
				run.Attempt, run.State, run.FailureReason, ReasonUsageLimit)
		}
	}
	attempts, err := h.store.CountStepAttempts(t.Context(),
		store.StepRef{TaskID: task.ID, StepID: "implement"}, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Failed != 0 {
		t.Errorf("failed attempts = %d, want 0", attempts.Failed)
	}
}

// waitForDrainedRepair polls until the repair request has been drained and the
// task has settled, which is how a repair that ends `blocked` announces it is
// done — its row is `interrupted`, so counting finished rows never sees it.
func (h *engineHarness) waitForDrainedRepair(t *testing.T, id int64) *store.Task {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last *store.Task
	for time.Now().Before(deadline) {
		task, err := h.store.GetTask(t.Context(), id)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		last = task
		if task.PendingRepair == nil &&
			task.State != store.TaskQueued && task.State != store.TaskRunning {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d never drained its repair request (state %s, pending %+v)",
		id, last.State, last.PendingRepair)
	return nil
}

// TestUsageLimitDuringARepairReblocksWithTheOriginalReason is decision 1's
// other branch. A repair is not a step of the workflow, so a quota stop that
// is not waited out cannot leave the task anywhere new: it goes back to
// `blocked` carrying the reason it was blocked with — not `usage_limit` —
// with the request drained, exactly as a finished repair does.
func TestUsageLimitDuringARepairReblocksWithTheOriginalReason(t *testing.T) {
	// The blocked step is a command step, so the wall is reached by the
	// repair agent and by nothing else.
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.UsageLimitRecheckInterval = config.Duration(time.Hour)
		c.UsageLimitAutoContinue = config.UsageLimitNever
	})
	blocked := blockedOnCheck(t, h)

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "try something"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}

	// Not waitForRepair: that one counts rows that finished in some state
	// other than `interrupted`, and a quota stop's row is interrupted. What
	// says the repair is over here is the drained request.
	after := h.waitForDrainedRepair(t, blocked.ID)
	if after.State != store.TaskBlocked || after.BlockReason != ReasonCheckFailed {
		t.Fatalf("task = %s/%q, want blocked/%s — the reason the task was blocked with",
			after.State, after.BlockReason, ReasonCheckFailed)
	}
	if after.PendingRepair != nil {
		t.Errorf("the repair request survived a stop that ends blocked: %+v", after.PendingRepair)
	}
	if after.AdmitNotBefore != nil || after.QueuedReason != "" {
		t.Errorf("a blocked task kept a hold: %q / %v", after.QueuedReason, after.AdmitNotBefore)
	}
	repairs := repairRuns(h.stepRuns(t, after.ID))
	if len(repairs) != 1 || repairs[0].State != store.StepInterrupted {
		t.Fatalf("repair rows = %+v, want one interrupted row", repairs)
	}
	if repairs[0].FailureReason != ReasonUsageLimit {
		t.Errorf("repair failure_reason = %q, want %s", repairs[0].FailureReason, ReasonUsageLimit)
	}
	// The observation is still recorded — the operator has to be able to see
	// that the account, not the repair, is what stopped.
	if q := claudeQuota(t, h); !q.ResetsAtReported && q.Source != store.QuotaSourceObserved {
		t.Errorf("quota row = %+v, want an observed estimate", q)
	}
}
