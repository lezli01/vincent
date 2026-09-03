package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
		"Step 1  build",        // the ordinary step is unchanged
		"Step 2  green (loop)", // the loop is named from the snapshot; it writes no row
		"iteration 1",          // every iteration gets a header…
		"iteration 2",          //
		"alpha",                // …carrying the for_each item it ran on
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

// TestLoopRollupDisplay pins what the board and the detail header render
// beside k/n. A task with no rollup — every task not currently in a loop —
// must render nothing at all, or the column would grow a permanent empty
// suffix.
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
			name: "count",
			rollup: &apiclient.LoopRollup{
				Driver: "count", Iteration: 4, Total: 10, MaxIterations: 10,
			},
			want: "loop 4/10",
		},
		{
			// A row written before the extent column existed carries none,
			// so the bound is the only number left to count against.
			name:   "a row with no recorded extent falls back to the bound",
			rollup: &apiclient.LoopRollup{Driver: "count", Iteration: 4, MaxIterations: 10},
			want:   "loop 4/10",
		},
		{
			// The whole of issue #317's third fault: the ceiling is not the
			// loop's number, and the extent is.
			name: "for_each counts against its real extent, not the ceiling",
			rollup: &apiclient.LoopRollup{
				Driver: "for_each", Iteration: 2, Total: 3, MaxIterations: 25,
				Item: "internal/store",
			},
			want: "loop 2/3 · internal/store",
		},
		{
			name: "the running body step is the last clause",
			rollup: &apiclient.LoopRollup{
				Driver: "for_each", Iteration: 4, Total: 10, MaxIterations: 10,
				Item: "alpha", BodyStep: "repair", BodyIndex: 2, BodyTotal: 3,
			},
			want: "loop 4/10 · alpha · repair 2/3",
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

// TestFormatStepFitsTheLoopToItsColumn: the step column carries as much of
// the rollup as its width allows and drops the rest from the tail — the body
// step first, then the `for_each` item, then the counter (issue #317). The
// alternative is what the cell did before: wrap a counter onto a second and
// third line, spending the row's whole height on the least of what it says.
func TestFormatStepFitsTheLoopToItsColumn(t *testing.T) {
	task := apiclient.Task{CurrentStep: 2, StepTotal: 7, StepName: "green"}
	if got, want := formatStep(task, true, widthStepMax), "3/7 green"; got != want {
		t.Errorf("formatStep without a loop = %q, want %q", got, want)
	}
	task.Loop = &apiclient.LoopRollup{
		Driver: "for_each", Iteration: 4, Total: 10, MaxIterations: 10,
		Item: "alpha", BodyStep: "repair", BodyIndex: 2, BodyTotal: 3,
	}
	for _, tt := range []struct {
		name  string
		width int
		want  string
	}{
		{name: "everything fits", width: 42, want: "3/7 green · loop 4/10 · alpha · repair 2/3"},
		{name: "one cell short of the body step", width: 41, want: "3/7 green · loop 4/10 · alpha"},
		{name: "one cell short of the item", width: 28, want: "3/7 green · loop 4/10"},
		{name: "one cell short of the counter", width: 20, want: "3/7 green"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatStep(task, true, tt.width); got != tt.want {
				t.Errorf("formatStep at width %d = %q, want %q", tt.width, got, tt.want)
			}
		})
	}

	// widthStepMax is measured off the tier boardcols.go names, a `count:`
	// loop reporting its body step. If this no longer fits exactly, one of
	// the two moved without the other.
	task.Loop.Driver, task.Loop.Item = "count", ""
	full := formatStep(task, true, widthStepMax)
	if want := "3/7 green · loop 4/10 · repair 2/3"; full != want {
		t.Errorf("formatStep at widthStepMax = %q, want %q", full, want)
	}
	if got := ansi.StringWidth(full); got != widthStepMax {
		t.Errorf("the widest tier is %d cells, widthStepMax is %d — boardcols.go is measured off it",
			got, widthStepMax)
	}

	// The narrow column drops the name, and the loop with it: what survives
	// the width budget is k/n (boardcols.go).
	if got, want := formatStep(task, false, widthStepShort), "3/7"; got != want {
		t.Errorf("formatStep without names = %q, want %q", got, want)
	}
}
