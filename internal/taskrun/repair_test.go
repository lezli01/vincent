package taskrun

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/scheduler"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// fakeAgentMark is the line cmd/fakeagent appends to FAKEAGENT_EDIT_FILE. The
// check below greps for it, so the repair agent changing the worktree is what
// makes the blocked step pass — not a timer and not a retry.
const fakeAgentMark = "fakeagent was here"

// repairSnapshot is a command step whose check fails until README.md carries
// the fake agent's line, followed by a step that only runs if the first one
// passed. Both the run and the check are spelled in the sh ∩ pwsh
// intersection (§8.3): `git grep -q` exits 1 on no match on every platform.
const repairSnapshot = `name: repairable
defaults:
  agent: claude
steps:
  - id: build
    type: command
    max_retries: 0
    run: git --version
    check: git grep -q "` + fakeAgentMark + `" -- README.md
  - id: after
    type: command
    run: git --version
`

// blockedOnCheck runs a task until its first step blocks on a failed check,
// which is the state every repair below starts from.
func blockedOnCheck(t *testing.T, h *engineHarness) *store.Task {
	t.Helper()
	task := h.createTask(t, repairSnapshot)
	h.start(t)
	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonCheckFailed {
		t.Fatalf("task = %s/%q, want blocked/check_failed", blocked.State, blocked.BlockReason)
	}
	return blocked
}

// repairRuns is the rows recorded under the reserved repair id.
func repairRuns(runs []store.StepRun) []store.StepRun {
	var out []store.StepRun
	for _, r := range runs {
		if r.StepID == RepairStepID {
			out = append(out, r)
		}
	}
	return out
}

// stepRunsFor is the rows of one workflow step, repairs excluded.
func stepRunsFor(runs []store.StepRun, stepID string) []store.StepRun {
	var out []store.StepRun
	for _, r := range runs {
		if r.StepID == stepID {
			out = append(out, r)
		}
	}
	return out
}

// TestRepairRunsInTheWorktreeAndReturnsToBlocked is the shape of the whole
// feature (task 025): one agent runs in the task's existing worktree, the
// change it makes is there afterwards, and the task comes back to `blocked`
// at the same step with the same reason — the repair decides nothing.
func TestRepairRunsInTheWorktreeAndReturnsToBlocked(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_EDIT_FILE", "README.md")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)
	before := h.stepRuns(t, blocked.ID)

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "make the check pass"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	after := h.waitForRepair(t, blocked.ID, 1)

	if after.State != store.TaskBlocked {
		t.Errorf("task = %s, want blocked again", after.State)
	}
	if after.BlockReason != ReasonCheckFailed {
		t.Errorf("block_reason = %q, want the reason it was blocked with", after.BlockReason)
	}
	if after.CurrentStep != blocked.CurrentStep {
		t.Errorf("current_step = %d, want %d — a repair never advances", after.CurrentStep, blocked.CurrentStep)
	}
	if after.PendingRepair != nil {
		t.Error("the repair request survived the re-block; it drains with that transition")
	}

	// The agent ran in this task's worktree, on its branch.
	readme, err := os.ReadFile(filepath.Join(after.WorktreePath, "README.md"))
	if err != nil {
		t.Fatalf("read the repaired worktree: %v", err)
	}
	if !strings.Contains(string(readme), fakeAgentMark) {
		t.Errorf("the repair agent did not change the task's worktree: %q", readme)
	}

	runs := h.stepRuns(t, after.ID)
	repairs := repairRuns(runs)
	if len(repairs) != 1 {
		t.Fatalf("repair rows = %d, want 1", len(repairs))
	}
	row := repairs[0]
	if row.StepIndex != blocked.CurrentStep {
		t.Errorf("repair row step_index = %d, want the blocked step's %d", row.StepIndex, blocked.CurrentStep)
	}
	if row.Attempt != 1 || row.State != store.StepSucceeded {
		t.Errorf("repair row = attempt %d/%s, want attempt 1/succeeded", row.Attempt, row.State)
	}
	if row.StepType != "agent" || row.Agent == "" {
		t.Errorf("repair row = %s/%q, want an agent row naming its adapter", row.StepType, row.Agent)
	}
	if row.TranscriptPath == "" {
		t.Error("the repair has no transcript; it is meant to be as auditable as any other run")
	}

	// The blocked step itself did not run again: a repair is not a retry.
	if got, want := len(stepRunsFor(runs, "build")), len(stepRunsFor(before, "build")); got != want {
		t.Errorf("build attempts = %d, want %d — a successful repair must not re-run the step", got, want)
	}
	if len(stepRunsFor(runs, "after")) != 0 {
		t.Error("the workflow advanced past the blocked step during a repair")
	}
}

// TestRepairThenRetryCompletesTheWorkflow is the acceptance criterion the
// issue names: after a repair that fixed the underlying problem, retrying the
// blocked step passes it and the workflow carries on.
func TestRepairThenRetryCompletesTheWorkflow(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_EDIT_FILE", "README.md")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "make the check pass"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	h.waitForRepair(t, blocked.ID, 1)

	if _, err := h.runner.Retry(t.Context(), blocked.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	done := h.waitForState(t, blocked.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done after a repair that fixed the check",
			done.State, done.BlockReason)
	}
	if len(stepRunsFor(h.stepRuns(t, done.ID), "after")) == 0 {
		t.Error("the workflow did not continue past the repaired step")
	}
}

// TestRepairDoesNotConsumeTheStepBudget is the mechanism the whole design
// rests on: the repair's row sits at the blocked step's index under a
// reserved id, and CountStepAttempts keys on the composite position — so the
// step's budget cannot see it (task 025).
func TestRepairDoesNotConsumeTheStepBudget(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)
	ref := store.StepRef{TaskID: blocked.ID, StepIndex: blocked.CurrentStep, StepID: "build"}
	before, err := h.store.CountStepAttempts(t.Context(), ref, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "look around"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	h.waitForRepair(t, blocked.ID, 1)

	after, err := h.store.CountStepAttempts(t.Context(), ref, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if after != before {
		t.Errorf("the blocked step's attempts changed across a repair: %+v → %+v", before, after)
	}
}

// TestSecondRepairIsAttemptTwoOfTheRepair: attempt numbers belong to the
// position, and the repair's position is its own. A second repair is attempt
// 2 of the repair, not attempt N+1 of the step.
func TestSecondRepairIsAttemptTwoOfTheRepair(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)

	for i := 1; i <= 2; i++ {
		if _, err := h.runner.Repair(t.Context(), blocked.ID,
			store.RepairRequest{Prompt: "again"}); err != nil {
			t.Fatalf("Repair %d: %v", i, err)
		}
		h.waitForRepair(t, blocked.ID, i)
	}
	repairs := repairRuns(h.stepRuns(t, blocked.ID))
	if len(repairs) != 2 {
		t.Fatalf("repair rows = %d, want 2", len(repairs))
	}
	if repairs[0].Attempt != 1 || repairs[1].Attempt != 2 {
		t.Errorf("repair attempts = %d, %d; want 1, 2", repairs[0].Attempt, repairs[1].Attempt)
	}
	if repairs[0].TranscriptPath == repairs[1].TranscriptPath {
		t.Error("both repairs wrote the same transcript file")
	}
}

// TestFailedRepairLeavesTheTaskBlockedWithTheSameReason: the repair's exit
// code decides nothing. A repair that failed leaves exactly what a repair
// that succeeded leaves — the task blocked where it was, with the reason it
// had — and the failure is on its own row for a human to read.
func TestFailedRepairLeavesTheTaskBlockedWithTheSameReason(t *testing.T) {
	// The blocked step is a command step, so this scenario reaches the repair
	// agent and nothing else.
	t.Setenv("FAKEAGENT_SCENARIO", "nonzero-exit")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "try something"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	after := h.waitForRepair(t, blocked.ID, 1)

	if after.State != store.TaskBlocked || after.BlockReason != ReasonCheckFailed {
		t.Errorf("task = %s/%q, want blocked/check_failed after a failed repair",
			after.State, after.BlockReason)
	}
	repairs := repairRuns(h.stepRuns(t, after.ID))
	if len(repairs) != 1 || repairs[0].State != store.StepFailed {
		t.Fatalf("repair rows = %+v, want one failed row", repairs)
	}
	// max_retries: 0 on the synthetic step — a failed repair fails fast
	// rather than silently paying for a second agent run.
	if repairs[0].Attempt != 1 {
		t.Errorf("the failed repair ran %d attempts, want 1", repairs[0].Attempt)
	}
}

// TestRepairPromptCarriesTheFailureContext asserts the block the daemon
// assembles: the task's own context, the blocked step's definition and
// failure, where the transcript is, and the operator's words last.
//
// It calls the assembler rather than reading it back off the wire because the
// fake agent's echo is truncated at 1000 characters, which a real failure
// block exceeds — the end-to-end leg is
// TestRepairPromptReachesTheAgentLiterally below.
func TestRepairPromptCarriesTheFailureContext(t *testing.T) {
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)
	project, err := h.store.GetProject(t.Context(), blocked.ProjectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	wf, _, err := workflow.Parse([]byte(blocked.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	req := store.RepairRequest{
		Prompt: "the operator's own words", BlockReason: blocked.BlockReason,
	}

	target, ok := h.runner.repairTargetOf(blocked, wf)
	if !ok {
		t.Fatal("no repair target for a task blocked on a workflow step")
	}
	prompt := h.runner.repairPrompt(t.Context(), blocked, project, target, req)
	for _, want := range []string{
		"engine test",              // the task title
		"a task",                   // its description
		blocked.BranchName,         // the branch the agent is on
		blocked.WorktreePath,       // and the worktree it works in
		"build",                    // the blocked step's id
		"git --version",            // its rendered command
		fakeAgentMark,              // its check
		ReasonCheckFailed,          // why it is blocked
		"the operator's own words", // and what the human asked for
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the repair prompt does not carry %q", want)
		}
	}
	run := stepRunsFor(h.stepRuns(t, blocked.ID), "build")[0]
	if !strings.Contains(prompt, run.TranscriptPath) {
		t.Error("the repair prompt does not say where the failed attempt's transcript is")
	}
	// The operator's words go last, after everything the daemon assembled.
	if strings.Index(prompt, "the operator's own words") < strings.Index(prompt, ReasonCheckFailed) {
		t.Error("the operator's prompt does not come after the failure context")
	}
}

// TestRepairPromptReachesTheAgentLiterally: the operator types prose, and
// §8.4 renders with missingkey=error — so a `{{` reaching Render unescaped
// would fail the repair before the process started.
func TestRepairPromptReachesTheAgentLiterally(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "replace {{.Nope}} with a literal"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	h.waitForRepair(t, blocked.ID, 1)

	row := repairRuns(h.stepRuns(t, blocked.ID))[0]
	if row.State != store.StepSucceeded {
		t.Fatalf("repair row = %s/%q, want a prompt that rendered as prose",
			row.State, row.FailureReason)
	}
	body, err := os.ReadFile(row.TranscriptPath)
	if err != nil {
		t.Fatalf("read repair transcript: %v", err)
	}
	if !strings.Contains(string(body), "You are repairing") {
		t.Error("the assembled prompt never reached the agent")
	}
	// And the braces survive Render as the two characters that were typed.
	rendered, err := workflow.Render("prompt",
		workflow.EscapeTemplate("replace {{.Nope}} with a literal"), workflow.RenderContext{})
	if err != nil {
		t.Fatalf("an escaped prose prompt failed to render: %v", err)
	}
	if rendered != "replace {{.Nope}} with a literal" {
		t.Errorf("escaped prompt rendered as %q", rendered)
	}
}

// TestRepairRowIsInvisibleToLaterSteps: the repair's row must not become a
// `.Steps` entry. A key no workflow author wrote, present exactly when
// somebody happened to press a key, is worse than no key at all.
func TestRepairRowIsInvisibleToLaterSteps(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_EDIT_FILE", "README.md")
	h := newEngineHarness(t)
	// The second step's guard reads `.Steps` for the reserved id. Under
	// missingkey=error an `index` on a map is a lookup, not an error, so the
	// guard is a real question with a real answer: is the repair visible?
	snapshot := `name: repairable
defaults:
  agent: claude
steps:
  - id: build
    type: command
    max_retries: 0
    run: git --version
    check: git grep -q "` + fakeAgentMark + `" -- README.md
  - id: after
    type: command
    if: '{{ if (index .Steps "` + RepairStepID + `").Status }}true{{ else }}false{{ end }}'
    run: git --version
`
	task := h.createTask(t, snapshot)
	h.start(t)
	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.BlockReason != ReasonCheckFailed {
		t.Fatalf("task = %s/%q, want blocked/check_failed", blocked.State, blocked.BlockReason)
	}
	if _, err := h.runner.Repair(t.Context(), task.ID,
		store.RepairRequest{Prompt: "fix it"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	h.waitForRepair(t, task.ID, 1)
	if _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	for _, r := range stepRunsFor(h.stepRuns(t, task.ID), "after") {
		if r.SkipReason != store.SkipReasonCondition {
			t.Errorf("the `after` guard saw the repair row: %s/%q", r.State, r.SkipReason)
		}
	}
}

// TestRepairWithoutAWorktreeReblocksWithoutAnAgent: a task blocked before its
// worktree existed re-enters ensureWorktree on the repair admission and
// re-blocks on the same reason, spawning nothing. Filtering `available_actions`
// by block reason would put a second, reason-shaped policy next to §6's
// state-shaped one to reach the same outcome (task 025).
func TestRepairWithoutAWorktreeReblocksWithoutAnAgent(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newEngineHarness(t)
	task := h.createTask(t, repairSnapshot)
	// The branch the task resolved to already exists, so `git worktree add -b`
	// refuses and the task blocks before any step runs (§10, task 001).
	claimBranch(t, h.repo, task.BranchName)
	h.start(t)
	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.BlockReason != worktree.ReasonBranchExists {
		t.Fatalf("task = %s/%q, want blocked/branch_exists", blocked.State, blocked.BlockReason)
	}

	if _, err := h.runner.Repair(t.Context(), task.ID,
		store.RepairRequest{Prompt: "fix the branch"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	// Repair moved the task to `queued`, so reaching `blocked` again means a
	// whole admission ran and blocked before any step could.
	after := h.waitForState(t, task.ID, store.TaskBlocked)
	if after.BlockReason != worktree.ReasonBranchExists {
		t.Errorf("block_reason = %q, want branch_exists again", after.BlockReason)
	}
	if got := repairRuns(h.stepRuns(t, task.ID)); len(got) != 0 {
		t.Errorf("an agent ran for a task with no worktree: %+v", got)
	}
	// The request survives, because nothing ran it: the next `retry` drains
	// it, and a second `repair` replaces it. What must not happen is a repair
	// silently becoming something else.
	if after.PendingRepair == nil {
		t.Error("the repair request was drained by a block that never ran it")
	}
	if _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	retried, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if retried.PendingRepair != nil {
		t.Error("retry left the repair request in place; retry means retry")
	}
}

// TestInterruptedRepairRerunsAsARepair is why the request drains at the
// re-block and not at the row insert (§12.4, task 025). Recovery finalizes
// the running row as `interrupted` and re-queues the task; the actor then
// walks from `current_step`. With the request already drained, that walk
// would silently become a plain retry of the blocked step — consuming its
// budget, and possibly unblocking the task without the operator asking.
func TestInterruptedRepairRerunsAsARepair(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)
	buildBefore := len(stepRunsFor(h.stepRuns(t, blocked.ID), "build"))

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "this one never finishes"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	h.waitForRepairRunning(t, blocked.ID)

	// The daemon goes down mid-repair and comes back up.
	h.runner.Stop()
	h.sched.Stop()
	if _, err := Recover(t.Context(), h.store, discardLog()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	requeued, err := h.store.GetTask(t.Context(), blocked.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if requeued.State != store.TaskQueued {
		t.Fatalf("task = %s after recovery, want queued", requeued.State)
	}
	if requeued.PendingRepair == nil {
		t.Fatal("recovery discarded the repair request; the next admission would be a plain retry")
	}

	// The restarted daemon's repair succeeds, so the outcome is observable.
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h.restart(t)
	// One *completed* repair: waitForRepair does not count the interrupted
	// row, which is exactly the one the crash left behind.
	after := h.waitForRepair(t, blocked.ID, 1)

	if after.State != store.TaskBlocked || after.BlockReason != ReasonCheckFailed {
		t.Errorf("task = %s/%q, want blocked/check_failed", after.State, after.BlockReason)
	}
	runs := h.stepRuns(t, after.ID)
	if got := len(stepRunsFor(runs, "build")); got != buildBefore {
		t.Errorf("build attempts = %d, want %d — the interrupted repair re-ran the step", got, buildBefore)
	}
	repairs := repairRuns(runs)
	if len(repairs) != 2 {
		t.Fatalf("repair rows = %d, want the interrupted one and its re-run", len(repairs))
	}
	if repairs[0].State != store.StepInterrupted {
		t.Errorf("the interrupted repair row is %s, want interrupted", repairs[0].State)
	}
	if repairs[1].State != store.StepSucceeded {
		t.Errorf("the re-run repair row is %s, want succeeded", repairs[1].State)
	}
}

// TestRepairRejectsAnEmptyPrompt: an agent launched with no instructions is
// spend with no question attached.
func TestRepairRejectsAnEmptyPrompt(t *testing.T) {
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)
	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "   \n "}); err == nil {
		t.Fatal("an empty repair prompt was accepted")
	} else if _, ok := AsRepairPrompt(err); !ok {
		t.Fatalf("Repair error = %v, want *RepairPromptError", err)
	}
	task, err := h.store.GetTask(t.Context(), blocked.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.State != store.TaskBlocked || task.PendingRepair != nil {
		t.Errorf("the refused repair moved the task: %s, pending %+v", task.State, task.PendingRepair)
	}
}

// TestRepairDoesNotMoveTheRetryCursor: a repair is not a retry, and moving
// the cursor would hand the blocked step a fresh budget nobody asked for
// (§7.2).
func TestRepairDoesNotMoveTheRetryCursor(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newEngineHarness(t)
	blocked := blockedOnCheck(t, h)
	if blocked.RetryCursorAt != nil {
		t.Fatalf("fixture already has a retry cursor: %v", blocked.RetryCursorAt)
	}
	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "have a look"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	after := h.waitForRepair(t, blocked.ID, 1)
	if after.RetryCursorAt != nil {
		t.Errorf("retry_cursor_at = %v after a repair; a repair is not a retry", after.RetryCursorAt)
	}
}

// claimBranch creates the branch a task resolved to, so its worktree cannot
// be created (§10, task 001).
func claimBranch(t *testing.T, repo, branch string) {
	t.Helper()
	cmd := gitx.New()
	if _, err := cmd.Run(t.Context(), repo, "branch", branch); err != nil {
		t.Fatalf("claim branch %s: %v", branch, err)
	}
}

// waitForRepair polls until n repair rows have finished and the task is back
// in a resting state.
func (h *engineHarness) waitForRepair(t *testing.T, id int64, n int) *store.Task {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		task, err := h.store.GetTask(t.Context(), id)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		done := 0
		for _, r := range repairRuns(h.stepRuns(t, id)) {
			if r.FinishedAt != nil && r.State != store.StepInterrupted {
				done++
			}
		}
		if done >= n && task.State != store.TaskQueued && task.State != store.TaskRunning {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d never finished %d repair(s)", id, n)
	return nil
}

// waitForRepairRunning polls until a repair row is live, which is when a
// crash is worth simulating.
func (h *engineHarness) waitForRepairRunning(t *testing.T, id int64) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range repairRuns(h.stepRuns(t, id)) {
			if r.State == store.StepRunning && r.PID != nil {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d never started a repair", id)
}

// restart replaces the harness's runner and scheduler with fresh ones over
// the same store and data directory — a daemon that went down and came back.
func (h *engineHarness) restart(t *testing.T) {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agents := agent.NewRegistry(claude.New(func() string { return fake }))
	h.runner = New(Deps{
		Store:     h.store,
		Config:    h.config,
		Worktrees: worktree.NewManager(gitx.New(), h.dataDir),
		Agents:    agents,
		Catalog:   agent.NewCatalogCache(agents),
		DataDir:   h.dataDir,
		Logger:    log,
	})
	sched := scheduler.New(scheduler.Deps{
		Store:    h.store,
		Config:   h.config,
		Admitter: h.runner,
		Logger:   log,
	})
	h.store.SetEventHook(func(e *store.Event) {
		if scheduler.WakeOn(e) {
			sched.Wake()
		}
	})
	h.runner.Start(t.Context())
	sched.Start(t.Context())
	h.sched = sched
	t.Cleanup(h.runner.Stop)
	t.Cleanup(sched.Stop)
}
