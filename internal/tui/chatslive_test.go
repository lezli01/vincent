package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// TestChatsBoardsScopeAgainstTheRealAPI is task 132.10 against the real
// handlers: two projects, each with a live and an archived chat. Both chats
// boards list only the selected project's, and a selection switch swaps the
// rows without leaving the view.
func TestChatsBoardsScopeAgainstTheRealAPI(t *testing.T) {
	const token = "chats-scope-token"
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	a := &store.Project{Name: "alpha", Path: filepath.Join(t.TempDir(), "a"), DefaultBranch: "main"}
	b := &store.Project{Name: "beta", Path: filepath.Join(t.TempDir(), "b"), DefaultBranch: "main"}
	for _, p := range []*store.Project{a, b} {
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		for _, state := range []chatstate.State{chatstate.Idle, chatstate.Archived} {
			c := &store.Chat{
				ProjectID: p.ID, Title: p.Name + " " + string(state), State: state,
				Agent: "claude", PermissionMode: "full-auto", Branch: "vincent/" + p.Name + "-" + string(state),
			}
			if err := st.CreateChat(ctx, c); err != nil {
				t.Fatalf("CreateChat: %v", err)
			}
		}
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

	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(connectedMsg{client: apiclient.New(ts.URL, token), dataDir: t.TempDir()})
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "alpha to be selected", func() bool { return m.sel.id == a.ID })
	p.push(m.switchTo(viewChats))

	titles := func(id viewID) []string {
		v := m.views[id].(*chatsView)
		out := []string{}
		for _, r := range v.rows() {
			out = append(out, r.chat.Title)
		}
		return out
	}
	shows := func(id viewID, want ...string) func() bool {
		return func() bool { return slices.Equal(titles(id), want) }
	}
	p.until(10*time.Second, "the chats board to list alpha's live chat", shows(viewChats, "alpha idle"))
	p.until(10*time.Second, "the archived chats board to list alpha's archived chat", shows(viewArchivedChats, "alpha archived"))

	p.push(m.selectProject(apiclient.Project{ID: b.ID, Name: b.Name}, "test"))
	p.until(10*time.Second, "the chats board to swap to beta's chat", shows(viewChats, "beta idle"))
	p.until(10*time.Second, "the archived chats board to swap to beta's chat", shows(viewArchivedChats, "beta archived"))
	if m.active != viewChats {
		t.Fatalf("the switch left the chats board for %v", m.active)
	}
}
