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
	attempts, err := h.store.CountStepAttempts(t.Context(), task.ID, 0, "implement", time.Time{})
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
