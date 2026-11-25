package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The probes for #1077's keys: every task tab and the issue screen.
func init() {
	taskProbe := func(key string, delta int) func(*testing.T) {
		return func(t *testing.T) {
			v := cardView(t, apiclient.TaskDetail{Task: apiclient.Task{ID: 4, State: stateRunning, StepTotal: 1}})
			_, cmd := v.update(registryKey(t, key))
			msg, ok := drain(cmd).(stepItemMsg)
			if !ok || msg.from != 4 || msg.delta != delta || msg.issue {
				t.Fatalf("%s produced %#v", key, drain(cmd))
			}
		}
	}
	issueProbe := func(key string, delta int) func(*testing.T) {
		return func(t *testing.T) {
			v := issueFixture()
			_, cmd := v.update(registryKey(t, key))
			msg, ok := drain(cmd).(stepItemMsg)
			if !ok || msg.from != v.issue.ID || msg.delta != delta || !msg.issue {
				t.Fatalf("%s produced %#v", key, drain(cmd))
			}
		}
	}
	for _, ctx := range []bindingContext{
		ctxTaskOverview, ctxTimeline, ctxTaskDetails, ctxOutput, ctxDiff, ctxTaskWorkflow, ctxTaskStepDetails, ctxTaskPull,
	} {
		if panelKeyProbes[ctx] == nil {
			panelKeyProbes[ctx] = map[string]func(*testing.T){}
		}
		panelKeyProbes[ctx]["alt+."] = taskProbe("alt+.", 1)
		panelKeyProbes[ctx]["alt+,"] = taskProbe("alt+,", -1)
	}
	panelKeyProbes[ctxIssue]["alt+."] = issueProbe("alt+.", 1)
	panelKeyProbes[ctxIssue]["alt+,"] = issueProbe("alt+,", -1)
}

// #1077: stepping walks the board's order and wraps.
func TestStepInWrapsTheBoardOrder(t *testing.T) {
	ids := []int64{6, 13, 15}
	for _, tc := range []struct {
		from  int64
		delta int
		want  int64
	}{{6, 1, 13}, {15, 1, 6}, {6, -1, 15}} {
		if got, ok := stepIn(ids, tc.from, tc.delta); !ok || got != tc.want {
			t.Errorf("stepIn(%d, %d) = %d, want %d", tc.from, tc.delta, got, tc.want)
		}
	}
	if _, ok := stepIn([]int64{6}, 6, 1); ok {
		t.Error("a task alone on the board stepped")
	}
	if _, ok := stepIn(ids, 99, 1); ok {
		t.Error("a task not on the board stepped")
	}
}

// #1077: from inside a task, alt+. opens the next card in the board's order.
func TestNextTaskOpensTheNeighbour(t *testing.T) {
	m := connectedRoot(t)
	s := m.views[viewHome].(*shell)
	b := taskBoardFixture()
	b.client = s.board.client
	s.board = b
	m.stepItem(stepItemMsg{from: 13, delta: 1}) // the open routes at once; its batch may be empty
	if m.selectedTask != 15 {
		t.Fatalf("the step opened #%d, want the card after #13", m.selectedTask)
	}
}

// stepFromTab opens task 13 on tab, then steps to the next card (15) the way
// alt+. does, and returns the workspace the step landed in.
func stepFromTab(t *testing.T, width int, tab taskViewTab) *taskView {
	t.Helper()
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	s := m.views[viewHome].(*shell)
	b := taskBoardFixture()
	b.client = s.board.client
	s.board = b
	m.Update(selectTaskMsg{id: 13, state: stateAwaitingGate, projectID: 1})
	tv := m.views[viewTask].(*taskView)
	if tab == taskTabPull {
		tv.tab = taskTabPull // the strip offers it only once a pull row lands
	} else {
		_ = tv.setTab(tab)
	}
	if m.active != viewTask || tv.tab != tab {
		t.Fatalf("fixture: on view %v tab %v, want the task on %v", m.active, tv.tab, tab)
	}
	m.Update(stepItemMsg{from: 13, delta: 1})
	if m.selectedTask != 15 || tv.detail.taskID != 15 {
		t.Fatalf("the step opened #%d, want #15", tv.detail.taskID)
	}
	return tv
}

// #1157: stepping to the next task keeps the tab the reader was on, rather
// than landing where a fresh open does.
func TestStepKeepsTheTab(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		tab   taskViewTab
	}{
		{"narrow diff", 80, taskTabDiff},
		{"narrow steps", 80, taskTabSteps},
		{"narrow workflow", 80, taskTabWorkflow},
		{"wide overview", 200, taskTabOverview},
		{"wide step details", 200, taskTabStepDetails},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tv := stepFromTab(t, tc.width, tc.tab); tv.tab != tc.tab {
				t.Errorf("the step landed on %v, want %v", tv.tab, tc.tab)
			}
		})
	}
}

// #1157: a tab the next task does not have falls back to where a fresh open
// lands — here the Pull request tab, once the next task's pull row says it
// has none.
func TestStepFallsBackWhenTheTabIsMissing(t *testing.T) {
	tv := stepFromTab(t, 80, taskTabPull)
	tv.applyPull(taskPullMsg{taskID: 15, pull: apiclient.GitHubTaskPull{}})
	if tv.tab != taskTabOverview {
		t.Errorf("a task with no pull request kept tab %v, want the Overview", tv.tab)
	}
}

// #1157: stepping to the next issue keeps the strip's tab too.
func TestIssueStepKeepsTheTab(t *testing.T) {
	m := connectedRoot(t)
	list := issuesFixture()
	m.views[viewIssues] = list
	m.sel = projectSel{id: 1, name: "api"}
	rows := list.rows()
	if len(rows) < 2 {
		t.Fatal("fixture: fewer than two issues on the board")
	}
	from, want := rows[0].issue.ID, rows[1].issue.ID
	m.Update(openIssueMsg{id: from, projectID: 1, back: viewIssues})
	iv := m.views[viewIssue].(*issueView)
	iv.tab = issueTabWork
	m.Update(stepItemMsg{from: from, delta: 1, issue: true})
	if iv.id != want {
		t.Fatalf("the step opened issue %d, want %d", iv.id, want)
	}
	if iv.tab != issueTabWork {
		t.Errorf("the step landed on tab %v, want Work", iv.tab)
	}
}
