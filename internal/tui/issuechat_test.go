package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatKeysOffered reports which of the chat workspace's two endings its
// footer offers for chat.
func chatKeysOffered(chat *apiclient.Chat) (closeKey, handoff bool) {
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.chat = chat
	for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
		switch b.key {
		case chatCloseKey:
			closeKey = true
		case chatHandoffKey:
			handoff = true
		}
	}
	return closeKey, handoff
}

// TestChatWorkspaceEndingsByKind is task 140 decision 3 in the chat
// workspace: a chat opened on an issue can be closed — letting go of the
// main worktree — and handed off, so its footer offers both; a chat on a task
// only closes, and a free one only hands off.
func TestChatWorkspaceEndingsByKind(t *testing.T) {
	task, issue := int64(4), int64(9)
	for _, tc := range []struct {
		name               string
		chat               apiclient.Chat
		wantClose, wantOff bool
	}{
		{"free", apiclient.Chat{ID: 1, State: "idle"}, false, true},
		{"on a task", apiclient.Chat{ID: 2, State: "idle", LinkedTaskID: &task}, true, false},
		{"on an issue", apiclient.Chat{ID: 3, State: "idle", IssueID: &issue}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := tc.chat
			gotClose, gotOff := chatKeysOffered(&chat)
			if gotClose != tc.wantClose || gotOff != tc.wantOff {
				t.Errorf("close %v hand-off %v, want %v %v", gotClose, gotOff, tc.wantClose, tc.wantOff)
			}
		})
	}
}

// TestIssueChatCloseAsksAboutTheWorktree holds the close confirmation on an
// issue chat to what closing it does: the issue's main worktree is freed for
// its next main task, not a task unlocked.
func TestIssueChatCloseAsksAboutTheWorktree(t *testing.T) {
	issue := int64(9)
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.chatID, v.chat = 3, &apiclient.Chat{ID: 3, State: "idle", IssueID: &issue}
	v.askClose()
	if !v.closing {
		t.Fatalf("an issue chat's close was refused: %q", v.note)
	}
	if q := v.closeQuestion(); !strings.Contains(q, "issue #9") || strings.Contains(q, "task #") {
		t.Errorf("close question = %q, want it to name issue #9's main worktree", q)
	}
	closed := *v.chat
	closed.State = "closed"
	v.applyClosed(chatClosedMsg{chatID: 3, chat: &closed})
	if !strings.Contains(v.note, "issue #9") {
		t.Errorf("closed note = %q, want it to name the issue", v.note)
	}
}

// TestIssueRowNamesTheChatOccupant is the list cell of an in-progress issue
// a chat holds (task 140.9): the chat, not a task count.
func TestIssueRowNamesTheChatOccupant(t *testing.T) {
	chat := int64(7)
	iss := apiclient.Issue{
		ID: 1, Lane: "in_progress", TaskCount: 0, SideActive: 1,
		MainWorktree: &apiclient.IssueMainWorktree{Branch: "vincent/1-x", OccupantChatID: &chat},
	}
	if got := issueTaskSummary(iss); got != "● chat #7 · +1 side" {
		t.Errorf("cell = %q, want %q", got, "● chat #7 · +1 side")
	}
}
