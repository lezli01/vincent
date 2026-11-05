package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// openChatFrom routes the openChatMsg among msgs into the root, the way the
// runtime would, and nothing else: nothing that reaches a daemon runs.
func openChatFrom(t *testing.T, m *root, msgs []tea.Msg) {
	t.Helper()
	for _, msg := range msgs {
		if open, ok := msg.(openChatMsg); ok {
			m.Update(open)
			return
		}
	}
	t.Fatalf("no openChatMsg among %#v", msgs)
}

// TestChatEscReturnsToItsOpener is issue #932: the chat workspace's esc goes
// back to the view that opened it — the issue detail for its Main worktree
// occupant, the archived-chats board for an archived chat — the way a task
// opened from an issue already returns to the issue (§15, task 130). A chat
// opened from the live chats board still returns there.
func TestChatEscReturnsToItsOpener(t *testing.T) {
	const pid = int64(1)
	chatID := int64(7)

	t.Run("issue detail", func(t *testing.T) {
		m := connectedRoot(t)
		m.sel = projectSel{id: pid, name: "one"}
		m.Update(openIssueMsg{id: 5, projectID: pid, back: viewIssues})
		if m.active != viewIssue {
			t.Fatalf("openIssueMsg landed on %v, want the issue detail", m.active)
		}
		detail := m.views[viewIssue].(*issueView)
		detail.issue = apiclient.Issue{
			ID: 5, ProjectID: pid, Title: "Talk it through", State: "open", Lane: "in_progress",
			MainWorktree: &apiclient.IssueMainWorktree{Branch: "vincent/5-talk", OccupantChatID: &chatID},
		}
		detail.loaded = true
		m.View() // the selection acts only on what the last render drew
		detail.cursor = 0
		cmd := detail.openTask()
		if cmd == nil {
			t.Fatal("enter on the Main worktree occupant opened nothing")
		}
		openChatFrom(t, m, tabFlatten(cmd))
		if m.active != viewChat {
			t.Fatalf("the open landed on %v, want the chat workspace", m.active)
		}
		pressEsc(m)
		if m.active != viewIssue {
			t.Errorf("esc from a chat opened on the issue went to %v, want the issue detail (%v)", m.active, viewIssue)
		}
		if detail.id != 5 {
			t.Errorf("the issue detail is on #%d after esc, want #5", detail.id)
		}
	})

	for _, tc := range []struct {
		name  string
		board viewID
	}{
		{"chats board", viewChats},
		{"archived chats board", viewArchivedChats},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := connectedRoot(t)
			m.sel = projectSel{id: pid, name: "one"}
			m.Update(selectViewMsg{id: tc.board})
			if m.active != tc.board {
				t.Fatalf("selectViewMsg landed on %v, want %v", m.active, tc.board)
			}
			b := m.views[tc.board].(*chatsView)
			b.chats = []apiclient.Chat{{ID: chatID, ProjectID: pid, Title: "a chat", State: "idle"}}
			b.loaded, b.loading = true, false
			b.cursor = 0
			_, cmd := m.Update(enterKey())
			openChatFrom(t, m, tabFlatten(cmd))
			if m.active != viewChat {
				t.Fatalf("enter landed on %v, want the chat workspace", m.active)
			}
			pressEsc(m)
			if m.active != tc.board {
				t.Errorf("esc from a chat opened on the %s went to %v, want %v", tc.name, m.active, tc.board)
			}
		})
	}
}
