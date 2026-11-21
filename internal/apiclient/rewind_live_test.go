package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestRewindAndForkAgainstTheRealAPI is task 167's client half: RewindChat,
// ForkChat, the turn and chat fields GetChat decodes, and the export's
// IncludeRewound, against the real handlers and a real runner over the fake
// agent — so the client's wire types and the server's cannot drift.
func TestRewindAndForkAgainstTheRealAPI(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "rewind.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	repo := testrepo.Init(t, "main")
	p := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	reg := agent.NewRegistry(claude.New(func() string { return fake }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Worktrees: wt, Agents: reg, DataDir: dataDir, Logger: log,
	})
	runner.Start(ctx)
	t.Cleanup(runner.Stop)
	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log, Store: st,
		Git: git, Worktrees: wt, Agents: reg, Catalog: agent.NewCatalogCache(reg),
		Chats: runner, Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	chat, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: p.ID, Title: "rewind", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	turn := func(prompt string) {
		t.Helper()
		if _, err := client.SendChat(ctx, chat.ID, prompt); err != nil {
			t.Fatal(err)
		}
		wait.Until(t, "the turn ends and its checkpoint is offered", func() bool {
			c, turns, err := client.GetChat(ctx, chat.ID)
			return err == nil && c.State == "idle" && len(turns) > 0 &&
				turns[len(turns)-1].Rewind != nil && turns[len(turns)-1].Rewind.Code != "chat_turn_live"
		})
	}
	testrepo.WriteFile(t, chat.WorktreePath, "a.txt", "one\n")
	turn("first")
	testrepo.WriteFile(t, chat.WorktreePath, "a.txt", "two\n")
	turn("second")
	testrepo.WriteFile(t, chat.WorktreePath, "a.txt", "three\n")

	_, turns, err := client.GetChat(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[1].CheckpointSHA == "" || turns[1].HeadBefore == "" || turns[1].Rewind == nil {
		t.Fatalf("turns = %+v", turns)
	}

	res, err := client.RewindChat(ctx, chat.ID, apiclient.RewindRequest{Turn: 2, Mode: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref != chatrun.RewoundRef(chat.ID, 1) || res.Chat.ID != chat.ID {
		t.Errorf("code rewind: ref %q id %q chat %d", res.Ref, res.RewindID, res.Chat.ID)
	}
	if b, _ := os.ReadFile(filepath.Join(chat.WorktreePath, "a.txt")); string(b) != "two\n" {
		t.Errorf("a.txt after rewind to turn 2 = %q, want the state before it", b)
	}
	got, _, err := client.GetChat(ctx, chat.ID)
	if err != nil || len(got.RewoundStates) != 1 || got.RewoundStates[0].K != 1 || got.RewoundStates[0].SHA == "" {
		t.Fatalf("rewound_states = %+v (%v)", got, err)
	}

	res, err = client.RewindChat(ctx, chat.ID, apiclient.RewindRequest{Turn: 2, Mode: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RewoundTurns) != 1 || res.RewoundTurns[0] != 2 || res.Chat.ResumePending == "" {
		t.Errorf("conversation rewind = %+v", res)
	}
	_, turns, err = client.GetChat(ctx, chat.ID)
	if err != nil || len(turns) != 2 || turns[1].RewoundAt == nil || turns[1].RewindID != res.RewindID {
		t.Fatalf("turns after the conversation rewind = %+v (%v)", turns, err)
	}
	_, err = client.RewindChat(ctx, chat.ID, apiclient.RewindRequest{Turn: 2})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "chat_turn_rewound" {
		t.Errorf("rewind to a rewound turn: %v", err)
	}

	for _, include := range []bool{false, true} {
		ex, err := client.ChatExport(ctx, chat.ID, apiclient.ChatExportOptions{Format: "md", IncludeRewound: include})
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(ex.Body), "## Turn 2"); has != include {
			t.Errorf("IncludeRewound=%v: export shows turn 2 = %v", include, has)
		}
	}

	fork, err := client.ForkChat(ctx, chat.ID, apiclient.ForkRequest{Turn: 1, Title: "forked"})
	if err != nil {
		t.Fatal(err)
	}
	if fork.Chat.ID == chat.ID || fork.Chat.Title != "forked" || fork.Chat.ForkedFrom == nil ||
		fork.Chat.ForkedFrom.ChatID != chat.ID || fork.Chat.ForkedFrom.Seq != 1 || fork.Chat.State != "idle" {
		t.Errorf("fork = %+v", fork.Chat)
	}
	if b, _ := os.ReadFile(filepath.Join(fork.Chat.WorktreePath, "a.txt")); string(b) != "one\n" {
		t.Errorf("fork a.txt = %q", b)
	}
	_, err = client.ForkChat(ctx, chat.ID, apiclient.ForkRequest{Turn: 2})
	if !errors.As(err, &apiErr) || apiErr.Code != "chat_turn_rewound" {
		t.Errorf("fork of a rewound turn: %v", err)
	}
}
