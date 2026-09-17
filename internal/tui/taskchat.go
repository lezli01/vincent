package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// A chat on a task (task 115, §6, §15): `T` from anywhere a task's actions
// are, the chat workspace for the conversation, and the task's own list of
// the chats it has had.
//
// Nothing here decides whether a task can be talked about. The daemon offers
// `chat` or it does not, and names the chat holding the lock in
// `open_chat_id`; the key reads those two and nothing else.

// taskChatKey is `T` (see its registry row for why that letter), and
// chatCloseKey is the linked chat's close in the chat workspace.
const (
	taskChatKey  = "T"
	chatCloseKey = "ctrl+q"
)

// Linked-chat event types the TUI reacts to. They are the two that move a
// task's lock; every other chat event leaves `open_chat_id` where it was.
const (
	eventChatCreated = "chat.created"
	eventChatClosed  = "chat.closed"
)

// taskChatOpenedMsg reports POST /v1/tasks/{id}/chat. It goes through the
// root, which opens the workspace on success, and is then broadcast so the
// bar that said "opening a chat…" can say what happened.
type taskChatOpenedMsg struct {
	taskID int64
	chatID int64
	err    error
}

// taskChatCmd is `T` on a task target. With a chat already open it is a
// navigation and asks the daemon nothing; otherwise it opens one. A 409 naming
// the lock — the row was stale, or another client opened a chat first — is
// the same answer the stale row would have given, so it opens that chat
// rather than reporting a refusal the human cannot act on. nil means the key
// does nothing for this target.
func taskChatCmd(client *apiclient.Client, t taskActions, bar *actionBar) tea.Cmd {
	if !t.offersChat() {
		return nil
	}
	if t.openChatID != 0 {
		id := t.openChatID
		return func() tea.Msg { return openChatMsg{id: id} }
	}
	if client == nil {
		bar.setStatus("not connected", true)
		return nil
	}
	bar.setStatus("opening a chat…", false)
	id := t.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		chat, err := client.OpenTaskChat(ctx, id, apiclient.OpenTaskChatRequest{})
		if err != nil {
			if chatID, locked := apiclient.TaskLockedByChat(err); locked {
				return taskChatOpenedMsg{taskID: id, chatID: chatID}
			}
			return taskChatOpenedMsg{taskID: id, err: err}
		}
		return taskChatOpenedMsg{taskID: id, chatID: chat.ID}
	}
}

// applyChat settles the bar after taskChatCmd: nothing to say when the
// workspace opened, and the daemon's refusal when it did not — a task with no
// worktree is the one a human will actually meet.
func (a *actionBar) applyChat(msg taskChatOpenedMsg) {
	if msg.err == nil {
		a.status, a.statusBad = "", false
		return
	}
	a.setStatus("chat: "+actionErrString(msg.err), true)
}

// lockEventTask reads the task a linked chat's opening or closing names. The
// id rides in the payload rather than in the envelope's task_id, which stays
// the task *events'* own (§13.3, task 115).
func lockEventTask(ev apiclient.Event) (int64, bool) {
	if ev.Type != eventChatCreated && ev.Type != eventChatClosed {
		return 0, false
	}
	return linkedTaskOf(ev)
}

// linkedTaskOf is the payload's linked_task_id on any chat event.
func linkedTaskOf(ev apiclient.Event) (int64, bool) {
	if !strings.HasPrefix(ev.Type, "chat.") || len(ev.Payload) == 0 {
		return 0, false
	}
	var body struct {
		LinkedTaskID *int64 `json:"linked_task_id"`
	}
	if json.Unmarshal(ev.Payload, &body) != nil || body.LinkedTaskID == nil {
		return 0, false
	}
	return *body.LinkedTaskID, true
}

func derefID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// taskChatsMsg carries GET /v1/chats?task_id=N&archived=all.
type taskChatsMsg struct {
	taskID int64
	chats  []apiclient.Chat
	err    error
}

// chatsCmd lists every chat this task has had, closed ones included: they are
// the task's history (task 115), and the daemon hides terminal chats unless
// asked. Refetched on open, on re-entering the workspace, and on any event of
// a chat linked to this task.
func (t *taskView) chatsCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		chats, err := client.ListChats(ctx, apiclient.ListChatsOptions{
			TaskID: id, Archived: apiclient.ArchivedAll,
		})
		return taskChatsMsg{taskID: id, chats: chats, err: err}
	}
}

func (t *taskView) applyChats(msg taskChatsMsg) {
	if msg.taskID != t.detail.taskID {
		return
	}
	if msg.err != nil {
		t.chatsErr = errString(msg.err)
		return
	}
	t.chats, t.chatsErr = msg.chats, ""
}

// chatNoteCmd refetches the list for an event about one of this task's chats.
func (t *taskView) chatNoteCmd(ev apiclient.Event) tea.Cmd {
	if id, ok := linkedTaskOf(ev); ok && id == t.detail.taskID {
		return t.chatsCmd()
	}
	return nil
}

// chatsSectionLines renders the Task Details tab's "Chats" section: one row
// per chat, newest first, in the chats board's own state vocabulary. The
// section exists only once there is a chat to list (detailLines).
func (t *taskView) chatsSectionLines() []string {
	if t.chatsErr != "" {
		return []string{styleBad.Render("  could not list this task's chats: " + t.chatsErr)}
	}
	// Newest first: at most one of them is open, and it is always the newest.
	chats := append([]apiclient.Chat(nil), t.chats...)
	sort.Slice(chats, func(i, j int) bool { return chats[i].ID > chats[j].ID })
	out := make([]string, 0, len(chats)+2)
	for _, c := range chats {
		state := chatStateLabel(c.State)
		row := fmt.Sprintf("  %-5s %-15s %-8s %11s  %s",
			"#"+strconv.FormatInt(c.ID, 10),
			applyStateStyle(c.State, state), c.Agent,
			c.CreatedAt.Local().Format("01-02 15:04"), c.Title)
		out = append(out, row)
	}
	if open := t.detail.task.OpenChatID; open != nil {
		out = append(out, "", styleDim.Render(fmt.Sprintf(
			"  chat #%d holds this task — %s reopens it; closing it there unlocks the task", *open, taskChatKey)))
	}
	return out
}

// linkedChatDecline is the refusal archive and hand-off get on a chat opened
// on a task, in the daemon's own terms (chat_linked_to_task): the worktree is
// the task's, and closing is the way this chat ends.
func linkedChatDecline(c apiclient.Chat) string {
	if chatstate.Terminal(chatstate.State(c.State)) {
		return fmt.Sprintf("this chat was opened on task #%d and is %s", derefID(c.LinkedTaskID), c.State)
	}
	return fmt.Sprintf("this chat works in task #%d's worktree, which that task owns — "+
		"close it from the chat (%s) instead", derefID(c.LinkedTaskID), chatCloseKey)
}
