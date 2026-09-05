package taskrun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// groupSnapshot builds a workflow whose only step is a parallel group holding
// the given sub-steps, already indented one level deeper than commandStep
// writes them.
func groupSnapshot(groupFields string, subSteps ...string) string {
	var sb strings.Builder
	sb.WriteString("name: verify\nsteps:\n  - id: group\n    type: parallel\n")
	if groupFields != "" {
		for _, line := range strings.Split(groupFields, "\n") {
			fmt.Fprintf(&sb, "    %s\n", line)
		}
	}
	sb.WriteString("    steps:\n")
	for _, sub := range subSteps {
		for _, line := range strings.Split(strings.TrimRight(sub, "\n"), "\n") {
			fmt.Fprintf(&sb, "  %s\n", line)
		}
	}
	return sb.String()
}

// sleepCmd is a portable "take about a second".
func sleepCmd(seconds int) string {
	return script(
		fmt.Sprintf("sleep %d", seconds),
		fmt.Sprintf("Start-Sleep -Seconds %d", seconds),
	)
}

// subRuns indexes a task's step runs by sub-step id.
func subRuns(runs []store.StepRun) map[string][]store.StepRun {
	out := map[string][]store.StepRun{}
	for _, r := range runs {
		out[r.StepID] = append(out[r.StepID], r)
	}
	return out
}

// TestParallelGroupRunsConcurrently is the point of the feature: three
// one-second sub-steps overlap in time rather than queueing behind each other.
// It also pins the row shape — one row per sub-step, all sharing the group's
// step_index, told apart by step_id (task 014 decision 16) — and that the group
// itself writes no row of its own (decision 17).
func TestParallelGroupRunsConcurrently(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := groupSnapshot("max_parallel: 3",
		commandStep("one", sleepCmd(1)),
		commandStep("two", sleepCmd(1)),
		commandStep("three", sleepCmd(1)),
	)
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 3 {
		t.Fatalf("step runs = %d, want 3 — one per sub-step and none for the group", len(runs))
	}
	// Concurrency is read off the recorded intervals, not off the wall clock.
	// The obvious test — "the whole task took under 2.5s" — measures the
	// sub-steps *plus* three shell spawns, and on a loaded Windows runner
	// under -race those spawns alone can cost more than the second of work
	// they wrap, so it failed at 2.57s on a run that was perfectly
	// concurrent. Overlap says exactly what the feature claims and says it
	// independently of how slow the host is: if the three had queued, each
	// would have started after the previous one finished, so the latest start
	// would land at or past the earliest finish. Any pause of the intervals
	// therefore proves they ran at once.
	latestStart, earliestFinish := runs[0].StartedAt, time.Time{}
	for _, r := range runs {
		if r.FinishedAt == nil {
			t.Fatalf("sub-step %q has no finished_at; concurrency cannot be judged", r.StepID)
		}
		if r.StartedAt.After(latestStart) {
			latestStart = r.StartedAt
		}
		if earliestFinish.IsZero() || r.FinishedAt.Before(earliestFinish) {
			earliestFinish = *r.FinishedAt
		}
	}
	if !latestStart.Before(earliestFinish) {
		t.Errorf("sub-step intervals do not overlap: last start %s is not before first finish %s — the group ran serially",
			latestStart.Format(time.RFC3339Nano), earliestFinish.Format(time.RFC3339Nano))
	}
	for _, r := range runs {
		if r.StepIndex != 0 {
			t.Errorf("sub-step %q step_index = %d, want the group's 0", r.StepID, r.StepIndex)
		}
		if r.State != store.StepSucceeded {
			t.Errorf("sub-step %q state = %s, want succeeded", r.StepID, r.State)
		}
		if r.StepID == "group" {
			t.Error("the group wrote a step_runs row of its own; its outcome is derived")
		}
	}
	if got := len(subRuns(runs)); got != 3 {
		t.Errorf("distinct sub-step ids = %d, want 3", got)
	}
}

// TestParallelGroupMaxParallelBounds: the same three sub-steps under
// max_parallel 1 must queue, which is what makes the knob real rather than
// advisory.
func TestParallelGroupMaxParallelBounds(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := groupSnapshot("max_parallel: 1",
		commandStep("one", sleepCmd(1)),
		commandStep("two", sleepCmd(1)),
	)
	task := h.createTask(t, snapshot)

	started := time.Now()
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	elapsed := time.Since(started)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	if elapsed < 2*time.Second {
		t.Errorf("group took %s for 2x1s sub-steps at max_parallel 1, want at least 2s", elapsed)
	}
}

// TestParallelGroupFailureDoesNotCancelSiblings covers decision 18: the group
// fails, but only after every sub-step it started has finished on its own.
func TestParallelGroupFailureDoesNotCancelSiblings(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := groupSnapshot("max_parallel: 3",
		commandStep("fails", script("exit 3", "exit 3"), "max_retries: 0"),
		commandStep("slow", sleepCmd(1), "max_retries: 0"),
	)
	task := h.createTask(t, snapshot)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked on the failing sub-step", blocked.State)
	}
	if blocked.BlockReason != ReasonNonzeroExit {
		t.Errorf("block_reason = %q, want %q", blocked.BlockReason, ReasonNonzeroExit)
	}
	by := subRuns(h.stepRuns(t, task.ID))
	if len(by["slow"]) != 1 || by["slow"][0].State != store.StepSucceeded {
		t.Errorf("sibling runs = %+v, want one succeeded row — a failure must not cancel it", by["slow"])
	}
	if len(by["fails"]) != 1 || by["fails"][0].State != store.StepFailed {
		t.Errorf("failing runs = %+v, want one failed row", by["fails"])
	}
}

// TestParallelGroupRetriesOnlyTheFailedSubStep covers the per-sub-step retry
// budget: the flaky sub-step burns its own retry while its siblings run once.
func TestParallelGroupRetriesOnlyTheFailedSubStep(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	// Succeeds on the second attempt: the first creates the marker and fails,
	// the second finds it.
	flaky := script(
		"if [ -f marker ]; then exit 0; fi; touch marker; exit 1",
		"if (Test-Path marker) { exit 0 }; New-Item -ItemType File marker | Out-Null; exit 1",
	)
	snapshot := groupSnapshot("max_parallel: 2",
		commandStep("flaky", flaky, "max_retries: 1"),
		commandStep("steady", script("true", "Write-Output ok"), "max_retries: 1"),
	)
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done after the retry", done.State, done.BlockReason)
	}
	by := subRuns(h.stepRuns(t, task.ID))
	if len(by["flaky"]) != 2 {
		t.Errorf("flaky attempts = %d, want 2", len(by["flaky"]))
	}
	if len(by["steady"]) != 1 {
		t.Errorf("steady attempts = %d, want 1 — a sibling's retry is not its own", len(by["steady"]))
	}
	// Attempt numbers are per sub-step, not per index: both start at 1.
	for id, runs := range by {
		if runs[0].Attempt != 1 {
			t.Errorf("sub-step %q first attempt = %d, want 1", id, runs[0].Attempt)
		}
	}
}

// TestParallelGroupRetryDoesNotRerunSucceededSubSteps is the re-admission
// half of "a retry re-runs only the failed sub-step": a human retry after a
// block must not redo work that already succeeded, which the engine derives
// from the rows rather than from a stored cursor.
func TestParallelGroupRetryDoesNotRerunSucceededSubSteps(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	// Fails on the first attempt only, so the human retry can succeed.
	oncePerTask := script(
		"if [ -f marker ]; then exit 0; fi; touch marker; exit 1",
		"if (Test-Path marker) { exit 0 }; New-Item -ItemType File marker | Out-Null; exit 1",
	)
	snapshot := groupSnapshot("max_parallel: 2",
		commandStep("fails", oncePerTask, "max_retries: 0"),
		commandStep("succeeds", script("true", "Write-Output ok"), "max_retries: 0"),
	)
	task := h.createTask(t, snapshot)

	if blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone); blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked before the retry", blocked.State)
	}
	if _, _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}

	by := subRuns(h.stepRuns(t, task.ID))
	if len(by["succeeds"]) != 1 {
		t.Errorf("already-succeeded sub-step ran %d times, want 1 — a retry re-runs only what failed",
			len(by["succeeds"]))
	}
	if len(by["fails"]) != 2 {
		t.Errorf("failed sub-step attempts = %d, want 2 (the failure and the retry)", len(by["fails"]))
	}
}

// TestParallelGroupTimeoutFails: a group-level timeout bounds the whole
// group, and its expiry is a failure rather than an interruption — an
// interruption would re-queue the task and run straight back into it.
func TestParallelGroupTimeoutFails(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := groupSnapshot("max_parallel: 2\ntimeout: 1s",
		commandStep("slow", sleepCmd(30), "max_retries: 0"),
		commandStep("quick", script("true", "Write-Output ok"), "max_retries: 0"),
	)
	task := h.createTask(t, snapshot)

	blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked on the group timeout", blocked.State)
	}
	if blocked.BlockReason != ReasonTimeout {
		t.Errorf("block_reason = %q, want %q", blocked.BlockReason, ReasonTimeout)
	}
}

// TestParallelSubStepTranscriptsDoNotCollide: sub-steps share a step_index,
// so their transcript names must not be built from the index alone.
func TestParallelSubStepTranscriptsDoNotCollide(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := groupSnapshot("max_parallel: 2",
		commandStep("alpha", script("echo alpha", "Write-Output alpha")),
		commandStep("beta", script("echo beta", "Write-Output beta")),
	)
	task := h.createTask(t, snapshot)
	if done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked); done.State != store.TaskDone {
		t.Fatalf("task state = %s, want done", done.State)
	}

	seen := map[string]string{}
	for _, r := range h.stepRuns(t, task.ID) {
		if r.TranscriptPath == "" {
			t.Errorf("sub-step %q recorded no transcript path", r.StepID)
			continue
		}
		if other, dup := seen[r.TranscriptPath]; dup {
			t.Errorf("sub-steps %q and %q share transcript %s", other, r.StepID, r.TranscriptPath)
		}
		seen[r.TranscriptPath] = r.StepID
	}
}

// TestGroupSiblingsStayInvisibleAcrossAdmissions is TestGroupSiblingsStay-
// Invisible's re-admission half, and it is the case §7.5's own reasoning did
// not cover.
//
// §7.5 says no sibling can be read by another sub-step's guard, and grounds
// that in *when* guards run: before anything in the group starts. That holds
// within one admission. It does not hold across two — a group re-admitted
// after one sub-step failed skips the ones that already succeeded, whose
// `succeeded` rows are still on disk — so the same guard, against the same
// context, would answer one way on the first run and another after a human
// pressed retry.
//
// `peek` writes what it can see of its sibling on every attempt: the first
// (which fails) and the retry. Both lines must be empty. `[succeeded]` on the
// second would mean set membership had become a function of retry history.
func TestGroupSiblingsStayInvisibleAcrossAdmissions(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	peek := script(
		`echo "[{{ (index .Steps "first" ).Status }}]" >> seen.txt; `+
			`if [ -f marker ]; then exit 0; fi; touch marker; exit 1`,
		`Add-Content -Path seen.txt -Value '[{{ (index .Steps "first" ).Status }}]'; `+
			`if (Test-Path marker) { exit 0 }; New-Item -ItemType File marker | Out-Null; exit 1`,
	)
	// max_parallel: 1 in declaration order, so `first` has finished and its row
	// is on disk by the time `peek` renders — only the filter can hide it.
	snapshot := groupSnapshot("max_parallel: 1",
		commandStep("first", script("true", "Write-Output ok"), "max_retries: 0"),
		commandStep("peek", peek, "max_retries: 0"),
	)
	task := h.createTask(t, snapshot)

	if blocked := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone); blocked.State != store.TaskBlocked {
		t.Fatalf("task state = %s, want blocked before the retry", blocked.State)
	}
	if _, _, err := h.runner.Retry(t.Context(), task.ID, store.Override{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}

	raw, err := os.ReadFile(filepath.Join(done.WorktreePath, "seen.txt"))
	if err != nil {
		t.Fatalf("read seen.txt: %v", err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 2 {
		t.Fatalf("seen.txt = %q, want one line per attempt", lines)
	}
	for i, line := range lines {
		if line != "[]" {
			t.Errorf("attempt %d saw sibling status %q, want %q — a group is a set in every admission",
				i+1, line, "[]")
		}
	}
}
