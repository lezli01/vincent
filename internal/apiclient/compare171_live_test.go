package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestCompareAgainstTheRealAPI is task 171.5: a compare forks a claude chat
// onto claude and codex from before a turn, sends both that turn's prompt,
// runs each side restricted where its agent can and full-auto — saying why —
// where it cannot, and pairs the two; with no room for both under
// max_parallel_chats it creates nothing.
func TestCompareAgainstTheRealAPI(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", filepath.Join(t.TempDir(), "prompts.jsonl"))
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "compare.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "config", "core.autocrlf", "false")
	p := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	var limit atomic.Int64
	limit.Store(4)
	cfg := func() config.Config {
		c := config.Default()
		c.MaxParallelChats = int(limit.Load())
		return c
	}
	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	reg := agent.NewRegistry(claude.New(func() string { return fake }), codex.New(func() string { return fake }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := chatrun.New(chatrun.Deps{
		Store: st, Config: cfg, Worktrees: wt, Agents: reg, DataDir: dataDir, Logger: log,
		TurnTools: func(c *store.Chat, turn *store.ChatTurn) []chatexport.ToolCall {
			return api.TurnToolCalls(dataDir, reg, log, c, turn)
		},
	})
	runner.Start(ctx)
	t.Cleanup(runner.Stop)
	s := api.New(api.Deps{
		Token: testToken, Config: cfg, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log, Store: st,
		Git: git, Worktrees: wt, Agents: reg, Catalog: agent.NewCatalogCache(reg),
		Chats: runner, Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, testToken)

	idleWith := func(id int64, n int) {
		t.Helper()
		wait.Until(t, "the turn ends", func() bool {
			c, turns, err := client.GetChat(ctx, id)
			return err == nil && c.State == "idle" && len(turns) == n && turns[n-1].State == "done"
		})
	}
	src, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: p.ID, Title: "src", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for i, prompt := range []string{"first question", "second question"} {
		if _, err := client.SendChat(ctx, src.ID, prompt); err != nil {
			t.Fatal(err)
		}
		idleWith(src.ID, i+1)
	}
	refused := func(name string, err error, status int, code string) {
		t.Helper()
		var apiErr *apiclient.Error
		if !errors.As(err, &apiErr) || apiErr.Status != status || (code != "" && apiErr.Code != code) {
			t.Errorf("%s = %v, want %d %s", name, err, status, code)
		}
	}
	chatCount := func() int {
		t.Helper()
		all, err := client.ListChats(ctx, apiclient.ListChatsOptions{ProjectID: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		return len(all)
	}

	_, err = client.CompareChat(ctx, src.ID, apiclient.CompareRequest{Turn: 2, Agents: []string{"claude", "claude"}})
	refused("the same agent twice", err, http.StatusBadRequest, "validation_failed")
	_, err = client.CompareChat(ctx, src.ID, apiclient.CompareRequest{Turn: 2, Agents: []string{"claude"}})
	refused("one agent", err, http.StatusBadRequest, "validation_failed")
	_, err = client.CompareChat(ctx, src.ID, apiclient.CompareRequest{Turn: 2, Agents: []string{"claude", "nope"}})
	refused("an unknown agent", err, http.StatusBadRequest, "validation_failed")

	// Room for one more chat, not two: nothing is created.
	limit.Store(1)
	before := chatCount()
	_, err = client.CompareChat(ctx, src.ID, apiclient.CompareRequest{Turn: 2, Agents: []string{"claude", "codex"}})
	refused("no room for both", err, http.StatusConflict, "chat_cap_reached")
	if after := chatCount(); after != before {
		t.Errorf("a refused compare left %d chats behind", after-before)
	}
	limit.Store(4)

	res, err := client.CompareChat(ctx, src.ID, apiclient.CompareRequest{Turn: 2, Agents: []string{"claude", "codex"}})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	cmp := res.Compare
	if cmp.Prompt != "second question" || cmp.Seq != 2 || cmp.SourceChatID != src.ID {
		t.Errorf("compare = %+v; want turn 2's own prompt", cmp)
	}
	if cmp.Left.Agent != "claude" || cmp.Left.PermissionMode != "restricted" || cmp.Left.ModeNote != "" {
		t.Errorf("left = %+v; claude keeps a chat restricted", cmp.Left)
	}
	if cmp.Right.Agent != "codex" || cmp.Right.PermissionMode != "full-auto" || cmp.Right.ModeNote == "" {
		t.Errorf("right = %+v; codex cannot, and the compare must say so", cmp.Right)
	}
	if !res.Right.Seeded || res.Right.Chat.Agent != "codex" || res.Left.Chat.PermissionMode != "restricted" {
		t.Errorf("sides = %+v / %+v", res.Left.Chat, res.Right.Chat)
	}
	left, right := res.Left.Chat.ID, res.Right.Chat.ID
	idleWith(left, 1)
	idleWith(right, 1)
	for _, id := range []int64{left, right} {
		c, turns, err := client.GetChat(ctx, id)
		if err != nil || turns[0].Prompt != "second question" || c.ForkedFrom == nil || c.ForkedFrom.ChatID != src.ID {
			t.Errorf("side %d: %+v %+v %v", id, c, turns, err)
		}
		got, err := client.GetChatCompare(ctx, id)
		if err != nil || got.ID != cmp.ID {
			t.Errorf("GET compare from side %d = %+v, %v", id, got, err)
		}
	}
	_, turns, _ := client.GetChat(ctx, left)
	if turns[0].PermissionMode != "restricted" {
		t.Errorf("the left turn did not run restricted: %q", turns[0].PermissionMode)
	}
	_, err = client.GetChatCompare(ctx, src.ID)
	refused("GET compare on the source", err, http.StatusNotFound, "")

	// Keep the left side: the right is archived, its branch deleted, and a
	// second keep is refused. The kept side carries on.
	rightBranch := res.Right.Chat.Branch
	kept, err := client.KeepCompareSide(ctx, left)
	if err != nil {
		t.Fatalf("keep left: %v", err)
	}
	if kept.Compare.KeptChatID == nil || *kept.Compare.KeptChatID != left || kept.Archived == nil ||
		kept.Archived.ID != right || kept.Archived.State != "archived" || !kept.BranchDeleted {
		t.Errorf("keep = %+v", kept)
	}
	if out, err := git.Run(ctx, repo, "branch", "--list", rightBranch); err != nil || out != "" {
		t.Errorf("the discarded side's branch %s is still there: %q %v", rightBranch, out, err)
	}
	_, err = client.KeepCompareSide(ctx, right)
	refused("a second keep", err, http.StatusConflict, "compare_kept")
	_, err = client.KeepCompareSide(ctx, src.ID)
	refused("keep on the source", err, http.StatusNotFound, "")
	if c, _, err := client.GetChat(ctx, left); err != nil || c.State != "idle" {
		t.Errorf("the kept side = %+v, %v", c, err)
	}

	// An explicit prompt is sent as given.
	res, err = client.CompareChat(ctx, src.ID, apiclient.CompareRequest{
		Turn: 1, Prompt: "a new question", Agents: []string{"codex", "claude"}, PermissionMode: "full-auto",
	})
	if err != nil || res.Compare.Prompt != "a new question" || res.Compare.Left.PermissionMode != "full-auto" ||
		res.Compare.Right.PermissionMode != "full-auto" || res.Compare.Left.ModeNote != "" {
		t.Fatalf("full-auto compare = %+v, %v", res, err)
	}
	idleWith(res.Left.Chat.ID, 1)
	idleWith(res.Right.Chat.ID, 1)
	// While either side holds a process, neither can be kept.
	if _, err := st.SetChatState(ctx, res.Right.Chat.ID, chatstate.Running); err != nil {
		t.Fatal(err)
	}
	_, err = client.KeepCompareSide(ctx, res.Left.Chat.ID)
	refused("keep while the other side runs", err, http.StatusConflict, "chat_turn_live")
	if c, _, _ := client.GetChat(ctx, res.Right.Chat.ID); c == nil || c.State != "running" {
		t.Errorf("a refused keep touched the other side: %+v", c)
	}
}
