package tui

import (
	"cmp"
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// attentionTarget is one thing `!` can land on (task 162): a root task in a
// taskstate.NeedsHuman state, or a chat in `awaiting_input`. Exactly one of
// the two pointers is set. It is a sum type rather than two lists because the
// jump walks both kinds in one order — whichever started waiting first — and
// a chat waiting longer than a task is visited before it.
//
// Only a chat that is *waiting* is a target. A chat whose turn finished while
// nobody looked is unread, not waiting (task 162, the narrowest reading of
// superseding task 067 decision 4): it asks nothing, so it never joins the
// count or the jump.
type attentionTarget struct {
	task *apiclient.Task
	chat *apiclient.Chat
}

func taskTarget(t apiclient.Task) attentionTarget { return attentionTarget{task: &t} }

func chatTarget(c apiclient.Chat) attentionTarget { return attentionTarget{chat: &c} }

// projectID is the project the target belongs to, which `!` switches to
// before it opens the target.
func (a attentionTarget) projectID() int64 {
	if a.chat != nil {
		return a.chat.ProjectID
	}
	return a.task.ProjectID
}

// id is the task's or the chat's own id; isChat says which space it is in.
func (a attentionTarget) id() int64 {
	if a.chat != nil {
		return a.chat.ID
	}
	return a.task.ID
}

func (a attentionTarget) isChat() bool { return a.chat != nil }

// same reports whether two targets name the same object: the kind matters,
// because task and chat ids are separate sequences.
func (a attentionTarget) same(o attentionTarget) bool {
	return a.isChat() == o.isChat() && a.id() == o.id()
}

// since is when the target started waiting, as near as a listing says it.
// A chat's `updated_at` is bumped by store.SetChatState, the write that puts
// it in awaiting_input, and a waiting chat takes no other write until it is
// answered; a task's is bumped by the state write that made it need a human.
// Neither listing carries a separate "entered state at", and adding one for
// an ordering would restate what `updated_at` already says.
func (a attentionTarget) since() time.Time {
	if a.chat != nil {
		return a.chat.UpdatedAt
	}
	return a.task.UpdatedAt
}

// waitingChats keeps the chats that are waiting on a human.
func waitingChats(chats []apiclient.Chat) []apiclient.Chat {
	var out []apiclient.Chat
	for _, c := range chats {
		if chatNeedsAttention(c.State) {
			out = append(out, c)
		}
	}
	return out
}

// chatsInProject keeps the chats of one project; project 0 keeps none, the
// way tasksInProject does.
func chatsInProject(chats []apiclient.Chat, project int64) []apiclient.Chat {
	if project == 0 {
		return nil
	}
	var out []apiclient.Chat
	for _, c := range chats {
		if c.ProjectID == project {
			out = append(out, c)
		}
	}
	return out
}

// mergeTargets is tasks (already in board order) and chats as one list,
// oldest wait first (task 162). The sort is stable, so targets that started
// waiting at the same instant — or whose times a test left zero — keep board
// order, tasks ahead of chats.
func mergeTargets(tasks []apiclient.Task, chats []apiclient.Chat) []attentionTarget {
	out := make([]attentionTarget, 0, len(tasks)+len(chats))
	for _, t := range tasks {
		out = append(out, taskTarget(t))
	}
	for _, c := range chats {
		out = append(out, chatTarget(c))
	}
	slices.SortStableFunc(out, func(a, b attentionTarget) int {
		return cmp.Compare(a.since().UnixNano(), b.since().UnixNano())
	})
	return out
}

// nextTarget is the target after cur in targets. wrapped reports that cur was
// the last one, so next is the first again — the point at which `!` crosses
// into another project instead, when one has a target (task 132 decision
// 52). A cur that is not in the list, or none at all, starts from the first.
// ok is false when targets is empty, which also reports wrapped: an empty
// selection is exhausted.
func nextTarget(targets []attentionTarget, cur *attentionTarget) (next attentionTarget, wrapped, ok bool) {
	if len(targets) == 0 {
		return attentionTarget{}, true, false
	}
	if cur != nil {
		for i, t := range targets {
			if !t.same(*cur) {
				continue
			}
			if i+1 < len(targets) {
				return targets[i+1], false, true
			}
			return targets[0], true, true
		}
	}
	return targets[0], false, true
}

type (
	// boardChatsMsg answers the live board's chat listing (task 162).
	boardChatsMsg struct {
		seq   int
		chats []apiclient.Chat
		err   error
	}
	// boardChatsRefreshMsg fires when the chat listing's debounce closes.
	boardChatsRefreshMsg struct{}
)

// chatsCmd fetches every project's live chats (task 162): no project, and
// the default archived scope, which hides every terminal chat. Nil on the
// archived board, which counts nothing, and before a client exists.
func (b *board) chatsCmd() tea.Cmd {
	client := b.client
	if client == nil || b.archived {
		return nil
	}
	b.chatsSeq++
	seq := b.chatsSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		chats, err := client.ListChats(ctx, apiclient.ListChatsOptions{})
		return boardChatsMsg{seq: seq, chats: chats, err: err}
	}
}

// applyChats takes a chat listing unless a newer one already landed. A
// failed fetch keeps the listing on screen: it is not news about which chats
// wait, and the next chat event fetches again.
func (b *board) applyChats(msg boardChatsMsg) {
	if msg.err != nil || msg.seq <= b.chatsApplied {
		return
	}
	b.chatsApplied = msg.seq
	b.chats = msg.chats
}

// scheduleChatsRefresh debounces the chat listing the way scheduleRefresh
// debounces the task listing: a turn's burst of events is one refetch.
func (b *board) scheduleChatsRefresh() tea.Cmd {
	if b.chatsPending {
		return nil
	}
	b.chatsPending = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return boardChatsRefreshMsg{} })
}

// refetchesChats reports whether a chat event can move a chat into or out of
// the listing's waiting set: one appearing, changing state, or leaving the
// live listing by archive, close or handoff. Turn events are not among them —
// a turn ending is followed by its own state change.
func refetchesChats(t string) bool {
	switch t {
	case "chat.created", "chat.state_changed", "chat.archived", "chat.closed", "chat.handed_off":
		return true
	}
	return false
}
