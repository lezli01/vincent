package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

func TestRoutedHomeRendersOnlyTheTaskBoard(t *testing.T) {
	views := newViews(context.Background())
	home := views[viewHome].(*shell)
	home.board.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{{
		ID: 1, ProjectName: "api", Title: "board-only task", State: stateRunning,
	}}})

	got := home.render(100, 24)
	if !strings.Contains(got, "board-only task") {
		t.Fatalf("home did not render the board row:\n%s", got)
	}
	for _, hidden := range []string{"Steps & Attempts", "Task Details", "Output", "Diff"} {
		if strings.Contains(got, hidden) {
			t.Errorf("home leaked task surface %q:\n%s", hidden, got)
		}
	}
}

func TestTaskWorkspaceDefaultsToStepsAndCyclesFourFullViewTabs(t *testing.T) {
	d := taskDetailFixture(t)
	v := newTaskView(d)

	if v.tab != taskTabSteps {
		t.Fatalf("initial tab = %v, want Steps & Attempts", v.tab)
	}
	want := []taskViewTab{taskTabDetails, taskTabOutput, taskTabDiff, taskTabSteps}
	for _, tab := range want {
		v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
		if v.tab != tab {
			t.Fatalf("tab advanced to %v, want %v", v.tab, tab)
		}
	}

	v.updateKey(tea.KeyPressMsg{Code: '2', Text: "2"})
	got := v.render(100, 80)
	for _, value := range []string{
		"Task Details", "A complete description", "ticket", "OPS-42",
		"project workflow.yaml", "vincent/7-task-workspace", "Workflow snapshot",
		"implement", "write the change",
	} {
		if !strings.Contains(got, value) {
			t.Errorf("details tab misses %q:\n%s", value, got)
		}
	}

	v.updateKey(tea.KeyPressMsg{Code: '3', Text: "3"})
	got = v.render(100, 28)
	if !strings.Contains(got, "no attempt selected") {
		t.Errorf("output tab did not use the full output surface:\n%s", got)
	}
	if strings.Contains(got, "A complete description") {
		t.Errorf("output tab leaked the details surface:\n%s", got)
	}
}

func TestTaskWorkspaceEscapeReturnsToBoard(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc produced no navigation command")
	}
	msg, ok := cmd().(selectViewMsg)
	if !ok || msg.id != viewHome {
		t.Fatalf("esc returned %#v, want selectViewMsg{viewHome}", msg)
	}
}

func TestStepsEnterOpensSelectedAttemptInOutput(t *testing.T) {
	d := taskDetailFixture(t)
	d.task.Steps = []apiclient.StepRun{
		{ID: 101, StepIndex: 0, StepID: "implement", StepName: "Implement", Attempt: 1, State: "failed"},
		{ID: 102, StepIndex: 0, StepID: "implement", StepName: "Implement", Attempt: 2, State: "succeeded"},
	}
	d.selectedRun = 101
	v := newTaskView(d)

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if v.tab != taskTabOutput {
		t.Fatalf("enter opened tab %v, want Output", v.tab)
	}
	if d.selectedRun != 101 {
		t.Fatalf("enter changed selected attempt to %d, want 101", d.selectedRun)
	}
}

func TestOutputTabSelectsWhichAttemptToShow(t *testing.T) {
	d := taskDetailFixture(t)
	d.task.Steps = []apiclient.StepRun{
		{ID: 101, StepIndex: 0, StepID: "implement", StepName: "Implement", Attempt: 1, State: "failed"},
		{ID: 102, StepIndex: 0, StepID: "implement", StepName: "Implement", Attempt: 2, State: "succeeded"},
	}
	d.selectedRun = 101
	v := newTaskView(d)
	v.tab = taskTabOutput

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyRight})

	if d.selectedRun != 102 || d.displayRun != 102 {
		t.Fatalf("right selected/displayed %d/%d, want 102/102", d.selectedRun, d.displayRun)
	}
	got := v.render(100, 28)
	for _, value := range []string{"2/2", "step 1 Implement", "attempt 2", "succeeded", "←/→ select"} {
		if !strings.Contains(got, value) {
			t.Errorf("output attempt selector misses %q:\n%s", value, got)
		}
	}

	v.updateKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if d.selectedRun != 101 || d.displayRun != 101 {
		t.Fatalf("h selected/displayed %d/%d, want 101/101", d.selectedRun, d.displayRun)
	}
}

func taskDetailFixture(t *testing.T) *detail {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	d := newDetail(context.Background())
	d.taskID = 7
	d.loaded = true
	d.task = apiclient.TaskDetail{
		Task: apiclient.Task{
			ID: 7, ProjectID: 3, ProjectName: "api", Title: "task workspace",
			Fields: map[string]string{"ticket": "OPS-42"}, Workflow: "release",
			WorkflowOrigin: &apiclient.WorkflowOrigin{Scope: "project", File: "workflow.yaml"},
			State:          stateRunning, Priority: 4, BranchName: "vincent/7-task-workspace",
			StepTotal: 1, CreatedAt: now, UpdatedAt: now,
		},
		Description: "A complete description of the task.",
		WorkflowSteps: []apiclient.WorkflowStep{{
			Index: 0, ID: "implement", Type: "agent", Prompt: "write the change",
		}},
	}
	return d
}
