package tui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// Task 167.7 against the real handlers, store, chat runner and worktree: the
// turn picker the chat view opens is filled from the daemon's per-turn
// options, a code rewind chosen in it restores the file on disk, and a
// conversation rewind hides the rewound turn and puts its prompt back in the
// composer, all from what the daemon answered.

const rewindLiveToken = "rewind-live-token"

func newRewindLive(t *testing.T) (*chatDiffLive, *apiclient.Client, *apiclient.Chat) {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "rewind.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)
	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	reg := agent.NewRegistry(claude.New(func() string { return fake }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Worktrees: wt, Agents: reg,
		DataDir: dataDir, Logger: log, Events: broker,
	})
	chats.Start(t.Context())
	t.Cleanup(chats.Stop)
	s := api.New(api.Deps{
		Token: rewindLiveToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Broker: broker, Git: git, Worktrees: wt, Agents: reg,
		Catalog: agent.NewCatalogCache(reg), Chats: chats, Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	client := apiclient.New(ts.URL, rewindLiveToken)
	// core.autocrlf off before the chat's worktree is checked out, so a
	// rewind writes back the bytes the test wrote: Git for Windows turns it
	// on globally, and a restore's read-tree then checks LF blobs out as CRLF.
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "config", "core.autocrlf", "false")
	p, err := client.CreateProject(t.Context(), apiclient.CreateProjectRequest{Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.CreateChat(t.Context(), apiclient.CreateChatRequest{ProjectID: p.ID, Title: "rewind", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = client
	l := &chatDiffLive{t: t, v: v, dir: c.WorktreePath, msgs: make(chan tea.Msg, 256), done: make(chan struct{})}
	t.Cleanup(func() { close(l.done) })
	v.update(tea.WindowSizeMsg{Width: 120, Height: 44})
	return l, client, c
}

// offered reports whether the view holds n turns and the newest one's rewind
// options say no turn is live.
func offered(v *chatView, n int) bool {
	turns := v.everyTurn()
	return len(turns) == n && turns[n-1].Rewind != nil && turns[n-1].Rewind.Code != "chat_turn_live" &&
		v.chat != nil && v.chat.State == "idle"
}

func TestChatRewindPickerLive(t *testing.T) {
	l, client, c := newRewindLive(t)
	v := l.v
	ctx := t.Context()
	turn := func(prompt string, n int) {
		t.Helper()
		if _, err := client.SendChat(ctx, c.ID, prompt); err != nil {
			t.Fatal(err)
		}
		l.until("turn "+prompt+" ends and is offered", func() bool {
			openChat(t, v, c.ID)
			return offered(v, n)
		})
	}
	openChat(t, v, c.ID)
	testrepo.WriteFile(t, l.dir, "a.txt", "one\n")
	turn("first", 1)
	testrepo.WriteFile(t, l.dir, "a.txt", "two\n")
	turn("second", 2)
	testrepo.WriteFile(t, l.dir, "a.txt", "three\n")

	// A code rewind to turn 2, chosen in the picker.
	v.openRewindPicker(false)
	p := v.rewind
	if p == nil || len(p.rows) != 2 || p.rows[p.cursor].seq != 2 {
		t.Fatalf("picker = %+v, want two turns with the cursor on turn 2", p)
	}
	for p.mode != "code" {
		l.press("tab")
	}
	l.press("enter")
	// Read without failing: the restore deletes and recreates the file, and a
	// poll may land between the two.
	l.until("the code rewind lands on disk", func() bool {
		b, err := os.ReadFile(filepath.Join(l.dir, "a.txt"))
		return err == nil && strings.ReplaceAll(string(b), "\r", "") == "two\n" && v.rewind == nil
	})

	// A conversation rewind to turn 2: the turn is hidden and its prompt is
	// the composer's text again.
	l.until("the view reloads after the code rewind", func() bool {
		openChat(t, v, c.ID)
		return offered(v, 2)
	})
	v.openRewindPicker(false)
	p = v.rewind
	for p.mode != "conversation" {
		l.press("tab")
	}
	l.press("enter")
	l.until("turn 2 is hidden and its prompt is back", func() bool {
		return len(v.turns) == 1 && v.composer.Value() == "second"
	})
	if all := v.everyTurn(); len(all) != 2 || all[1].RewoundAt == nil {
		t.Errorf("every turn = %+v, want turn 2 kept and marked rewound", all)
	}

	// A fork of turn 1 opens nothing here but answers a chat forked from it.
	v.openRewindPicker(true)
	p = v.rewind
	if len(p.rows) != 1 || p.rows[0].seq != 1 {
		t.Fatalf("fork picker rows = %+v, want turn 1 only", p.rows)
	}
	cmd := v.rewindCmd(p, p.rows[0])
	msg, ok := cmd().(chatRewoundMsg)
	if !ok || msg.err != nil || msg.fork == nil || msg.fork.Chat.ForkedFrom == nil || msg.fork.Chat.ForkedFrom.Seq != 1 {
		t.Fatalf("fork answered %+v", msg)
	}
}
