package taskrun

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// loopTotalsAt is the extent recorded on each body row of one step index, in
// row order. Rows the loop itself owns have no iteration and are left out.
func loopTotalsAt(runs []store.StepRun, index int, stepID string) []int {
	var out []int
	for _, r := range runs {
		if r.StepIndex == index && r.Iteration > 0 && (stepID == "" || r.StepID == stepID) {
			out = append(out, r.LoopTotal)
		}
	}
	return out
}

func equalInts(a, b []int) bool {
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

// TestLoopCountRecordsItsExtentOnEveryBodyRow: a `count:` loop writes how many
// iterations it planned onto every row it produces (issue #317).
//
// The number was previously only knowable from the definition, which is why a
// reader had to guess at one — and guessed the global ceiling for the driver
// that carries no number at all. Recording it beside the item costs one column
// and answers "2 of how many?" from the row, which is the only place a
// blocked or paused loop still has an answer.
func TestLoopCountRecordsItsExtentOnEveryBodyRow(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)

	snapshot := loopSnapshot("count: 5", commandStep("tick", appendCmd("ticks.txt", "x")))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	got := loopTotalsAt(h.stepRuns(t, task.ID), 0, "tick")
	if !equalInts(got, []int{5, 5, 5, 5, 5}) {
		t.Errorf("loop_total column = %v, want 5 on each of five body rows", got)
	}
}

// TestLoopForEachRecordsItsListLengthOnEveryBodyRow: the driver that opened
// #317. A three-item `for_each` with neither `count:` nor `max_iterations:`
// records 3 — its own number, not the ceiling that happens to bound it.
func TestLoopForEachRecordsItsListLengthOnEveryBodyRow(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)

	snapshot := loopSnapshot("for_each: [alpha, beta, gamma]",
		commandStep("visit", appendCmd("items.txt", "{{ .Loop.Item }}")))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if got := loopTotalsAt(runs, 0, "visit"); !equalInts(got, []int{3, 3, 3}) {
		t.Errorf("loop_total column = %v, want 3 on each of three body rows", got)
	}
	// The extent is recorded beside the item, not instead of it.
	var items []string
	for _, r := range runs {
		if r.Iteration > 0 && r.StepID == "visit" {
			items = append(items, r.LoopItem)
		}
	}
	if !equalStrings(items, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("loop_item column = %v, want [alpha beta gamma]", items)
	}
}

// TestLoopExtentOnReadmissionIsTheClampedOne: a loop re-admitted over rows it
// already has records the extent planLoop computed, clamp included.
//
// planLoop never lets a re-derived `for_each` list shorten a loop below the
// iterations already on record (§7.8, task 016 decision 8). Recording the
// pre-clamp list length instead would let a later row disagree with an earlier
// one about the same loop — row 1 saying "of 3", row 3 saying "of 1" — which
// is the one way a per-row extent can be worse than the guess it replaces.
//
// The fixture stands in for an admission interrupted mid-body: iterations 1
// and 2 have a finished `head` row each and no `tail` row, against a list that
// now re-derives to a single item.
func TestLoopExtentOnReadmissionIsTheClampedOne(t *testing.T) {
	h := newEngineHarness(t)
	ctx := t.Context()

	snapshot := loopSnapshot("for_each: [alpha]",
		commandStep("head", appendCmd("head.txt", "h")),
		commandStep("tail", appendCmd("tail.txt", "t")),
	)
	task := h.createTask(t, snapshot)

	// Seeded before the scheduler runs, so the task is admitted onto them.
	for i, item := range []string{"alpha", "beta"} {
		finished := time.Now()
		if err := h.store.CreateStepRun(ctx, &store.StepRun{
			TaskID: task.ID, StepIndex: 0, StepID: "head", StepType: "command",
			Attempt: 1, Iteration: i + 1, LoopItem: item,
			State: store.StepSucceeded, FinishedAt: &finished,
		}); err != nil {
			t.Fatalf("seed iteration %d: %v", i+1, err)
		}
	}
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	// Two iterations survive the clamp, so both `tail` rows say "of 2" — the
	// list length of 1 is never what any row records.
	if got := loopTotalsAt(h.stepRuns(t, task.ID), 0, "tail"); !equalInts(got, []int{2, 2}) {
		t.Errorf("loop_total on the resumed rows = %v, want [2 2] — the clamped extent, "+
			"not the one-item list it re-derived", got)
	}
	if got := countLines(t, done.WorktreePath, "tail.txt"); got != 2 {
		t.Errorf("tail ran %d times, want 2 — the clamp keeps both recorded iterations", got)
	}
}
