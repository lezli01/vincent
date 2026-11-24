package tui

import (
	"testing"

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
		panelKeyProbes[ctx]["alt+n"] = taskProbe("alt+n", 1)
		panelKeyProbes[ctx]["alt+shift+n"] = taskProbe("alt+shift+n", -1)
	}
	panelKeyProbes[ctxIssue]["alt+n"] = issueProbe("alt+n", 1)
	panelKeyProbes[ctxIssue]["alt+shift+n"] = issueProbe("alt+shift+n", -1)
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

// #1077: from inside a task, alt+n opens the next card in the board's order.
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
