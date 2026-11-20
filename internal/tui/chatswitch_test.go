package tui

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 165's chat switch. The probes join panelKeyProbes here rather than in
// its literal so the eleven keys on each surface are written once.

// switchChats is a project's chats in an order the board does not draw them
// in: idle, running and waiting mixed, with one ended chat the switch must
// skip. Board order is 3, 5 (waiting, newest first), 2 (running), 1, 4 (idle,
// newest first).
func switchChats() []apiclient.Chat {
	mk := func(id int64, state string, minute int) apiclient.Chat {
		c := testChat(id, state, "chat "+strconv.FormatInt(id, 10))
		c.UpdatedAt = time.Date(2026, 8, 31, 11, minute, 0, 0, time.UTC)
		return c
	}
	return []apiclient.Chat{
		mk(1, "idle", 30), mk(2, "running", 10), mk(3, "awaiting_input", 40),
		mk(4, "idle", 20), mk(5, "awaiting_input", 5), mk(6, "archived", 50),
	}
}

var switchOrder = []int64{3, 5, 2, 1, 4}

// switchBoard is the live chats board holding switchChats for project 7,
// listing ended chats too so the switch has one to skip.
func switchBoard() *chatsView {
	v := newChatsView()
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	v.project = projectSel{id: 7, name: "repo"}
	v.scope = apiclient.ArchivedAll
	v.applyLoaded(chatsLoadedMsg{chats: switchChats()})
	return v
}

// switchChatView is chat id open, with switchBoard beside it.
func switchChatView(id int64) *chatView {
	v := chatViewFixture()
	v.chatID = id
	v.chat = &apiclient.Chat{ID: id, ProjectID: 7, State: "idle"}
	v.side = switchBoard()
	v.back = viewIssues
	return v
}

// pressSwitch presses key on p and returns the chat the press opens.
func pressSwitch(t *testing.T, p panel, key string) (openChatMsg, bool) {
	t.Helper()
	var cmd tea.Cmd
	switch v := p.(type) {
	case *chatsView:
		_, cmd = v.updateKey(registryKey(t, key))
	case *chatView:
		_, cmd = v.updateKey(registryKey(t, key))
	default:
		t.Fatalf("pressSwitch on %T", p)
	}
	if cmd == nil {
		return openChatMsg{}, false
	}
	msg, ok := drain(cmd).(openChatMsg)
	if !ok {
		t.Fatalf("%s produced %T, want openChatMsg", key, drain(cmd))
	}
	return msg, true
}

func init() {
	board, chat := panelKeyProbes[ctxChats], panelKeyProbes[ctxChat]
	expect := func(p func() panel, key string, want int64) func(*testing.T) {
		return func(t *testing.T) {
			got, ok := pressSwitch(t, p(), key)
			if !ok || got.id != want {
				t.Fatalf("%s opened %d (ok %v), want chat %d", key, got.id, ok, want)
			}
		}
	}
	// From the board the cursor's chat (the first, 3) is where a step
	// starts; from a chat, the chat itself (2, the third).
	onBoard := func() panel { return switchBoard() }
	inChat := func() panel { return switchChatView(2) }
	board["alt+n"] = expect(onBoard, "alt+n", 5)
	board["alt+shift+n"] = expect(onBoard, "alt+shift+n", 4)
	board["]"] = expect(onBoard, "]", 5)
	chat["alt+n"] = expect(inChat, "alt+n", 1)
	chat["alt+shift+n"] = expect(inChat, "alt+shift+n", 5)
	for i := 1; i <= 9; i++ {
		key := fmt.Sprintf("alt+%d", i)
		if i <= len(switchOrder) {
			board[key] = expect(onBoard, key, switchOrder[i-1])
			// From a chat other than the target, so the press is a switch.
			from := func() panel { return switchChatView(switchOrder[i%len(switchOrder)]) }
			chat[key] = expect(from, key, switchOrder[i-1])
			continue
		}
		past := func(p func() panel) func(*testing.T) {
			return func(t *testing.T) {
				if got, ok := pressSwitch(t, p(), key); ok {
					t.Fatalf("%s is past the last of %d chats and opened %d", key, len(switchOrder), got.id)
				}
			}
		}
		board[key], chat[key] = past(onBoard), past(inChat)
	}
}

// TestChatSwitchOrderIsTheBoards: waiting, running, idle, newest first in a
// band, ended chats left out — and the board's own slice left alone.
func TestChatSwitchOrderIsTheBoards(t *testing.T) {
	chats := switchChats()
	order := chatSwitchOrder(chats)
	got := make([]int64, 0, len(order))
	for _, c := range order {
		got = append(got, c.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint(switchOrder) {
		t.Fatalf("order %v, want %v", got, switchOrder)
	}
	if chats[0].ID != 1 {
		t.Error("chatSwitchOrder sorted the caller's slice")
	}
	// The board draws the same order.
	b := switchBoard()
	var drawn []int64
	for _, r := range b.rows() {
		if r.chat.State != "archived" {
			drawn = append(drawn, r.chat.ID)
		}
	}
	if fmt.Sprint(drawn) != fmt.Sprint(switchOrder) {
		t.Errorf("the board draws %v, the switch walks %v", drawn, switchOrder)
	}
}

// TestChatSwitchWraps: next from the last chat is the first, previous from
// the first is the last; an ended chat steps into the order at its ends.
func TestChatSwitchWraps(t *testing.T) {
	order := chatSwitchOrder(switchChats())
	cases := []struct {
		name    string
		sw      chatSwitch
		current int64
		want    int64
	}{
		{"next from the last", chatSwitch{step: 1}, 4, 3},
		{"previous from the first", chatSwitch{step: -1}, 3, 4},
		{"next in the middle", chatSwitch{step: 1}, 2, 1},
		{"next from an ended chat", chatSwitch{step: 1}, 6, 3},
		{"previous from an ended chat", chatSwitch{step: -1}, 6, 4},
		{"the fifth", chatSwitch{nth: 5}, 3, 4},
	}
	for _, tc := range cases {
		got, ok := tc.sw.target(order, tc.current)
		if !ok || got.ID != tc.want {
			t.Errorf("%s: %d (ok %v), want %d", tc.name, got.ID, ok, tc.want)
		}
	}
	if _, ok := (chatSwitch{nth: 6}).target(order, 3); ok {
		t.Error("the sixth of five chats is a target")
	}
	if _, ok := (chatSwitch{step: 1}).target(nil, 3); ok {
		t.Error("a project with no live chats has a target")
	}
	// In a chat, wrapping keeps the opener: esc still leads where it led.
	v := switchChatView(4)
	got, ok := pressSwitch(t, v, "alt+n")
	if !ok || got.id != 3 || got.back != viewIssues || got.projectID != 7 {
		t.Errorf("alt+n from the last chat: %+v (ok %v), want chat 3, back to issues, project 7", got, ok)
	}
	// The one chat already open is not a switch.
	if _, ok := pressSwitch(t, switchChatView(3), "alt+1"); ok {
		t.Error("alt+1 on the first chat re-opened it")
	}
}

// TestChatSwitchFromAPendingQuestion: the popup owns the keyboard, but not
// the switch. The question stays the chat's and is back when the chat is.
func TestChatSwitchFromAPendingQuestion(t *testing.T) {
	v := switchChatView(3)
	v.chat = &apiclient.Chat{ID: 3, ProjectID: 7, State: "awaiting_input"}
	v.form = newAnswerForm(apiclient.InputRequest{Kind: "question", Questions: []apiclient.InputQuestion{{Text: "which branch?", Options: []string{"a", "b"}}}})
	got, ok := pressSwitch(t, v, "alt+n")
	if !ok || got.id != 5 {
		t.Fatalf("alt+n under a pending question opened %d (ok %v), want chat 5", got.id, ok)
	}
	if v.form == nil {
		t.Error("the switch put the question away before leaving the chat")
	}
	v.open(got.id)
	if v.form != nil {
		t.Error("the question followed the reader into another chat")
	}
}

// A draft surviving a switch away and back is task 164's: the switch opens
// the chat it lands on through chatView.open, whose leaveDraft saves the chat
// being left to the daemon, and TestChatDraftSurvivesSwitchAndRelaunch holds
// that path. The in-view draft map the switch first carried was dropped when
// it met task 164 on the merge train.
// TestChatSwitchFetchesAnotherProjectsChats: with no board for the chat's
// project the switch lists the project, and a listing that lands after the
// reader moved on switches nothing.
func TestChatSwitchFetchesAnotherProjectsChats(t *testing.T) {
	v := switchChatView(3)
	v.chat.ProjectID = 9
	v.client = offlineClient()
	_, cmd := v.updateKey(registryKey(t, "alt+n"))
	if cmd == nil {
		t.Fatal("alt+n with no board for the project fetched nothing")
	}
	chats := switchChats()
	for i := range chats {
		chats[i].ProjectID = 9
	}
	open := v.applySwitchList(chatSwitchListMsg{from: 3, sw: chatSwitch{step: 1}, chats: chats})
	if msg, ok := drain(open).(openChatMsg); !ok || msg.id != 5 || msg.projectID != 9 {
		t.Fatalf("the fetched list opened %+v, want chat 5 of project 9", drain(open))
	}
	if cmd := v.applySwitchList(chatSwitchListMsg{from: 1, sw: chatSwitch{step: 1}, chats: chats}); cmd != nil {
		t.Error("a listing for a chat the reader left still switched")
	}
}

// TestChatsBoardBracketsAreTextInAChat: `]` is the board's alias, and in an
// open chat it is typed.
func TestChatsBoardBracketsAreTextInAChat(t *testing.T) {
	v := switchChatView(3)
	if _, cmd := v.updateKey(registryKey(t, "]")); cmd != nil {
		if _, opened := drain(cmd).(openChatMsg); opened {
			t.Fatal("] switched chats from the composer")
		}
	}
	if v.composer.Value() != "]" {
		t.Errorf("the composer holds %q, want the typed ]", v.composer.Value())
	}
	archived := switchBoard()
	archived.archived = true
	if _, ok := pressSwitch(t, archived, "alt+n"); ok {
		t.Error("the archived board switched chats")
	}
}
