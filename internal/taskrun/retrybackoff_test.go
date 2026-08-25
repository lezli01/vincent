package taskrun

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// Paced retries (§7.2, task 028). `retry_backoff` spends the wait between two
// attempts the way task 003 spends a quota wall: the task returns to `queued`
// carrying a §11 admission hold and the actor ends with the admission, so the
// wait costs no concurrency slot.
//
// Two backoffs are used throughout. A test that *observes* a hold uses an
// hour, so it cannot expire mid-assertion; one that needs the task to come
// back uses a millisecond, where what is being waited on is the scheduler
// noticing rather than the hold itself.
const (
	longBackoff  = "retry_backoff: 1h"
	shortBackoff = "retry_backoff: 1ms"
)

// TestRetryBackoffReQueuesInsteadOfRetryingInPlace is the core claim: the
// second attempt does not happen in this admission, and the task waits on a
// clock rather than on a human. The attempt row is what separates it from a
// usage-limit hold — `failed`, and one retry poorer.
func TestRetryBackoffReQueuesInsteadOfRetryingInPlace(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, "name: paced\nsteps:\n"+
		commandStep("flaky", "exit 3", "max_retries: 1", longBackoff))
	before := time.Now()
	h.start(t)

	held := h.waitForHold(t, task.ID)
	if held.QueuedReason != ReasonRetryBackoff {
		t.Errorf("queued_reason = %q, want %s", held.QueuedReason, ReasonRetryBackoff)
	}
	if held.BlockReason != "" {
		t.Errorf("block_reason = %q; a paced retry is a wait, not a block", held.BlockReason)
	}
	if got := held.AdmitNotBefore.Sub(before); got < 55*time.Minute || got > 65*time.Minute {
		t.Errorf("admit_not_before is %s away, want about the 1h retry_backoff", got)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("attempts = %d, want 1: the second one is what the hold is for", len(runs))
	}
	if runs[0].State != store.StepFailed || runs[0].FailureReason != ReasonNonzeroExit {
		t.Errorf("attempt = %s/%q, want failed/%s — the row keeps what actually failed",
			runs[0].State, runs[0].FailureReason, ReasonNonzeroExit)
	}
	// And it spent a retry, which is the whole difference from `usage_limit`.
	attempts, err := h.store.CountStepAttempts(t.Context(),
		store.StepRef{TaskID: task.ID, StepID: "flaky"}, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Failed != 1 {
		t.Errorf("failed attempts = %d, want 1: a paced retry delays the budget, it does not refund it",
			attempts.Failed)
	}
	if !hasEvent(h.eventTypes(t, task.ID), eventStepRetrying) {
		t.Error("no step.retrying event: a paced retry emits it where an immediate one does")
	}
}

// TestRetryBackoffReleasesTheSlot is why the wait is a requeue and not a
// sleep. With one slot, a paced task must not keep the queue from moving.
func TestRetryBackoffReleasesTheSlot(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) { c.MaxParallelTasks = 1 })
	// The paced task is older, so §11 admits it first.
	paced := h.createTask(t, "name: paced\nsteps:\n"+
		commandStep("flaky", "exit 3", "max_retries: 1", longBackoff))
	other := h.createTask(t, "name: other\nsteps:\n"+
		commandStep("work", script("echo second task ran", "Write-Output 'second task ran'")))
	h.start(t)

	held := h.waitForHold(t, paced.ID)
	if held.QueuedReason != ReasonRetryBackoff {
		t.Fatalf("queued_reason = %q, want %s", held.QueuedReason, ReasonRetryBackoff)
	}
	done := h.waitForState(t, other.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("second task = %s (%s), want done — the paced task kept its slot",
			done.State, done.BlockReason)
	}
}

// TestRetryBackoffPreservesTheBudgetAcrossTheRequeue is the test that catches
// the recount going wrong. `max_retries: 2` means three attempts however many
// admissions they are spread over — not four, and not forever.
func TestRetryBackoffPreservesTheBudgetAcrossTheRequeue(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, "name: paced\nsteps:\n"+
		commandStep("flaky", "exit 3", "max_retries: 2", shortBackoff))
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("task = %s/%q, want blocked/nonzero_exit", blocked.State, blocked.BlockReason)
	}
	if blocked.QueuedReason != "" || blocked.AdmitNotBefore != nil {
		t.Errorf("hold survived the block: %q / %v", blocked.QueuedReason, blocked.AdmitNotBefore)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 3 {
		t.Fatalf("attempts = %d, want exactly 3 (max_retries: 2) across two requeues", len(runs))
	}
	for i, run := range runs {
		if run.Attempt != i+1 {
			t.Errorf("row %d numbered attempt %d, want %d", i, run.Attempt, i+1)
		}
		if run.State != store.StepFailed {
			t.Errorf("attempt %d = %s, want failed", run.Attempt, run.State)
		}
	}
}

// TestRetryBackoffNeverGrantsARetry: the budget decides *whether* there is
// another attempt, and only then does the backoff decide *when*. A step out of
// budget blocks at once, hour-long backoff or not.
func TestRetryBackoffNeverGrantsARetry(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, "name: paced\nsteps:\n"+
		commandStep("flaky", "exit 3", "max_retries: 0", longBackoff))
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("task = %s/%q, want blocked/nonzero_exit with no wait first",
			blocked.State, blocked.BlockReason)
	}
	if blocked.QueuedReason != "" || blocked.AdmitNotBefore != nil {
		t.Errorf("hold survived the block: %q / %v", blocked.QueuedReason, blocked.AdmitNotBefore)
	}
	if runs := h.stepRuns(t, task.ID); len(runs) != 1 {
		t.Fatalf("attempts = %d, want 1", len(runs))
	}
}

// TestRetryBackoffOutranksAllowFailure is the trap this feature had to avoid.
// The step loop checks `allow_failure` first, which is safe only while a
// `failed` outcome means the budget is spent. A paced failure has budget left,
// so advancing on it would spend an allow_failure step's first failure as
// though it were its last.
func TestRetryBackoffOutranksAllowFailure(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, "name: paced\nsteps:\n"+
		commandStep("probe", "exit 3", "allow_failure: true", "max_retries: 1", shortBackoff)+
		commandStep("after", script("echo after", "Write-Output after")))
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done — allow_failure advances once the budget is spent",
			done.State, done.BlockReason)
	}
	byStep := subRuns(h.stepRuns(t, task.ID))
	if got := len(byStep["probe"]); got != 2 {
		t.Errorf("probe attempts = %d, want 2: the retry is paced, not skipped", got)
	}
	if got := len(byStep["after"]); got != 1 {
		t.Errorf("after rows = %d, want 1", got)
	}
}

// TestRetryBackoffOutranksAllowFailureInAGroup is the same trap in
// group.go's sub-step goroutine — and, in passing, that a re-admitted group
// re-runs only the sub-step that has work left.
func TestRetryBackoffOutranksAllowFailureInAGroup(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, groupSnapshot("",
		commandStep("probe", "exit 3", "allow_failure: true", "max_retries: 1", shortBackoff),
		commandStep("other", script("echo ok", "Write-Output ok")),
	))

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	byStep := subRuns(h.stepRuns(t, task.ID))
	if got := len(byStep["probe"]); got != 2 {
		t.Errorf("probe attempts = %d, want 2", got)
	}
	if got := len(byStep["other"]); got != 1 {
		t.Errorf("other rows = %d, want 1: a succeeded sibling must not re-run on re-admission", got)
	}
}

// TestRetryBackoffOutranksAllowFailureInALoopBody is the same trap in
// loop.go's runBodyStep.
func TestRetryBackoffOutranksAllowFailureInALoopBody(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, loopSnapshot("count: 1",
		commandStep("probe", "exit 3", "allow_failure: true", "max_retries: 1", shortBackoff)))

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	if runs := h.stepRuns(t, task.ID); len(runs) != 2 {
		t.Fatalf("probe attempts = %d, want 2", len(runs))
	}
}

// TestRetryBackoffResumesTheSameLoopIteration: the loop derives its position
// from its rows, so a paced retry must come back to iteration 2 rather than
// restarting the loop. Only the second iteration fails, so a restart would
// leave iteration 1 with a second row.
func TestRetryBackoffResumesTheSameLoopIteration(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, loopSnapshot("count: 2",
		commandStep("probe", "{{ if eq .Loop.Index 2 }}exit 1{{ else }}exit 0{{ end }}",
			"max_retries: 1", shortBackoff)))

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("task = %s/%q, want blocked/nonzero_exit", blocked.State, blocked.BlockReason)
	}
	byIteration := iterationsOf(h.stepRuns(t, task.ID))
	if got := len(byIteration[1]); got != 1 {
		t.Errorf("iteration 1 rows = %d, want 1: the re-admitted loop restarted instead of resuming", got)
	}
	if got := len(byIteration[2]); got != 2 {
		t.Errorf("iteration 2 rows = %d, want 2 (max_retries: 1)", got)
	}
}

// TestCollectGroupPrefersASpentFailureOverAPacedOne pins the third precedence
// tier. Waiting on a sibling whose budget is already gone only delays the
// block: the task would be held, re-admitted, and blocked anyway.
func TestCollectGroupPrefersASpentFailureOverAPacedOne(t *testing.T) {
	until := time.Now().Add(time.Hour)

	got := collectGroup([]stepOutcome{
		{state: store.StepFailed, reason: ReasonNonzeroExit, backoffUntil: &until},
		{state: store.StepFailed, reason: ReasonCheckFailed},
	})
	if got.backoffUntil != nil || got.reason != ReasonCheckFailed {
		t.Errorf("collectGroup = %s/%q (backoff %v), want the spent failure check_failed",
			got.state, got.reason, got.backoffUntil)
	}

	// With no spent failure the paced one is the group's outcome, so the wait
	// still reaches the task.
	got = collectGroup([]stepOutcome{
		{state: store.StepSucceeded},
		{state: store.StepFailed, reason: ReasonNonzeroExit, backoffUntil: &until},
	})
	if got.backoffUntil == nil {
		t.Errorf("collectGroup = %s/%q, want the paced failure so the group can wait", got.state, got.reason)
	}

	// An interruption still outranks both: it consumed no retry.
	got = collectGroup([]stepOutcome{
		{state: store.StepFailed, reason: ReasonNonzeroExit, backoffUntil: &until},
		{state: store.StepInterrupted, reason: ReasonUsageLimit},
	})
	if got.state != store.StepInterrupted {
		t.Errorf("collectGroup = %s/%q, want interrupted", got.state, got.reason)
	}
}

// TestParallelGroupBlocksRatherThanWaitingOnASpentSibling is the same
// precedence end to end: `spent` has no budget left and `paced` has an hour to
// wait, so the group must block now rather than hold for an hour first.
func TestParallelGroupBlocksRatherThanWaitingOnASpentSibling(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, groupSnapshot("",
		commandStep("spent", "exit 4", "max_retries: 0"),
		commandStep("paced", "exit 3", "max_retries: 5", longBackoff),
	))

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}
	if blocked.QueuedReason != "" || blocked.AdmitNotBefore != nil {
		t.Errorf("hold survived the block: %q / %v", blocked.QueuedReason, blocked.AdmitNotBefore)
	}
}

// TestUsageLimitIgnoresRetryBackoff: a quota wall is not a paced retry, and a
// workflow that sets one everywhere must not change which path a usage limit
// takes or what it costs the budget.
func TestUsageLimitIgnoresRetryBackoff(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.UsageLimitRecheckInterval = config.Duration(time.Hour)
	})
	task := h.createTask(t, "name: quota\ndefaults:\n  "+shortBackoff+"\nsteps:\n"+
		"  - id: implement\n    type: agent\n    max_retries: 1\n    prompt: \"Do the work\"\n")
	before := time.Now()
	h.start(t)

	held := h.waitForHold(t, task.ID)
	if held.QueuedReason != ReasonUsageLimit {
		t.Fatalf("queued_reason = %q, want %s", held.QueuedReason, ReasonUsageLimit)
	}
	if got := held.AdmitNotBefore.Sub(before); got < 55*time.Minute || got > 65*time.Minute {
		t.Errorf("admit_not_before is %s away, want the 1h recheck interval rather than the 1ms backoff", got)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepInterrupted {
		t.Fatalf("attempts = %d (first %v), want one interrupted row — a quota stop consumes no retry",
			len(runs), runs)
	}
}

// TestNoRetryBackoffIsTodaysBehaviour is the regression bar: with nothing
// configured the retries happen inside one admission, and the task never
// enters a hold.
func TestNoRetryBackoffIsTodaysBehaviour(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, "name: flaky\nsteps:\n"+
		commandStep("flaky", "exit 3", "max_retries: 1"))
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("task = %s/%q, want blocked/nonzero_exit", blocked.State, blocked.BlockReason)
	}
	if runs := h.stepRuns(t, task.ID); len(runs) != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry)", len(runs))
	}
	// The hold clears on the way out of `queued`, so the finished row cannot
	// answer "was it ever held" — the state-change payloads can.
	for _, p := range h.statePayloads(t, task.ID) {
		if _, held := p["queued_reason"]; held {
			t.Errorf("task was held (%v) with no retry_backoff configured", p)
		}
	}
}
