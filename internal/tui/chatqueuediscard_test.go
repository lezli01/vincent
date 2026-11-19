package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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

// TestChatFooterSaysEnterQueuesWhileATurnRuns is task 156 decision 5 in the
// footer (review F2): enter is offered while a turn runs or waits on a
// question, reading `enter queue`, and steer is shown beside a running turn
// as unavailable.
func TestChatFooterSaysEnterQueuesWhileATurnRuns(t *testing.T) {
	hints := func(v *chatView) map[string]string {
		out := map[string]string{}
		for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
			if b.context == ctxChat && b.hint != "" {
				out[b.key] = b.hint
			}
		}
		return out
	}
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.chatID = 3
	for _, tc := range []struct {
		state, enter, steer string
	}{
		{"idle", "enter send", ""},
		{"running", "enter queue", "ctrl+s steer (unavailable)"},
		{"awaiting_input", "enter queue", ""},
		{"closed", "", ""},
	} {
		v.chat = &apiclient.Chat{ID: 3, State: tc.state, SteerReason: "no agent can steer yet"}
		h := hints(v)
		if h["enter"] != tc.enter {
			t.Errorf("%s: enter hint = %q, want %q", tc.state, h["enter"], tc.enter)
		}
		if h["ctrl+s"] != tc.steer {
			t.Errorf("%s: steer hint = %q, want %q", tc.state, h["ctrl+s"], tc.steer)
		}
	}
	v.chat = &apiclient.Chat{ID: 3, State: "running", CanSteer: true}
	if got := hints(v)["ctrl+s"]; got != "ctrl+s steer" {
		t.Errorf("a chat that can steer shows %q, want %q", got, "ctrl+s steer")
	}
	// On screen at a width that has room, after the keys 80 columns keep.
	v.chat = &apiclient.Chat{ID: 3, State: "running"}
	line, _ := buildFooter(140, v.liveBindings(bindingsFor(ctxChat)), nil, taskActions{}, attentionTally{}, false, true)
	if got := ansi.Strip(line); !strings.Contains(got, "enter queue") {
		t.Errorf("the 140-column footer of a running chat lacks enter queue: %q", got)
	}
	v.chat = nil
	if got := hints(v)["enter"]; got != "" {
		t.Errorf("an unloaded chat offers enter: %q", got)
	}
}

// TestAltRStartsAnIdleChatsUnpausedQueue is review F4's way out in the TUI:
// an idle chat left holding an unpaused queue — nothing will drain it — says
// so under the queue, and alt+r starts it rather than refusing because the
// queue is not paused.
func TestAltRStartsAnIdleChatsUnpausedQueue(t *testing.T) {
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = apiclient.New("http://127.0.0.1:0", "t")
	v.chatID, v.chat = 3, &apiclient.Chat{ID: 3, State: "idle", QueuedCount: 1}
	v.queue = &apiclient.ChatQueue{Messages: []apiclient.ChatQueuedMessage{{ID: 7, ChatID: 3, Prompt: "next"}}}
	if got := ansi.Strip(strings.Join(v.queueLines(80), "\n")); !strings.Contains(got, "alt+r starts the next") {
		t.Errorf("the stranded queue's lines = %q, want them to say alt+r starts it", got)
	}
	if cmd := v.resumeQueueCmd(); cmd == nil {
		t.Errorf("alt+r on an idle chat's unpaused queue was refused: %q", v.note)
	}
	// A live chat's unpaused queue drains by itself: nothing to resume.
	v.chat.State = "running"
	if cmd := v.resumeQueueCmd(); cmd != nil {
		t.Error("alt+r on a running chat's unpaused queue sent a resume")
	}
	if got := ansi.Strip(strings.Join(v.queueLines(80), "\n")); strings.Contains(got, "alt+r") {
		t.Errorf("a running chat's queue offers alt+r: %q", got)
	}
}
