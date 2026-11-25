package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/keymap"
)

// #1138, #1164: the "not available" note is about the card the key was
// pressed on. The next key — the one that moves the cursor off it — takes it
// away; a real action result the human has not read yet stays.
func TestBoardCursorMoveClearsTheUnavailableNote(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 120, 30)
	s := m.views[viewHome].(*shell)
	b := taskBoardFixture()
	b.actions = s.bar
	s.board = b
	placeCursorOn(t, b, 21) // running: approve is not on offer
	m.Update(registryKey(t, opKey(keymap.Approve)))
	if !strings.Contains(m.status.text, "approve: not available while running") {
		t.Fatalf("a on a running task said %q", m.status.text)
	}
	m.Update(registryKey(t, "down"))
	if m.status.text != "" {
		t.Errorf("the note outlived the cursor move: %q", m.status.text)
	}

	b.actions.setStatus("cancel · now cancelled", false)
	m.Update(registryKey(t, "down"))
	if m.status.text != "cancel · now cancelled" {
		t.Errorf("a cursor move wiped an action's result: %q", m.status.text)
	}
}

// #1138, #1164: in the workspace the note goes when the tab changes, and an
// action's result does not.
func TestWorkspaceTabChangeClearsTheUnavailableNote(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 100, 30)
	v := newTaskView(taskDetailFixture(t)) // running
	v.width, v.height = 100, 30
	v.detail.actions.setStatusHolder(m.status)
	m.views[viewTask] = v
	m.active = viewTask
	m.Update(registryKey(t, opKey(keymap.Approve)))
	if !strings.Contains(m.status.text, "approve: not available while running") {
		t.Fatalf("a on a running task said %q", m.status.text)
	}
	m.Update(registryKey(t, "tab"))
	if m.status.text != "" {
		t.Errorf("the note outlived the tab change: %q", m.status.text)
	}

	v.detail.actions.setStatus("pause · now paused", false)
	m.Update(registryKey(t, "tab"))
	if m.status.text != "pause · now paused" {
		t.Errorf("a tab change wiped an action's result: %q", m.status.text)
	}
}

// #1138: R, F, T and Q in the workspace say why when the task does not offer
// them, as every other task key has since #1059.
func TestWorkspaceTaskKeysNotOfferedSayWhy(t *testing.T) {
	for _, tc := range []struct {
		op   keymap.Op
		want string
	}{
		{keymap.Repair, "repair: not available while running"},
		{keymap.FollowUp, "follow-up: not available while running"},
		{keymap.Chat, "chat: not available while running"},
		{keymap.Diagnose, "diagnose: not available while running"},
	} {
		t.Run(string(tc.op), func(t *testing.T) {
			v := newTaskView(taskDetailFixture(t)) // running, offers nothing
			v.width, v.height = 100, 30
			_, cmd := v.update(registryKey(t, opKey(tc.op)))
			if v.popup || v.detail.actions.capturing() {
				t.Fatal("the key opened something on a task that does not offer it")
			}
			if msg := drain(cmd); msg != nil {
				t.Fatalf("the key produced %#v", msg)
			}
			if v.detail.actions.status().text != tc.want {
				t.Errorf("status = %q, want %q", v.detail.actions.status().text, tc.want)
			}
		})
	}
}

// #1138: enter and a on an empty issues board say why they did nothing.
func TestEmptyIssuesBoardKeysSayWhy(t *testing.T) {
	for _, k := range []string{opKey(keymap.OpenRow), opKey(keymap.Add)} {
		v := issuesFixture()
		v.issues = nil
		v.clampCursor()
		_, cmd := v.update(registryKey(t, k))
		if msg := drain(cmd); msg != nil {
			t.Fatalf("%s produced %#v", k, msg)
		}
		if !strings.Contains(v.status().text, "no issue selected") {
			t.Errorf("%s on an empty board said %q", k, v.status().text)
		}
	}
}
