package taskrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// Guards, condition steps and allow_failure end to end (§7.7, task 015).
// Command steps only: nothing here needs an agent, and a gate-speed test is
// one more test that actually gets run.

func TestEngineGuardSkipsStepAndCarriesOn(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := "name: guarded\nsteps:\n" +
		commandStep("first", script("echo one", "Write-Output one")) +
		commandStep("skipped", script("echo two", "Write-Output two"), `if: "{{ false }}"`) +
		commandStep("last", script("echo three", "Write-Output three"))
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 3 {
		t.Fatalf("rows = %d, want 3 (a skipped step still records one)", len(runs))
	}
	skipped := runs[1]
	if skipped.State != store.StepSkipped {
		t.Errorf("guarded step state = %s, want skipped", skipped.State)
	}
	if skipped.SkipReason != store.SkipReasonCondition {
		t.Errorf("skip_reason = %q, want %q — a human skip leaves it empty",
			skipped.SkipReason, store.SkipReasonCondition)
	}
	if skipped.TranscriptPath != "" {
		t.Error("a skipped step opened a transcript")
	}
	if runs[2].State != store.StepSucceeded {
		t.Errorf("step after the skip = %s, want succeeded: a skip carries on", runs[2].State)
	}
}

func TestEngineConditionStepStopsTheWorkflow(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := `name: early-finish
steps:
` + commandStep("first", script("echo one", "Write-Output one")) + `  - id: gate
    type: condition
    if: "{{ false }}"
` + commandStep("never", script("echo two", "Write-Output two"))
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done — a false condition finishes successfully",
			done.State, done.BlockReason)
	}
	if done.CurrentStep != 3 {
		t.Errorf("current_step = %d, want 3 (len(steps)): completion runs the ordinary path",
			done.CurrentStep)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("rows = %d, want 2: the steps after the stop are never considered", len(runs))
	}
	if runs[1].State != store.StepStopped {
		t.Errorf("condition row state = %s, want stopped", runs[1].State)
	}
}

func TestEngineConditionStepTrueContinues(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := `name: carry-on
steps:
  - id: gate
    type: condition
    if: "{{ true }}"
` + commandStep("after", script("echo done", "Write-Output done"))
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 || runs[0].State != store.StepSucceeded {
		t.Fatalf("rows = %+v, want a succeeded condition then the next step", runs)
	}
}

func TestEngineAllowFailureAdvancesAndFeedsTheNextGuard(t *testing.T) {
	h := newEngineHarness(t)
	// The probe fails; allow_failure advances past it; the guard on the next
	// step reads the exit code the probe left behind, which is the whole
	// point of pairing the two fields.
	snapshot := "name: probe\nsteps:\n" +
		commandStep("probe", script("exit 3", "exit 3"), "allow_failure: true", "max_retries: 0") +
		commandStep("fixup", script("echo fixing", "Write-Output fixing"),
			`if: '{{ ne (index .Steps "probe").ExitCode 0 }}'`)
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("rows = %d, want 2", len(runs))
	}
	if runs[0].State != store.StepFailed || runs[0].FailureReason != ReasonNonzeroExit {
		t.Errorf("probe row = %s/%q, want failed/nonzero_exit — allow_failure advances, it does not relabel",
			runs[0].State, runs[0].FailureReason)
	}
	if runs[1].State != store.StepSucceeded {
		t.Errorf("fixup = %s, want succeeded: its guard should have read exit code 3", runs[1].State)
	}
}

func TestEngineAllowFailureIgnoresFailuresTheStepDidNotProduce(t *testing.T) {
	h := newEngineHarness(t)
	// A render failure is vincent failing to run the step, not the step
	// failing — allow_failure must not swallow it (decision 5).
	snapshot := `name: not-mine
steps:
  - id: broken
    type: command
    allow_failure: true
    max_retries: 0
    run: "echo {{.Task.Titel}}"
`
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonTemplateError {
		t.Fatalf("task = %s/%q, want blocked/template_error", blocked.State, blocked.BlockReason)
	}
}

func TestEngineGuardErrorBlocksWithoutRetrying(t *testing.T) {
	h := newEngineHarness(t)
	// Renders to "7", which is neither true nor false. The budget is
	// deliberately not spent on re-rendering it (decision 14).
	snapshot := "name: bad-guard\nsteps:\n" +
		commandStep("step", script("echo hi", "Write-Output hi"),
			`if: "{{ 7 }}"`, "max_retries: 3")
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonConditionError {
		t.Fatalf("task = %s/%q, want blocked/condition_error", blocked.State, blocked.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("rows = %d, want 1: a guard error is not retried", len(runs))
	}
	if runs[0].FailureReason != ReasonConditionError {
		t.Errorf("row reason = %q, want condition_error", runs[0].FailureReason)
	}
}

func TestEngineGuardReadsHost(t *testing.T) {
	h := newEngineHarness(t)
	// `.Host` is what closes §8.1.1's deferred per-step `platforms:`.
	snapshot := "name: host\nsteps:\n" +
		commandStep("elsewhere", script("echo no", "Write-Output no"),
			`if: '{{ eq .Host.OS "plan9" }}'`)
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepSkipped {
		t.Fatalf("rows = %+v, want one skipped step", runs)
	}
}

func TestEngineGuardedSubStepsSubsetTheGroup(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := `name: group
steps:
  - id: verify
    type: parallel
    steps:
      - id: ran
        type: command
        run: ` + script("echo ran", "Write-Output ran") + `
      - id: guarded-off
        type: command
        if: "{{ false }}"
        run: ` + script("exit 9", "exit 9") + `
`
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done — the failing sub-step was guarded off",
			done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	byID := map[string]store.StepRun{}
	for _, r := range runs {
		byID[r.StepID] = r
	}
	if got := byID["guarded-off"]; got.State != store.StepSkipped ||
		got.SkipReason != store.SkipReasonCondition {
		t.Errorf("guarded sub-step = %s/%q, want skipped/condition", got.State, got.SkipReason)
	}
	if byID["ran"].State != store.StepSucceeded {
		t.Errorf("unguarded sub-step = %s, want succeeded", byID["ran"].State)
	}
}

// Lane guards subset a fan-out (§7.6, task 015 decision 11). These live here
// rather than in fanout_test.go because what they prove is the guard, not the
// join.

func TestFanOutGuardedLaneIsNotSpawned(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := `name: root
steps:
  - id: build
    type: fan_out
    lanes:
      - id: api
        steps:
` + indent(writeFileStep("write-api", "api.txt", "api")) +
		`      - id: skipped
        if: "{{ false }}"
        steps:
` + indent(writeFileStep("write-skipped", "skipped.txt", "skipped"))
	task := h.createTask(t, snapshot)

	lanes := h.waitForChildren(t, task.ID, 1)
	if lanes[0].LaneID != "api" {
		t.Fatalf("spawned lane = %q, want api", lanes[0].LaneID)
	}
	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent = %s/%q, want done", done.State, done.BlockReason)
	}
	if !h.fileOnBranch(t, task.BranchName, "api.txt") {
		t.Error("the selected lane's file is missing from the parent's branch")
	}
	if h.fileOnBranch(t, task.BranchName, "skipped.txt") {
		t.Error("the guarded-off lane ran anyway")
	}
}

func TestFanOutWithEveryLaneGuardedOffIsANoOp(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	// The parent must not park here: `awaiting_children` with no children
	// would be re-queued the moment the scheduler looked, spawn nothing and
	// park again.
	snapshot := `name: root
steps:
  - id: build
    type: fan_out
    lanes:
      - id: none
        if: "{{ false }}"
        steps:
` + indent(writeFileStep("write-none", "none.txt", "none")) +
		commandStep("after", script("echo after", "Write-Output after"))
	task := h.createTask(t, snapshot)

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent = %s/%q, want done", done.State, done.BlockReason)
	}
	children, err := h.store.ListChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("children = %d, want 0", len(children))
	}
	var sawFanOut, sawAfter bool
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID == "build" && run.State == store.StepSucceeded {
			sawFanOut = true
		}
		if run.StepID == "after" && run.State == store.StepSucceeded {
			sawAfter = true
		}
	}
	if !sawFanOut {
		t.Error("the no-op fan_out recorded no row; the step was reached and chose nothing")
	}
	if !sawAfter {
		t.Error("the workflow did not carry on past the no-op fan_out")
	}
}
