package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// connectedChatsRoot is the shell a human has on screen when they press a key
// on the chats board: connected, sized, chats active and holding rows.
func connectedChatsRoot(t *testing.T) *root {
	t.Helper()
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.views[viewChats] = chatsFixture()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.switchTo(viewChats)
	if m.active != viewChats {
		t.Fatalf("fixture is on %v, want the chats board", m.active)
	}
	return m
}

// assertStartedAChat holds the acceptance of §15's one context-dependent key:
// the chats board stays on screen and its new-chat form is up.
func assertStartedAChat(t *testing.T, m *root) {
	t.Helper()
	if m.active == viewNewTask {
		t.Fatal("n on the chats board opened the new-task form; §15: on the chats board it makes a chat")
	}
	if m.active != viewChats {
		t.Fatalf("n left the chats board for %v", m.active)
	}
	if v, ok := m.views[viewChats].(*chatsView); !ok {
		t.Fatalf("the chats slot holds %T", m.views[viewChats])
	} else if v.create == nil {
		t.Fatal("n did not open the new-chat form")
	}
}

// TestChatsBoardNStartsAChatThroughRoot drives the key through the layer a
// real keystroke takes — the root model — rather than calling
// chatsView.updateKey directly the way the registry probes in
// bindings_test.go do. §15 (amended 2026-08-31, task 067) makes `n` the one
// key whose meaning depends on where you are: "on the chats board it makes a
// chat, and everywhere else it still makes a task". A resting chats board
// captures no input, so root.updateKey's global `n` runs first and never
// delegates. The palette subtest is the same defect by the other route: a
// keyed entry replays its keypress through root.updateKey.
func TestChatsBoardNStartsAChatThroughRoot(t *testing.T) {
	t.Run("direct key", func(t *testing.T) {
		m := connectedChatsRoot(t)
		m.Update(key("n"))
		assertStartedAChat(t, m)
	})

	t.Run("palette", func(t *testing.T) {
		m := connectedChatsRoot(t)
		m.Update(key(":"))
		if m.palette == nil {
			t.Fatal(": did not open the palette")
		}
		const label = "start a chat in the project you are looking at"
		found := false
		for i, e := range m.palette.matches() {
			if e.label == label {
				m.palette.cursor, found = i, true
				break
			}
		}
		if !found {
			t.Fatalf("the palette on the chats board offers no %q row", label)
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.palette != nil {
			t.Fatal("running the entry did not close the palette")
		}
		assertStartedAChat(t, m)
	})
}

// TestChatsBoardPaletteStillReachesNewTask guards the other half of the same
// collision. The chats board owns `n`, but the palette's "new task" row names
// its destination in its label, so it must still land there rather than
// replaying a keypress the board now answers with a chat.
func TestChatsBoardPaletteStillReachesNewTask(t *testing.T) {
	m := connectedChatsRoot(t)
	m.Update(key(":"))
	if m.palette == nil {
		t.Fatal(": did not open the palette")
	}
	const label = "new task — for the project you are looking at"
	found := false
	for i, e := range m.palette.matches() {
		if e.label == label {
			m.palette.cursor, found = i, true
			break
		}
	}
	if !found {
		t.Fatalf("the palette on the chats board offers no %q row", label)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.active != viewNewTask {
		t.Fatalf("the palette's new-task row left the root on %v", m.active)
	}
	if v, ok := m.views[viewChats].(*chatsView); ok && v.create != nil {
		t.Fatal("the palette's new-task row opened the new-chat form instead")
	}
}
