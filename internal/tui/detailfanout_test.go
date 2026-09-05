package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// fanOutRun is the `fan_out` step's own row (§7.6): the parked parent's
// attempt, whose round rides the `iteration` column (task 080 decision 3).
// Built as a fixture rather than taken from a running engine, because these
// tests are about what the row SAYS, not about who writes it. The step index
// is fixed at 1 and the id at `spread`, matching the two-step workflow
// loadFanOut describes.
func fanOutRun(id int64, round int, state string) apiclient.StepRun {
	r := attempt(id, 1, 1, "spread", state, state == "running")
	r.StepType = stepTypeFanOut
	r.Iteration = round
	r.Agent = nil
	r.TranscriptPath = nil
	return r
}

// loadFanOut wires a task detail carrying both halves the annotation needs:
// the rows the timeline draws, and the §13.2 children rollup it reads the
// lane counts from.
func loadFanOut(d *detail, children *apiclient.ChildrenRollup, rows []apiclient.StepRun) {
	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task: apiclient.Task{
				ID: d.taskID, Title: "fanned out", State: stateAwaitingChildren,
				StepTotal: 2, CurrentStep: 1, Children: children,
			},
			Steps: rows,
			WorkflowSteps: []apiclient.WorkflowStep{
				{Index: 0, ID: "build", Type: "command"},
				{Index: 1, ID: "spread", Type: stepTypeFanOut},
			},
		},
	})
}

// TestDetailTimelineAnnotatesRunningFanOut: a fan-out whose lanes are still
// working is on the timeline as a `running` row, and that row says what the
// subtree is doing — the round and the lane counts — the way the board
// annotates the parent (issue #322, boardStateLabel).
func TestDetailTimelineAnnotatesRunningFanOut(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 21

	loadFanOut(d,
		&apiclient.ChildrenRollup{Total: 5, Settled: 3},
		[]apiclient.StepRun{
			attempt(1, 0, 1, "build", "succeeded", false),
			fanOutRun(2, 0, "running"),
		})

	got := ansi.Strip(d.timelinePanel(30))
	for _, want := range []string{
		"Step 2  spread",     // the fan-out is on the timeline at all
		"running",            //
		"round 0 · 3/5 done", // …carrying the round and the lane rollup
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline missing %q:\n%s", want, got)
		}
	}
}

// TestDetailTimelineFanOutReportsBlockedLanes: the rollup's priority order is
// the board's — a blocked lane outranks the done counter, because that is the
// one a reader has to act on (ChildrenRollup.Summary).
func TestDetailTimelineFanOutReportsBlockedLanes(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 22

	loadFanOut(d,
		&apiclient.ChildrenRollup{Total: 5, Settled: 1, Blocked: []int64{31, 32}},
		[]apiclient.StepRun{fanOutRun(1, 0, "running")})

	got := ansi.Strip(d.timelinePanel(30))
	if want := "round 0 · 2 blocked"; !strings.Contains(got, want) {
		t.Errorf("timeline missing %q:\n%s", want, got)
	}
	if strings.Contains(got, "done") {
		t.Errorf("blocked lanes must outrank the done counter:\n%s", got)
	}
}

// TestDetailTimelineFanOutRoundOnlyOnceOnScreen: a multi-round fan-out draws
// `round N` tier headers (task 080 decision 3), so the row beneath one must
// not repeat it — but it still carries the lane counts, which no header has.
func TestDetailTimelineFanOutRoundOnlyOnceOnScreen(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 23

	loadFanOut(d,
		&apiclient.ChildrenRollup{Total: 4, Settled: 2},
		[]apiclient.StepRun{
			fanOutRun(1, 0, "succeeded"),
			fanOutRun(2, 1, "running"),
		})

	got := ansi.Strip(d.timelinePanel(30))
	if want := "round 1"; !strings.Contains(got, want) {
		t.Fatalf("timeline missing the tier header %q:\n%s", want, got)
	}
	if strings.Contains(got, "round 1 · ") {
		t.Errorf("round named twice — the tier header already carries it:\n%s", got)
	}
	if want := "2/4 done"; !strings.Contains(got, want) {
		t.Errorf("timeline missing %q:\n%s", want, got)
	}
}

// TestDetailTimelineFanOutWithoutRollup: `children` is absent from tasks the
// server has no subtree for, and an empty rollup summarizes to nothing. Both
// render the row exactly as it would have rendered without this annotation —
// no separator and no bracket left dangling.
func TestDetailTimelineFanOutWithoutRollup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		children *apiclient.ChildrenRollup
	}{
		{"nil", nil},
		{"empty", &apiclient.ChildrenRollup{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 24
			row := fanOutRun(1, 0, "running")
			loadFanOut(d, tc.children, []apiclient.StepRun{row})

			got := ansi.Strip(d.timelinePanel(30))
			if !strings.Contains(got, "Step 2  spread") {
				t.Fatalf("timeline missing the fan-out step:\n%s", got)
			}
			// The attempt line alone: the header above it carries `·`
			// separators of its own, which say nothing about lanes.
			line := attemptLineOf(t, got)
			for _, unwanted := range []string{"round", "·", "(", "done", "blocked"} {
				if strings.Contains(line, unwanted) {
					t.Errorf("plain row must not carry %q: %q", unwanted, line)
				}
			}
			if want := "running"; !strings.Contains(line, want) {
				t.Errorf("attempt line %q missing %q", line, want)
			}
		})
	}
}

// TestDetailTimelineRollupOnlyOnTheRunningFanOut: every other row is
// unchanged. A settled fan-out attempt is history — the rollup describes the
// subtree as it is now — and an ordinary step never had lanes at all.
func TestDetailTimelineRollupOnlyOnTheRunningFanOut(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 25

	loadFanOut(d,
		&apiclient.ChildrenRollup{Total: 5, Settled: 5},
		[]apiclient.StepRun{
			attempt(1, 0, 1, "build", "running", true),
			fanOutRun(2, 0, "succeeded"),
		})

	got := ansi.Strip(d.timelinePanel(30))
	if strings.Contains(got, "5/5 done") {
		t.Errorf("rollup annotated a row that is not a running fan-out:\n%s", got)
	}
}

// TestDetailFanOutRollupSurvivesWrapping: the timeline wraps between whole
// styled fields (wrapTimelineFields), so a narrow pane pushes the rollup onto
// a continuation line rather than cutting it in half.
func TestDetailFanOutRollupSurvivesWrapping(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 26
	d.width = 34

	loadFanOut(d,
		&apiclient.ChildrenRollup{Total: 12, Settled: 7},
		[]apiclient.StepRun{fanOutRun(1, 0, "running")})

	got := ansi.Strip(d.timelinePanel(30))
	if want := "round 0 · 7/12 done"; !strings.Contains(got, want) {
		t.Errorf("timeline missing %q at width %d:\n%s", want, d.width, got)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w > d.width {
			t.Errorf("line wider than the pane (%d > %d): %q", w, d.width, line)
		}
	}
}

// attemptLineOf is the one attempt row on a rendered timeline. The panel's
// header carries `·` separators of its own, so an assertion about what the
// row does *not* say has to be scoped to the row.
func attemptLineOf(t *testing.T, panel string) string {
	t.Helper()
	for _, line := range strings.Split(panel, "\n") {
		if strings.Contains(line, "Attempt") {
			return line
		}
	}
	t.Fatalf("no attempt line on the timeline:\n%s", panel)
	return ""
}
