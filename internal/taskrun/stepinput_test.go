package taskrun

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// What each attempt was actually given (issue #323). Every test here asserts
// against what the *process* received or against the row the engine wrote at
// the moment it rendered — never against a re-render, which is the drift the
// record exists to make visible.

// readPromptLines is what the echo-prompt scenario wrote: one JSON string per
// invocation, in the order the invocations happened.
func readPromptLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		var prompt string
		if err := json.Unmarshal([]byte(line), &prompt); err != nil {
			t.Fatalf("decode prompt line %q: %v", line, err)
		}
		out = append(out, prompt)
	}
	return out
}

// TestStepInputIsTheBytesTheAdapterReceived is the question issue #323 opens
// with, asked of a *retried* attempt — the case where the recorded prompt and
// a re-render provably differ, because the daemon appends the
// `<previous-attempt-failure>` block on top of the §8.4 render.
//
// The comparison is against what the fake CLI read from its own stdin, so a
// record that drifted from the wire by so much as a newline fails here.
func TestStepInputIsTheBytesTheAdapterReceived(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompts.jsonl")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)
	t.Setenv("FAKEAGENT_PROMPT_FAIL_FIRST", "1")

	h := newEngineHarness(t)
	snapshot := `name: echoing
steps:
  - id: implement
    type: agent
    max_retries: 1
    prompt: |
      Implement {{.Task.Title}} on {{.Task.BranchName}}
`
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done: the retry succeeds", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("attempts = %d, want 2 (the failure and its retry)", len(runs))
	}
	wire := readPromptLines(t, promptFile)
	if len(wire) != 2 {
		t.Fatalf("the CLI was invoked %d times, want 2", len(wire))
	}
	for i, run := range runs {
		if run.RenderedPrompt == nil {
			t.Fatalf("attempt %d recorded no prompt", run.Attempt)
		}
		if *run.RenderedPrompt != wire[i] {
			t.Errorf("attempt %d recorded\n%q\nbut the CLI received\n%q", run.Attempt, *run.RenderedPrompt, wire[i])
		}
	}
	if strings.Contains(wire[0], "<previous-attempt-failure") {
		t.Error("the first attempt carried a failure block")
	}
	if !strings.Contains(wire[1], "<previous-attempt-failure") {
		t.Fatalf("the retry carried no failure block, so nothing here distinguishes it from a re-render:\n%q", wire[1])
	}
	// The resolution that used to exist only in the `debug: true` transcript
	// note, recorded on every run instead.
	if runs[1].PermissionMode != "full-auto" {
		t.Errorf("permission_mode = %q, want full-auto (§16's default)", runs[1].PermissionMode)
	}
	if runs[1].TimeoutMS <= 0 {
		t.Errorf("timeout_ms = %d, want the resolved agent timeout", runs[1].TimeoutMS)
	}
	if runs[1].WorkDir != done.WorktreePath {
		t.Errorf("work_dir = %q, want the task worktree %q", runs[1].WorkDir, done.WorktreePath)
	}
}

// TestStepInputSurvivesIntoAnInterruptedRow is the attempt the tab exists for:
// one that never finished. The input must be on the row while it is still
// `running` — before any process exit could carry it — and must still be
// there after §12.4 recovery finalizes the row as `interrupted`.
//
// The second half re-opens the row the engine wrote, which is the shape a
// killed daemon leaves behind: a `running` row nobody is going to finish. Its
// bytes are the real ones, written by the real engine in the first half; only
// the crash is staged, because a live actor racing Recover for the same row
// would decide which of two writers finalized it and prove neither.
func TestStepInputSurvivesIntoAnInterruptedRow(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newEngineHarness(t)
	snapshot := `name: hanging
steps:
  - id: implement
    type: agent
    max_retries: 0
    prompt: "Implement {{.Task.Title}}"
`
	task := h.createTask(t, snapshot)
	h.start(t)

	live := h.waitForStepRun(t, task.ID, func(r store.StepRun) bool {
		return r.State == store.StepRunning && r.RenderedPrompt != nil
	}, "a running row carrying its rendered prompt")
	recorded := *live.RenderedPrompt
	if !strings.Contains(recorded, task.Title) {
		t.Errorf("running row recorded %q, want the rendered title %q", recorded, task.Title)
	}
	if live.WorkDir == "" || live.TimeoutMS <= 0 || live.PermissionMode == "" {
		t.Errorf("running row resolution = work_dir %q, timeout_ms %d, permission_mode %q; want all three recorded",
			live.WorkDir, live.TimeoutMS, live.PermissionMode)
	}
	h.sched.Stop()
	h.runner.Stop()

	// Back to `running` with no process to find, which is what a daemon that
	// died mid-step leaves. UpdateStepRun cannot carry the input columns, so
	// whatever survives below survived on its own.
	crashed, err := h.store.GetStepRun(t.Context(), live.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	crashed.State, crashed.FinishedAt, crashed.PID, crashed.ProcIdentity = store.StepRunning, nil, nil, nil
	if err := h.store.UpdateStepRun(t.Context(), crashed); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	if _, err := Recover(t.Context(), h.store, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	after, err := h.store.GetStepRun(t.Context(), live.ID)
	if err != nil {
		t.Fatalf("GetStepRun after recovery: %v", err)
	}
	if after.State != store.StepInterrupted {
		t.Fatalf("row after recovery = %s, want interrupted", after.State)
	}
	if after.RenderedPrompt == nil || *after.RenderedPrompt != recorded {
		t.Errorf("recovered row recorded %v, want the prompt it was given: %q", after.RenderedPrompt, recorded)
	}
}

// waitForStepRun polls until one of the task's step runs satisfies want.
func (h *engineHarness) waitForStepRun(
	t *testing.T, id int64, want func(store.StepRun) bool, describe string,
) store.StepRun {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, run := range h.stepRuns(t, id) {
			if want(run) {
				return run
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no step run of task %d ever matched: %s", id, describe)
	return store.StepRun{}
}

// TestStepInputInALoopBodyIsItsOwnIteration: each body row records the script
// its *own* iteration rendered and the whole list that admission planned, and
// changing the step the list came from afterwards changes neither. That drift
// — a detail view re-rendering `{{ .Steps.discover.Result }}` against today's
// facts and showing work the row never did — is what the record prevents.
func TestStepInputInALoopBodyIsItsOwnIteration(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	list := script("printf 'alpha\\nbeta\\n'", "'alpha'; 'beta'")
	snapshot := "name: each\nsteps:\n" +
		commandStep("discover", list) +
		"  - id: visit\n    type: loop\n    for_each: '{{ .Steps.discover.Result }}'\n    steps:\n" +
		bodyIndent(commandStep("touch", appendCmd("items.txt", "{{ .Loop.Item }}")))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	body := map[int]store.StepRun{}
	var discover store.StepRun
	for _, run := range h.stepRuns(t, task.ID) {
		switch run.StepID {
		case "touch":
			body[run.Iteration] = run
		case "discover":
			discover = run
		}
	}
	if len(body) != 2 {
		t.Fatalf("body rows = %d, want one per iteration", len(body))
	}
	const wantList = `["alpha","beta"]`
	for iteration, item := range map[int]string{1: "alpha", 2: "beta"} {
		run := body[iteration]
		if run.RenderedRun == nil || !strings.Contains(*run.RenderedRun, item) {
			t.Errorf("iteration %d recorded %v, want a script bound to %q", iteration, run.RenderedRun, item)
		}
		if run.LoopItem != item {
			t.Errorf("iteration %d loop_item = %q, want %q", iteration, run.LoopItem, item)
		}
		if run.RenderedForEach == nil || *run.RenderedForEach != wantList {
			t.Errorf("iteration %d rendered_for_each = %v, want %s", iteration, run.RenderedForEach, wantList)
		}
	}

	// The list's source, rewritten after the fact. A row that re-derived its
	// input on read would now report a loop over gamma and delta.
	discover.ResultSummary = "gamma\ndelta\n"
	if err := h.store.UpdateStepRun(t.Context(), &discover); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	for _, run := range h.stepRuns(t, task.ID) {
		if run.StepID != "touch" {
			continue
		}
		if run.RenderedForEach == nil || *run.RenderedForEach != wantList {
			t.Errorf("after the source changed, iteration %d reports %v, want the list it actually ran: %s",
				run.Iteration, run.RenderedForEach, wantList)
		}
		want := body[run.Iteration].RenderedRun
		if run.RenderedRun == nil || want == nil || *run.RenderedRun != *want {
			t.Errorf("after the source changed, iteration %d reports %v, want %v",
				run.Iteration, run.RenderedRun, want)
		}
	}
}

// TestStepInputInAFanOutLaneIsItsOwnLane: a lane is a task of its own, and its
// rows record the script that lane rendered rather than the parent's template.
func TestStepInputInAFanOutLaneIsItsOwnLane(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, fanOutSnapshot([2]string{"alpha", "alpha.txt"}, [2]string{"beta", "beta.txt"}))

	lanes := h.waitForChildren(t, task.ID, 2)
	for _, lane := range lanes {
		h.waitForStateWithin(t, lane.ID, fanOutBudget, store.TaskDone, store.TaskBlocked, store.TaskAborted)
	}
	for _, lane := range lanes {
		var seen bool
		for _, run := range h.stepRuns(t, lane.ID) {
			if run.RenderedRun == nil {
				continue
			}
			seen = true
			if !strings.Contains(*run.RenderedRun, lane.LaneID) {
				t.Errorf("lane %q recorded %q, want its own lane's script", lane.LaneID, *run.RenderedRun)
			}
			for _, other := range lanes {
				if other.LaneID != lane.LaneID && strings.Contains(*run.RenderedRun, other.LaneID) {
					t.Errorf("lane %q recorded lane %q's script: %q", lane.LaneID, other.LaneID, *run.RenderedRun)
				}
			}
		}
		if !seen {
			t.Errorf("lane %q recorded no rendered script", lane.LaneID)
		}
	}
}

// TestStepInputRecordsAFalseGuardsRenderedValue: the skipped row keeps the raw
// template in result_summary, as it always has, and records what that template
// rendered *to* beside it — the value that says why the step did not run.
func TestStepInputRecordsAFalseGuardsRenderedValue(t *testing.T) {
	h := newEngineHarness(t)
	guard := `{{ eq .Task.Title "something else" }}`
	snapshot := "name: guarded\nsteps:\n" +
		commandStep("skipped", script("echo no", "Write-Output no"), `if: '`+guard+`'`)
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepSkipped {
		t.Fatalf("rows = %+v, want one skipped row", runs)
	}
	if runs[0].RenderedIf == nil || *runs[0].RenderedIf != "false" {
		t.Errorf("rendered_if = %v, want \"false\"", runs[0].RenderedIf)
	}
	if runs[0].ResultSummary != guard {
		t.Errorf("result_summary = %q, want the raw template %q — the rendered value is a companion, not a replacement",
			runs[0].ResultSummary, guard)
	}
}

// TestStepInputGuardIsRecordedButNeverReplayed holds task 015 decision 10 by
// test rather than by comment: the column is display only.
//
// The guard renders to `maybe` on attempt 1 — neither literal, so the row is
// recorded with that value and the task blocks — and to `true` on attempt 2. A
// retry that read the recorded value back would block again on the same
// `maybe`; one that re-evaluates runs the step.
func TestStepInputGuardIsRecordedButNeverReplayed(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := "name: reeval\nsteps:\n" +
		commandStep("gated", script("echo hi", "Write-Output hi"),
			`if: '{{ if gt .Step.Attempt 1 }}true{{ else }}maybe{{ end }}'`, "max_retries: 0")
	task := h.createTask(t, snapshot)
	h.start(t)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked || blocked.BlockReason != ReasonConditionError {
		t.Fatalf("task = %s/%q, want blocked/condition_error", blocked.State, blocked.BlockReason)
	}
	first := h.stepRuns(t, task.ID)
	if len(first) != 1 {
		t.Fatalf("rows = %d, want 1: a guard error is not retried", len(first))
	}
	// A guard that rendered to neither literal is exactly the one whose value
	// a human needs, and it is recorded rather than lost to the error message.
	if first[0].RenderedIf == nil || *first[0].RenderedIf != "maybe" {
		t.Fatalf("rendered_if = %v, want \"maybe\"", first[0].RenderedIf)
	}

	if _, _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task after retry = %s/%q, want done: the guard is re-evaluated, not replayed",
			done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("rows = %d, want 2", len(runs))
	}
	if runs[1].State != store.StepSucceeded {
		t.Errorf("second row = %s, want succeeded — the recorded %q was not consulted",
			runs[1].State, *first[0].RenderedIf)
	}
	if runs[0].RenderedIf == nil || *runs[0].RenderedIf != "maybe" {
		t.Errorf("the first row's rendered_if became %v; it records what that verdict saw", runs[0].RenderedIf)
	}
}

// TestStepInputRecordsTheLevelThatSuppliedEachField: §8.6 provenance is
// persisted, not re-resolved on read. Patching the task's overrides afterwards
// is the case that separates the two — the resolver would now name `task` for
// all three, and the row must still name the levels that actually supplied
// this attempt's values.
func TestStepInputRecordsTheLevelThatSuppliedEachField(t *testing.T) {
	h := newEngineHarness(t)
	snapshot := `name: sourced
defaults:
  agent: claude
  model: sonnet
steps:
  - id: implement
    type: agent
    effort: low
    prompt: "Do the work"
`
	task := h.createTask(t, snapshot)
	task.ModelOverride = "opus"
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("rows = %d, want 1", len(runs))
	}
	want := [3]string{"workflow", "task", "step"}
	got := [3]string{runs[0].AgentSource, runs[0].ModelSource, runs[0].EffortSource}
	if got != want {
		t.Fatalf("sources = %v, want %v (defaults agent, task model, step effort)", got, want)
	}

	done.AgentOverride, done.ModelOverride, done.EffortOverride = "codex", "haiku", "high"
	if err := h.store.UpdateTask(t.Context(), done); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	after := h.stepRuns(t, task.ID)
	got = [3]string{after[0].AgentSource, after[0].ModelSource, after[0].EffortSource}
	if got != want {
		t.Errorf("sources after patching the overrides = %v, want %v", got, want)
	}
	if after[0].Agent != "claude" || after[0].Model != "opus" || after[0].Effort != "low" {
		t.Errorf("selection after patching = %s/%s/%s, want claude/opus/low",
			after[0].Agent, after[0].Model, after[0].Effort)
	}
}

// TestStepInputOfACommandStep: one row carries the body's script and its
// check's, written at two different moments of the same attempt, plus the
// shell §8.3 resolved for them and both timeouts.
func TestStepInputOfACommandStep(t *testing.T) {
	h := newEngineHarness(t)
	body := script("echo body", "Write-Output body")
	check := script("echo check", "Write-Output check")
	snapshot := "name: commanding\nsteps:\n" +
		commandStep("build", body,
			`check: '`+check+` {{ .Task.Title }}'`, "timeout: 45s", "check_timeout: 20s")
	task := h.createTask(t, snapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s/%q, want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("rows = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.RenderedRun == nil || !strings.Contains(*run.RenderedRun, "body") {
		t.Errorf("rendered_run = %v, want the body script", run.RenderedRun)
	}
	if run.RenderedCheck == nil || !strings.Contains(*run.RenderedCheck, task.Title) {
		t.Errorf("rendered_check = %v, want the rendered check command", run.RenderedCheck)
	}
	if run.Shell == "" {
		t.Error("shell = \"\", want the §8.3 shell the step ran under")
	}
	if run.TimeoutMS != 45_000 {
		t.Errorf("timeout_ms = %d, want 45000", run.TimeoutMS)
	}
	if run.CheckTimeoutMS != 20_000 {
		t.Errorf("check_timeout_ms = %d, want 20000", run.CheckTimeoutMS)
	}
	if run.WorkDir != done.WorktreePath {
		t.Errorf("work_dir = %q, want %q", run.WorkDir, done.WorktreePath)
	}
}
