package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/keymap"
)

// #1138: the "not available" note is about the card the key was pressed on.
// Moving the cursor to another card takes it away; a real action result the
// human has not read yet stays.
func TestBoardCursorMoveClearsTheUnavailableNote(t *testing.T) {
	moveOff := func(t *testing.T, b *board) {
		t.Helper()
		from, _ := b.cardSelected()
		for _, k := range []string{"down", "up", "right", "left"} {
			b.update(registryKey(t, k))
			if id, _ := b.cardSelected(); id != from {
				return
			}
		}
		t.Fatal("no key moved the cursor off the card")
	}

	b := taskBoardFixture()
	b.actions = &actionBar{}
	placeCursorOn(t, b, 21) // running: approve is not on offer
	b.update(registryKey(t, opKey(keymap.Approve)))
	if !strings.Contains(b.actions.status, "approve: not available while running") {
		t.Fatalf("a on a running task said %q", b.actions.status)
	}
	moveOff(t, b)
	if b.actions.status != "" {
		t.Errorf("the note outlived the cursor move: %q", b.actions.status)
	}

	b.actions.setStatus("cancel · now cancelled", false)
	moveOff(t, b)
	if b.actions.status != "cancel · now cancelled" {
		t.Errorf("a cursor move wiped an action's result: %q", b.actions.status)
	}
}

// #1138: in the workspace the note goes when the tab changes, and an
// action's result does not.
func TestWorkspaceTabChangeClearsTheUnavailableNote(t *testing.T) {
	v := newTaskView(taskDetailFixture(t)) // running
	v.width, v.height = 100, 30
	v.update(registryKey(t, opKey(keymap.Approve)))
	if !strings.Contains(v.detail.actions.status, "approve: not available while running") {
		t.Fatalf("a on a running task said %q", v.detail.actions.status)
	}
	v.update(registryKey(t, "tab"))
	if v.detail.actions.status != "" {
		t.Errorf("the note outlived the tab change: %q", v.detail.actions.status)
	}

	v.detail.actions.setStatus("pause · now paused", false)
	v.update(registryKey(t, "tab"))
	if v.detail.actions.status != "pause · now paused" {
		t.Errorf("a tab change wiped an action's result: %q", v.detail.actions.status)
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
		{keymap.Diagnose, "ask why: not available while running"},
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
			if v.detail.actions.status != tc.want {
				t.Errorf("status = %q, want %q", v.detail.actions.status, tc.want)
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
		if !strings.Contains(v.note, "no issue selected") {
			t.Errorf("%s on an empty board said %q", k, v.note)
		}
	}
}
