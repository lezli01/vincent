package taskrun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// loopSnapshot builds a workflow whose only step is a loop holding the given
// body steps, indented one level deeper than commandStep writes them.
func loopSnapshot(loopFields string, body ...string) string {
	var sb strings.Builder
	sb.WriteString("name: repeat\nsteps:\n  - id: loop\n    type: loop\n")
	for _, line := range strings.Split(strings.TrimRight(loopFields, "\n"), "\n") {
		fmt.Fprintf(&sb, "    %s\n", line)
	}
	sb.WriteString("    steps:\n")
	for _, step := range body {
		for _, line := range strings.Split(strings.TrimRight(step, "\n"), "\n") {
			fmt.Fprintf(&sb, "  %s\n", line)
		}
	}
	return sb.String()
}

// appendCmd writes one line to a file in the worktree, portably. It is how a
// loop test counts its own iterations without depending on the row order.
func appendCmd(file, text string) string {
	return script(
		fmt.Sprintf("echo %s >> %s", text, file),
		fmt.Sprintf("Add-Content -Path %s -Value '%s'", file, text),
	)
}

// countLines is how many lines a marker file in the task's worktree has.
func countLines(t *testing.T, worktree, file string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(worktree, file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return len(strings.Fields(string(raw)))
}

// iterationsOf indexes a loop's rows by iteration, in row order.
func iterationsOf(runs []store.StepRun) map[int][]store.StepRun {
	out := map[int][]store.StepRun{}
	for _, r := range runs {
		out[r.Iteration] = append(out[r.Iteration], r)
	}
	return out
}

// TestLoopCountRunsItsBody is the shape §7.8 exists to allow: a body run a
// fixed number of times in the task's one worktree. It also pins the row
// shape — one row per body step per iteration, all sharing the loop's
// step_index, told apart by step_id and a 1-based iteration — and that the
// loop itself writes no row of its own (decision 7).
func TestLoopCountRunsItsBody(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := loopSnapshot("count: 3",
		commandStep("tick", appendCmd("ticks.txt", "x")),
	)
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	if got := countLines(t, done.WorktreePath, "ticks.txt"); got != 3 {
		t.Errorf("the body ran %d times, want 3", got)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 3 {
		t.Fatalf("step runs = %d, want 3 — one per iteration and none for the loop", len(runs))
	}
	for i, r := range runs {
		if r.StepID == "loop" {
			t.Error("the loop wrote a step_runs row of its own; its outcome is derived")
		}
		if r.StepIndex != 0 {
			t.Errorf("row %d step_index = %d, want the loop's 0", i, r.StepIndex)
		}
		if want := i + 1; r.Iteration != want {
			t.Errorf("row %d iteration = %d, want %d (1-based, in position order)", i, r.Iteration, want)
		}
		if r.State != store.StepSucceeded {
			t.Errorf("row %d state = %s, want succeeded", i, r.State)
		}
	}
}

// TestLoopBreakEndsTheLoop is the converge archetype end to end: a probe that
// is allowed to fail, a break reading it, and a repair. The break must be
// able to see a *failed* row two lines above it in its own body, which is the
// whole of decision 9's positional visibility rule — under the old
// `StepIndex >= index` filter the loop would never break.
func TestLoopBreakEndsTheLoop(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	// The probe fails while `repairs.txt` has fewer than two lines, so the
	// third pass is the one that breaks.
	probe := script(
		"test -f repairs.txt && test $(wc -l < repairs.txt) -ge 2",
		"if (-not (Test-Path repairs.txt)) { exit 1 }; if ((Get-Content repairs.txt).Count -lt 2) { exit 1 }",
	)
	snapshot := loopSnapshot("count: 8",
		commandStep("suite", probe, "allow_failure: true", "max_retries: 0"),
		"  - {id: passed, type: break, if: '{{ eq (index .Steps \"suite\").ExitCode 0 }}'}\n",
		commandStep("repair", appendCmd("repairs.txt", "fix")),
	)
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done — a break ends the loop successfully",
			done.State, done.BlockReason)
	}
	if got := countLines(t, done.WorktreePath, "repairs.txt"); got != 2 {
		t.Errorf("repairs = %d, want 2 — the loop must break on the pass that finds the probe green", got)
	}

	byIteration := iterationsOf(h.stepRuns(t, task.ID))
	if len(byIteration) != 3 {
		t.Fatalf("iterations = %d, want 3 (two repairs, then a break)", len(byIteration))
	}
	// The break took on iteration 3, and a taken break is `stopped`: the loop
	// ended there, and the row is what says so.
	third := byIteration[3]
	if len(third) != 2 {
		t.Fatalf("iteration 3 rows = %d, want 2 — the probe and the break that ended the loop", len(third))
	}
	if third[1].StepID != "passed" || third[1].State != store.StepStopped {
		t.Errorf("iteration 3 last row = %s/%s, want passed/stopped", third[1].StepID, third[1].State)
	}
	// A break that did not take is an ordinary `succeeded` decision row.
	if first := byIteration[1]; first[1].State != store.StepSucceeded {
		t.Errorf("iteration 1 break state = %s, want succeeded — it evaluated and let the body carry on",
			first[1].State)
	}
}

// TestLoopConditionEndsTheIteration covers decision 3's other half: a
// `condition` inside a body is `continue`. Its false verdict must end that
// iteration and leave the loop running, not stop the task at `done` the way
// the same step type does at the top level (§7.7).
func TestLoopConditionEndsTheIteration(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := loopSnapshot("count: 3",
		commandStep("head", appendCmd("head.txt", "h")),
		"  - {id: onward, type: condition, if: '{{ .Loop.IsLast }}'}\n",
		commandStep("tail", appendCmd("tail.txt", "t")),
	)
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	if got := countLines(t, done.WorktreePath, "head.txt"); got != 3 {
		t.Errorf("head ran %d times, want 3 — a false condition ends the iteration, not the loop", got)
	}
	if got := countLines(t, done.WorktreePath, "tail.txt"); got != 1 {
		t.Errorf("tail ran %d times, want 1 — only the last iteration got past the condition", got)
	}
}

// TestLoopForEachFromStepOutput is the second driver, fed by a list a step
// discovered at run time — the case `fan_out` cannot serve, because its lane
// list has to be static in the snapshot (decision 4).
func TestLoopForEachFromStepOutput(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	list := script(
		"printf 'alpha\\nbeta\\ngamma\\n'",
		"'alpha'; 'beta'; 'gamma'",
	)
	snapshot := "name: each\nsteps:\n" +
		commandStep("discover", list) +
		"  - id: visit\n    type: loop\n    for_each: '{{ .Steps.discover.Result }}'\n    steps:\n" +
		bodyIndent(commandStep("touch", appendCmd("items.txt", "{{ .Loop.Item }}")))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	raw, err := os.ReadFile(filepath.Join(done.WorktreePath, "items.txt"))
	if err != nil {
		t.Fatalf("read items.txt: %v", err)
	}
	if got := strings.Fields(string(raw)); !equalStrings(got, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("items = %v, want [alpha beta gamma] in list order", got)
	}

	// The item is recorded on the row, not re-derived (decision 8).
	var items []string
	for _, r := range h.stepRuns(t, task.ID) {
		if r.StepIndex == 1 {
			items = append(items, r.LoopItem)
		}
	}
	if !equalStrings(items, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("loop_item column = %v, want [alpha beta gamma]", items)
	}
}

// TestLoopForEachEmptyListSucceeds: a loop with nothing to iterate decided
// correctly, so it succeeds having run nothing — the same posture §7.5 takes
// for a group whose every sub-step was guarded off.
func TestLoopForEachEmptyListSucceeds(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := "name: each\nsteps:\n" +
		commandStep("discover", script("true", "exit 0")) +
		"  - id: visit\n    type: loop\n    for_each: '{{ .Steps.discover.Result }}'\n    steps:\n" +
		bodyIndent(commandStep("touch", appendCmd("items.txt", "x")))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	for _, r := range h.stepRuns(t, task.ID) {
		if r.StepIndex == 1 {
			t.Errorf("the loop wrote a row (%s) for an empty list; it must run nothing", r.StepID)
		}
	}
}

// TestLoopForEachOverCeilingBlocks pins decision 5: reaching the cap is not a
// decision the workflow made, so the task blocks with `loop_limit` before the
// first iteration rather than truncating the list or advancing.
func TestLoopForEachOverCeilingBlocks(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) { c.Loop.MaxIterations = 2 })
	h.start(t)
	list := script("printf 'a\\nb\\nc\\n'", "'a'; 'b'; 'c'")
	snapshot := "name: each\nsteps:\n" +
		commandStep("discover", list) +
		"  - id: visit\n    type: loop\n    for_each: '{{ .Steps.discover.Result }}'\n    steps:\n" +
		bodyIndent(commandStep("touch", appendCmd("items.txt", "{{ .Loop.Item }}")))
	task := h.createTask(t, snapshot)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked", blocked.State)
	}
	if blocked.BlockReason != ReasonLoopLimit {
		t.Errorf("block_reason = %q, want %q", blocked.BlockReason, ReasonLoopLimit)
	}
	for _, r := range h.stepRuns(t, task.ID) {
		if r.StepIndex == 1 {
			t.Errorf("the loop ran %q before blocking; the cap is checked before iteration 1", r.StepID)
		}
	}
}

// TestLoopBodyFailureBlocksTheTask: retries are for a step that failed,
// iterations are for a body that succeeded and must run again (decision 6).
// A body step that exhausts its budget fails the iteration and blocks the
// task with that step's own reason, not with `loop_limit`.
func TestLoopBodyFailureBlocksTheTask(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := loopSnapshot("count: 3",
		commandStep("tick", appendCmd("ticks.txt", "x")),
		commandStep("boom", script("exit 3", "exit 3"), "max_retries: 0"),
	)
	task := h.createTask(t, snapshot)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked", blocked.State)
	}
	if blocked.BlockReason != ReasonNonzeroExit {
		t.Errorf("block_reason = %q, want the body step's own %q", blocked.BlockReason, ReasonNonzeroExit)
	}
	byIteration := iterationsOf(h.stepRuns(t, task.ID))
	if len(byIteration) != 1 {
		t.Errorf("iterations = %d, want 1 — a failed body step ends the loop, it does not roll on", len(byIteration))
	}
}

// TestLoopRetryResumesMidIteration is decision 7's payoff and decision 12's
// `retry`: a re-admitted loop skips the body steps whose latest attempt
// succeeded and carries on from the failed one, in the iteration it was in.
// Restarting at iteration 1 would discard work a human may have waited an
// hour for.
func TestLoopRetryResumesMidIteration(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	// `boom` fails while the flag file is absent; the test creates it and
	// retries, standing in for the human who fixed whatever was wrong.
	boom := script(
		"test -f fixed.txt",
		"if (-not (Test-Path fixed.txt)) { exit 1 }",
	)
	snapshot := loopSnapshot("count: 2",
		commandStep("tick", appendCmd("ticks.txt", "x")),
		commandStep("boom", boom, "max_retries: 0"),
	)
	task := h.createTask(t, snapshot)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked", blocked.State)
	}
	if got := countLines(t, blocked.WorktreePath, "ticks.txt"); got != 1 {
		t.Fatalf("ticks before the retry = %d, want 1", got)
	}
	if err := os.WriteFile(filepath.Join(blocked.WorktreePath, "fixed.txt"), []byte("ok\n"), 0o600); err != nil {
		t.Fatalf("write flag file: %v", err)
	}
	if _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state after retry = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	// Two ticks, not three: iteration 1's `tick` had already succeeded, so
	// the resumed loop went straight to `boom`.
	if got := countLines(t, done.WorktreePath, "ticks.txt"); got != 2 {
		t.Errorf("ticks = %d, want 2 — a resumed loop must not re-run a body step that succeeded", got)
	}
}

// TestLoopSkipSkipsTheWholeLoop pins decision 12: `skip` keeps its §6 meaning
// and skips the whole loop step. There is no "skip this iteration" action —
// a second meaning for one word would need a state nobody can see.
func TestLoopSkipSkipsTheWholeLoop(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := loopSnapshot("count: 5",
		commandStep("tick", appendCmd("ticks.txt", "x")),
		commandStep("boom", script("exit 1", "exit 1"), "max_retries: 0"),
	) + commandStep("after", appendCmd("after.txt", "done"))
	task := h.createTask(t, snapshot)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked", blocked.State)
	}
	if _, err := h.runner.Skip(t.Context(), task.ID); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state after skip = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	if got := countLines(t, done.WorktreePath, "ticks.txt"); got != 1 {
		t.Errorf("ticks = %d, want 1 — skip must advance past the loop, not into its next iteration", got)
	}
	if got := countLines(t, done.WorktreePath, "after.txt"); got != 1 {
		t.Errorf("the step after the loop ran %d times, want 1", got)
	}
}

// TestLoopTranscriptsCarryTheirIteration pins decision 13's naming. Nothing
// parses these — `transcript_path` is on the row — so what the test defends is
// the human reading a directory, and the collision that made the segment
// necessary: without it every iteration of `tick` would open, and truncate,
// one file.
func TestLoopTranscriptsCarryTheirIteration(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := loopSnapshot("count: 3", commandStep("tick", appendCmd("ticks.txt", "x")))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	seen := map[string]bool{}
	for _, r := range h.stepRuns(t, task.ID) {
		name := filepath.Base(r.TranscriptPath)
		want := fmt.Sprintf("0-i%d-tick-1.jsonl", r.Iteration)
		if name != want {
			t.Errorf("iteration %d transcript = %q, want %q", r.Iteration, name, want)
		}
		if seen[name] {
			t.Errorf("two iterations share the transcript %q; each would truncate the other", name)
		}
		seen[name] = true
	}
}

// bodyIndent shifts an already-rendered step one YAML level deeper, for a
// loop body written inline rather than through loopSnapshot.
func bodyIndent(block string) string {
	var sb strings.Builder
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		fmt.Fprintf(&sb, "  %s\n", line)
	}
	return sb.String()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGroupSiblingsStayInvisible is the case decision 9 says a naive rewrite
// of the visibility filter would break. `run.StepIndex >= env.index` hid a
// group sibling's failed row because siblings share the group's index; the
// positional rule has to keep hiding it, and does so because a `parallel`
// sub-step has no body position and therefore never precedes another.
//
// `max_parallel: 1` makes the probe finish before the reader starts, so the
// row is there to be seen and only the filter stops it. What the reader
// renders is the zero StepResult — `index` on a missing map key — so a 4 in
// the file would mean §7.5's sibling-blindness had been lost.
func TestGroupSiblingsStayInvisible(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := groupSnapshot("max_parallel: 1",
		commandStep("probe", script("exit 4", "exit 4"), "allow_failure: true", "max_retries: 0"),
		commandStep("peek", appendCmd("seen.txt", `{{ (index .Steps "probe").ExitCode }}`)),
	)
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	raw, err := os.ReadFile(filepath.Join(done.WorktreePath, "seen.txt"))
	if err != nil {
		t.Fatalf("read seen.txt: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "0" {
		t.Errorf("sibling read exit code %q, want %q — a group is a set and its members must not see each other",
			got, "0")
	}
}

// TestLoopBodyStepSeesEarlierBodySteps is the other side of the same rule and
// the reason it had to widen: a body step's guard reads what a body step
// above it in the *same* iteration produced, which sharing the loop's
// step_index used to make impossible.
func TestLoopBodyStepSeesEarlierBodySteps(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := loopSnapshot("count: 2",
		commandStep("probe", script("exit 4", "exit 4"), "allow_failure: true", "max_retries: 0"),
		commandStep("peek", appendCmd("peeked.txt", "x"),
			`if: '{{ eq (index .Steps "probe").ExitCode 4 }}'`),
	)
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	if got := countLines(t, done.WorktreePath, "peeked.txt"); got != 2 {
		t.Errorf("guarded body step ran %d times, want 2 — it must read the probe above it", got)
	}
}
