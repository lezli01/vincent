package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// tierDetail is a task whose step 1 repeats: a two-step body (`suite`, then
// `repair`) run once per entry in `iters`, on the iteration column. kind is
// the snapshot's type for that step — "loop", "fan_out", or "" for a task
// whose snapshot never arrived, where the timeline has only the rows to go on.
func tierDetail(t *testing.T, kind string, iters ...int) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 41

	rows := []apiclient.StepRun{attempt(1, 0, 1, "build", "succeeded", false)}
	id := int64(2)
	for _, it := range iters {
		for _, name := range []string{"suite", "repair"} {
			rows = append(rows, iteration(id, it, name, "succeeded", ""))
			id++
		}
	}
	task := apiclient.TaskDetail{
		Task:  apiclient.Task{ID: d.taskID, Title: "tiered", State: stateRunning, StepTotal: 2},
		Steps: rows,
	}
	if kind != "" {
		task.WorkflowSteps = []apiclient.WorkflowStep{
			{Index: 0, ID: "build", Type: "command"},
			{Index: 1, ID: "green", Type: kind},
		}
	}
	d.applyLoaded(detailLoadedMsg{id: d.taskID, task: task})
	d.focus = focusTimeline
	return d
}

// tierTaskView is tierDetail's loop in the full-screen workspace, on the tab
// whose keys the folds belong to.
func tierTaskView(t *testing.T, iters ...int) *taskView {
	t.Helper()
	v := newTaskView(tierDetail(t, "loop", iters...))
	v.tab = taskTabSteps
	return v
}

// tierStepIndex is the step every tierDetail fixture repeats on.
const tierStepIndex = 1

// firstRowOfTier is the row a folded tier's header stands for — the one row
// of it ↑/↓ may select.
func firstRowOfTier(d *detail, k tierKey) int64 {
	for _, r := range d.attempts() {
		if r.StepIndex == k.index && r.Iteration == k.iteration {
			return r.ID
		}
	}
	return 0
}

// selectTier puts the timeline cursor on a tier's first row, the way ↑/↓
// would land on it.
func selectTier(t *testing.T, d *detail, iteration int) {
	t.Helper()
	id := firstRowOfTier(d, tierKey{tierStepIndex, iteration})
	if id == 0 {
		t.Fatalf("no row at step %d iteration %d", tierStepIndex, iteration)
	}
	d.selectedRun = id
}

// TestTimelineFoldOpensAnEarlierIteration: before issue #317 the rows of a
// pass that was not the latest were skipped by the renderer and reachable by
// nothing, so a loop on iteration 7 hid the failure in iteration 1. The tier
// opens now, and what it draws is attempts with output behind them.
func TestTimelineFoldOpensAnEarlierIteration(t *testing.T) {
	d := tierDetail(t, "loop", 1, 2)

	got := d.renderTimeline(200)
	if !strings.Contains(got, "iteration 1") {
		t.Fatalf("the folded tier lost its header:\n%s", got)
	}
	if n := strings.Count(got, "· suite"); n != 1 {
		t.Fatalf("`suite` tiers on screen = %d, want 1 — a folded tier draws no rows:\n%s", n, got)
	}

	// space, from the timeline, on the folded tier's first row.
	selectTier(t, d, 1)
	d.updateKey(registryKey(t, "space"))
	got = d.renderTimeline(200)
	if n := strings.Count(got, "· suite"); n != 2 {
		t.Fatalf("`suite` tiers after opening = %d, want 2:\n%s", n, got)
	}

	// The attempts it drew are hit-testable and their transcripts fetchable:
	// walking onto iteration 1's second row moves the output pane to it and
	// puts that attempt's transcript in flight. (The fixture has no client,
	// so the fetch is a nil command; `fetching` is what says one was asked
	// for.)
	d.moveTimelineSelection(1)
	if d.displayRun != d.selectedRun {
		t.Fatalf("output pane is showing run %d, cursor is on %d", d.displayRun, d.selectedRun)
	}
	if d.noTranscript || !d.fetching {
		t.Fatalf("no transcript fetch for the newly reachable attempt (noTranscript=%v fetching=%v)",
			d.noTranscript, d.fetching)
	}
	d.renderTimeline(200)
	if !slices.Contains(d.visibleRuns, d.selectedRun) {
		t.Fatalf("run %d is selected but not on screen (visible %v)", d.selectedRun, d.visibleRuns)
	}
}

// TestTimelineFoldAllTiers: O and C act on every tier of the task, not on the
// one under the cursor — the Diff tab's two letters in the Diff tab's meaning.
func TestTimelineFoldAllTiers(t *testing.T) {
	v := tierTaskView(t, 1, 2, 3)

	v.updateKey(registryKey(t, "O"))
	got := v.detail.renderTimeline(200)
	if n := strings.Count(got, "· suite"); n != 3 {
		t.Fatalf("O left %d of 3 tiers open:\n%s", n, got)
	}
	if strings.Contains(got, diffFoldClosed) {
		t.Fatalf("O left a tier folded:\n%s", got)
	}

	v.updateKey(registryKey(t, "C"))
	got = v.detail.renderTimeline(200)
	if n := strings.Count(got, "· suite"); n != 0 {
		t.Fatalf("C left %d tiers open, including the latest:\n%s", n, got)
	}
	if n := strings.Count(got, diffFoldClosed); n != 3 {
		t.Fatalf("closed glyphs = %d, want 3:\n%s", n, got)
	}
}

// TestTimelineFoldKeysInTheWorkspace pins the vocabulary the workspace routes:
// → opens, ← closes, and enter means both things — the fold on a folded tier,
// the Output tab on a drawn attempt.
func TestTimelineFoldKeysInTheWorkspace(t *testing.T) {
	v := tierTaskView(t, 1, 2)
	selectTier(t, v.detail, 1)

	v.updateKey(registryKey(t, "enter"))
	if v.tab != taskTabSteps {
		t.Fatalf("enter on a folded tier left the tab at %v, want Steps & Attempts", v.tab)
	}
	if v.detail.timelineFolded() {
		t.Fatal("enter on a folded tier did not open it")
	}

	// Now that the row under the cursor is drawn, enter means what it always
	// meant.
	v.updateKey(registryKey(t, "enter"))
	if v.tab != taskTabOutput {
		t.Fatalf("enter on a drawn attempt opened %v, want the Output tab", v.tab)
	}

	v.tab = taskTabSteps
	v.updateKey(registryKey(t, "left"))
	if !v.detail.timelineFolded() {
		t.Fatal("← did not close the tier the cursor is in")
	}
	v.updateKey(registryKey(t, "right"))
	if v.detail.timelineFolded() {
		t.Fatal("→ did not open the folded tier")
	}

	// ←/→ are the Output tab's attempt walk there, not folds.
	v.tab = taskTabOutput
	before := v.detail.selectedRun
	v.updateKey(registryKey(t, "right"))
	if v.detail.selectedRun == before {
		t.Fatal("→ on the Output tab stopped moving the attempt selection")
	}
}

// TestTimelineCursorIsNeverInvisible is the criterion that names the bug:
// moveSelection walked every row including the ones the renderer skipped, so
// the highlight vanished and the window jumped to the top. Driving ↓ from the
// first row to the last and back, every selection is either a drawn row or the
// first row of a folded tier whose header carries the highlight.
func TestTimelineCursorIsNeverInvisible(t *testing.T) {
	d := tierDetail(t, "loop", 1, 2, 3, 4)
	// Not the arrival default: a reader who opened an old pass and closed the
	// newest is exactly who walks over a fold.
	d.setFold(tierKey{tierStepIndex, 1}, true)
	d.setFold(tierKey{tierStepIndex, 4}, false)

	runs := d.attempts()
	d.selectedRun = runs[0].ID
	for range len(runs) + 2 {
		assertCursorDrawn(t, d)
		d.moveTimelineSelection(1)
	}
	for range len(runs) + 2 {
		assertCursorDrawn(t, d)
		d.moveTimelineSelection(-1)
	}
	if d.selectedRun != runs[0].ID {
		t.Fatalf("↑ came to rest on run %d, want the first row %d", d.selectedRun, runs[0].ID)
	}
}

func assertCursorDrawn(t *testing.T, d *detail) {
	t.Helper()
	out := d.renderTimeline(200)
	if slices.Contains(d.visibleRuns, d.selectedRun) {
		return
	}
	runs := d.attempts()
	k, folded := d.foldedSelection(loopIndexes(runs), latestIterations(runs))
	if !folded {
		t.Fatalf("run %d is selected, is not on screen, and is in no folded tier:\n%s", d.selectedRun, out)
	}
	if first := firstRowOfTier(d, k); first != d.selectedRun {
		t.Fatalf("run %d is selected inside a folded tier whose first row is %d — "+
			"↑/↓ must stop on the first row only", d.selectedRun, first)
	}
	header := styleSelected.Render("    " + iterationHeader(d.runByID(d.selectedRun), false, d.tierNoun(k.index)))
	if !strings.Contains(out, header) {
		t.Fatalf("the folded tier standing in for run %d is not highlighted:\n%s", d.selectedRun, out)
	}
}

// TestTimelineTierNoun: a multi-round `fan_out` writes its round on the very
// column a loop writes its pass on (task 080 decision 3), 0-based. Only the
// snapshot's step type tells the two apart, and round 0 needs a header like
// every other tier — it used to get none, because the renderer only emitted
// one on a change and started counting at 0.
func TestTimelineTierNoun(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want []string
	}{
		{name: "fan_out rounds are 0-based", kind: "fan_out", want: []string{"round 0", "round 1", "round 2"}},
		{name: "a loop's are iterations", kind: "loop", want: []string{"iteration 0", "iteration 1", "iteration 2"}},
		// No snapshot: the rows alone cannot say which it was, so the
		// row-only derivation keeps the loop's word (structureLabel's
		// fallback is the same judgement).
		{name: "no snapshot keeps the row-derived word", kind: "", want: []string{"iteration 0", "iteration 1", "iteration 2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tierDetail(t, tt.kind, 0, 1, 2)
			got := d.renderTimeline(200)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("timeline missing %q:\n%s", want, got)
				}
			}
			if n := strings.Count(got, diffFoldClosed); n != 2 {
				t.Errorf("folded tiers = %d, want the two that are not the latest:\n%s", n, got)
			}
			// The step header names the structure from the snapshot's type,
			// and falls back to the word the rows imply when there is none.
			label := "(loop)"
			if tt.kind != "" {
				label = "green (" + tt.kind + ")"
			}
			if !strings.Contains(got, label) {
				t.Errorf("step header missing %q:\n%s", label, got)
			}
		})
	}
}

// TestTimelineFoldBindingsRegistered: the keys are only discoverable because
// they are in the registry — that is what puts them in the footer hints and in
// the `?` overlay.
func TestTimelineFoldBindingsRegistered(t *testing.T) {
	rows := bindingsFor(ctxTimeline)
	for _, key := range []string{"space", "right", "left", "O", "C"} {
		var found *binding
		for i, b := range rows {
			if b.key == key {
				found = &rows[i]
				break
			}
		}
		if found == nil {
			t.Errorf("%q is not registered under ctxTimeline", key)
			continue
		}
		if !found.fold {
			t.Errorf("%q is registered without the fold marker the board's fold rows carry", key)
		}
	}

	line := ansi.Strip(renderFooter(160, rows, &actionBar{}, taskActions{}, 0, false))
	if !strings.Contains(line, "space fold") {
		t.Errorf("the fold hint never reached the footer: %q", line)
	}
}

// TestTimelineFoldsSurviveARefresh: the folds are a reader's decision, so a
// poll that reinstalls the same task must not close what they opened, while
// opening another task starts at the arrival default again (task 016
// decision 14).
func TestTimelineFoldsSurviveARefresh(t *testing.T) {
	d := tierDetail(t, "loop", 1, 2)
	selectTier(t, d, 1)
	d.setTimelineFold(true)

	d.applyLoaded(detailLoadedMsg{id: d.taskID, task: d.task})
	if d.timelineFolded() {
		t.Fatal("a refresh closed the tier the reader opened")
	}

	d.open(d.taskID+1, stateRunning)
	if d.timelineFolds != nil {
		t.Fatalf("opening another task kept %d fold decisions from the last one", len(d.timelineFolds))
	}
}
