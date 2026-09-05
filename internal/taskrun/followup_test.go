package taskrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// followUpTestWorkflow is the compiled document a follow-up request carries:
// what the API's compile step produces for the `command` form, spelled out
// here so the action tests need no HTTP handler.
const followUpTestWorkflow = `name: follow-up
description: an ad-hoc follow-up run on a finished task
steps:
  - id: follow-up
    name: follow-up
    type: command
    run: git status
    max_retries: 0
`

// followUpBase is the workflow a follow-up is run *on*: two ordinary command
// steps that succeed, so the task reaches `done` with its cursor one past the
// last step and its rows occupying indices 0 and 1. Round 1 therefore lands at
// index 2 (task 027 decision 2).
const followUpBase = `name: base
defaults:
  agent: claude
steps:
  - id: build
    type: command
    run: git --version
  - id: verify
    type: command
    run: git --version
`

// followUpBaseSteps is len(followUpBase.steps) — where round 1's rows go.
const followUpBaseSteps = 2

// doneTask runs followUpBase to completion and returns the finished task.
func doneTask(t *testing.T, h *engineHarness) *store.Task {
	t.Helper()
	task := h.createTask(t, followUpBase)
	h.start(t)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	return done
}

// abortedTask runs followUpBase far enough to own a worktree, then cancels it,
// which is the `aborted` origin a follow-up may also be launched from.
func abortedTask(t *testing.T, h *engineHarness) *store.Task {
	t.Helper()
	done := doneTask(t, h)
	// Cancel is not valid from `done`, so the aborted origin is manufactured
	// through the store the way every other state fixture in this package is.
	aborted, _, err := h.store.TransitionTask(t.Context(), done.ID,
		store.TaskDone, store.TaskAborted, store.TaskChange{})
	if err != nil {
		t.Fatalf("make the task aborted: %v", err)
	}
	return aborted
}

// compileFollowUp is what the API's handler does before persisting a request:
// build the document, write the §8.6 request level into it, and marshal.
func compileFollowUp(t *testing.T, req store.FollowUpRequest) store.FollowUpRequest {
	t.Helper()
	wf, err := CompileFollowUp(req)
	if err != nil {
		t.Fatalf("CompileFollowUp: %v", err)
	}
	ApplyFollowUpSelection(wf.Steps, req)
	out, err := workflow.Marshal(wf)
	if err != nil {
		t.Fatalf("marshal follow-up workflow: %v", err)
	}
	req.Workflow = string(out)
	return req
}

// agentFollowUp is the `agent` form, pinned to the fake adapter.
func agentFollowUp(t *testing.T, prompt string) store.FollowUpRequest {
	t.Helper()
	return compileFollowUp(t, store.FollowUpRequest{
		Form: store.FollowUpAgent, Prompt: prompt, Agent: "claude",
	})
}

// commandFollowUp is the `command` form.
func commandFollowUp(t *testing.T, run string) store.FollowUpRequest {
	t.Helper()
	return compileFollowUp(t, store.FollowUpRequest{Form: store.FollowUpCommand, Run: run})
}

// workflowFollowUp is the `workflow` form, whose document is written out by
// the caller exactly as the registry would have held it.
func workflowFollowUp(t *testing.T, name, yaml string) store.FollowUpRequest {
	t.Helper()
	req := store.FollowUpRequest{
		Form: store.FollowUpWorkflow, WorkflowName: name, Agent: "claude",
	}
	wf, _, err := workflow.Parse([]byte(yaml), workflow.Options{})
	if err != nil {
		t.Fatalf("parse follow-up workflow: %v", err)
	}
	ApplyFollowUpSelection(wf.Steps, req)
	out, merr := workflow.Marshal(wf)
	if merr != nil {
		t.Fatalf("marshal follow-up workflow: %v", merr)
	}
	req.Workflow = string(out)
	return req
}

// followUpRuns is the rows of one follow-up round: those at the round's own
// index, which is what tells a follow-up row from a workflow step's (§5.4,
// decision 2).
func followUpRuns(runs []store.StepRun, round int) []store.StepRun {
	index := followUpBaseSteps + round - 1
	var out []store.StepRun
	for _, r := range runs {
		if r.StepIndex == index {
			out = append(out, r)
		}
	}
	return out
}

// settle polls until the task is at rest: the follow-up finished, blocked or
// parked. `queued` and `running` are the two it waits out.
func (h *engineHarness) settle(t *testing.T, id int64) *store.Task {
	t.Helper()
	return h.waitForStateWithin(t, id, 90*time.Second,
		store.TaskDone, store.TaskAborted, store.TaskBlocked,
		store.TaskAwaitingGate, store.TaskPaused)
}

// TestFollowUpAgentRunsInTheWorktreeAndReturnsToDone is the shape of the
// whole feature: an agent runs in the *finished* task's existing worktree,
// the change it makes is there afterwards, the row lands past the snapshot's
// last index, and the task is `done` again.
func TestFollowUpAgentRunsInTheWorktreeAndReturnsToDone(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_EDIT_FILE", "README.md")
	h := newEngineHarness(t)
	done := doneTask(t, h)
	before := h.stepRuns(t, done.ID)

	if _, err := h.runner.FollowUp(t.Context(), done.ID,
		agentFollowUp(t, "add a line to the readme")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	after := h.settle(t, done.ID)

	if after.State != store.TaskDone {
		t.Errorf("task = %s/%q, want done again", after.State, after.BlockReason)
	}
	if after.PendingFollowUp != nil {
		t.Error("the follow-up request survived the restore; it drains with that transition")
	}
	if after.CurrentStep != done.CurrentStep {
		t.Errorf("current_step = %d, want %d — a follow-up never walks the task's own cursor",
			after.CurrentStep, done.CurrentStep)
	}

	readme, err := os.ReadFile(filepath.Join(after.WorktreePath, "README.md"))
	if err != nil {
		t.Fatalf("read the followed-up worktree: %v", err)
	}
	if !strings.Contains(string(readme), fakeAgentMark) {
		t.Errorf("the follow-up agent did not change the task's worktree: %q", readme)
	}

	runs := h.stepRuns(t, after.ID)
	round1 := followUpRuns(runs, 1)
	if len(round1) != 1 {
		t.Fatalf("round 1 rows = %d, want 1 at index %d", len(round1), followUpBaseSteps)
	}
	if round1[0].StepID != FollowUpStepID || round1[0].State != store.StepSucceeded {
		t.Errorf("row = %s/%s, want %s/succeeded",
			round1[0].StepID, round1[0].State, FollowUpStepID)
	}
	if round1[0].Iteration != 0 {
		t.Errorf("iteration = %d, want 0 — iteration keeps its §7.8 loop meaning", round1[0].Iteration)
	}
	if round1[0].TranscriptPath == "" {
		t.Error("the follow-up run has no transcript; it is a step run like any other")
	}
	// The original workflow's rows are untouched: a follow-up appends nothing
	// to the snapshot and spends none of its budgets.
	if got, want := len(runs)-len(round1), len(before); got != want {
		t.Errorf("workflow rows = %d, want %d unchanged", got, want)
	}
}

// TestFollowUpCommandRunsUnderTheDaemonShell covers the second form: no agent
// is involved, and the command runs in the task's worktree under §8.3's shell.
func TestFollowUpCommandRunsUnderTheDaemonShell(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)

	// `git config -f` writes a file with no `touch`, `printf` or redirection,
	// so one spelling works in both sh and pwsh (CLAUDE.md).
	req := commandFollowUp(t, "git config -f follow-up.txt vincent.followup ran")
	if _, err := h.runner.FollowUp(t.Context(), done.ID, req); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	after := h.settle(t, done.ID)

	if after.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done again", after.State, after.BlockReason)
	}
	if _, err := os.Stat(filepath.Join(after.WorktreePath, "follow-up.txt")); err != nil {
		t.Errorf("the command follow-up did not run in the task's worktree: %v", err)
	}
	round1 := followUpRuns(h.stepRuns(t, after.ID), 1)
	if len(round1) != 1 || round1[0].StepType != workflow.StepCommand {
		t.Fatalf("round 1 rows = %+v, want one command row", round1)
	}
	if round1[0].Agent != "" {
		t.Errorf("agent = %q, want none — a command follow-up runs no agent", round1[0].Agent)
	}
}

// TestFollowUpOnAnAbortedTaskReturnsToAborted is decision 5: a follow-up
// decides nothing about a task's verdict. A command that exits 0 must not be
// able to reverse a human's abort.
func TestFollowUpOnAnAbortedTaskReturnsToAborted(t *testing.T) {
	h := newEngineHarness(t)
	aborted := abortedTask(t, h)

	req := commandFollowUp(t, "git --version")
	if _, err := h.runner.FollowUp(t.Context(), aborted.ID, req); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	after := h.settle(t, aborted.ID)

	if after.State != store.TaskAborted {
		t.Errorf("task = %s, want aborted — a successful follow-up is not a promotion", after.State)
	}
	if len(followUpRuns(h.stepRuns(t, after.ID), 1)) != 1 {
		t.Error("the follow-up did not run on the aborted task")
	}
}

// TestSecondFollowUpIsRoundTwo is decision 2's numbering: repeat follow-ups
// occupy distinct indices, so they are separate rounds rather than further
// attempts of the first.
func TestSecondFollowUpIsRoundTwo(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)

	for i := range 2 {
		req := commandFollowUp(t, "git --version")
		if _, err := h.runner.FollowUp(t.Context(), done.ID, req); err != nil {
			t.Fatalf("FollowUp %d: %v", i+1, err)
		}
		if got := h.settle(t, done.ID); got.State != store.TaskDone {
			t.Fatalf("after follow-up %d task = %s/%q, want done", i+1, got.State, got.BlockReason)
		}
	}

	runs := h.stepRuns(t, done.ID)
	for round := 1; round <= 2; round++ {
		rows := followUpRuns(runs, round)
		if len(rows) != 1 {
			t.Fatalf("round %d rows = %d, want 1 at index %d",
				round, len(rows), followUpBaseSteps+round-1)
		}
		if rows[0].Attempt != 1 {
			t.Errorf("round %d attempt = %d, want 1 — a second follow-up is a round, not a retry",
				round, rows[0].Attempt)
		}
	}
}

// TestFollowUpWorkflowRunsEveryStepThroughAGate is decision 3 and decision 4
// together: a multi-step follow-up workflow runs to the end, a `manual` gate
// inside it parks the task and `approve` advances the *follow-up's* cursor
// rather than the task's.
func TestFollowUpWorkflowRunsEveryStepThroughAGate(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)

	yaml := "name: tidy\nsteps:\n" +
		commandStep("first", "git config -f follow-up-first.txt vincent.first ran", "max_retries: 0") +
		"  - id: sign-off\n    type: manual\n    instructions: look at it\n" +
		commandStep("second", "git config -f follow-up-second.txt vincent.second ran", "max_retries: 0")

	if _, err := h.runner.FollowUp(t.Context(), done.ID, workflowFollowUp(t, "tidy", yaml)); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	parked := h.settle(t, done.ID)
	if parked.State != store.TaskAwaitingGate {
		t.Fatalf("task = %s/%q, want awaiting_gate at the follow-up's manual step",
			parked.State, parked.BlockReason)
	}
	if parked.CurrentStep != done.CurrentStep {
		t.Errorf("current_step = %d, want %d — a gate inside a follow-up moves the follow-up's cursor",
			parked.CurrentStep, done.CurrentStep)
	}
	if parked.PendingFollowUp == nil || parked.PendingFollowUp.Cursor != 1 {
		t.Fatalf("follow-up cursor = %+v, want 1 — the gate is step 2 of the round",
			parked.PendingFollowUp)
	}

	if _, err := h.runner.Approve(t.Context(), done.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	after := h.settle(t, done.ID)
	if after.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done after the gate was approved",
			after.State, after.BlockReason)
	}
	for _, name := range []string{"follow-up-first.txt", "follow-up-second.txt"} {
		if _, err := os.Stat(filepath.Join(after.WorktreePath, name)); err != nil {
			t.Errorf("%s missing: the follow-up did not run every step: %v", name, err)
		}
	}

	// Every row of the round sits at the round's own index, under the id its
	// author wrote — never rewritten, because `if:` guards and `.Steps` refer
	// to those ids (decision 2).
	rows := followUpRuns(h.stepRuns(t, after.ID), 1)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.StepID)
	}
	for _, want := range []string{"first", "sign-off", "second"} {
		if !containsString(ids, want) {
			t.Errorf("round 1 ids = %v, missing %q", ids, want)
		}
	}
}

// TestFollowUpLoopKeepsIterationMeaning is the other half of decision 2: a
// `loop` inside a follow-up numbers its passes in `iteration`, which the
// round number does not touch.
func TestFollowUpLoopKeepsIterationMeaning(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)

	yaml := "name: twice\nsteps:\n  - id: passes\n    type: loop\n    count: 2\n    steps:\n" +
		indentBody(commandStep("tick", "git --version", "max_retries: 0"))

	if _, err := h.runner.FollowUp(t.Context(), done.ID, workflowFollowUp(t, "twice", yaml)); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	after := h.settle(t, done.ID)
	if after.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", after.State, after.BlockReason)
	}
	seen := map[int]bool{}
	for _, r := range followUpRuns(h.stepRuns(t, after.ID), 1) {
		if r.StepID == "tick" {
			seen[r.Iteration] = true
		}
	}
	if !seen[1] || !seen[2] {
		t.Errorf("loop iterations recorded = %v, want passes 1 and 2", seen)
	}
}

// TestFollowUpFanOutSpawnsAndJoins is the most expensive thing decision 3
// bought: a `fan_out` inside a follow-up parks the finished task in
// `awaiting_children`, its lanes run as ordinary tasks off its branch, and
// the join resumes at the *follow-up's* cursor rather than at `current_step`.
func TestFollowUpFanOutSpawnsAndJoins(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)

	yaml := "name: spread\nsteps:\n  - id: spread\n    type: fan_out\n    lanes:\n" +
		"      - id: a\n        steps:\n" + indent(writeFileStep("write-a", "a.txt", "a")) +
		"      - id: b\n        steps:\n" + indent(writeFileStep("write-b", "b.txt", "b"))

	if _, err := h.runner.FollowUp(t.Context(), done.ID, workflowFollowUp(t, "spread", yaml)); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	lanes := h.waitForChildren(t, done.ID, 2)
	for _, lane := range lanes {
		if lane.ParentStepIndex == nil || *lane.ParentStepIndex != followUpBaseSteps {
			t.Fatalf("lane %d parent_step_index = %v, want %d — the follow-up's own index",
				lane.ID, lane.ParentStepIndex, followUpBaseSteps)
		}
		if lane.BaseBranch != done.BranchName {
			t.Errorf("lane %d base_branch = %q, want the finished task's branch %q",
				lane.ID, lane.BaseBranch, done.BranchName)
		}
	}

	after := h.waitForStateWithin(t, done.ID, fanOutBudget,
		store.TaskDone, store.TaskAborted, store.TaskBlocked)
	if after.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done after the join", after.State, after.BlockReason)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if !h.fileOnBranch(t, after.BranchName, name) {
			t.Errorf("%s is not on the task's branch; the join did not merge every lane", name)
		}
	}
	// The join is a row of the round, at the round's index, under the author's
	// own step id.
	var joined bool
	for _, r := range followUpRuns(h.stepRuns(t, after.ID), 1) {
		if r.StepID == "spread" && r.StepType == workflow.StepFanOut {
			joined = true
		}
	}
	if !joined {
		t.Error("no fan_out row at the follow-up's index; the join was recorded somewhere else")
	}
}

// TestFailedFollowUpBlocksAtItsOwnIndexAndRetryReRunsIt is decision 6: the
// block lands at the follow-up's row index, the request survives it, and
// `retry` re-runs the follow-up rather than completing the task.
func TestFailedFollowUpBlocksAtItsOwnIndexAndRetryReRunsIt(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)

	req := commandFollowUp(t, "exit 3")
	if _, err := h.runner.FollowUp(t.Context(), done.ID, req); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	blocked := h.settle(t, done.ID)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonNonzeroExit {
		t.Fatalf("task = %s/%q, want blocked/nonzero_exit", blocked.State, blocked.BlockReason)
	}
	if blocked.PendingFollowUp == nil {
		t.Fatal("the follow-up request did not survive the block; retry could not re-run it")
	}
	rows := followUpRuns(h.stepRuns(t, blocked.ID), 1)
	if len(rows) == 0 || rows[len(rows)-1].State != store.StepFailed {
		t.Fatalf("round 1 rows = %+v, want a failed row at the follow-up's own index", rows)
	}

	if _, _, err := h.runner.Retry(t.Context(), blocked.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	again := h.settle(t, blocked.ID)
	if again.State != store.TaskBlocked {
		t.Errorf("task = %s, want blocked again — retry re-runs the follow-up, it does not complete the task",
			again.State)
	}
	if got := len(followUpRuns(h.stepRuns(t, again.ID), 1)); got <= len(rows) {
		t.Errorf("round 1 rows = %d, want more than %d — the retry ran the follow-up again",
			got, len(rows))
	}
}

// TestEditRetryOnABlockedFollowUpIsRefused: an override rewrites a step of
// the *snapshot*, and a follow-up is deliberately not in the snapshot
// (decision 1). Refusing says so; the alternative was a 500 from a rewrite
// with nowhere to land.
func TestEditRetryOnABlockedFollowUpIsRefused(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)
	if _, err := h.runner.FollowUp(t.Context(), done.ID, commandFollowUp(t, "exit 3")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	blocked := h.settle(t, done.ID)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}

	_, _, err := h.runner.Retry(t.Context(), blocked.ID, store.Override{Run: "git --version"})
	if _, ok := AsFollowUpOverride(err); !ok {
		t.Fatalf("Retry with an override: err = %v, want FollowUpOverrideError", err)
	}
}

// TestSkipAbandonsAFollowUpAndRestoresTheOrigin is the rest of decision 6:
// `skip` from a follow-up's block runs nothing more and puts the task back
// where it came from — `aborted` here, which is the case a promotion bug
// would show up in.
func TestSkipAbandonsAFollowUpAndRestoresTheOrigin(t *testing.T) {
	h := newEngineHarness(t)
	aborted := abortedTask(t, h)
	if _, err := h.runner.FollowUp(t.Context(), aborted.ID, commandFollowUp(t, "exit 3")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	blocked := h.settle(t, aborted.ID)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}

	if _, err := h.runner.Skip(t.Context(), blocked.ID); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	after := h.settle(t, blocked.ID)
	if after.State != store.TaskAborted {
		t.Errorf("task = %s, want aborted — skip abandons the follow-up, it does not decide the verdict",
			after.State)
	}
	if after.PendingFollowUp != nil {
		t.Error("the abandoned request survived the restore")
	}
}

// TestCancelDuringAFollowUpAbortsADoneTask is decision 8: `done → aborted`
// becomes reachable, because the follow-up's process is live and `cancel`
// means what it always means.
func TestCancelDuringAFollowUpAbortsADoneTask(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)
	if _, err := h.runner.FollowUp(t.Context(), done.ID, commandFollowUp(t, "exit 3")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	blocked := h.settle(t, done.ID)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}

	after, err := h.runner.Cancel(t.Context(), blocked.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if after.State != store.TaskAborted {
		t.Errorf("task = %s, want aborted", after.State)
	}
	if after.PendingFollowUp != nil {
		t.Error("cancel left the follow-up request behind; it must drop it")
	}
}

// TestRepairReadsAFollowUpRowAsItsFailureContext is decision 6's last clause.
// Before this, `runRepair` warned and no-oped whenever `current_step` was past
// the end of the snapshot — which is every finished task, and therefore every
// blocked follow-up.
func TestRepairReadsAFollowUpRowAsItsFailureContext(t *testing.T) {
	h := newEngineHarness(t)
	done := doneTask(t, h)
	if _, err := h.runner.FollowUp(t.Context(), done.ID, commandFollowUp(t, "exit 3")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	blocked := h.settle(t, done.ID)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task = %s, want blocked", blocked.State)
	}
	project, err := h.store.GetProject(t.Context(), blocked.ProjectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	wf, _, perr := workflow.Parse([]byte(blocked.WorkflowSnapshot), workflow.Options{})
	if perr != nil {
		t.Fatalf("parse snapshot: %v", perr)
	}

	target, ok := h.runner.repairTargetOf(blocked, wf)
	if !ok {
		t.Fatal("no repair target for a task blocked in a follow-up; the rescue would be unavailable")
	}
	if !target.followUp {
		t.Error("the repair target is not the follow-up's step")
	}
	if want := followUpBaseSteps; target.index != want {
		t.Errorf("repair index = %d, want %d — the follow-up's own row", target.index, want)
	}
	prompt := h.runner.repairPrompt(t.Context(), blocked, project, target,
		store.RepairRequest{Prompt: "make it exit 0", BlockReason: blocked.BlockReason})
	for _, want := range []string{
		"follow-up run",          // the prompt says which kind of block this is
		"blocked-follow-up-step", // and tags the element as one
		"exit 3",                 // the command that failed
		string(store.TaskDone),   // nothing about the origin is lost
		blocked.WorktreePath,     // the worktree the repair agent works in
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("repair prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// TestInterruptedFollowUpResumesAsAFollowUp is §12.4 over the second cursor:
// a daemon that died mid-follow-up must come back and run a follow-up, not
// silently complete the task.
func TestInterruptedFollowUpResumesAsAFollowUp(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	done := doneTask(t, h)
	if _, err := h.runner.FollowUp(t.Context(), done.ID,
		agentFollowUp(t, "take your time")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	waitForFollowUpRunning(t, h, done.ID)

	// The daemon goes down mid-run and comes back: Recover finalizes the open
	// row and re-queues the task, and the request is still on it.
	h.runner.Stop()
	h.sched.Stop()
	if _, err := Recover(t.Context(), h.store, h.runner.deps.Logger); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	requeued, err := h.store.GetTask(t.Context(), done.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if requeued.PendingFollowUp == nil {
		t.Fatal("recovery dropped the follow-up request; the next admission would complete the task")
	}
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h.restart(t)
	after := h.settle(t, done.ID)
	if after.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", after.State, after.BlockReason)
	}
	// The re-run is another attempt of the *follow-up* step, at the follow-up's
	// own index — not a row of the workflow, and not a round of its own.
	rows := followUpRuns(h.stepRuns(t, after.ID), 1)
	if len(rows) < 2 {
		t.Fatalf("round 1 rows = %d, want the interrupted attempt and its re-run", len(rows))
	}
	if len(followUpRuns(h.stepRuns(t, after.ID), 2)) != 0 {
		t.Error("the resumed follow-up became round 2; an interruption is not a new round")
	}
}

// TestEarlierRoundsAreInvisibleToALaterRound is decision 9: the original
// workflow's results stay visible to a follow-up — reading them is the point
// — while earlier rounds' rows are hidden, the way `__repair` rows are.
func TestEarlierRoundsAreInvisibleToALaterRound(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, followUpBase)
	env := &stepEnv{
		task:     task,
		index:    followUpBaseSteps + 1, // round 2
		followUp: &followUpEnv{base: followUpBaseSteps, round: 2, order: map[string]int{"x": 0}},
		step:     workflow.Step{ID: "x"},
	}
	cases := []struct {
		name string
		run  store.StepRun
		want bool
	}{
		{"the workflow's own step", store.StepRun{StepIndex: 0, StepID: "build"}, false},
		{"round 1", store.StepRun{StepIndex: followUpBaseSteps, StepID: "follow-up"}, true},
		{"this round", store.StepRun{StepIndex: followUpBaseSteps + 1, StepID: "x"}, false},
	}
	for _, tc := range cases {
		if got := env.blindTo(&tc.run); got != tc.want {
			t.Errorf("blindTo(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestFollowUpRoundIndex pins decision 2's arithmetic on its own, so a change
// to it fails here rather than in a gate.
func TestFollowUpRoundIndex(t *testing.T) {
	for round, want := range map[int]int{1: 4, 2: 5, 3: 6} {
		if got := followUpRowIndex(4, round); got != want {
			t.Errorf("followUpRowIndex(4, %d) = %d, want %d", round, got, want)
		}
	}
	// A corrupt round number places the row somewhere real rather than
	// underneath the workflow's last step.
	if got := followUpRowIndex(4, 0); got != 4 {
		t.Errorf("followUpRowIndex(4, 0) = %d, want 4", got)
	}
}

// waitForFollowUpRunning polls until a follow-up row has a live process,
// which is when a crash is worth simulating.
func waitForFollowUpRunning(t *testing.T, h *engineHarness, id int64) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range followUpRuns(h.stepRuns(t, id), 1) {
			if r.State == store.StepRunning && r.PID != nil {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %d never started a follow-up", id)
}

// indentBody shifts a commandStep block under a `loop`'s `steps:`, which sits
// two levels further in than the top level it is written at.
func indentBody(block string) string {
	var sb strings.Builder
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		sb.WriteString("    " + line + "\n")
	}
	return sb.String()
}

func containsString(haystack []string, want string) bool {
	for _, v := range haystack {
		if v == want {
			return true
		}
	}
	return false
}
