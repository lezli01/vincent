package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// withLanes makes a task a fan-out parent waiting on its lanes, with the
// served rollup.
func withLanes(total, settled int, byState map[string]int) func(*apiclient.Task) {
	return func(t *apiclient.Task) {
		t.State = stateAwaitingChildren
		t.Children = &apiclient.ChildrenRollup{Total: total, Settled: settled, ByState: byState}
	}
}

// #1067: a folded parent whose lane needs a human says so on its card, in
// the attention style, names L, and is in Needs you.
func TestParentCardSaysALaneNeedsYou(t *testing.T) {
	cases := []struct {
		byState map[string]int
		want    string
		bad     bool
	}{
		{map[string]int{stateBlocked: 1, stateDone: 2, stateRunning: 1}, "! 1 lane blocked (L) · 2 of 4 done", true},
		{map[string]int{stateAwaitingInput: 1, stateDone: 2, stateRunning: 1}, "! 1 lane asks you (L) · 2 of 4 done", false},
		{map[string]int{stateAwaitingGate: 2, stateDone: 2}, "! 2 lanes to approve (L) · 2 of 4 done", false},
	}
	for _, tc := range cases {
		p := task(7, stateAwaitingChildren, withLanes(4, 2, tc.byState))
		got, style := taskCardStatus(p, testNow)
		if got != tc.want {
			t.Errorf("status = %q, want %q", got, tc.want)
		}
		if want := styleWarn; tc.bad {
			if style.GetForeground() != styleBad.GetForeground() {
				t.Errorf("%q is not in the failure colour", got)
			}
		} else if style.GetForeground() != want.GetForeground() {
			t.Errorf("%q is not in the attention colour", got)
		}
		if taskColumnOfTask(p) != taskColumnNeedsYou {
			t.Errorf("%q is not in Needs you", got)
		}
	}
	quiet := task(8, stateAwaitingChildren, withLanes(3, 2, map[string]int{stateDone: 2, stateRunning: 1}))
	if got, _ := taskCardStatus(quiet, testNow); got != "⑂ 2 of 3 lanes done" {
		t.Errorf("a parent whose lanes are working says %q", got)
	}
	if taskColumnOfTask(quiet) != taskColumnRunning {
		t.Error("a parent whose lanes are working left Running")
	}
}

// #1067: the card, the column, the `!` count and `H` share one predicate.
func TestParentCardAndAttentionShareOnePredicate(t *testing.T) {
	tasks := []apiclient.Task{
		task(1, stateBlocked),
		task(2, stateRunning),
		task(3, stateAwaitingChildren, withLanes(2, 1, map[string]int{stateBlocked: 1, stateDone: 1})),
		task(4, stateAwaitingChildren, withLanes(2, 1, map[string]int{stateRunning: 1, stateDone: 1})),
		task(5, stateAwaitingChildren, func(t *apiclient.Task) {
			t.Children = &apiclient.ChildrenRollup{Total: 2, AwaitingGate: []int64{9}}
		}),
		task(6, stateAwaitingChildren),
	}
	for _, tk := range tasks {
		status, _ := taskCardStatus(tk, testNow)
		keeps := keepsAttention(tk)
		if strings.HasPrefix(status, attentionBadge+" ") != keeps {
			t.Errorf("#%d: card %q, keepsAttention %v", tk.ID, status, keeps)
		}
		if (taskColumnOfTask(tk) == taskColumnNeedsYou) != keeps {
			t.Errorf("#%d: column disagrees with keepsAttention %v", tk.ID, keeps)
		}
	}
	if got := countAttention(tasks); got != 3 {
		t.Errorf("countAttention = %d, want the blocked task and the two parents", got)
	}
}

// #1067: `!` counts such a parent and lands on it.
func TestNextAttentionReachesAParentWhoseLaneNeedsYou(t *testing.T) {
	m := attentionRoot(t,
		task(3, stateAwaitingChildren, withLanes(2, 1, map[string]int{stateBlocked: 1, stateDone: 1})),
	)
	b := m.views[viewHome].(*shell).board
	if got := b.attentionTally(); got.here != 1 {
		t.Fatalf("tally = %+v, want the parent counted here", got)
	}
	targets := b.selectionTargets()
	if len(targets) != 1 || targets[0].id() != 3 {
		t.Fatalf("targets = %+v, want the parent", targets)
	}
	if got := b.attentionTargets(testProjectID); len(got) != 1 || got[0].id() != 3 {
		t.Fatalf("cross-project targets = %+v, want the parent", got)
	}
}

func failedAttempt(id int64, attempt int, reason string) apiclient.StepRun {
	return apiclient.StepRun{
		ID: id, StepIndex: 1, StepID: "unit-implement", StepName: "unit-implement",
		Attempt: attempt, State: "failed", FailureReason: &reason,
	}
}

func TestRepeatedFailure(t *testing.T) {
	steps := []apiclient.StepRun{
		failedAttempt(1, 1, "nonzero_exit"),
		failedAttempt(2, 2, "check_failed"),
		failedAttempt(3, 3, "check_failed"),
		failedAttempt(4, 4, "check_failed"),
		failedAttempt(5, 5, "check_failed"),
	}
	running := apiclient.StepRun{ID: 6, StepIndex: 1, StepID: "unit-implement", Attempt: 6, State: "running"}
	if got := repeatedFailure(append(steps, running), running); got != "attempts 2–5 each failed: check failed" {
		t.Errorf("running attempt 6 = %q", got)
	}
	sixth := failedAttempt(6, 6, "check_failed")
	if got := repeatedFailure(append(steps, sixth), sixth); got != "same failure as attempts 2–5" {
		t.Errorf("failed attempt 6 = %q", got)
	}
	second := failedAttempt(3, 3, "check_failed")
	if got := repeatedFailure(steps[:2], second); got != "same failure as attempt 2" {
		t.Errorf("failed attempt 3 after one same = %q", got)
	}
	other := failedAttempt(6, 6, "timeout")
	if got := repeatedFailure(append(steps, other), other); got != "" {
		t.Errorf("a new kind of failure is marked repeated: %q", got)
	}
	first := failedAttempt(2, 2, "check_failed")
	if got := repeatedFailure(steps[:1], first); got != "" {
		t.Errorf("a first check failure is marked repeated: %q", got)
	}
}

// #1067: a lane's own failure card marks a repeated failure.
func TestFailureCardMarksARepeatedFailure(t *testing.T) {
	task := cardTask("check_failed", failedRun(5, 3, "check_failed"))
	task.Steps = append(task.Steps, failedRun(3, 1, "check_failed"), failedRun(4, 2, "check_failed"))
	got := ansi.Strip(cardView(t, task).render(100, 40))
	if !strings.Contains(got, "↻ same failure as attempts 1–2") {
		t.Errorf("card misses the repeat:\n%s", got)
	}
}

// #1067: the parent says which lanes it waits on, with each one's step and
// attempt, and a repeated failure.
func TestParentOverviewNamesTheLaneItWaitsOn(t *testing.T) {
	d := taskDetailFixture(t)
	d.width = 160
	d.task.State = stateAwaitingChildren
	// Never dialled: the reads are returned as commands and not run.
	d.client = apiclient.New("http://127.0.0.1:1", "token")
	v := newTaskView(d)
	v.tab = taskTabOverview
	running := laneRow(903, "unit-b", 1, stateRunning, "", "vincent/903")
	running.UpdatedAt = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	v.update(taskLaneListMsg{taskID: 7, children: []apiclient.Task{
		laneRow(902, "unit-a", 0, stateDone, "", "vincent/902"),
		running,
		laneRow(904, "unit-c", 2, stateDone, "", "vincent/904"),
	}})
	if _, ok := v.laneAts[903]; !ok {
		t.Fatal("the running lane was not read")
	}
	if _, ok := v.laneAts[902]; ok {
		t.Fatal("a done lane was read")
	}
	at := apiclient.StepRun{ID: 6, StepIndex: 1, StepID: "unit-implement", StepName: "unit-implement", Attempt: 6, State: "running"}
	v.update(laneAtMsg{parentID: 7, laneID: 903, at: laneAt{
		updated: running.UpdatedAt, run: at, hasRun: true, repeat: "attempts 2–5 each failed: check failed",
	}})
	got := ansi.Strip(v.render(160, 50))
	want := "unit-b  #903 · running · step 2 unit-implement · attempt 6 · attempts 2–5 each failed: check failed"
	if !strings.Contains(got, "Waiting on") || !strings.Contains(got, want) {
		t.Errorf("overview misses %q:\n%s", want, got)
	}
}
