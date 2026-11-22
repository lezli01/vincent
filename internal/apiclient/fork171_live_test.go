package apiclient_test

import (
	"errors"
	"fmt"
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
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/testutil/wait"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestCrossAgentForkAgainstTheRealAPI is task 171's client half: a claude
// chat forked onto codex is seeded, names its source's agent, renders its
// turn-0 context, takes a PATCH of that context until its first turn, and
// hands the codex CLI exactly the edited text.
func TestCrossAgentForkAgainstTheRealAPI(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	promptFile := filepath.Join(t.TempDir(), "prompts.jsonl")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "fork.db"))
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
	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	reg := agent.NewRegistry(claude.New(func() string { return fake }), codex.New(func() string { return fake }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Worktrees: wt, Agents: reg, DataDir: dataDir, Logger: log,
		TurnTools: func(c *store.Chat, turn *store.ChatTurn) []chatexport.ToolCall {
			return api.TurnToolCalls(dataDir, reg, log, c, turn)
		},
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

	turnOn := func(id int64, prompt string, n int) {
		t.Helper()
		if _, err := client.SendChat(ctx, id, prompt); err != nil {
			t.Fatal(err)
		}
		wait.Until(t, "the turn ends", func() bool {
			c, turns, err := client.GetChat(ctx, id)
			return err == nil && c.State == "idle" && len(turns) == n && turns[n-1].State == "done"
		})
	}
	src, err := client.CreateChat(ctx, apiclient.CreateChatRequest{ProjectID: p.ID, Title: "src", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	turnOn(src.ID, "first question", 1)
	turnOn(src.ID, "second question", 2)

	// A same-agent fork ignores no model: it is refused, since it copies.
	_, err = client.ForkChat(ctx, src.ID, apiclient.ForkRequest{Turn: 2, Model: "x"})
	if apiErr := (*apiclient.Error)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("same-agent fork with a model = %v, want 400", err)
	}
	_, err = client.ForkChat(ctx, src.ID, apiclient.ForkRequest{Turn: 2, Agent: "nope"})
	if apiErr := (*apiclient.Error)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("fork onto an unknown agent = %v, want 400", err)
	}

	fork, err := client.ForkChat(ctx, src.ID, apiclient.ForkRequest{Turn: 2, Agent: "codex"})
	if err != nil {
		t.Fatalf("cross-agent fork: %v", err)
	}
	fc := fork.Chat
	if !fork.Seeded || fc.Agent != "codex" || fc.ResumePending != "seeded" || fc.SessionID != "" {
		t.Fatalf("fork = %+v", fork)
	}
	if fc.ForkedFrom == nil || fc.ForkedFrom.Agent != "claude" || fc.ForkedFrom.ChatID != src.ID {
		t.Errorf("forked_from = %+v", fc.ForkedFrom)
	}
	if !strings.Contains(fc.PendingPreamble, "first question") || !strings.Contains(fc.PendingPreamble, "held with claude") {
		t.Errorf("pending_preamble:\n%s", fc.PendingPreamble)
	}
	// The kept turn 1 ran claude-shaped fakeagent, which calls Edit: its
	// summary is read from the real transcript through the real normalizer
	// (review F6), not a stub.
	if want := "<tools>\n" + chatexport.ToolSummary("Edit", "") + "\n</tools>"; strings.Count(fc.PendingPreamble, want) != 1 {
		t.Errorf("pending_preamble does not summarize turn 1's Edit call as %q:\n%s", want, fc.PendingPreamble)
	}
	got, _, err := client.GetChat(ctx, fc.ID)
	if err != nil || got.PendingPreamble != fc.PendingPreamble || got.ForkedFrom == nil || got.ForkedFrom.Agent != "claude" {
		t.Errorf("GET does not render turn 0 / forked_from.agent: %+v, %v", got, err)
	}
	// A list row says it is seeded but leaves the context to GET (review F3).
	rows, err := client.ListChats(ctx, apiclient.ListChatsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, row := range rows {
		if row.ID == fc.ID {
			listed = true
			if row.ResumePending != "seeded" || row.PendingPreamble != "" {
				t.Errorf("list row: resume_pending %q, pending_preamble %d bytes; want seeded and none",
					row.ResumePending, len(row.PendingPreamble))
			}
		}
	}
	if !listed {
		t.Errorf("the fork is not listed")
	}

	edited := "EDITED-CONTEXT: the human asked a first question, and you answered it.\n"
	patched, err := client.PatchChat(ctx, fc.ID, apiclient.PatchChatRequest{PendingPreamble: &edited})
	if err != nil || patched.PendingPreamble != edited {
		t.Fatalf("PATCH pending_preamble = %+v, %v", patched, err)
	}
	// A preamble edit travels alone.
	title := "renamed"
	_, err = client.PatchChat(ctx, fc.ID, apiclient.PatchChatRequest{PendingPreamble: &edited, Title: &title})
	if apiErr := (*apiclient.Error)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("preamble with a title = %v, want 400", err)
	}

	// The export shows the pending turn 0 and where the fork came from.
	exportMD := func() string {
		t.Helper()
		out, err := client.ChatExportMarkdown(ctx, fc.ID)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if md := exportMD(); !strings.Contains(md, "## "+chatexport.TurnZeroHeading) || !strings.Contains(md, "EDITED-CONTEXT") ||
		!strings.Contains(md, fmt.Sprintf("**forked from:** #%d (claude), before turn 2", src.ID)) {
		t.Errorf("export before the first turn lacks turn 0:\n%s", md)
	}

	turnOn(fc.ID, "third question", 1)
	// Once consumed, turn 0 lives on the turn that ran it (decision 4).
	if md := exportMD(); !strings.Contains(md, "### "+chatexport.TurnZeroHeading) || !strings.Contains(md, "EDITED-CONTEXT") ||
		strings.Contains(md, chatexport.NotePendingPreamble) {
		t.Errorf("export after the first turn lacks the consumed turn 0:\n%s", md)
	}
	data, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "EDITED-CONTEXT") || !strings.Contains(last, "third question") ||
		strings.Contains(last, "held with claude") {
		t.Errorf("codex was not handed the edited context:\n%s", last)
	}
	_, turns, err := client.GetChat(ctx, fc.ID)
	if err != nil || len(turns) != 1 || !turns[0].Seeded {
		t.Errorf("fork's turn 1 = %+v, %v", turns, err)
	}

	// The plan is consumed: a second edit is 409.
	_, err = client.PatchChat(ctx, fc.ID, apiclient.PatchChatRequest{PendingPreamble: &edited})
	if apiErr := (*apiclient.Error)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict ||
		apiErr.Code != api.CodeChatPreambleNotPending {
		t.Errorf("PATCH after a turn = %v, want 409 %s", err, api.CodeChatPreambleNotPending)
	}
	// A natively resumed fork has no preamble to edit.
	same, err := client.ForkChat(ctx, src.ID, apiclient.ForkRequest{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	if same.Chat.ResumePending != "resumed" || same.Chat.PendingPreamble != "" {
		t.Errorf("same-agent fork = %+v", same.Chat)
	}
	_, err = client.PatchChat(ctx, same.Chat.ID, apiclient.PatchChatRequest{PendingPreamble: &edited})
	if apiErr := (*apiclient.Error)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("PATCH on a resumed fork = %v, want 409", err)
	}

	// A restricted source forked onto codex, which cannot keep a resumed
	// chat restricted, is refused by task 162's check — unless the caller
	// chooses the mode.
	restricted := "restricted"
	if _, err := client.PatchChat(ctx, src.ID, apiclient.PatchChatRequest{PermissionMode: &restricted}); err != nil {
		t.Fatalf("restrict the source: %v", err)
	}
	_, err = client.ForkChat(ctx, src.ID, apiclient.ForkRequest{Turn: 2, Agent: "codex"})
	if apiErr := (*apiclient.Error)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest ||
		apiErr.Code != api.CodeAgentCannotRestrictResume {
		t.Errorf("restricted source onto codex = %v, want 400 %s", err, api.CodeAgentCannotRestrictResume)
	}
	full, err := client.ForkChat(ctx, src.ID, apiclient.ForkRequest{Turn: 2, Agent: "codex", PermissionMode: "full-auto"})
	if err != nil || full.Chat.PermissionMode != "full-auto" {
		t.Fatalf("fork with permission_mode full-auto = %+v, %v", full, err)
	}
	// An archived seeded fork keeps its plan on the row, but has no next
	// turn to seed: the edit is refused as rewind and fork are (review F4).
	if _, err := client.ArchiveChat(ctx, full.Chat.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err = client.PatchChat(ctx, full.Chat.ID, apiclient.PatchChatRequest{PendingPreamble: &edited})
	if apiErr := (*apiclient.Error)(nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict ||
		apiErr.Code != "chat_terminal" {
		t.Errorf("PATCH on an archived fork = %v, want 409 chat_terminal", err)
	}
}
