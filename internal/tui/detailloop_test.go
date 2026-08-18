package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// iteration is `attempt` with a loop position on it (§7.8). The step index is
// the loop's own — a body step shares it, which is the whole reason the
// iteration column exists — so it is fixed at 1 here, matching the workflow
// these tests describe.
func iteration(id int64, iter int, name, state, item string) apiclient.StepRun {
	r := attempt(id, 1, 1, name, state, false)
	r.Iteration = iter
	if item != "" {
		r.LoopItem = &item
	}
	return r
}

// TestDetailTimelineGroupsLoopIterations: a loop body's rows all share the
// loop's step index *and* repeat, so the timeline needs a tier above the
// sub-step one. Ten passes of a four-step body is forty rows, and a reader
// arriving at a blocked task wants the pass it stopped on — so the iterations
// are folded shut with the latest open (task 016 decision 14).
func TestDetailTimelineGroupsLoopIterations(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 11

	build := attempt(1, 0, 1, "build", "succeeded", false)
	rows := []apiclient.StepRun{
		build,
		iteration(2, 1, "suite", "failed", "alpha"),
		iteration(3, 1, "repair", "succeeded", "alpha"),
		iteration(4, 2, "suite", "succeeded", "beta"),
		iteration(5, 2, "repair", "succeeded", "beta"),
	}
	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task:  apiclient.Task{ID: d.taskID, Title: "looped", State: stateRunning, StepTotal: 2},
			Steps: rows,
			WorkflowSteps: []apiclient.WorkflowStep{
				{Index: 0, ID: "build", Type: "command"},
				{Index: 1, ID: "green", Type: "loop"},
			},
		},
	})

	got := d.timelinePanel(30)
	for _, want := range []string{
		"1 build",        // the ordinary step is unchanged
		"2 green (loop)", // the loop is named from the snapshot; it writes no row
		"iteration 1",    // every iteration gets a header…
		"iteration 2",    //
		"alpha",          // …carrying the for_each item it ran on
		"beta",
		diffFoldClosed, // iteration 1 is folded shut
		diffFoldOpen,   // iteration 2, the latest, is open
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline missing %q:\n%s", want, got)
		}
	}

	// Folded means folded: iteration 1's attempts are not rendered, so the
	// only body-step tiers on screen belong to iteration 2.
	if n := strings.Count(got, "· suite"); n != 1 {
		t.Errorf("`suite` tiers on screen = %d, want 1 — only the open iteration renders its attempts", n)
	}

	// The iterations must not interleave: iteration 1's header comes before
	// iteration 2's, and a body step belongs under its own pass.
	first := strings.Index(got, "iteration 1")
	second := strings.Index(got, "iteration 2")
	if first < 0 || second < 0 || first > second {
		t.Errorf("iteration headers out of order (%d, %d):\n%s", first, second, got)
	}
}

// TestLoopRollupDisplay pins what the board renders beside k/n. A task with no
// rollup — every task not currently in a loop — must render nothing at all,
// or the column would grow a permanent empty suffix.
func TestLoopRollupDisplay(t *testing.T) {
	tests := []struct {
		name   string
		rollup *apiclient.LoopRollup
		want   string
	}{
		{name: "absent", rollup: nil},
		{
			name:   "before the first iteration has a row",
			rollup: &apiclient.LoopRollup{Driver: "count", MaxIterations: 10},
		},
		{
			name:   "count",
			rollup: &apiclient.LoopRollup{Driver: "count", Iteration: 4, MaxIterations: 10},
			want:   "loop 4/10",
		},
		{
			name: "for_each names its item",
			rollup: &apiclient.LoopRollup{
				Driver: "for_each", Iteration: 2, MaxIterations: 25, Item: "internal/store",
			},
			want: "loop 2/25 internal/store",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rollup.Display(); got != tt.want {
				t.Errorf("Display() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatStepAppendsTheLoop: the board's step column carries the iteration
// only for a task that is in one, so nothing changes for every other row.
func TestFormatStepAppendsTheLoop(t *testing.T) {
	task := apiclient.Task{CurrentStep: 2, StepTotal: 7, StepName: "green"}
	if got, want := formatStep(task, true), "3/7 green"; got != want {
		t.Errorf("formatStep without a loop = %q, want %q", got, want)
	}
	task.Loop = &apiclient.LoopRollup{Driver: "count", Iteration: 4, MaxIterations: 10}
	if got, want := formatStep(task, true), "3/7 green · loop 4/10"; got != want {
		t.Errorf("formatStep in a loop = %q, want %q", got, want)
	}
	// The narrow column drops the name, and the loop with it: what survives
	// the width budget is k/n (boardcols.go).
	if got, want := formatStep(task, false), "3/7"; got != want {
		t.Errorf("formatStep without names = %q, want %q", got, want)
	}
}
