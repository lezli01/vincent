package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// unreadLive is one real API over httptest with two TUI clients on it, both
// on the chats board of one project holding one idle chat.
type unreadLive struct {
	st     *store.Store
	chat   *store.Chat
	a, b   *root
	pa, pb *pump
}

func newUnreadLive(t *testing.T) *unreadLive {
	t.Helper()
	const token = "unread-live-token"
	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	p := &store.Project{Name: "alpha", Path: filepath.Join(t.TempDir(), "a"), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c := &store.Chat{
		ProjectID: p.ID, Title: "the conversation", State: chatstate.Idle,
		Agent: "claude", PermissionMode: "full-auto", Branch: "vincent/c-1",
	}
	if err := st.CreateChat(ctx, c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	h := &unreadLive{st: st, chat: c}
	start := func() (*root, *pump) {
		m := newRoot(testCtx(t), connector{}, ackedDir(t))
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
		_, cmd := m.Update(connectedMsg{client: apiclient.New(ts.URL, token), dataDir: t.TempDir()})
		pm := newPump(t, m, cmd)
		pm.until(10*time.Second, "the project to be selected", func() bool { return m.sel.id == p.ID })
		pm.push(m.switchTo(viewChats))
		pm.until(10*time.Second, "the chats board to list the chat", func() bool { return len(unreadBoard(m).chats) == 1 })
		return m, pm
	}
	h.a, h.pa = start()
	h.b, h.pb = start()
	// A chat view's stream is cancelled only by the next open, and an open
	// SSE request keeps httptest's Close waiting: stop both before it runs.
	t.Cleanup(func() {
		for _, m := range []*root{h.a, h.b} {
			if v := m.views[viewChat].(*chatView); v.streamStop != nil {
				v.streamStop()
			}
		}
	})
	return h
}

func unreadBoard(m *root) *chatsView { return m.views[viewChats].(*chatsView) }

// drawsUnread reports whether m's chats board draws the chat with the ● mark — on
// the data and on the card the board renders.
func drawsUnread(m *root, unread bool) func() bool {
	return func() bool {
		v := unreadBoard(m)
		if len(v.chats) != 1 || v.chats[0].Unread != unread {
			return false
		}
		card := ansi.Strip(strings.Join(v.chatCard(v.chats[0], false, 40), "\n"))
		return strings.Contains(card, unreadGlyph) == unread
	}
}

// finishTurn runs one turn of the chat to done in the store, the writes
// chatrun makes: the turn's start, its end, and the chat back to idle.
func (h *unreadLive) finishTurn(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	turn, err := h.st.CreateChatTurn(ctx, h.chat.ID, "go on")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	ended := time.Now()
	turn.State, turn.EndedAt = chatstate.TurnDone, &ended
	if err := h.st.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	if _, err := h.st.SetChatState(ctx, h.chat.ID, chatstate.Idle); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
}

func (h *unreadLive) unreadInStore(t *testing.T) bool {
	t.Helper()
	c, err := h.st.GetChat(context.Background(), h.chat.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	return c.Unread
}

// TestUnreadAcrossTwoClients is task 165's acceptance criterion "unread
// correct across two TUI clients": a turn ends and both boards draw ●; one
// client opens the chat, and the other's mark clears on the chat.viewed
// event, without a key pressed there.
func TestUnreadAcrossTwoClients(t *testing.T) {
	h := newUnreadLive(t)
	if !drawsUnread(h.a, false)() || !drawsUnread(h.b, false)() {
		t.Fatal("a chat with no finished turn is drawn unread")
	}

	h.finishTurn(t)
	h.pa.until(10*time.Second, "client A to mark the chat unread", drawsUnread(h.a, true))
	h.pb.until(10*time.Second, "client B to mark the chat unread", drawsUnread(h.b, true))

	h.pa.push(func() tea.Msg { return openChatMsg{id: h.chat.ID, projectID: h.chat.ProjectID} })
	h.pa.until(10*time.Second, "client A to open the chat", func() bool {
		return h.a.active == viewChat && h.a.views[viewChat].(*chatView).chat != nil
	})
	h.pa.until(10*time.Second, "the view to reach the daemon", func() bool { return !h.unreadInStore(t) })
	h.pb.until(10*time.Second, "client B's mark to clear on chat.viewed", drawsUnread(h.b, false))
}

// TestUnreadFollowsFocus: a turn that finishes in front of a focused reader
// is viewed; one that finishes while the terminal is blurred stays unread,
// and focus coming back does not clear it.
func TestUnreadFollowsFocus(t *testing.T) {
	h := newUnreadLive(t)
	h.pa.push(func() tea.Msg { return openChatMsg{id: h.chat.ID, projectID: h.chat.ProjectID} })
	v := h.a.views[viewChat].(*chatView)
	h.pa.until(10*time.Second, "client A to open the chat", func() bool { return v.chat != nil })

	turns := func(n int) func() bool { return func() bool { return len(v.turns) == n && v.runningTurn() == nil } }
	h.finishTurn(t)
	h.pa.until(10*time.Second, "the finished turn to land in the open chat", turns(1))
	h.pa.until(10*time.Second, "the turn seen to finish to be marked viewed", func() bool { return !h.unreadInStore(t) })

	h.pa.push(func() tea.Msg { return tea.BlurMsg{} })
	h.pa.until(10*time.Second, "the chat view to see the blur", func() bool { return v.blurred })
	h.finishTurn(t)
	h.pa.until(10*time.Second, "the second turn to land in the open chat", turns(2))
	h.pb.until(10*time.Second, "client B to draw the blurred turn unread", drawsUnread(h.b, true))

	h.pa.push(func() tea.Msg { return tea.FocusMsg{} })
	h.pa.until(10*time.Second, "the chat view to see the focus", func() bool { return !v.blurred })
	h.pa.settle(10*time.Second, 300*time.Millisecond, "focus to settle", func() (bool, int) { return true, 0 })
	if !h.unreadInStore(t) {
		t.Fatal("regaining focus marked the chat viewed")
	}
}
