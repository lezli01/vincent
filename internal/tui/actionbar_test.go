package tui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/taskstate"
)

// targetFor builds the bar's view of a task in a given state, straight from
// the state machine — so these tests cannot drift from §6 by restating it.
func targetFor(state taskstate.State) taskActions {
	actions := taskstate.HumanActionsFrom(state)
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		names = append(names, string(a))
	}
	return taskActions{id: 7, state: string(state), actions: names}
}

// TestActionBarOffersExactlyTheValidActions: the bar is derived from
// available_actions for every state, so an FSM change can never leave a dead
// action on screen or hide a live one.
func TestActionBarOffersExactlyTheValidActions(t *testing.T) {
	every := []string{
		apiclient.ActionPause, apiclient.ActionResume, apiclient.ActionCancel,
		apiclient.ActionRetry, apiclient.ActionSkip, apiclient.ActionApprove,
		apiclient.ActionReject, apiclient.ActionArchive,
	}
	for _, state := range []taskstate.State{
		taskstate.Queued, taskstate.Running, taskstate.AwaitingGate,
		taskstate.AwaitingInput, taskstate.Blocked, taskstate.Paused,
		taskstate.Done, taskstate.Aborted, taskstate.Archived,
	} {
		t.Run(string(state), func(t *testing.T) {
			var bar actionBar
			target := targetFor(state)
			line := bar.render(target)
			for _, action := range every {
				got := strings.Contains(line, action)
				if want := target.has(action); got != want {
					t.Errorf("%s in %s: rendered = %v, valid = %v (%q)", action, state, got, want, line)
				}
			}
		})
	}
}

// TestActionKeyIsIgnoredWhenTheActionIsNotOffered: an unavailable key falls
// through to the view, so j/k keep navigating on a row that cannot be skipped.
func TestActionKeyIsIgnoredWhenTheActionIsNotOffered(t *testing.T) {
	var bar actionBar
	running := targetFor(taskstate.Running)

	if cmd, handled := bar.handleKey("s", nil, running); handled || cmd != nil {
		t.Errorf("skip on a running task: handled = %v, want the key to fall through", handled)
	}
	if _, handled := bar.handleKey("p", nil, running); !handled {
		t.Error("pause on a running task was not handled")
	}
}

// TestPauseKeyPicksTheOfferedDirection: one key, two actions, and the daemon
// decides which one exists.
func TestPauseKeyPicksTheOfferedDirection(t *testing.T) {
	if got, _ := resolveAction("p", targetFor(taskstate.Paused)); got != apiclient.ActionResume {
		t.Errorf("p on a paused task = %q, want resume", got)
	}
	if got, _ := resolveAction("p", targetFor(taskstate.Running)); got != apiclient.ActionPause {
		t.Errorf("p on a running task = %q, want pause", got)
	}
	if _, ok := resolveAction("p", targetFor(taskstate.Done)); ok {
		t.Error("p on a finished task resolved to an action")
	}
}

// TestDestructiveActionsConfirmFirst: cancel kills a live process tree and
// archive removes a worktree — neither fires on one keystroke.
func TestDestructiveActionsConfirmFirst(t *testing.T) {
	for _, tc := range []struct {
		key   string
		state taskstate.State
		want  string
	}{
		{"c", taskstate.Running, "cancel"},
		{"A", taskstate.Done, "archive"},
	} {
		var bar actionBar
		target := targetFor(tc.state)
		cmd, handled := bar.handleKey(tc.key, nil, target)
		if !handled || cmd != nil {
			t.Fatalf("%s: handled = %v, cmd = %v; want a confirmation, not a call", tc.key, handled, cmd != nil)
		}
		if !bar.capturing() {
			t.Errorf("%s: the confirmation does not hold the keyboard", tc.key)
		}
		if got := bar.render(target); !strings.Contains(got, tc.want) {
			t.Errorf("%s: prompt = %q, want it to name the action", tc.key, got)
		}
		if _, _ = bar.handleKey("n", nil, target); bar.capturing() {
			t.Errorf("%s: n did not dismiss the confirmation", tc.key)
		}
	}
}

// TestConfirmationSwallowsOtherKeys: a question is a question — the next
// action must not start underneath it.
func TestConfirmationSwallowsOtherKeys(t *testing.T) {
	var bar actionBar
	target := targetFor(taskstate.Running)
	bar.handleKey("c", nil, target)

	if _, handled := bar.handleKey("p", nil, target); !handled {
		t.Error("p reached the view while a confirmation was pending")
	}
	if !bar.capturing() {
		t.Error("the confirmation was dismissed by an unrelated key")
	}
}

// TestDirtyArchiveAsksAgainWithForce: force *is* the dirty confirmation (§6),
// so the refusal becomes the second question rather than an error.
func TestDirtyArchiveAsksAgainWithForce(t *testing.T) {
	var bar actionBar
	target := targetFor(taskstate.Done)
	bar.applyResult(actionResultMsg{
		taskID: target.id,
		action: apiclient.ActionArchive,
		err: &apiclient.Error{
			Status: http.StatusConflict, Code: "invalid_state",
			Message: "worktree has uncommitted changes",
			Details: map[string]string{"reason": "worktree_dirty"},
		},
	})
	if !bar.capturing() || !bar.force {
		t.Fatalf("dirty archive did not re-ask with force (pending %q, force %v)", bar.pending, bar.force)
	}
	if got := bar.render(target); !strings.Contains(got, "uncommitted") {
		t.Errorf("prompt = %q, want it to say what is lost", got)
	}
}

// TestConflictReportsTheStateItFound: §13.1's details exist so a stale bar can
// say what the daemon actually has.
func TestConflictReportsTheStateItFound(t *testing.T) {
	var bar actionBar
	bar.applyResult(actionResultMsg{
		taskID: 7,
		action: apiclient.ActionApprove,
		err: &apiclient.Error{
			Status: http.StatusConflict, Code: "invalid_state",
			Message: "task is not awaiting a gate",
			Details: map[string]string{"state": "running"},
		},
	})
	if !strings.Contains(bar.status, "running") || !bar.statusBad {
		t.Errorf("status = %q (bad = %v), want it to name the found state", bar.status, bar.statusBad)
	}
}

// TestSuccessReportsTheNewState keeps the feedback specific: "approve · now
// queued" tells a user the gate actually moved.
func TestSuccessReportsTheNewState(t *testing.T) {
	var bar actionBar
	bar.applyResult(actionResultMsg{
		taskID: 7, action: apiclient.ActionApprove,
		task: apiclient.Task{ID: 7, State: "queued"},
	})
	if !strings.Contains(bar.status, "queued") || bar.statusBad {
		t.Errorf("status = %q (bad = %v), want the new state", bar.status, bar.statusBad)
	}
}
