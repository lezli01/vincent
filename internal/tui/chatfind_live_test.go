package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestChatSearchLive drives the conversation search against the real
// handlers over httptest (task 157 decision 2): the daemon counts a match in
// a turn this view never loaded, landing on it fetches that turn through the
// transcript route, and the cursor ends on the line that shows it. It is what
// keeps the search's wire types from drifting between client and server.
func TestChatFindLive(t *testing.T) {
	const token = "chat-search-token"
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "search.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	chat := &store.Chat{
		ProjectID: project.ID, Title: "search", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, "transcripts", worktree.ChatOwner(chat.ID).Dir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Eight turns, so the oldest is outside the five fetched up front; only
	// the first says the word.
	for seq := 1; seq <= 8; seq++ {
		turn, err := st.CreateChatTurn(ctx, chat.ID, fmt.Sprintf("question %d", seq))
		if err != nil {
			t.Fatal(err)
		}
		turn.State, turn.ResultText = chatstate.TurnDone, fmt.Sprintf("answer %d", seq)
		if _, err := st.FinishChatTurn(ctx, turn, ""); err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprintf("plain answer %d", seq)
		if seq == 1 {
			text = "the Needle is in turn one"
		}
		line := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`+"\n", text)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.jsonl", seq)), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	agents := agent.NewRegistry(claude.New(func() string { return "claude" }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Agents: agents,
		Catalog: agent.NewCatalogCache(agents), Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = apiclient.New(ts.URL, token)
	v.chatID = chat.ID
	v.composer.Focus()
	loaded := runCmd(t, v.loadCmd(), 30*time.Second)
	msg, ok := loaded.(chatLoadedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("loading the chat produced %#v", loaded)
	}
	v.applyLoaded(msg)
	// The eager fetches: the five newest turns, never turn 1.
	for _, seq := range []int{4, 5, 6, 7, 8} {
		v.fetched[seq] = true
		v.applyTranscript(runCmd(t, v.transcriptCmd(seq), 30*time.Second).(chatTranscriptMsg))
	}
	v.render(100, 30)

	v.openSearchLine()
	v.search.buf = "needle"
	_, cmd := v.updateKey(registryKey(t, "enter"))
	res := runCmd(t, cmd, 30*time.Second).(chatSearchMsg)
	if res.err != nil {
		t.Fatalf("search: %v", res.err)
	}
	if res.result.Total != 1 || len(res.result.Turns) != 1 || res.result.Turns[0].Seq != 1 {
		t.Fatalf("search = %+v, want turn 1 alone", res.result)
	}
	fetch := v.applySearch(res)
	if fetch == nil {
		t.Fatal("landing on turn 1 fetched nothing")
	}
	got := runCmd(t, fetch, 30*time.Second).(chatTranscriptMsg)
	if got.seq != 1 {
		t.Fatalf("fetched turn %d, want 1", got.seq)
	}
	v.applyTranscript(got)
	if a := anchorAt(v.anchors, v.cursor); a.turn != 1 {
		t.Fatalf("the cursor is in turn %d, want 1", a.turn)
	}
	if line := ansi.Strip(v.lines[v.cursor]); !strings.Contains(line, "Needle") {
		t.Fatalf("the cursor is on %q", line)
	}
	if label := ansi.Strip(v.edgeLabel()); !strings.Contains(label, "1/1") {
		t.Fatalf("edge label %q, want 1/1", label)
	}
}
