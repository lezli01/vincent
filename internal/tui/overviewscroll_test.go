package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The wheel scrolls an Overview taller than its pane, and the cue says where
// the window is (#799 W1).
func TestOverviewWheelScrolls(t *testing.T) {
	v := awaitingLanesView(t, 10)
	before := ansi.Strip(v.render(80, 20))
	if !strings.Contains(before, "↓ more") {
		t.Fatalf("a clipped Overview carries no cue:\n%s", before)
	}
	v.update(tea.MouseWheelMsg{X: 10, Y: 8, Button: tea.MouseWheelDown})
	after := ansi.Strip(v.render(80, 20))
	if v.overview.Offset() == 0 || after == before {
		t.Fatalf("the wheel did not scroll the Overview (offset %d):\n%s", v.overview.Offset(), after)
	}
	if !strings.Contains(after, "↑") {
		t.Errorf("a scrolled Overview's cue does not say there is more above:\n%s", after)
	}
	v.updateKey(key("home"))
	if v.overview.Offset() != 0 {
		t.Errorf("home left the Overview at offset %d", v.overview.Offset())
	}
	v.updateKey(key("end"))
	if !strings.Contains(ansi.Strip(v.render(80, 20)), "1 steps") {
		t.Errorf("end does not reach the jump links:\n%s", ansi.Strip(v.render(80, 20)))
	}
}

// A blocked block_detail longer than the evidence budget is cut, and the cut
// says so (task 070 decision 2) rather than pushing the actions off screen.
func TestFailureCardCapsBlockDetailAtTheEvidenceBudget(t *testing.T) {
	task := cardTask("worktree_dirty", failedRun(5, 1, "nonzero_exit"))
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("dirty path %02d", i))
	}
	task.BlockDetail = ptr(strings.Join(lines, "\n"))
	v := cardView(t, task)
	got := ansi.Strip(v.render(80, 30))
	if !strings.Contains(got, "more line") {
		t.Errorf("a cut block detail does not say what it left out:\n%s", got)
	}
	if strings.Contains(got, "↓ more") {
		t.Errorf("the block detail pushed the card past the pane:\n%s", got)
	}
	if !strings.Contains(got, "c      cancel") {
		t.Errorf("the actions are not on screen:\n%s", got)
	}
}

// awaitingLanesView is a fan-out parent waiting on 30 lanes, ten of them
// settled and `blocked` of them, from lane-17 on, blocked.
func awaitingLanesView(t *testing.T, blocked int) *taskView {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 7
	task := apiclient.TaskDetail{
		Task: apiclient.Task{
			ID: 7, ProjectID: 3, Title: "fan out", State: stateAwaitingChildren, CurrentStep: 0, StepTotal: 1,
			Children: &apiclient.ChildrenRollup{Total: 30, Settled: 10},
		},
		WorkflowSteps: []apiclient.WorkflowStep{{Index: 0, ID: "lanes", Type: stepTypeFanOut}},
		Steps: []apiclient.StepRun{{
			ID: 9, StepIndex: 0, StepID: "lanes", StepName: "lanes", StepType: stepTypeFanOut,
			Attempt: 1, State: "succeeded",
		}},
	}
	d.applyLoaded(detailLoadedMsg{id: 7, task: task})
	v := newTaskView(d)
	v.tab = taskTabOverview
	var lanes []apiclient.Task
	for i := range 30 {
		state := stateRunning
		switch {
		case i < 10:
			state = stateDone
		case i >= 17 && i < 17+blocked:
			state = stateBlocked
		}
		block := ""
		if state == stateBlocked {
			block = "check_failed"
		}
		lanes = append(lanes, laneRow(int64(100+i), fmt.Sprintf("lane-%02d", i), i, state, block, ""))
	}
	v.update(taskLaneListMsg{taskID: 7, children: lanes})
	return v
}

// A parent waiting on 30 lanes, one of them blocked, names the blocked lane
// and `l` opens it (#799 W12); its link is the steps, never an output it
// does not have.
func TestOverviewNamesTheBlockedLaneAndLOpensIt(t *testing.T) {
	v := awaitingLanesView(t, 1)
	got := ansi.Strip(v.render(120, 40))
	for _, want := range []string{"Waiting on 20 of 30 lanes", "lane-17", "check failed", "l open lane-17", "1 steps"} {
		if !strings.Contains(got, want) {
			t.Errorf("the Overview misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "full output") {
		t.Errorf("the Overview links an output the parent never wrote:\n%s", got)
	}
	var offered bool
	for _, b := range v.liveBindings(bindingsFor(ctxTaskOverview)) {
		offered = offered || b.key == "l"
	}
	if !offered {
		t.Error("the footer does not offer l for the blocked lane")
	}
	if id, ok := v.laneJump(); !ok || id != 117 {
		t.Fatalf("laneJump on the Overview = (%d, %v), want lane-17's task 117", id, ok)
	}
	cmd := v.updateKey(registryKey(t, "l"))
	if cmd == nil {
		t.Fatal("l produced no command")
	}
	if msg, ok := cmd().(openTaskMsg); !ok || msg.id != 117 || msg.from != 7 {
		t.Fatalf("l produced %+v, want lane task 117 from parent 7", cmd())
	}
	v.updateKey(synthKey("1"))
	if v.tab != taskTabSteps {
		t.Errorf("1 landed on %v, want the steps", v.tab)
	}
}

// At 120×40 a done task's long result grows into the rows the Overview has
// free instead of stopping at six lines over an empty screen (#799 W13).
func TestOutcomeResultUsesTheFreeRows(t *testing.T) {
	task := outcomeTask(stateDone)
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("- finding %02d", i))
	}
	task.Steps[0].ResultSummary = strings.Join(lines, "\n")
	v := outcomeView(t, task)
	frame := strings.Split(ansi.Strip(v.render(120, 40)), "\n")
	shown := 0
	for _, line := range frame {
		if strings.Contains(line, "finding ") {
			shown++
		}
	}
	if shown <= 6 {
		t.Fatalf("the result shows %d lines at 120×40:\n%s", shown, strings.Join(frame, "\n"))
	}
	// The body fills its pane: the result takes what the rest leaves, and it
	// still says it was cut.
	body := strings.Split(ansi.Strip(v.renderOverview(120, 30)), "\n")
	if len(body) < 29 {
		t.Errorf("a 30-row Overview draws %d rows over a cut result:\n%s", len(body), strings.Join(body, "\n"))
	}
	if !strings.Contains(strings.Join(body, "\n"), "    …") {
		t.Errorf("the cut result does not say it was cut:\n%s", strings.Join(body, "\n"))
	}
}
