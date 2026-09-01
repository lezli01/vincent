package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

func TestTaskWorkspaceDefaultsToStepsAndCyclesEveryFullViewTab(t *testing.T) {
	d := taskDetailFixture(t)
	v := newTaskView(d)

	if v.tab != taskTabSteps {
		t.Fatalf("initial tab = %v, want Steps & Attempts", v.tab)
	}
	want := []taskViewTab{taskTabDetails, taskTabOutput, taskTabDiff, taskTabWorkflow, taskTabSteps}
	for _, tab := range want {
		v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
		if v.tab != tab {
			t.Fatalf("tab advanced to %v, want %v", v.tab, tab)
		}
	}

	v.updateKey(tea.KeyPressMsg{Code: '2', Text: "2"})
	got := v.render(100, 80)
	for _, value := range []string{
		"Task Details", "A complete description", "Description", "Overview",
		"Execution", "Fields", "Lifecycle", "Workflow snapshot",
	} {
		if !strings.Contains(got, value) {
			t.Errorf("details tab misses %q:\n%s", value, got)
		}
	}
	if strings.Contains(got, "OPS-42") {
		t.Errorf("details tab rendered an unselected section:\n%s", got)
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

func TestTaskDetailsUsesAiryResponsiveFactGroups(t *testing.T) {
	d := taskDetailFixture(t)
	worktree := "/tmp/vincent/worktrees/task-7"
	d.task.WorktreePath = &worktree
	d.task.Fields["deployment_environment_owner"] = "platform operations"
	d.task.Fields["unbroken"] = strings.Repeat("§", 90)
	v := newTaskView(d)
	v.tab = taskTabDetails

	v.render(130, 80) // lays out the dynamic sidebar before keys address it
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	wide := ansi.Strip(v.render(130, 80))
	lines := strings.Split(wide, "\n")
	pairedFacts := false
	for _, line := range lines {
		if strings.Contains(line, "state") && strings.Contains(line, "project") {
			pairedFacts = true
		}
	}
	if !pairedFacts {
		t.Fatalf("wide details did not arrange overview facts in two columns:\n%s", wide)
	}
	if strings.Contains(wide, "platform operations") {
		t.Fatalf("Overview leaked the unselected Fields content:\n%s", wide)
	}

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Execution
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyDown}) // Fields
	narrow := ansi.Strip(v.renderDetails(60, 100))
	for _, value := range []string{"deployment_environment_owner", "platform operations"} {
		if !strings.Contains(narrow, value) {
			t.Errorf("narrow details lost wrapped field content %q:\n%s", value, narrow)
		}
	}
	if got := strings.Count(narrow, "§"); got != 90 {
		t.Errorf("narrow details kept %d/90 characters from an unbroken value", got)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if got := ansi.StringWidth(line); got > 60 {
			t.Errorf("narrow detail line is %d cells wide, want at most 60: %q", got, line)
		}
	}
}

func TestTaskDetailsSidebarSelectsOneSectionAtATime(t *testing.T) {
	d := taskDetailFixture(t)
	d.task.Warnings = []string{"review the generated lockfile"}
	v := newTaskView(d)
	v.tab = taskTabDetails

	got := ansi.Strip(v.renderDetails(100, 30))
	if v.details.section != "Description" || !strings.Contains(got, "A complete description") {
		t.Fatalf("default section = %q, want visible Description:\n%s", v.details.section, got)
	}
	if !strings.Contains(got, "Warnings") || strings.Contains(got, "review the generated lockfile") {
		t.Fatalf("dynamic Warnings section was missing or leaked its content:\n%s", got)
	}

	v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	got = ansi.Strip(v.renderDetails(100, 30))
	if v.details.section != "Workflow snapshot" || !strings.Contains(got, "write the change") {
		t.Fatalf("end selected %q, want Workflow snapshot:\n%s", v.details.section, got)
	}
	if strings.Contains(got, "A complete description") {
		t.Fatalf("Workflow snapshot leaked Description content:\n%s", got)
	}
}

func TestTaskDetailsSidebarSupportsMouseSelection(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	v.tab = taskTabDetails
	v.bodyY = 3
	v.renderDetails(100, 30)

	fields := -1
	for i, title := range v.details.sections {
		if title == "Fields" {
			fields = i
			break
		}
	}
	if fields < 0 {
		t.Fatal("Fields is missing from the detail sidebar")
	}
	v.updateClick(tea.MouseClickMsg{
		X: 3,
		Y: v.bodyY + v.details.sidebarY + fields - v.details.sidebarTop,
	})

	got := ansi.Strip(v.renderDetails(100, 30))
	if v.details.section != "Fields" || !strings.Contains(got, "OPS-42") {
		t.Fatalf("mouse selected %q, want visible Fields:\n%s", v.details.section, got)
	}
}

func taskDetailFixture(t *testing.T) *detail {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	d := newDetail(context.Background(), newLevelHolder(), newRawHolder())
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
