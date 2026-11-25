package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// chatSearchLive serves a live chat and an archived one, each with many
// turns, where only turn 3 of each mentions its distinctive word.
func chatSearchLive(t *testing.T) (*root, *pump, map[string]int64) {
	t.Helper()
	const token = "chat-search-token"
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
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, tc := range []struct {
		title, word string
		end         chatstate.State
	}{{"live talk", "walrus", chatstate.Idle}, {"old talk", "narwhal", chatstate.Archived}} {
		c := &store.Chat{
			ProjectID: p.ID, Title: tc.title, State: chatstate.Idle, Agent: "claude",
			PermissionMode: "full_auto", Branch: "vincent/" + tc.word, BaseBranch: "main",
		}
		if err := st.CreateChat(ctx, c); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 12; i++ {
			turn, err := st.CreateChatTurn(ctx, c.ID, fmt.Sprintf("question %d", i))
			if err != nil {
				t.Fatal(err)
			}
			answer := fmt.Sprintf("answer %d\nline two\nline three\nline four", i)
			if i == 3 {
				answer = "the " + tc.word + " is here\nline two\nline three"
			}
			now := time.Now()
			turn.State, turn.ResultText, turn.EndedAt = chatstate.TurnDone, answer, &now
			if err := st.UpdateChatTurn(ctx, turn); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SetChatState(ctx, c.ID, chatstate.Idle); err != nil {
				t.Fatal(err)
			}
		}
		if tc.end != chatstate.Idle {
			if _, err := st.SetChatState(ctx, c.ID, tc.end); err != nil {
				t.Fatal(err)
			}
		}
		ids[tc.word] = c.ID
	}

	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: st, Broker: broker,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_, cmd := m.Update(connectedMsg{client: apiclient.New(ts.URL, token), dataDir: t.TempDir()})
	pm := newPump(t, m, cmd)
	pm.until(10*time.Second, "alpha to be selected", func() bool { return m.sel.id == p.ID })
	chat := m.views[viewChat].(*chatView)
	t.Cleanup(func() {
		if chat.streamStop != nil {
			chat.streamStop()
		}
	})
	return m, pm, ids
}

func typeRunes(m *root, p *pump, text string) {
	for _, r := range text {
		_, cmd := m.Update(key(string(r)))
		p.push(cmd)
	}
}

// searchAndLand types word into the search, waits for its match row, and
// presses enter on it, then waits for the chat to sit with turn 3's heading
// at the top of the window.
func searchAndLand(t *testing.T, m *root, p *pump, word string, chatID int64) {
	t.Helper()
	sv := m.views[viewChatSearch].(*chatSearchView)
	sv.query.SetValue("")
	typeRunes(m, p, word)
	p.until(10*time.Second, "the search to find "+word, func() bool {
		return len(sv.results) == 1 && sv.results[0].Chat.ID == chatID
	})
	if r := sv.rows[sv.cursor]; r.match < 0 || sv.results[r.chat].Matches[r.match].Seq != 3 {
		t.Fatalf("cursor row = %+v, want turn 3's match", r)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.push(cmd)
	cv := m.views[viewChat].(*chatView)
	p.until(10*time.Second, "the chat to land on turn 3", func() bool {
		if m.active != viewChat || cv.chatID != chatID || cv.chat == nil {
			return false
		}
		m.View()
		at, ok := cv.turnAt[3]
		return ok && cv.vp.YOffset() == at && at > 0 && cv.fetched[3]
	})
	if cv.following {
		t.Error("a landing left follow mode on")
	}
	if !strings.Contains(strings.Split(cv.vp.View(), "\n")[0]+strings.Split(cv.vp.View(), "\n")[1], "question 3") {
		t.Errorf("the window's top is not turn 3's prompt:\n%s", cv.vp.View())
	}
}

// TestChatSearchAgainstTheRealAPI is task 158's TUI half: the palette entry
// and the chats board's `S` both open the search; enter on a turn's match
// opens the chat — live or archived — with that turn at the top, fetching
// its transcript; esc comes back with the query and cursor kept.
func TestChatSearchAgainstTheRealAPI(t *testing.T) {
	m, p, ids := chatSearchLive(t)

	// The palette entry, from the tasks tab.
	p.push(m.switchTo(viewHome))
	m.openPalette()
	found := false
	for _, e := range m.palette.entries {
		if e.nav && e.navTarget == viewChatSearch && strings.HasPrefix(e.label, "search all chats") {
			found = true
		}
	}
	if !found {
		t.Fatal("the palette offers no search all chats entry")
	}
	typeRunes(m, p, "search all chats")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.push(cmd)
	p.until(5*time.Second, "the palette to open the search", func() bool { return m.active == viewChatSearch })

	searchAndLand(t, m, p, "narwhal", ids["narwhal"])
	if m.views[viewChat].(*chatView).chat.State != "archived" {
		t.Error("the archived chat did not open as archived")
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	p.push(cmd)
	sv := m.views[viewChatSearch].(*chatSearchView)
	p.until(5*time.Second, "esc to return to the search", func() bool { return m.active == viewChatSearch })
	if sv.query.Value() != "narwhal" || sv.cursor != 1 || len(sv.results) != 1 {
		t.Errorf("search after esc = %q cursor %d, %d results; want it kept", sv.query.Value(), sv.cursor, len(sv.results))
	}

	// `S` on the chats board.
	p.push(m.switchTo(viewChats))
	_, cmd = m.Update(key(opKey(keymap.ChatSearch)))
	p.push(cmd)
	p.until(5*time.Second, "S to open the search", func() bool { return m.active == viewChatSearch })
	searchAndLand(t, m, p, "walrus", ids["walrus"])
}
