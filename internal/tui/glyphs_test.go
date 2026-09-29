package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestChildrenBreakdownClauses: the breakdown names every lane exactly once,
// drops the states nobody is in, and leads with what a human has to act on
// (task 129.10 decision 2).
func TestChildrenBreakdownClauses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		rollup *apiclient.ChildrenRollup
		want   string
	}{
		{name: "nil rollup", rollup: nil, want: ""},
		{name: "empty rollup", rollup: &apiclient.ChildrenRollup{}, want: ""},
		{
			name: "actionability order, zeros dropped",
			rollup: &apiclient.ChildrenRollup{Total: 5, ByState: map[string]int{
				stateDone: 2, stateRunning: 1, stateAwaitingGate: 1, stateBlocked: 1, stateQueued: 0,
			}},
			want: "×1 !1 ●1 ✓2",
		},
		{
			name: "both human waits share the attention glyph",
			rollup: &apiclient.ChildrenRollup{Total: 3, ByState: map[string]int{
				stateAwaitingGate: 1, stateAwaitingInput: 2,
			}},
			want: "!3",
		},
		{
			name: "a nested parent counts as running",
			rollup: &apiclient.ChildrenRollup{Total: 2, ByState: map[string]int{
				stateRunning: 1, stateAwaitingChildren: 1,
			}},
			want: "●2",
		},
		{
			name: "the rest follow, and a state with no glyph is spelled out",
			rollup: &apiclient.ChildrenRollup{Total: 6, ByState: map[string]int{
				stateDone: 1, statePaused: 1, stateQueued: 2, stateAborted: 1, "novel": 1,
			}},
			want: "✓1 ■1 1 novel 1 paused ○2",
		},
		{
			name:   "the id lists stand in for a missing by_state",
			rollup: &apiclient.ChildrenRollup{Total: 3, Blocked: []int64{4, 5}, AwaitingGate: []int64{6}},
			want:   "×2 !1",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := breakdownText(childrenBreakdown(tt.rollup)); got != tt.want {
				t.Errorf("breakdown = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBreakdownBlockedSurvivesTheCut: the STATE cell wraps to three lines at
// widthState and cuts what is left from the tail, so a long breakdown loses
// its least actionable clauses and never the blocked count — including on an
// 80-column board.
func TestBreakdownBlockedSurvivesTheCut(t *testing.T) {
	parent := apiclient.Task{
		ID: 9, Title: "fan out", State: stateAwaitingChildren, StepTotal: 2, CurrentStep: 1,
		ProjectName: "proj", CreatedAt: testNow, UpdatedAt: testNow,
		Children: &apiclient.ChildrenRollup{Total: 70, Blocked: []int64{1}, ByState: map[string]int{
			stateBlocked: 11, stateAwaitingGate: 12, stateRunning: 13, stateDone: 14,
			stateQueued: 15, statePaused: 16, stateAborted: 17, "limbo": 18,
		}},
	}
	lines := wrapCellLines(boardStateLabel(parent), widthState, boardRowLines)
	if len(lines) > boardRowLines {
		t.Fatalf("state cell is %d lines, want at most %d", len(lines), boardRowLines)
	}
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "×11") {
		t.Errorf("the blocked clause was cut: %q", lines)
	}
	if strings.Contains(joined, "paused") {
		t.Errorf("the tail survived a cut that should have taken it: %q", lines)
	}

	b := groupedBoard(parent)
	out := ansi.Strip(b.render(80, 20))
	if !strings.Contains(out, "×11") {
		t.Errorf("an 80-column board lost the blocked clause:\n%s", out)
	}
}

// TestBoardStepPipsOnlyInSpareWidth: pips are the last thing the STEP cell
// says — present when the column has room left, absent otherwise — and they
// never change what STATE says.
func TestBoardStepPipsOnlyInSpareWidth(t *testing.T) {
	running := task(1, stateRunning, withStep("green", 1, 4))
	if got, want := boardStepPips(running), "✓●○○"; got != want {
		t.Errorf("pips = %q, want %q", got, want)
	}
	for state, want := range map[string]string{
		stateBlocked: "✓×○○", stateAwaitingGate: "✓!○○", stateDone: "✓●○○",
		statePaused: "✓○○○", stateQueued: "✓○○○",
	} {
		tk := task(1, state, withStep("green", 1, 4))
		if state == stateDone {
			tk.CurrentStep = 4 // a finished task's cursor sits past the last step
			want = "✓✓✓✓"
		}
		if got := boardStepPips(tk); got != want {
			t.Errorf("%s pips = %q, want %q", state, got, want)
		}
	}

	if got := formatStep(running, true, widthStepMax); got != "2/4 green ✓●○○" {
		t.Errorf("wide STEP = %q, want the pips appended", got)
	}
	if got := formatStep(running, true, ansi.StringWidth("2/4 green ✓●○")); got != "2/4 green" {
		t.Errorf("tight STEP = %q, want the pips left out rather than cut", got)
	}

	// On the board: absent at 80 columns, where STEP is at its base width,
	// present with room to spare — and STATE reads the same either way.
	long := task(2, stateRunning, withStep("implement", 3, 9))
	b := groupedBoard(long)
	narrow := ansi.Strip(b.render(80, 20))
	wide := ansi.Strip(b.render(200, 20))
	if strings.Contains(narrow, boardStepPips(long)) {
		t.Errorf("pips crowded into an 80-column board:\n%s", narrow)
	}
	if !strings.Contains(wide, boardStepPips(long)) {
		t.Errorf("a 200-column board has room for the pips and did not show them:\n%s", wide)
	}
	for _, out := range []string{narrow, wide} {
		if !strings.Contains(out, stateRunning) || strings.Contains(out, stateRunning+" ✓") {
			t.Errorf("pips touched the STATE cell:\n%s", out)
		}
	}
}

// TestLaneRowsReadByLaneID: a lane's title repeats its parent's, so an
// expanded lane reads `lane <id>` — and keeps its title when the daemon sent
// no id to read.
func TestLaneRowsReadByLaneID(t *testing.T) {
	lane := laneTask(42, "api", 0, stateRunning)
	lane.Title = "fan out: api"
	if got := laneTitle(boardRow{task: lane, lane: 1}); got != "lane api" {
		t.Errorf("lane title = %q, want %q", got, "lane api")
	}
	lane.LaneID = nil
	if got := laneTitle(boardRow{task: lane, lane: 1}); got != "fan out: api" {
		t.Errorf("lane without an id = %q, want its title", got)
	}
	root := task(7, stateAwaitingChildren)
	if got := laneTitle(boardRow{task: root}); got != root.Title {
		t.Errorf("a board row = %q, want its own title", got)
	}
}

// loopBodyRun is one body row of the loop at step index 1.
func loopBodyRun(id int64, iteration, n int, stepID, state string) apiclient.StepRun {
	r := attempt(id, 1, n, stepID, state, state == "running")
	r.Iteration = iteration
	return r
}

// TestLoopStripOutcomes: an iteration reads as its worst newest-attempt body
// row, oldest first, and a long loop keeps its newest ten behind `…+k`.
func TestLoopStripOutcomes(t *testing.T) {
	runs := []apiclient.StepRun{
		loopBodyRun(1, 1, 1, "test", "succeeded"),
		loopBodyRun(2, 1, 1, "fix", "skipped"),
		// A failed attempt retried green is a green iteration.
		loopBodyRun(3, 2, 1, "test", "failed"),
		loopBodyRun(4, 2, 2, "test", "succeeded"),
		loopBodyRun(5, 3, 1, "test", "succeeded"),
		loopBodyRun(6, 3, 1, "fix", "rejected"),
		loopBodyRun(7, 4, 1, "test", "succeeded"),
		loopBodyRun(8, 4, 1, "done", stepStateStopped),
		loopBodyRun(9, 5, 1, "test", "running"),
		// Another step index is not this loop's.
		attempt(10, 0, 1, "build", "failed", false),
	}
	if got, want := ansi.Strip(renderLoopStrip(runs, 1)), "✓✓×■●"; got != want {
		t.Errorf("strip = %q, want %q", got, want)
	}
	if got := renderLoopStrip(runs, 3); got != "" {
		t.Errorf("a step with no iterations has a strip: %q", got)
	}

	long := make([]apiclient.StepRun, 0, 13)
	for i := 1; i <= 13; i++ {
		state := "succeeded"
		if i == 2 || i == 12 {
			state = "failed"
		}
		long = append(long, loopBodyRun(int64(i), i, 1, "test", state))
	}
	if got, want := ansi.Strip(renderLoopStrip(long, 1)), "…+3 ✓✓✓✓✓✓✓✓×✓"; got != want {
		t.Errorf("capped strip = %q, want %q", got, want)
	}
}

// loadLoopTask wires a task parked inside a loop at step index 1 of three.
func loadLoopTask(d *detail, stepType string, rows []apiclient.StepRun) {
	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task: apiclient.Task{
				ID: d.taskID, Title: "looping", State: stateRunning,
				StepTotal: 3, CurrentStep: 1,
				Loop: &apiclient.LoopRollup{Driver: "count", Iteration: 3, Total: 5, MaxIterations: 5},
			},
			Steps: rows,
			WorkflowSteps: []apiclient.WorkflowStep{
				{Index: 0, ID: "build", Type: "command"},
				{Index: 1, ID: "green", Type: stepType},
				{Index: 2, ID: "ship", Type: "command"},
			},
		},
	})
}

// TestWorkspaceHeaderPipsAndLoopStrip: the header's step clause carries one
// pip per workflow step — the loop is one of them, however many rows it
// wrote — and its loop clause the iteration strip.
func TestWorkspaceHeaderPipsAndLoopStrip(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 31
	loadLoopTask(d, "loop", []apiclient.StepRun{
		attempt(1, 0, 1, "build", "succeeded", false),
		loopBodyRun(2, 1, 1, "test", "succeeded"),
		loopBodyRun(3, 2, 1, "test", "failed"),
		loopBodyRun(4, 3, 1, "test", "running"),
		loopBodyRun(5, 3, 1, "fix", "succeeded"),
	})
	header := ansi.Strip(strings.Join(d.headerLines(), "\n"))
	for _, want := range []string{"step 2/3 ✓●○", "loop 3/5 ✓×●"} {
		if !strings.Contains(header, want) {
			t.Errorf("header missing %q:\n%s", want, header)
		}
	}

	// The Steps tab puts the same strip on one line where the tiers begin.
	timeline := ansi.Strip(d.timelinePanel(30))
	if !strings.Contains(timeline, "iterations ✓×●") {
		t.Errorf("timeline has no iteration strip:\n%s", timeline)
	}
}

// TestHeaderPipsMarkAGate: a gate's row reads `running` for as long as it
// waits, so the current pip of a task waiting on a human is `!`.
func TestHeaderPipsMarkAGate(t *testing.T) {
	tk := apiclient.Task{State: stateAwaitingGate, StepTotal: 3, CurrentStep: 1}
	runs := []apiclient.StepRun{
		attempt(1, 0, 1, "build", "succeeded", false),
		attempt(2, 1, 1, "review", "running", true),
	}
	if got, want := ansi.Strip(renderStepPips(tk, runs)), "✓!○"; got != want {
		t.Errorf("pips = %q, want %q", got, want)
	}
}

// TestFanOutRoundsGetNoIterationStrip: a multi-round fan_out rides the
// iteration column, but its tiers are rounds, not passes of a body.
func TestFanOutRoundsGetNoIterationStrip(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 32
	loadFanOut(d, nil, []apiclient.StepRun{
		fanOutRun(1, 0, "succeeded"),
		fanOutRun(2, 1, "running"),
	})
	if got := ansi.Strip(d.timelinePanel(30)); strings.Contains(got, "iterations") {
		t.Errorf("a fan_out drew an iteration strip:\n%s", got)
	}
}

// TestGlyphIndicatorsSurviveMonochrome: every new indicator carries its
// meaning in the glyph, so the ASCII profile — NO_COLOR — strips colour and
// nothing else.
func TestGlyphIndicatorsSurviveMonochrome(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 33
	loadLoopTask(d, "loop", []apiclient.StepRun{
		attempt(1, 0, 1, "build", "succeeded", false),
		loopBodyRun(2, 1, 1, "test", "failed"),
		loopBodyRun(3, 2, 1, "test", "running"),
	})
	parent := apiclient.Task{State: stateAwaitingChildren, Children: &apiclient.ChildrenRollup{
		Total: 3, ByState: map[string]int{stateBlocked: 1, stateAwaitingGate: 1, stateDone: 1},
	}}
	cell := stateCell(parent)
	frame := strings.Join([]string{
		strings.Join(d.headerLines(), "\n"),
		d.timelinePanel(30),
		cell.render(boardStateLabel(parent)),
	}, "\n")

	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatalf("downgrade write: %v", err)
	}
	plain := buf.String()
	if ansi.Strip(plain) != ansi.Strip(frame) {
		t.Error("stripping colour changed the content")
	}
	for _, seq := range []string{"[38;5;", "[38;2;", "[31m", "[32m", "[36m"} {
		if strings.Contains(plain, seq) {
			t.Errorf("colour sequence %q survived the NO_COLOR downgrade", seq)
		}
	}
	text := ansi.Strip(plain)
	for _, want := range []string{"step 2/3 ✓●○", "loop 3/5 ×●", "iterations ×●", "awaiting_children (×1 !1 ✓1)"} {
		if !strings.Contains(text, want) {
			t.Errorf("monochrome frame lost %q:\n%s", want, plain)
		}
	}
}
