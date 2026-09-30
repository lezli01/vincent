package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// failureView is a workspace on a task whose attempts are runs, with the
// Overview up the way a fresh open leaves it.
func failureView(t *testing.T, state string, runs ...apiclient.StepRun) *taskView {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 4
	d.applyLoaded(detailLoadedMsg{id: 4, task: apiclient.TaskDetail{
		Task:  apiclient.Task{ID: 4, Title: "failing task", State: state, StepTotal: 4},
		Steps: runs,
	}})
	return newTaskView(d)
}

func iterationRun(id int64, index, iteration, n int, name, state string) apiclient.StepRun {
	r := attempt(id, index, n, name, state, false)
	r.Iteration = iteration
	return r
}

func pressN(t *testing.T, v *taskView) any {
	t.Helper()
	cmd := v.updateKey(registryKey(t, "N"))
	if cmd == nil {
		return nil
	}
	return cmd()
}

// TestNextFailureFollowsExecutionOrderAndWraps is decision 3: one sequence in
// execution order, starting after the attempt cursor, wrapping at the end.
func TestNextFailureFollowsExecutionOrderAndWraps(t *testing.T) {
	v := failureView(t, stateBlocked,
		attempt(1, 0, 1, "plan", "failed", false),
		attempt(2, 1, 1, "build", "succeeded", false),
		attempt(3, 2, 1, "test", "failed", false),
		attempt(4, 3, 1, "ship", "failed", false),
	)
	d := v.detail
	d.selectedRun = 2
	for _, want := range []int64{3, 4, 1, 3} {
		pressN(t, v)
		if d.selectedRun != want || v.tab != taskTabOutput {
			t.Fatalf("N landed on attempt %d (tab %v), want %d on Output", d.selectedRun, v.tab, want)
		}
	}
}

// TestNextFailureSkipsARetryThatPassed: a step's newest attempt is its stop,
// so one that failed and then passed is not a failure any more.
func TestNextFailureSkipsARetryThatPassed(t *testing.T) {
	v := failureView(t, stateBlocked,
		attempt(1, 0, 1, "plan", "failed", false),
		attempt(2, 0, 2, "plan", "succeeded", false),
		attempt(3, 1, 1, "build", "failed", false),
		attempt(4, 1, 2, "build", "failed", false),
	)
	targets := v.detail.failureTargets()
	if len(targets) != 1 || targets[0].run.ID != 4 {
		t.Fatalf("targets = %+v, want only build's newest attempt 4", targets)
	}
}

// TestNextFailureOnACleanTaskSaysSo: no stops, no move, and the status line
// says why nothing happened.
func TestNextFailureOnACleanTaskSaysSo(t *testing.T) {
	v := failureView(t, stateDone,
		attempt(1, 0, 1, "plan", "succeeded", false),
		attempt(2, 1, 1, "build", "succeeded", false),
	)
	before := v.detail.selectedRun
	if msg := pressN(t, v); msg != nil {
		t.Fatalf("N on a clean task emitted %T", msg)
	}
	if v.tab != taskTabOverview || v.detail.selectedRun != before {
		t.Errorf("N moved a clean task to tab %v, cursor %d", v.tab, v.detail.selectedRun)
	}
	if !strings.Contains(v.detail.actions.status, "no failures") {
		t.Errorf("status = %q, want it to say there are no failures", v.detail.actions.status)
	}
}

// TestNextFailureUnfoldsItsIteration: a failed iteration folded shut behind
// the latest one opens when N lands on it, as enter and → would open it.
func TestNextFailureUnfoldsItsIteration(t *testing.T) {
	v := failureView(t, stateBlocked,
		iterationRun(1, 0, 1, 1, "fix", "failed"),
		iterationRun(2, 0, 2, 1, "fix", "succeeded"),
		iterationRun(3, 0, 3, 1, "fix", "running"),
	)
	d := v.detail
	if d.tierOpen(0, 1, 3) {
		t.Fatal("iteration 1 starts open; the fixture proves nothing")
	}
	d.selectedRun = 2
	pressN(t, v)
	if d.selectedRun != 1 || !d.tierOpen(0, 1, 3) {
		t.Errorf("N left cursor %d with iteration 1 open=%v; want attempt 1, unfolded",
			d.selectedRun, d.tierOpen(0, 1, 3))
	}
}

// TestNextFailureStartsFromTheCursor is 088 decision 7's shared cursor: the
// cycle begins after whatever attempt the reader is on.
func TestNextFailureStartsFromTheCursor(t *testing.T) {
	v := failureView(t, stateBlocked,
		attempt(1, 0, 1, "plan", "failed", false),
		attempt(2, 1, 1, "build", "failed", false),
		attempt(3, 2, 1, "test", "failed", false),
	)
	v.detail.selectedRun = 2
	pressN(t, v)
	if v.detail.selectedRun != 3 {
		t.Errorf("N from attempt 2 landed on %d, want 3", v.detail.selectedRun)
	}
}

// TestNextFailureOpensABlockedLane: a blocked lane sits at its fan_out's
// position and opens the way `l` does, carrying the landing with it.
func TestNextFailureOpensABlockedLane(t *testing.T) {
	fan := attempt(2, 1, 1, "lanes", "failed", false)
	fan.StepType = stepTypeFanOut
	v := failureView(t, stateBlocked, attempt(1, 0, 1, "plan", "succeeded", false), fan)
	v.detail.laneRows = []apiclient.Task{
		{ID: 41, State: stateDone},
		{ID: 42, State: stateBlocked},
	}
	msg, ok := pressN(t, v).(openTaskMsg)
	if !ok || msg.id != 42 || msg.from != 4 || !msg.failure {
		t.Fatalf("N opened %+v, want blocked lane 42 from task 4 with the landing", msg)
	}
}

// TestPendingFailureLandsWhenTheLaneLoads is the second half of a lane jump:
// once the lane `N` opened has loaded, the workspace is on its failure.
func TestPendingFailureLandsWhenTheLaneLoads(t *testing.T) {
	d := newTestDetail(t)
	v := newTaskView(d)
	v.pendingFailure = 42
	v.update(selectTaskMsg{id: 42, state: stateBlocked})
	if v.pendingFailure != 42 {
		t.Fatal("opening the lane dropped the pending landing")
	}
	v.update(detailLoadedMsg{id: 42, task: apiclient.TaskDetail{
		Task: apiclient.Task{ID: 42, State: stateBlocked, StepTotal: 1},
		Steps: []apiclient.StepRun{
			attempt(7, 0, 1, "build", "failed", false),
			attempt(8, 1, 1, "test", "succeeded", false),
		},
	}})
	if v.pendingFailure != 0 || v.tab != taskTabOutput || d.selectedRun != 7 {
		t.Errorf("after the lane loaded: pending %d, tab %v, cursor %d; want Output on attempt 7",
			v.pendingFailure, v.tab, d.selectedRun)
	}
}

// TestFailureCardLinkIsNextFailuresLanding: the Overview failure card's `3`
// is the same landing N makes — same attempt, same tab, and a re-land when
// the attempt is already on screen.
func TestFailureCardLinkIsNextFailuresLanding(t *testing.T) {
	v := failureView(t, stateBlocked,
		attempt(1, 0, 1, "plan", "succeeded", false),
		attempt(2, 1, 1, "build", "failed", false),
	)
	v.detail.task.CurrentStep = 1
	v.detail.displayRun = 2
	v.detail.land = false
	v.updateKey(registryKey(t, "3"))
	if v.tab != taskTabOutput || v.detail.selectedRun != 2 || !v.detail.land || v.detail.following {
		t.Errorf("3 left tab %v, cursor %d, land %v, following %v; want Output on attempt 2, landing, not following",
			v.tab, v.detail.selectedRun, v.detail.land, v.detail.following)
	}
}
