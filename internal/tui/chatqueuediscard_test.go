package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The three endings that delete a chat's queue — close, archive and hand-off
// — say how many queued messages go with it (task 156 decision 6, review F1).
// An idle chat can hold one: a cancelled or failed turn pauses the queue and
// leaves the chat idle.

const discardTwo = "2 queued messages will be discarded"

func TestChatCloseNamesTheDiscardedQueue(t *testing.T) {
	task := int64(4)
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.chatID, v.chat = 3, &apiclient.Chat{ID: 3, State: "idle", LinkedTaskID: &task, QueuedCount: 2, QueuePaused: true}
	if q := v.closeQuestion(); !strings.Contains(q, discardTwo) {
		t.Errorf("close question = %q, want it to say %q", q, discardTwo)
	}
	v.chat.QueuedCount = 1
	if q := v.closeQuestion(); !strings.Contains(q, "1 queued message will be discarded") {
		t.Errorf("close question = %q, want the singular count", q)
	}
	v.chat.QueuedCount = 0
	if q := v.closeQuestion(); strings.Contains(q, "queued") {
		t.Errorf("close question = %q names a queue the chat does not have", q)
	}
}

func TestChatArchiveNamesTheDiscardedQueue(t *testing.T) {
	v := chatsFixture()
	for i := range v.chats {
		if v.chats[i].ID == 1 { // the idle one
			v.chats[i].QueuedCount, v.chats[i].QueuePaused = 2, true
		}
	}
	for v.cursor = 0; ; v.cursor++ {
		if c, ok := v.current(); !ok {
			t.Fatal("the idle chat is not on the board")
		} else if c.ID == 1 {
			break
		}
	}
	c, _ := v.current()
	v.updateKey(keyPress(opKey(keymap.Archive)))
	if v.confirm == nil || !strings.Contains(v.confirm.text, discardTwo) {
		t.Fatalf("archive of chat %d with a queue asks %+v, want it to say %q", c.ID, v.confirm, discardTwo)
	}
	v.confirm = nil
	v.applyArchived(chatArchivedMsg{id: c.ID, err: &apiclient.Error{
		Code: "conflict", Details: map[string]string{"reason": "worktree_dirty"},
	}})
	if v.confirm == nil || !strings.Contains(v.confirm.text, discardTwo) {
		t.Fatalf("the forced archive asks %+v, want it to say %q", v.confirm, discardTwo)
	}
}

func TestChatHandoffFormNamesTheDiscardedQueue(t *testing.T) {
	n := branchForm(t)
	n.handoff = &apiclient.Chat{ID: 3, Branch: "vincent/chat-3", BaseBranch: "main", QueuedCount: 2, QueuePaused: true}
	if got := n.renderRow(ntCreate); !strings.Contains(got, discardTwo) {
		t.Errorf("hand-off submit row = %q, want it to say %q", got, discardTwo)
	}
	n.handoff.QueuedCount = 0
	if got := n.renderRow(ntCreate); strings.Contains(got, "queued") {
		t.Errorf("hand-off submit row = %q names a queue the chat does not have", got)
	}
}
