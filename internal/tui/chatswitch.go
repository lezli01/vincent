package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/keymap"
)

// The chat switch (task 162): next_chat, prev_chat and chat_1 … chat_9 move
// between the selected project's live chats without going back to the board.
//
// The order is the board's own — waiting on you, then running, then idle,
// sortChats' bands — so "the third chat" is the third card a reader sees.
// Terminal chats are not in it: an ended chat has no composer to switch to,
// and the board hides them by default. A next or previous wraps around; a
// number past the end does nothing.
//
// The defaults are alt+ keys because they work from the composer, which owns
// every printable key. A macOS terminal sends them only with Option set to
// act as Meta (Terminal.app's "Use Option as Meta key", iTerm2's "Esc+");
// where that is not an option they stay rebindable through tui.keys. The
// chats board also answers `]` and `[`, fixed aliases recorded in
// keymap.fixed: no text field has the keyboard there. In an open chat they
// stay text.

// chatSwitch is one press: a step through the order, or a position in it.
type chatSwitch struct {
	// step is +1 or -1 for next and previous; zero when nth is set.
	step int
	// nth is the 1-based position chat_N asks for.
	nth int
}

// chatSwitchFor reads a press as a chat switch, under the effective keymap.
func chatSwitchFor(key string) (chatSwitch, bool) {
	if key == "" {
		return chatSwitch{}, false
	}
	switch key {
	case opKey(keymap.NextChat):
		return chatSwitch{step: 1}, true
	case opKey(keymap.PrevChat):
		return chatSwitch{step: -1}, true
	}
	for i, op := range keymap.ChatJumps {
		if opKey(op) == key {
			return chatSwitch{nth: i + 1}, true
		}
	}
	return chatSwitch{}, false
}

// chatSwitchOrder is the project's live chats in the board's order. It sorts
// a copy: the caller's slice is the board's, which has its own cursor on it.
func chatSwitchOrder(chats []apiclient.Chat) []apiclient.Chat {
	out := make([]apiclient.Chat, 0, len(chats))
	for _, c := range chats {
		if !chatstate.Terminal(chatstate.State(c.State)) {
			out = append(out, c)
		}
	}
	sortChats(out)
	return out
}

// target is where the press lands from current. A current chat that is not
// in the order — an ended one, or none — makes next the first chat and
// previous the last.
func (s chatSwitch) target(order []apiclient.Chat, current int64) (apiclient.Chat, bool) {
	n := len(order)
	if n == 0 {
		return apiclient.Chat{}, false
	}
	if s.nth > 0 {
		if s.nth > n {
			return apiclient.Chat{}, false
		}
		return order[s.nth-1], true
	}
	at := -1
	for i := range order {
		if order[i].ID == current {
			at = i
			break
		}
	}
	if at < 0 {
		if s.step < 0 {
			return order[n-1], true
		}
		return order[0], true
	}
	return order[((at+s.step)%n+n)%n], true
}

// chatSwitchListMsg is the project's chats, fetched for a switch pressed in
// a chat whose project the chats board is not holding.
type chatSwitchListMsg struct {
	// from is the chat the press was made in: a listing that lands after the
	// human has moved on switches nothing.
	from  int64
	sw    chatSwitch
	chats []apiclient.Chat
	err   error
}

// switchChat answers a chat switch pressed in the open chat. The list comes
// from the live chats board when it is showing this chat's project — it is
// kept current by the event stream already — and from GET /v1/chats
// otherwise.
func (v *chatView) switchChat(sw chatSwitch) tea.Cmd {
	pid := v.project.id
	if v.chat != nil {
		pid = v.chat.ProjectID
	}
	if pid == 0 {
		return nil
	}
	if chats, ok := v.side.liveChatsOf(pid); ok {
		return v.switchWithin(sw, chats)
	}
	client, from := v.client, v.chatID
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		chats, err := client.ListChats(ctx, apiclient.ListChatsOptions{ProjectID: pid})
		return chatSwitchListMsg{from: from, sw: sw, chats: chats, err: err}
	}
}

// applySwitchList finishes a switch whose list had to be fetched.
func (v *chatView) applySwitchList(msg chatSwitchListMsg) tea.Cmd {
	if msg.from != v.chatID {
		return nil
	}
	if msg.err != nil {
		v.note, v.noteBad = "could not list the project's chats: "+errString(msg.err), true
		return nil
	}
	return v.switchWithin(msg.sw, msg.chats)
}

// switchWithin opens the target through openChatMsg, the way every open
// goes: the root points this view at it, and open records the view and
// keeps the draft of the chat being left. esc keeps leading where it led.
func (v *chatView) switchWithin(sw chatSwitch, chats []apiclient.Chat) tea.Cmd {
	t, ok := sw.target(chatSwitchOrder(chats), v.chatID)
	if !ok || t.ID == v.chatID {
		return nil
	}
	open := openChatMsg{id: t.ID, projectID: t.ProjectID, back: v.back}
	return func() tea.Msg { return open }
}

// liveChatsOf is the board's list when it is the live board, loaded, showing
// project pid and listing the live chats. ok is false otherwise, and for a
// nil board.
func (v *chatsView) liveChatsOf(pid int64) ([]apiclient.Chat, bool) {
	if v == nil || v.archived || !v.loaded || v.project.id != pid || v.scope == apiclient.ArchivedOnly {
		return nil, false
	}
	return v.chats, true
}

// switchChat answers a chat switch on the chats board: it moves from the
// chat under the cursor, and opens the target with the cursor on it.
func (v *chatsView) switchChat(sw chatSwitch) tea.Cmd {
	var current int64
	if c, ok := v.current(); ok {
		current = c.ID
	}
	t, ok := sw.target(chatSwitchOrder(v.chats), current)
	if !ok {
		return nil
	}
	v.selectedID = t.ID
	v.restoreSelection()
	open := openChatMsg{id: t.ID, projectID: t.ProjectID, back: v.viewID()}
	return func() tea.Msg { return open }
}
