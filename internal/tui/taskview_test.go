package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/tui/workflowgraph"
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

// ---------------------------------------------------------------------------
// #316: reaching a fan-out's lanes from the task workspace.
// ---------------------------------------------------------------------------

// openThroughJump is what the root does with an openTaskMsg: push the task
// being left, then open the target by the ordinary selectTaskMsg path. Tests
// drive it directly because the binding registry row for `l` lands with a
// later unit; the handler and the behaviour are what this file proves.
func openThroughJump(v *taskView, from int64, id int64, state string) {
	v.pushTask(from)
	v.update(selectTaskMsg{id: id, state: state})
}

func TestWorkspaceEscapePopsOneTaskPerPress(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	v.alive = func(int64) (string, bool) { return stateRunning, true }

	v.update(selectTaskMsg{id: 7, state: stateRunning})
	if len(v.stack) != 0 {
		t.Fatalf("a task opened from the board has stack %v, want empty", v.stack)
	}
	for _, hop := range [][2]int64{{7, 41}, {41, 42}, {42, 43}} {
		openThroughJump(v, hop[0], hop[1], stateRunning)
	}
	if got, want := v.stack, []int64{7, 41, 42}; !equalInt64s(got, want) {
		t.Fatalf("stack after three jumps = %v, want %v", got, want)
	}

	// Three escapes, three tasks — and only then the board.
	for _, want := range []int64{42, 41, 7} {
		id, ok := escapeTo(t, v)
		if !ok {
			t.Fatalf("esc went to the board, want task %d", want)
		}
		if id != want {
			t.Fatalf("esc landed on task %d, want %d", id, want)
		}
	}
	if _, ok := escapeTo(t, v); ok {
		t.Fatalf("esc from the task the chain started on did not reach the board")
	}
}

func TestWorkspaceEscapeDropsAnArchivedTaskFromTheStack(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	// 42 was archived while the reader was inside its lane; 7 is still there.
	v.alive = func(id int64) (string, bool) {
		if id == 42 {
			return "", false
		}
		return stateBlocked, true
	}
	v.update(selectTaskMsg{id: 7, state: stateRunning})
	openThroughJump(v, 7, 42, stateRunning)
	openThroughJump(v, 42, 43, stateRunning)

	id, ok := escapeTo(t, v)
	if !ok || id != 7 {
		t.Fatalf("esc landed on (%d, %v), want task 7 — the archived hop is dropped, not popped to", id, ok)
	}
	if len(v.stack) != 0 {
		t.Fatalf("stack after the drop = %v, want empty", v.stack)
	}
}

func TestWorkspaceEscapeFromABoardOpenedLaneReachesTheBoard(t *testing.T) {
	v := newTaskView(taskDetailFixture(t))
	v.alive = func(int64) (string, bool) { return stateRunning, true }
	// A lane opened straight from the board has nothing behind it.
	v.update(selectTaskMsg{id: 42, state: stateBlocked})
	if _, ok := escapeTo(t, v); ok {
		t.Fatalf("esc from a board-opened lane did not reach the board")
	}
}

// escapeTo presses esc and reports the task the workspace landed on, applying
// the pop to the view exactly as the root's message loop would.
func escapeTo(t *testing.T, v *taskView) (int64, bool) {
	t.Helper()
	cmd := v.updateKey(synthKey("esc"))
	if cmd == nil {
		t.Fatalf("esc produced no command")
	}
	switch msg := cmd().(type) {
	case selectViewMsg:
		return 0, false
	case navPopMsg:
		next := v.applyPop(msg)
		if next == nil {
			t.Fatalf("a pop produced no command")
		}
		switch out := next().(type) {
		case selectViewMsg:
			return 0, false
		case selectTaskMsg:
			v.update(out)
			return out.id, true
		default:
			t.Fatalf("pop produced %T", out)
		}
	default:
		t.Fatalf("esc produced %T", msg)
	}
	return 0, false
}

func equalInt64s(a, b []int64) bool {
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

// fanOutFixture is a parent parked on a `fan_out` join, with two lanes.
func fanOutFixture(t *testing.T, reason, message string) *taskView {
	t.Helper()
	d := taskDetailFixture(t)
	d.width = 120
	d.task.State = stateBlocked
	d.task.BlockReason = &reason
	d.task.WorkflowSteps = []apiclient.WorkflowStep{{Index: 0, ID: "lanes", Type: stepTypeFanOut}}
	d.task.Steps = []apiclient.StepRun{{
		ID: 9, StepIndex: 0, StepID: "lanes", StepName: "lanes", StepType: stepTypeFanOut,
		Attempt: 1, State: "failed", FailureReason: &reason, ResultSummary: message,
	}}
	d.selectedRun = 9
	v := newTaskView(d)
	v.update(taskLaneListMsg{taskID: 7, children: []apiclient.Task{
		laneRow(42, "api", 0, stateBlocked, "worktree_dirty", "vincent/42-api"),
		laneRow(43, "web", 1, stateDone, "", "vincent/43-web"),
	}})
	return v
}

func laneRow(id int64, lane string, order int, state, block, branch string) apiclient.Task {
	row := apiclient.Task{
		ID: id, ProjectID: 3, Title: lane + " lane", State: state,
		BranchName: branch, LaneID: &lane, LaneOrder: &order,
	}
	parent := int64(7)
	row.ParentTaskID = &parent
	if block != "" {
		row.BlockReason = &block
	}
	return row
}

func TestFanOutFailureNamesTheLaneItsTaskAndTheLanesOwnBlockReason(t *testing.T) {
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)

	header := strings.Join(v.detail.headerLines(), "\n")
	for _, want := range []string{"lane_failed", `lane "api"`, "task 42", "worktree_dirty", "l open the lane"} {
		if !strings.Contains(ansi.Strip(header), want) {
			t.Errorf("header misses %q:\n%s", want, ansi.Strip(header))
		}
	}

	row := ansi.Strip(strings.Join(v.detail.attemptLines(v.detail.task.Steps[0], false), "\n"))
	for _, want := range []string{`lane "api"`, "task 42", "worktree_dirty", "l open the lane"} {
		if !strings.Contains(row, want) {
			t.Errorf("the fan_out step row misses %q:\n%s", want, row)
		}
	}

	// And the jump it offers goes to that lane.
	id, ok := v.laneJump()
	if !ok || id != 42 {
		t.Fatalf("laneJump from the blamed fan_out row = (%d, %v), want (42, true)", id, ok)
	}
}

func TestFanOutMergeConflictNamesTheConflictingLaneAndItsPaths(t *testing.T) {
	v := fanOutFixture(t, "merge_conflict", "lane \"api\" (task 42) conflicts in:\ninternal/api/server.go")
	header := ansi.Strip(strings.Join(v.detail.headerLines(), "\n"))
	for _, want := range []string{"merge_conflict", `lane "api"`, "task 42", "internal/api/server.go", "worktree_dirty"} {
		if !strings.Contains(header, want) {
			t.Errorf("header misses %q:\n%s", want, header)
		}
	}
}

func TestFanOutDeriveFailuresSurfaceTheEnginesOwnMessage(t *testing.T) {
	for reason, message := range map[string]string{
		"fan_out_limit":   "fan_out over the limit: 200 lanes exceeds fan_out.max_tasks 64",
		"fan_out_invalid": "line 12: lane id \"api\" is used twice",
	} {
		v := fanOutFixture(t, reason, message)
		header := ansi.Strip(strings.Join(v.detail.headerLines(), "\n"))
		if !strings.Contains(header, reason) || !strings.Contains(header, message) {
			t.Errorf("header for %s misses the engine's message:\n%s", reason, header)
		}
		// Neither reason blames a lane, and neither may invent one.
		blame, ok := v.detail.laneBlame()
		if !ok {
			t.Fatalf("%s produced no attribution", reason)
		}
		if blame.taskID != 0 {
			t.Errorf("%s blamed task %d; it names no lane", reason, blame.taskID)
		}
	}
}

func TestOpenLaneFromTheFanOutStepRowPushesTheParent(t *testing.T) {
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	cmd := v.updateKey(synthKey("l"))
	if cmd == nil {
		t.Fatalf("l on the fan_out row produced no command")
	}
	msg, ok := cmd().(openTaskMsg)
	if !ok {
		t.Fatalf("l produced %T, want openTaskMsg", cmd())
	}
	if msg.id != 42 || msg.from != 7 || msg.state != stateBlocked {
		t.Fatalf("l opened %+v, want task 42 from 7 in state blocked", msg)
	}

	// A row that is not a fan_out has no lanes to open, whatever the task
	// around it is doing.
	v.detail.task.Steps = append(v.detail.task.Steps, apiclient.StepRun{
		ID: 10, StepIndex: 1, StepID: "ship", StepType: "command", Attempt: 1, State: "failed",
	})
	v.detail.selectedRun = 10
	if _, ok := v.laneJump(); ok {
		t.Errorf("l offered a lane from a command step row")
	}
}

func TestOpenParentWorksWhateverTheParentIsDoing(t *testing.T) {
	for _, state := range []string{stateBlocked, stateDone, stateRunning} {
		d := taskDetailFixture(t)
		parent := int64(7)
		d.taskID = 42
		d.task.Task = apiclient.Task{
			ID: 42, ProjectID: 3, Title: "api lane", State: state, ParentTaskID: &parent,
		}
		v := newTaskView(d)
		id, ok := v.parentJump()
		if !ok || id != 7 {
			t.Fatalf("parentJump for a lane of a %s parent = (%d, %v), want (7, true)", state, id, ok)
		}
		msg, ok := v.updateKey(synthKey("U"))().(openTaskMsg)
		if !ok || msg.id != 7 || msg.from != 42 {
			t.Fatalf("U from a lane of a %s parent = %+v", state, msg)
		}
	}

	// A task that is not a lane offers nothing.
	v := newTaskView(taskDetailFixture(t))
	if _, ok := v.parentJump(); ok {
		t.Errorf("parentJump offered a parent for a root task")
	}
}

func TestOutputPaneLaneSelectorWalksTheLanesAndBackToTheTask(t *testing.T) {
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	v.width, v.height = 120, 30
	v.setTab(taskTabOutput)

	if _, ok := v.outputLane(); ok {
		t.Fatalf("the Output pane starts on a lane, want the task's own output")
	}
	v.selectLane(1)
	if id, ok := v.outputLane(); !ok || id != 42 {
		t.Fatalf("> selected (%d, %v), want lane task 42", id, ok)
	}
	v.selectLane(1)
	if id, ok := v.outputLane(); !ok || id != 43 {
		t.Fatalf("a second > selected (%d, %v), want lane task 43", id, ok)
	}
	v.selectLane(1)
	if _, ok := v.outputLane(); ok {
		t.Fatalf("the cycle did not come back to the task's own output")
	}
	v.selectLane(-1)
	if id, ok := v.outputLane(); !ok || id != 43 {
		t.Fatalf("< from the task's own output selected (%d, %v), want the last lane", id, ok)
	}

	v.selectLane(1)
	v.selectLane(1)
	got := ansi.Strip(v.render(120, 30))
	for _, want := range []string{"Lane", "api (task 42)", "worktree_dirty", "</> select lane"} {
		if !strings.Contains(got, want) {
			t.Errorf("the Output pane's lane selector misses %q:\n%s", want, got)
		}
	}
	// And `l` opens whatever the selector is pointed at.
	msg, ok := v.updateKey(synthKey("l"))().(openTaskMsg)
	if !ok || msg.id != 42 {
		t.Fatalf("l from the Output pane opened %+v, want lane task 42", msg)
	}
}

func TestPullRequestTabRendersOneRowPerLane(t *testing.T) {
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	v.pull = apiclient.GitHubTaskPull{Linked: true, Repo: "lezli01/vincent", Number: 300}
	number := int64(42)
	v.applyLanePulls(taskLanePullsMsg{taskID: 7, pulls: []apiclient.GitHubPullRequest{{
		Number: 301, State: "open", TaskID: &number,
	}}})
	v.tab = taskTabPull

	got := ansi.Strip(v.render(140, 30))
	for _, want := range []string{
		"lezli01/vincent#300", // the parent's own row is unchanged
		"lanes — 2",
		"#301 open", "api · task 42", "vincent/42-api",
		"no pull request", "web · task 43", "vincent/43-web",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the Pull Request tab's lane rows miss %q:\n%s", want, got)
		}
	}
}

func TestOpenLaneFromTheWorkflowGraphSelection(t *testing.T) {
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	v.width, v.height = 160, 40
	wf := &apiclient.WorkflowBody{Name: "fixture", Steps: []apiclient.WorkflowStepDef{
		{ID: "plan", Type: "agent"},
		{ID: "lanes", Type: stepTypeFanOut, Lanes: []apiclient.WorkflowLaneDef{
			{ID: "api", Steps: []apiclient.WorkflowStepDef{{ID: "build", Type: "command"}}},
			{ID: "web", Steps: []apiclient.WorkflowStepDef{{ID: "build", Type: "command"}}},
		}},
	}}
	v.setTab(taskTabWorkflow)
	v.update(taskWorkflowMsg{taskID: 7, def: apiclient.TaskWorkflow{
		TaskID: 7, Name: "fixture", Definition: wf,
	}})

	// The graph selection is a node *inside* a lane, and the lane column it
	// belongs to is what names it (workflowgraph.LaneKey).
	var inner string
	for _, col := range v.workflow.graph.Lanes() {
		if col.Key == workflowgraph.LaneKey("lanes", "web") && len(col.Nodes) > 0 {
			inner = col.Nodes[0]
			break
		}
	}
	if inner == "" {
		t.Fatalf("the graph drew no node inside the web lane")
	}
	v.workflow.graph.Select(inner)

	id, ok := v.laneJump()
	if !ok || id != 43 {
		t.Fatalf("laneJump from a node inside the web lane = (%d, %v), want lane task 43", id, ok)
	}
	msg, ok := v.updateKey(synthKey("l"))().(openTaskMsg)
	if !ok || msg.id != 43 || msg.from != 7 {
		t.Fatalf("l on the Workflow tab opened %+v, want lane task 43 from 7", msg)
	}

	// A node outside every lane resolves to no lane at all.
	v.workflow.graph.Select("plan")
	if _, ok := v.laneJump(); ok {
		t.Errorf("l offered a lane from a top-level node")
	}
}
