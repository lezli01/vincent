package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Unread marks (task 156). A chat is unread when a turn finished after anyone
// last looked at it; the daemon derives the flag from last_viewed_at, so
// every client agrees, and a chat.viewed event tells the others when one of
// them cleared it.
//
// This client records a view at two moments only: when a chat is opened, and
// when a turn finishes while the chat is open in a terminal that has focus.
// A turn that finishes in an unfocused terminal leaves the chat unread —
// nobody saw it — and regaining focus does not mark it either: the mark is
// cleared by the next open or the next turn seen to finish, never by a
// window coming to the front with the reader's attention somewhere else.

// markViewedCmd is POST /v1/chats/{id}/viewed. The answer is dropped: the
// daemon's mark is monotonic and its chat.viewed event is what moves every
// board, this one's included, so there is nothing for the result to change
// here, and a failure costs only a mark that stays lit.
func (v *chatView) markViewedCmd() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		_, _ = client.MarkChatViewed(ctx, id)
		return nil
	}
}

// turnFinished reports whether a turn ended between two loads of one chat: a
// turn that was running before and is not now, or one that started and
// ended entirely between them.
func turnFinished(before, after []apiclient.ChatTurn) bool {
	was := make(map[int64]string, len(before))
	for _, t := range before {
		was[t.ID] = t.State
	}
	for _, t := range after {
		if t.State == "running" {
			continue
		}
		if prev, seen := was[t.ID]; !seen || prev == "running" {
			return true
		}
	}
	return false
}
