package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
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

// TestChatSettingsOverTheWire is task 160 against the real handlers: PatchChat
// changes the model between two turns, SendChatWith overrides one turn, each
// turn's recorded model comes back on the wire, the typed refusal decodes,
// and /v1/agents publishes the capability.
func TestChatSettingsOverTheWire(t *testing.T) {
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "chatsettings.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	bin := func() string { return fake }
	reg := agent.NewRegistry(claude.New(bin), codex.New(bin))
	cfg := config.Default
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{Store: st, Config: cfg, Agents: reg, DataDir: dataDir, Logger: log})
	chats.Start(ctx)
	t.Cleanup(chats.Stop)
	s := api.New(api.Deps{
		Token: testToken, Config: cfg, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Agents: reg, Catalog: agent.NewCatalogCache(reg), Chats: chats,
		Git: gitx.New(), Worktrees: worktree.NewManager(gitx.New(), dataDir), WakeRunner: func() {},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	chat, err := client.CreateChat(ctx, apiclient.CreateChatRequest{
		ProjectID: project.ID, Title: "settings", Agent: "claude", Model: "haiku",
	})
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	idle := func() {
		t.Helper()
		wait.Until(t, "chat idle", func() bool {
			c, _, err := client.GetChat(ctx, chat.ID)
			return err == nil && c.State == "idle"
		})
	}
	if _, err := client.SendChat(ctx, chat.ID, "one"); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	idle()
	model := "sonnet"
	patched, err := client.PatchChat(ctx, chat.ID, apiclient.PatchChatRequest{Model: &model})
	if err != nil || patched.Model != "sonnet" {
		t.Fatalf("PatchChat = %+v, %v; want model sonnet", patched, err)
	}
	if _, err := client.SendChat(ctx, chat.ID, "two"); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	idle()
	if _, err := client.SendChatWith(ctx, chat.ID, "three", apiclient.TurnSettings{Model: "opus"}); err != nil {
		t.Fatalf("SendChatWith: %v", err)
	}
	idle()
	_, turns, err := client.GetChat(ctx, chat.ID)
	if err != nil || len(turns) != 3 {
		t.Fatalf("GetChat = %d turns, %v", len(turns), err)
	}
	for i, want := range []string{"haiku", "sonnet", "opus"} {
		if turns[i].Model != want || turns[i].PermissionMode != "full-auto" {
			t.Errorf("turn %d = %q/%q, want %s/full-auto", i+1, turns[i].Model, turns[i].PermissionMode, want)
		}
	}

	cx, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: project.ID, Title: "cx", Agent: "codex"})
	if err != nil {
		t.Fatalf("CreateChat codex: %v", err)
	}
	restricted := "restricted"
	_, err = client.PatchChat(ctx, cx.ID, apiclient.PatchChatRequest{PermissionMode: &restricted})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != api.CodeAgentCannotRestrictResume {
		t.Errorf("PatchChat codex restricted = %v, want 400 %s", err, api.CodeAgentCannotRestrictResume)
	}
	agents, err := client.ListAgents(ctx, false)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	for _, a := range agents {
		if a.CannotChangeOnResume() != (a.Name == "codex") {
			t.Errorf("%s CannotChangeOnResume = %v", a.Name, a.CannotChangeOnResume())
		}
	}
}
