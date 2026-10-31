package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/store"
)

// issueChats is every chat the daemon holds for an issue, closed ones
// included.
func (h *actionLiveHarness) issueChats(t *testing.T, issueID int64) []store.Chat {
	t.Helper()
	chats, err := h.st.ListChats(context.Background(), store.ChatFilter{Archived: store.ArchivedAll})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	var out []store.Chat
	for _, c := range chats {
		if c.IssueID != nil && *c.IssueID == issueID {
			out = append(out, c)
		}
	}
	return out
}

// pressIssueChat presses `T` on the active issue screen and waits for the
// chat workspace to be showing a chat opened on issueID, returning its id.
func (h *actionLiveHarness) pressIssueChat(t *testing.T, issueID int64) int64 {
	t.Helper()
	h.press(t, opKey(keymap.Chat))
	chat := h.m.views[viewChat].(*chatView)
	// pressChat's reason: the workspace's per-chat stream must stop before
	// httptest's Close waits for it.
	t.Cleanup(func() {
		if chat.streamStop != nil {
			chat.streamStop()
		}
	})
	h.p.until(30*time.Second, "T to open a chat on the issue", func() bool {
		return h.m.active == viewChat && chat.chat != nil &&
			chat.chat.IssueID != nil && *chat.chat.IssueID == issueID
	})
	return chat.chatID
}

// TestIssueChatKeyAgainstTheRealAPI is task 140.9 and 140.10 against the
// real handlers: `T` on the issue detail opens a chat on the issue's main
// worktree and lands in its workspace, which names the issue; the detail's
// Main worktree section then names the chat, read from occupant_chat_id over
// the wire, and enter on it — or `T` again — reopens that chat rather than
// asking for a second; the list's row names it too; a chat event carrying
// issue_id re-reads the screens when the chat is closed; and a stale row whose
// `T` meets the daemon's issue_has_live_chat opens the live chat instead of
// reporting a refusal.
func TestIssueChatKeyAgainstTheRealAPI(t *testing.T) {
	h := newActionLiveHarness(t)
	ctx := context.Background()
	iss, err := h.st.CreateIssue(ctx, store.NewIssue{ProjectID: h.projectID, Title: "Talk it through"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	detail := h.m.views[viewIssue].(*issueView)
	_, cmd := h.m.Update(openIssueMsg{id: iss.ID, projectID: h.projectID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the issue detail to load", func() bool {
		return h.m.active == viewIssue && detail.loaded && detail.id == iss.ID
	})
	if !paletteOffersKey(h.m, opKey(keymap.Chat)) {
		t.Fatal("the issue detail does not offer T")
	}

	first := h.pressIssueChat(t, iss.ID)
	if chats := h.issueChats(t, iss.ID); len(chats) != 1 || chats[0].ID != first {
		t.Fatalf("the daemon holds %+v, want the one chat T opened (#%d)", chats, first)
	}
	if got, err := h.st.GetIssue(ctx, iss.ID); err != nil || got.Lane != issuestate.LaneInProgress {
		t.Fatalf("issue after T = %+v, %v; want in_progress", got, err)
	}
	if header := ansi.Strip(h.m.View().Content); !strings.Contains(header, fmt.Sprintf("on issue #%d", iss.ID)) {
		t.Errorf("the chat workspace does not name the issue:\n%s", header)
	}

	_, cmd = h.m.Update(selectViewMsg{id: viewIssue})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail to read the chat occupant", func() bool {
		return h.m.active == viewIssue && issueOccupantChat(detail.issue) == first
	})
	if page := ansi.Strip(h.m.View().Content); !strings.Contains(page, fmt.Sprintf("chat #%d", first)) {
		t.Errorf("the Main worktree section does not name chat #%d:\n%s", first, page)
	}

	// T again is a navigation: the same chat, no second one.
	if again := h.pressIssueChat(t, iss.ID); again != first {
		t.Errorf("T on an issue a chat holds opened chat #%d, want #%d", again, first)
	}
	if chats := h.issueChats(t, iss.ID); len(chats) != 1 {
		t.Errorf("T on an issue a chat holds made a second chat: %+v", chats)
	}

	// enter on the Main worktree section opens the chat too.
	_, cmd = h.m.Update(selectViewMsg{id: viewIssue})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail to come back", func() bool { return h.m.active == viewIssue })
	h.m.View() // the selection acts only on what the last render drew
	detail.cursor = 0
	h.press(t, "enter")
	h.p.until(30*time.Second, "enter on the occupant to open the chat", func() bool {
		chat := h.m.views[viewChat].(*chatView)
		return h.m.active == viewChat && chat.chatID == first
	})

	list := h.m.views[viewIssues].(*issuesView)
	_, cmd = h.m.Update(selectViewMsg{id: viewIssues})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the list row to name the chat", func() bool {
		row, ok := list.current()
		return h.m.active == viewIssues && ok && row.issue.ID == iss.ID && issueOccupantChat(row.issue) == first
	})
	if cell := issueTaskSummary(mustRow(t, list).issue); !strings.Contains(cell, fmt.Sprintf("chat #%d", first)) {
		t.Errorf("list cell = %q, want it to name chat #%d", cell, first)
	}

	// Closing the chat lets go of the worktree; its chat.closed names the
	// issue, which is what re-reads the list without a key press. Every
	// re-read the opening's events scheduled has landed first, so the one
	// that drops the occupant can only be the close's.
	h.p.until(30*time.Second, "the list to settle", func() bool { return !list.refreshWait && !list.loading })
	time.Sleep(2 * refreshDebounce)
	h.p.until(30*time.Second, "the list to settle again", func() bool { return !list.refreshWait && !list.loading })
	if _, err := h.st.CloseChat(ctx, first); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	h.p.until(30*time.Second, "the list to drop the chat occupant", func() bool {
		row, ok := list.current()
		return ok && issueOccupantChat(row.issue) == 0
	})

	// A chat opened by another client, under a row that has not heard of it
	// yet: the daemon's issue_has_live_chat names it, and T opens it.
	issueID := iss.ID
	other := &store.Chat{
		ProjectID: h.projectID, Title: "elsewhere", Agent: "claude", PermissionMode: "full_auto",
		BaseBranch: "main", IssueID: &issueID,
	}
	if err := h.st.CreateIssueChat(ctx, other, func(int64) string { return "unused" }); err != nil {
		t.Fatalf("CreateIssueChat: %v", err)
	}
	for i := range list.issues {
		if list.issues[i].ID == iss.ID && list.issues[i].MainWorktree != nil {
			list.issues[i].MainWorktree.OccupantChatID = nil
		}
	}
	if got := h.pressIssueChat(t, iss.ID); got != other.ID {
		t.Errorf("T on a stale row opened chat #%d, want the live chat #%d", got, other.ID)
	}
	if chats := h.issueChats(t, iss.ID); len(chats) != 2 {
		t.Errorf("the stale row's T made a chat of its own: %+v", chats)
	}
}

func mustRow(t *testing.T, v *issuesView) issueRow {
	t.Helper()
	row, ok := v.current()
	if !ok {
		t.Fatal("no row under the cursor")
	}
	return row
}
