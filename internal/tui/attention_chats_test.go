package tui

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// waitingChat is a chat of project in state, which it entered at since.
func waitingChat(id, project int64, state string, since time.Time) apiclient.Chat {
	return apiclient.Chat{ID: id, ProjectID: project, State: state, Title: "chat", CreatedAt: since, UpdatedAt: since}
}

// TestAttentionTallyCountsWaitingChats: a chat in awaiting_input counts in
// whichever project it is (task 165, decision row 37), and only then — an
// idle chat, an unread finished turn included, asks nothing.
func TestAttentionTallyCountsWaitingChats(t *testing.T) {
	m := attentionRoot(t,
		task(1, stateAwaitingInput),
		task(2, stateBlocked, inProjectID(otherProjectID, "other")),
	)
	b := m.views[viewHome].(*shell).board
	b.chats = []apiclient.Chat{
		waitingChat(10, testProjectID, "awaiting_input", testNow),
		waitingChat(11, testProjectID, "idle", testNow),
		waitingChat(12, otherProjectID, "awaiting_input", testNow),
		waitingChat(13, otherProjectID, "running", testNow),
	}
	if got := b.attentionTally(); got.here != 2 || got.elsewhere != 2 {
		t.Fatalf("tally = %+v, want 2 here (a task and a chat) and 2 elsewhere", got)
	}
}

// TestBoardRefetchesChatsOnChatEvents: the chat events that can move a chat
// into or out of awaiting_input schedule one debounced refetch of the
// cross-project listing; a turn event does not, nor does the archived board.
func TestBoardRefetchesChatsOnChatEvents(t *testing.T) {
	b := testBoard()
	if cmd := b.updateNote(apiclient.EventNote{Event: apiclient.Event{ID: 1, Type: "chat.turn_changed"}}); cmd != nil || b.chatsPending {
		t.Fatal("a turn event scheduled a chat refetch")
	}
	for _, typ := range []string{"chat.created", "chat.state_changed", "chat.archived", "chat.closed", "chat.handed_off"} {
		b.chatsPending = false
		b.updateNote(apiclient.EventNote{Event: apiclient.Event{ID: 2, Type: typ}})
		if !b.chatsPending {
			t.Errorf("%s did not schedule a chat refetch", typ)
		}
	}
	// The debounce holds a second event.
	if cmd := b.scheduleChatsRefresh(); cmd != nil {
		t.Error("a second event while pending scheduled another refetch")
	}

	a := newArchivedBoard()
	a.updateNote(apiclient.EventNote{Event: apiclient.Event{ID: 3, Type: "chat.state_changed"}})
	if a.chatsPending {
		t.Error("the archived board scheduled a chat refetch")
	}
}

// TestBoardChatsDropsStaleAnswers: an answer older than one applied, or a
// failed one, leaves the listing as it is.
func TestBoardChatsDropsStaleAnswers(t *testing.T) {
	b := testBoard()
	fresh := []apiclient.Chat{waitingChat(1, testProjectID, "awaiting_input", testNow)}
	b.update(boardChatsMsg{seq: 2, chats: fresh})
	b.update(boardChatsMsg{seq: 1})
	b.update(boardChatsMsg{seq: 3, err: errors.New("down")})
	if len(b.chats) != 1 || b.chats[0].ID != 1 {
		t.Fatalf("chats = %+v, want the seq 2 answer kept", b.chats)
	}
}

// TestAttentionTargetsOldestWaitFirst: one project's tasks and chats are one
// list, ordered by when each started waiting (task 165).
func TestAttentionTargetsOldestWaitFirst(t *testing.T) {
	m := attentionRoot(t,
		task(1, stateAwaitingInput, withUpdated(testNow.Add(-1*time.Minute))),
		task(2, stateBlocked, withUpdated(testNow.Add(-3*time.Minute))),
	)
	b := m.views[viewHome].(*shell).board
	b.chats = []apiclient.Chat{
		waitingChat(10, testProjectID, "awaiting_input", testNow.Add(-2*time.Minute)),
		waitingChat(11, testProjectID, "awaiting_input", testNow.Add(-5*time.Minute)),
		waitingChat(12, otherProjectID, "awaiting_input", testNow.Add(-9*time.Minute)),
	}
	got := describeTargets(b.attentionTargets(testProjectID))
	want := []string{"chat 11", "task 2", "chat 10", "task 1"}
	if !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
}

// TestNextAttentionWalksTasksAndChats: `!` walks the selection's tasks and
// waiting chats in one order, opens a chat with esc going back to where `!`
// was pressed, moves on from the open chat when pressed inside it, and
// crosses into the other project — to its chat — once the selection is
// exhausted.
func TestNextAttentionWalksTasksAndChats(t *testing.T) {
	m := attentionRoot(t,
		task(1, stateAwaitingInput, withUpdated(testNow.Add(-4*time.Minute))),
		task(2, stateBlocked, withUpdated(testNow.Add(-1*time.Minute))),
	)
	s := m.views[viewHome].(*shell)
	s.board.chats = []apiclient.Chat{
		waitingChat(10, testProjectID, "awaiting_input", testNow.Add(-2*time.Minute)),
		waitingChat(20, otherProjectID, "awaiting_input", testNow.Add(-9*time.Minute)),
	}
	s.board.restoreSelection(s.board.rows())
	s.board.selectedID = 1
	s.board.restoreSelection(s.board.rows())
	m.active = viewHome

	// Task 1, then chat 10 (waiting since before task 2).
	_, cmd := m.Update(keyPress("!"))
	drainPicker(m, cmd)
	cv := m.views[viewChat].(*chatView)
	if m.active != viewChat || cv.chatID != 10 {
		t.Fatalf("first !: active %v chat %d, want the chat workspace on chat 10", m.active, cv.chatID)
	}
	if cv.back != viewHome {
		t.Fatalf("chat back = %v, want the board `!` was pressed from", cv.back)
	}

	// Inside the chat, with the composer not holding the keyboard, `!`
	// moves on from the chat to task 2.
	cv.composer.Blur()
	got := m.nextAttention()
	drainPicker(m, got)
	if id, _ := s.board.selected(); m.active != viewHome || id != 2 {
		t.Fatalf("! in chat 10: active %v, task %d; want the board on task 2", m.active, id)
	}

	// Task 2 is the selection's last: the next press crosses to the other
	// project's waiting chat, whose way back is still the board.
	drainPicker(m, m.nextAttention())
	if m.sel.id != otherProjectID {
		t.Fatalf("selection = %d, want the cross into %d", m.sel.id, otherProjectID)
	}
	if m.active != viewChat || cv.chatID != 20 || cv.back != viewHome {
		t.Fatalf("cross: active %v chat %d back %v, want chat 20 back to the board", m.active, cv.chatID, cv.back)
	}
	if m.status.text == "" {
		t.Error("crossing projects raised no notice")
	}
}

// TestNextAttentionInsideChatKeepsItsWayBack: pressed inside a chat, the next
// chat keeps that chat's own way back rather than pointing at the chat that
// was just left.
func TestNextAttentionInsideChatKeepsItsWayBack(t *testing.T) {
	m := attentionRoot(t)
	s := m.views[viewHome].(*shell)
	s.board.chats = []apiclient.Chat{
		waitingChat(10, testProjectID, "awaiting_input", testNow.Add(-2*time.Minute)),
		waitingChat(11, testProjectID, "awaiting_input", testNow.Add(-1*time.Minute)),
	}
	_, cmd := m.Update(openChatMsg{id: 10, projectID: testProjectID, back: viewIssues})
	drainPicker(m, cmd)
	drainPicker(m, m.nextAttention())
	cv := m.views[viewChat].(*chatView)
	if cv.chatID != 11 || cv.back != viewIssues {
		t.Fatalf("chat %d back %v, want chat 11 back to the issues view", cv.chatID, cv.back)
	}
}

func describeTargets(ts []attentionTarget) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		kind := "task"
		if t.isChat() {
			kind = "chat"
		}
		out = append(out, kind+" "+strconv.FormatInt(t.id(), 10))
	}
	return out
}
