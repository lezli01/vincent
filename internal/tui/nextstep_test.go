package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

func waitingTask(state string) apiclient.TaskDetail {
	return apiclient.TaskDetail{
		Task: apiclient.Task{
			ID: 4, Title: "waiting task", State: state, CurrentStep: 0, StepTotal: 2, StepName: "review",
			AvailableActions: []string{
				apiclient.ActionApprove, apiclient.ActionReject, apiclient.ActionSkip, apiclient.ActionCancel,
			},
		},
		WorkflowSteps: []apiclient.WorkflowStep{
			{Index: 0, ID: "review", Type: "manual", Instructions: "Check the release notes by hand."},
			{Index: 1, ID: "ship", Type: "command"},
		},
		Steps: []apiclient.StepRun{{ID: 1, StepIndex: 0, StepID: "review", StepType: "manual", Attempt: 1, State: "running"}},
	}
}

// #1068: a task at a gate gets the pinned card on the other tabs, with the
// gate's instructions and its keys.
func TestPinnedCardForAGate(t *testing.T) {
	v := cardView(t, waitingTask(stateAwaitingGate))
	v.tab = taskTabSteps
	got := ansi.Strip(strings.Join(v.taskFailureCard(100), "\n"))
	for _, want := range []string{"Waiting at a gate for your approval", "Check the release notes by hand.", "a approve", "x reject"} {
		if !strings.Contains(got, want) {
			t.Errorf("gate card misses %q:\n%s", want, got)
		}
	}
}

// #1068: a task asking a question gets it pinned, with enter to answer.
func TestPinnedCardForAQuestion(t *testing.T) {
	task := waitingTask(stateAwaitingInput)
	task.AvailableActions = []string{apiclient.ActionAnswer, apiclient.ActionCancel}
	req, err := json.Marshal(apiclient.InputRequest{Kind: "question", Questions: []apiclient.InputQuestion{{Text: "Which database?"}}})
	if err != nil {
		t.Fatal(err)
	}
	task.PendingInput = req
	v := cardView(t, task)
	v.tab = taskTabSteps
	got := ansi.Strip(strings.Join(v.taskFailureCard(100), "\n"))
	for _, want := range []string{"A question is waiting for you", "Which database?", "enter answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("question card misses %q:\n%s", want, got)
		}
	}
	// Not on the Overview, which says it at length.
	v.tab = taskTabOverview
	if card := v.taskFailureCard(100); card != nil {
		t.Error("the Overview pins a card of its own")
	}
}

// #1068: a done task names follow-up and archive on its Overview and in the
// footer.
func TestDoneTaskNamesItsNextSteps(t *testing.T) {
	task := apiclient.TaskDetail{
		Task: apiclient.Task{
			ID: 4, Title: "done task", State: stateDone, CurrentStep: 1, StepTotal: 1,
			AvailableActions: []string{apiclient.ActionFollowUp, apiclient.ActionArchive, apiclient.ActionChat},
		},
		Steps: []apiclient.StepRun{{ID: 1, StepIndex: 0, StepName: "build", Attempt: 1, State: stepSucceeded, ResultSummary: "all green"}},
	}
	got := ansi.Strip(cardView(t, task).render(100, 50))
	for _, want := range []string{"next  F follow up · T chat · A archive", "all green"} {
		if !strings.Contains(got, want) {
			t.Errorf("done Overview misses %q:\n%s", want, got)
		}
	}
	target := taskActions{id: 4, state: stateDone, actions: task.AvailableActions}
	_, hits := buildFooter(160, nil, &actionBar{}, target, attentionTally{}, false, false)
	keys := map[string]bool{}
	for _, h := range hits {
		keys[h.key] = true
	}
	if !keys["F"] {
		t.Error("the footer does not offer F follow up on a done task")
	}
}

// #1068: a P refusal on the Overview lands on the status line.
func TestPullRefusalOnTheOverviewIsOnTheStatusLine(t *testing.T) {
	v := cardView(t, apiclient.TaskDetail{Task: apiclient.Task{ID: 4, State: stateDone, StepTotal: 1}})
	v.openPullCmd()
	if v.detail.actions.status != "no pull request is linked to this task" || !v.detail.actions.statusBad {
		t.Fatalf("status = %q", v.detail.actions.status)
	}
}

// conditionEndedTask is task 841's shape: survey reported, has-candidates
// exited 1 (allowed), and the any-candidates condition ended the task.
func conditionEndedTask() apiclient.TaskDetail {
	reason := "nonzero_exit"
	guard := "false"
	return apiclient.TaskDetail{
		Task: apiclient.Task{ID: 4, Title: "triage", State: stateDone, CurrentStep: 2, StepTotal: 4},
		Steps: []apiclient.StepRun{
			{
				ID: 1, StepIndex: 0, StepID: "survey", StepName: "survey", Attempt: 1, State: stepSucceeded,
				StatusMessage: ptr("0 of 11 open issue(s) ready for triage"),
			},
			{
				ID: 2, StepIndex: 1, StepID: "has-candidates", StepName: "has-candidates", Attempt: 1, State: "failed",
				FailureReason: &reason, ExitCode: ptr(1),
			},
			{
				ID: 3, StepIndex: 2, StepID: "any-candidates", StepName: "any-candidates", StepType: "condition",
				Attempt: 1, State: stepStateStopped, RenderedIf: &guard,
			},
		},
	}
}

// #1068: the outcome card says a condition ended the task, which, and the
// last status before it; the command the condition read is not a failure.
func TestOutcomeCardSaysAConditionEndedTheTask(t *testing.T) {
	got := ansi.Strip(cardView(t, conditionEndedTask()).render(120, 50))
	for _, want := range []string{
		"■ ended early at step 3 any-candidates — its condition was false",
		"step 2 has-candidates exited 1, which the condition read — not a failure",
		"last status before it  “0 of 11 open issue(s) ready for triage”",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("outcome misses %q:\n%s", want, got)
		}
	}
}

// #1068: an allowed failure is drawn as passed in the pips and the rail.
func TestAllowedFailureIsNotDrawnAsAFailure(t *testing.T) {
	task := conditionEndedTask()
	if pips := ansi.Strip(renderStepPips(task.Task, task.Steps)); strings.Contains(pips, ic("×")) {
		t.Errorf("pips draw the allowed failure as failed: %q", pips)
	}
	if !allowedFailure(task.Task, 1, "failed") {
		t.Error("a done task's failed step is not allowed")
	}
	running := apiclient.Task{State: stateBlocked, CurrentStep: 1, StepTotal: 3}
	if allowedFailure(running, 1, "failed") {
		t.Error("the step a task is blocked on reads as allowed")
	}
}
