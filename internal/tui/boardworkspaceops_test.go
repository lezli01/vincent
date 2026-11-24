package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// Review finding 6: R, F and E are hinted on the board, and their forms live
// in the task workspace. On a task that offers the action the key opens the
// workspace with the operation to run; on one that does not it says why.
func TestBoardWorkspaceKeysOpenTheTask(t *testing.T) {
	b := taskBoardFixture()
	b.actions = &actionBar{}
	b.tasks[0].AvailableActions = append(b.tasks[0].AvailableActions, apiclient.ActionRepair)
	placeCursorOn(t, b, 6) // blocked, offers repair and retry
	_, cmd := b.updateKey(registryKey(t, opKey(keymap.Repair)))
	msg, ok := drain(cmd).(selectTaskMsg)
	if !ok || msg.id != 6 || msg.then != keymap.Repair || !msg.fromBoard {
		t.Fatalf("R produced %#v, want an open of #6 that runs repair", drain(cmd))
	}
	_, cmd = b.updateKey(registryKey(t, opKey(keymap.EditRetry)))
	if msg, ok := drain(cmd).(selectTaskMsg); !ok || msg.then != keymap.EditRetry {
		t.Fatalf("E produced %#v, want an open that runs edit+retry", drain(cmd))
	}
	_, cmd = b.updateKey(registryKey(t, opKey(keymap.FollowUp)))
	if cmd != nil {
		t.Fatal("F opened a blocked task for a follow-up it does not offer")
	}
	if !strings.Contains(b.actions.status, "follow-up: not available while") {
		t.Fatalf("F said %q, want why it did nothing", b.actions.status)
	}
}

// The workspace runs the board's operation once the task has loaded, once.
func TestWorkspaceRunsTheBoardsOperationOnLoad(t *testing.T) {
	d := taskDetailFixture(t)
	task := d.task
	task.State = stateBlocked
	task.AvailableActions = []string{apiclient.ActionRepair, apiclient.ActionRetry}
	d.taskID, d.loaded = 0, false
	v := newTaskView(d)
	v.width, v.height = 100, 30
	v.update(selectTaskMsg{id: 7, state: stateBlocked, then: keymap.Repair})
	if v.detail.repair != nil {
		t.Fatal("the form opened before the task loaded")
	}
	v.update(detailLoadedMsg{id: 7, seq: 1, task: task})
	if v.detail.repair == nil || !v.popup {
		t.Fatal("the repair form is not up once the task loaded")
	}
	v.detail.repair, v.popup = nil, false
	v.update(detailLoadedMsg{id: 7, seq: 2, task: task})
	if v.detail.repair != nil {
		t.Fatal("a later refresh opened the form again")
	}
}
