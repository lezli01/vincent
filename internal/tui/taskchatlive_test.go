package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/store"
)

// finishWithWorktree leaves a parked task `done` with a worktree on record,
// which is what a chat on it needs: the daemon never makes one for a chat
// (task 119). The path only has to be recorded — nothing in these tests runs
// a turn in it.
func (h *actionLiveHarness) finishWithWorktree(t *testing.T, id int64) {
	t.Helper()
	ctx := context.Background()
	path := t.TempDir()
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskQueued, store.TaskRunning,
		store.TaskChange{WorktreePath: &path}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskRunning, store.TaskDone,
		store.TaskChange{}); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// linkedChats is every chat the daemon holds for a task, closed ones included.
func (h *actionLiveHarness) linkedChats(t *testing.T, id int64) []store.Chat {
	t.Helper()
	chats, err := h.st.ListChats(context.Background(), store.ChatFilter{TaskID: &id, Archived: store.ArchivedAll})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	return chats
}

// paletteOffersKey opens the palette on the active surface and reports
// whether it lists a row for key — the one place every task-action key is
// offered, whatever the footer had room for.
func paletteOffersKey(m *root, key string) bool {
	m.openPalette()
	defer func() { m.palette = nil }()
	for _, e := range m.palette.entries {
		if e.key == key {
			return true
		}
	}
	return false
}

// pressChat presses `T` in the task workspace and waits for the chat
// workspace to be showing a chat opened on that task, returning its id.
func (h *actionLiveHarness) pressChat(t *testing.T, taskID int64) int64 {
	t.Helper()
	h.press(t, opKey(keymap.Chat))
	chat := h.m.views[viewChat].(*chatView)
	// The workspace's per-chat stream is not under the root's context, and
	// an open SSE connection holds httptest's Close. Cleanups run
	// last-registered first, so this one runs before the server's.
	t.Cleanup(func() {
		if chat.streamStop != nil {
			chat.streamStop()
		}
	})
	h.p.until(30*time.Second, "T to open the chat workspace", func() bool {
		return h.m.active == viewChat && chat.chat != nil &&
			chat.chat.LinkedTaskID != nil && *chat.chat.LinkedTaskID == taskID
	})
	return chat.chatID
}

// backToTask re-enters the task workspace and waits for the task's lock to
// read as want: the open chat's id, or 0 for none.
func (h *actionLiveHarness) backToTask(t *testing.T, want int64) {
	t.Helper()
	_, cmd := h.m.Update(selectViewMsg{id: viewTask})
	h.p.push(cmd)
	h.p.until(30*time.Second, fmt.Sprintf("the task to read open_chat_id=%d", want), func() bool {
		target := detailOf(h.m).target()
		return h.m.active == viewTask && target.openChatID == want &&
			target.has(apiclient.ActionChat) == (want == 0)
	})
}

// TestTaskChatKeyOpensAndReopensLive is task 119's key against the real
// handlers: `T` is offered exactly when the daemon offers `chat`, opens the
// chat workspace on a chat linked to the task, and — once the task carries
// `open_chat_id` — reopens that chat rather than asking for a second. The
// chats board then names the task on the chat's row.
func TestTaskChatKeyOpensAndReopensLive(t *testing.T) {
	h := newActionLiveHarness(t)
	task := h.createParkedTask(t, "talkable")

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID, projectID: task.ProjectID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the task workspace to load", func() bool {
		return detailOf(h.m).taskID == task.ID && detailOf(h.m).loaded
	})
	if paletteOffersKey(h.m, opKey(keymap.Chat)) {
		t.Fatal("a queued task offers T")
	}

	h.finishWithWorktree(t, task.ID)
	h.p.until(30*time.Second, "the daemon to offer chat", func() bool {
		return detailOf(h.m).target().has(apiclient.ActionChat)
	})
	if !paletteOffersKey(h.m, opKey(keymap.Chat)) {
		t.Fatal("a done task does not offer T")
	}

	first := h.pressChat(t, task.ID)
	if chats := h.linkedChats(t, task.ID); len(chats) != 1 || chats[0].ID != first {
		t.Fatalf("the daemon holds %+v, want the one chat T opened (#%d)", chats, first)
	}

	// Locked: the daemon offers no `chat`, and the key is still on offer
	// because it now means "reopen".
	h.backToTask(t, first)
	if !paletteOffersKey(h.m, opKey(keymap.Chat)) {
		t.Fatal("a task holding an open chat does not offer T to reopen it")
	}
	if again := h.pressChat(t, task.ID); again != first {
		t.Fatalf("T opened chat #%d, want the open chat #%d", again, first)
	}
	if chats := h.linkedChats(t, task.ID); len(chats) != 1 {
		t.Fatalf("reopening created a chat: the daemon holds %d", len(chats))
	}

	// The chats board names the task, and archive declines on the row.
	_, cmd = h.m.Update(selectViewMsg{id: viewChats})
	h.p.push(cmd)
	marker := fmt.Sprintf("▶ #%d", task.ID)
	h.p.until(30*time.Second, "the chats board to name the task", func() bool {
		return strings.Contains(content(h.m), marker)
	})
	board := h.m.views[viewChats].(*chatsView)
	h.press(t, "A")
	if board.confirm != nil || !strings.Contains(board.note, "which that task owns") {
		t.Fatalf("A on a linked chat asked %v / noted %q, want the linked refusal", board.confirm, board.note)
	}
}

// TestTaskWorkspaceListsLinkedChatsLive: a task's workspace lists every chat
// opened on it, the closed one included — they are its history (task 119).
func TestTaskWorkspaceListsLinkedChatsLive(t *testing.T) {
	h := newActionLiveHarness(t)
	task := h.createParkedTask(t, "talked-about")
	h.finishWithWorktree(t, task.ID)

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID, projectID: task.ProjectID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the daemon to offer chat", func() bool {
		return detailOf(h.m).taskID == task.ID && detailOf(h.m).target().has(apiclient.ActionChat)
	})

	first := h.pressChat(t, task.ID)
	// This harness wires no chat runner, so the close is the store's —
	// the same transaction the daemon's route ends in.
	if _, err := h.st.CloseChat(context.Background(), first); err != nil {
		t.Fatalf("CloseChat: %v", err)
	}
	h.backToTask(t, 0)
	second := h.pressChat(t, task.ID)
	if second == first {
		t.Fatalf("T after a close reopened #%d, want a new chat", first)
	}
	h.backToTask(t, second)

	view := h.m.views[viewTask].(*taskView)
	h.p.until(30*time.Second, "the Chats section to list both chats", func() bool {
		doc := splitTaskDetailDocument(view.detailLines(160))
		for _, s := range doc.sections {
			if s.title != "Chats" {
				continue
			}
			lines := strings.Join(s.lines, "\n")
			return strings.Contains(lines, fmt.Sprintf("#%d ", first)) &&
				strings.Contains(lines, "closed") &&
				strings.Contains(lines, fmt.Sprintf("#%d ", second))
		}
		return false
	})
}

// TestChatWithSendsTheChosenAgentLive is task 161 decision 7 against the real
// handlers: `chat_with` opens the form, and what it sends is the agent, model
// and effort chosen there, with the first message as turn 1's prompt. Once
// the task holds the chat, the key is a navigation like `T`. Then `r` on the
// chats board renames the chat and leaves its branch alone.
func TestChatWithSendsTheChosenAgentLive(t *testing.T) {
	h := newActionLiveHarness(t)
	task := h.createParkedTask(t, "talk with a choice")
	_, cmd := h.m.Update(selectTaskMsg{id: task.ID, projectID: task.ProjectID})
	h.p.push(cmd)
	h.finishWithWorktree(t, task.ID)
	h.p.until(30*time.Second, "the daemon to offer chat", func() bool {
		d := detailOf(h.m)
		return d.taskID == task.ID && d.loaded && d.target().has(apiclient.ActionChat)
	})
	if !paletteOffersKey(h.m, opKey(keymap.ChatWith)) {
		t.Fatal("a done task does not offer chat_with")
	}

	_, cmd = h.m.Update(registryKey(t, "alt+t"))
	h.p.push(cmd)
	h.p.until(30*time.Second, "the chat-with form to load its agents", func() bool {
		return h.m.chatWith != nil && h.m.chatWith.loaded
	})
	f := h.m.chatWith
	a, ok := f.agent()
	if !ok {
		t.Fatalf("the form offers no agent (err %q)", f.err)
	}
	if len(a.Models) > 0 {
		f.model = a.Models[0].Value
	}
	if len(a.Efforts) > 0 {
		f.effort = a.Efforts[0].Value
	}
	f.message.SetValue("why did it stop?")
	_, cmd = h.m.Update(registryKey(t, "ctrl+s"))
	if h.m.chatWith != nil {
		t.Fatal("ctrl+s left the form open")
	}
	h.p.push(cmd)
	chat := h.m.views[viewChat].(*chatView)
	t.Cleanup(func() {
		if chat.streamStop != nil {
			chat.streamStop()
		}
	})
	h.p.until(30*time.Second, "the chat workspace on the task's chat", func() bool {
		return h.m.active == viewChat && chat.chat != nil &&
			chat.chat.LinkedTaskID != nil && *chat.chat.LinkedTaskID == task.ID
	})
	chats := h.linkedChats(t, task.ID)
	if len(chats) != 1 {
		t.Fatalf("the daemon holds %d chats, want one", len(chats))
	}
	got := chats[0]
	if got.Agent != a.Name || got.Model != f.model || got.Effort != f.effort {
		t.Fatalf("chat runs %s/%s/%s, want the chosen %s/%s/%s", got.Agent, got.Model, got.Effort, a.Name, f.model, f.effort)
	}
	// This harness wires no chat runner, so the daemon keeps the chat and
	// refuses the send: the message must come back to the composer.
	turns, err := h.st.ListChatTurns(context.Background(), got.ID)
	if err != nil || len(turns) != 0 {
		t.Fatalf("turns = %+v, %v; want none without a runner", turns, err)
	}
	if chat.composer.Value() != "why did it stop?" || !chat.noteBad {
		t.Fatalf("composer %q (note %q), want the unsent first message back", chat.composer.Value(), chat.note)
	}

	// Locked now: chat_with navigates rather than opening the form.
	h.backToTask(t, got.ID)
	_, cmd = h.m.Update(registryKey(t, "alt+t"))
	if h.m.chatWith != nil {
		t.Fatal("chat_with opened the form on a task that already holds a chat")
	}
	if msg, ok := drain(cmd).(openChatMsg); !ok || msg.id != got.ID {
		t.Fatalf("chat_with produced %#v, want the open chat #%d", drain(cmd), got.ID)
	}

	// Rename from the chats board.
	_, cmd = h.m.Update(selectViewMsg{id: viewChats})
	h.p.push(cmd)
	board := h.m.views[viewChats].(*chatsView)
	h.p.until(30*time.Second, "the chats board to list the chat", func() bool {
		c, ok := board.current()
		return ok && c.ID == got.ID
	})
	h.press(t, "r")
	h.p.until(10*time.Second, "the rename prompt", func() bool { return h.m.rename != nil })
	if v := h.m.rename.input.Value(); v != got.Title {
		t.Fatalf("the prompt holds %q, want the current title %q", v, got.Title)
	}
	h.m.rename.input.SetValue("a better name")
	_, cmd = h.m.Update(registryKey(t, "enter"))
	h.p.push(cmd)
	h.p.until(30*time.Second, "the rename to land", func() bool {
		c, err := h.st.GetChat(context.Background(), got.ID)
		return err == nil && c.Title == "a better name" && h.m.rename == nil
	})
	after, err := h.st.GetChat(context.Background(), got.ID)
	if err != nil || after.Branch != got.Branch {
		t.Fatalf("branch %q after rename, want %q untouched (%v)", after.Branch, got.Branch, err)
	}
}

// TestChatWorkspacePaletteRenames: the conversation has no key for rename —
// its composer owns every printable one — so the palette carries the row.
func TestChatWorkspacePaletteRenames(t *testing.T) {
	v := &chatView{chat: &apiclient.Chat{ID: 4, Title: "old"}}
	rows := v.paletteExtras()
	if len(rows) != 1 || !strings.Contains(rows[0].label, "rename chat") {
		t.Fatalf("palette extras = %+v, want the rename row", rows)
	}
	if msg, ok := rows[0].action().(openChatRenameMsg); !ok || msg.id != 4 || msg.title != "old" {
		t.Fatalf("the row produced %#v, want the rename prompt for chat 4", rows[0].action())
	}
}
