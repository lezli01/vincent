package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/tui/workflowgraph"
)

// workflowTabFixture is the workspace sitting on the Workflow tab with a
// three-step snapshot already drawn.
func workflowTabFixture(t *testing.T) *taskView {
	t.Helper()
	v := tabbedTaskFixture(t, taskTabWorkflow)
	v.width, v.height = 120, 40
	v.workflow.taskID = v.detail.taskID
	v.applyWorkflow(taskWorkflowMsg{taskID: v.detail.taskID, def: apiclient.TaskWorkflow{
		TaskID: v.detail.taskID, Name: "fixture", Definition: &apiclient.WorkflowBody{
			Name: "fixture",
			Steps: []apiclient.WorkflowStepDef{
				{ID: "plan", Type: "agent"},
				{ID: "build", Type: "command"},
				{ID: "ship", Type: "command"},
			},
		},
	}})
	return v
}

// `tab` on the Workflow tab is the workspace's tab cycle, not the graph's
// source-order node walk (task 051 decision 5). The two collide, and the tab
// cycle is the one 049 built the muscle memory on.
func TestTabOnTheWorkflowTabMovesToTheNextTab(t *testing.T) {
	v := workflowTabFixture(t)
	before := v.workflow.graph.Selected()
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if v.tab != taskTabSteps {
		t.Fatalf("tab moved to %v, want Steps & Attempts", v.tab)
	}
	if got := v.workflow.graph.Selected(); got != before {
		t.Errorf("tab also walked the graph selection to %q; it must not", got)
	}
}

// esc still leaves the workspace from the new tab.
func TestEscapeLeavesTheWorkflowTab(t *testing.T) {
	v := workflowTabFixture(t)
	cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc produced no command")
	}
	msg, ok := cmd().(selectViewMsg)
	if !ok || msg.id != viewHome {
		t.Fatalf("esc produced %#v, want a return to the board", cmd())
	}
}

// The overlay joins step rows onto nodes: the newest attempt wins, a false
// guard reads apart from a human skip, and the task's own parked state lands
// on the step that owns it.
func TestBuildOverlayJoinsStepRunsOntoNodes(t *testing.T) {
	condition := "condition"
	task := apiclient.TaskDetail{
		Task: apiclient.Task{State: "blocked", CurrentStep: 1, BlockReason: strptr("check_failed")},
		WorkflowSteps: []apiclient.WorkflowStep{
			{ID: "plan"}, {ID: "build"}, {ID: "ship"},
		},
		Steps: []apiclient.StepRun{
			{ID: 1, StepID: "plan", State: "succeeded", Attempt: 1},
			{ID: 2, StepID: "build", State: "failed", Attempt: 1},
			{ID: 3, StepID: "build", State: "failed", Attempt: 2},
			{ID: 4, StepID: "ship", State: "skipped", SkipReason: &condition},
		},
	}
	d := workflowgraph.Build(&apiclient.WorkflowBody{Steps: []apiclient.WorkflowStepDef{
		{ID: "plan", Type: "agent"}, {ID: "build", Type: "command"}, {ID: "ship", Type: "command"},
	}})
	ov := buildOverlay(task, d.Nodes, nil, nil)

	if got := ov.Nodes["build"]; got.Attempt != 2 || got.State != "failed" {
		t.Errorf("build = %+v, want the newest attempt", got)
	}
	if got := ov.Nodes["build"]; got.Task != "blocked" || got.BlockReason != "check_failed" {
		t.Errorf("the parked task did not land on its own step: %+v", got)
	}
	if got := ov.Nodes["ship"]; got.SkipReason != "condition" {
		t.Errorf("ship = %+v, want a guard skip distinguishable from a human one", got)
	}
	if got := ov.Nodes["plan"]; got.Task != "" {
		t.Errorf("plan = %+v, want the parked state only on the current step", got)
	}
}

// A lane's inline steps are never painted from the parent's rows: they run in
// the child task, and the lane's caption carries that child instead
// (decision 1).
func TestBuildOverlayLeavesLaneStepsToTheirChild(t *testing.T) {
	lane := "api"
	wf := &apiclient.WorkflowBody{Steps: []apiclient.WorkflowStepDef{
		{ID: "build", Type: "command"},
		{ID: "spread", Type: "fan_out", Lanes: []apiclient.WorkflowLaneDef{
			{ID: "api", Steps: []apiclient.WorkflowStepDef{{ID: "build", Type: "command"}}},
		}},
	}}
	d := workflowgraph.Build(wf)
	m := workflowgraph.New()
	m.SetDefinition(wf)
	task := apiclient.TaskDetail{
		Task:          apiclient.Task{State: "running"},
		WorkflowSteps: []apiclient.WorkflowStep{{ID: "build"}, {ID: "spread"}},
		Steps:         []apiclient.StepRun{{ID: 1, StepID: "build", State: "running", Attempt: 1}},
	}
	ov := buildOverlay(task, d.Nodes, m.Lanes(), map[string]apiclient.Task{
		"api": {ID: 77, State: "running", LaneID: &lane},
	})

	if _, painted := ov.Nodes[workflowgraph.LaneKey("spread", "api")+"/build"]; painted {
		t.Error("the parent's step_run painted a lane's inline step")
	}
	if got := ov.Nodes["build"]; got.State != "running" {
		t.Errorf("the top-level build = %+v, want the parent's row", got)
	}
	if got := ov.Lanes[workflowgraph.LaneKey("spread", "api")]; got.ChildTaskID != 77 {
		t.Errorf("lane rollup = %+v, want the child task", got)
	}
}

// An attempt no node answers for — a follow-up round's step — is drawn
// off-graph rather than dropped (decision 3).
func TestBuildOverlayKeepsOffSnapshotRuns(t *testing.T) {
	d := workflowgraph.Build(&apiclient.WorkflowBody{
		Steps: []apiclient.WorkflowStepDef{{ID: "plan", Type: "agent"}},
	})
	ov := buildOverlay(apiclient.TaskDetail{
		WorkflowSteps: []apiclient.WorkflowStep{{ID: "plan"}},
		Steps: []apiclient.StepRun{
			{ID: 1, StepID: "plan", State: "succeeded"},
			{ID: 2, StepID: "follow_up_1", StepName: "follow up", StepType: "agent", State: "running"},
		},
	}, d.Nodes, nil, nil)

	if len(ov.Off) != 1 || ov.Off[0].StepID != "follow_up_1" {
		t.Fatalf("off-snapshot runs = %+v, want the follow-up round's step", ov.Off)
	}
}

// A snapshot that does not parse is a 200 with findings, and the tab says so
// rather than rendering a blank pane.
func TestWorkflowTabSaysWhenTheSnapshotDoesNotParse(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabWorkflow)
	v.width, v.height = 120, 40
	v.workflow.taskID = v.detail.taskID
	v.applyWorkflow(taskWorkflowMsg{taskID: v.detail.taskID, def: apiclient.TaskWorkflow{
		TaskID: v.detail.taskID,
		Errors: []apiclient.WorkflowFinding{{Line: 4, Message: "unknown step type: agnet"}},
	}})
	got := v.render(120, 40)
	if !strings.Contains(got, "does not parse") || !strings.Contains(got, "agnet") {
		t.Errorf("the tab does not report the findings:\n%s", got)
	}
}

// A terminal too narrow for one node says so rather than drawing a topology
// that is not the workflow's (task 017 decision 8) — measured at tab-body
// width, which is what this pane actually gets.
func TestWorkflowTabFallsBackWhenTooNarrow(t *testing.T) {
	v := workflowTabFixture(t)
	got := v.render(12, 30)
	if !strings.Contains(got, "too narrow") {
		t.Errorf("a 12-column terminal drew a graph anyway:\n%s", got)
	}
}

func strptr(s string) *string { return &s }
