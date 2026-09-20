package tui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// The skill list against the real handlers (task 124.13): the chat workspace
// asks GET /v1/chats/{id}/skills, the daemon's claude adapter answers it by
// spawning the CLI in input mode (task 124.7), and the invocation the human
// picks reaches that CLI byte for byte on the next turn.
//
// It is the seam the model tests cannot cover: they hand chatView an answer,
// and the claim here is that the one the daemon builds is the same shape,
// and that nothing between the list and the agent's argv rewrites it.

// chatSkillsLive is the workspace wired to a real daemon stack over httptest.
type chatSkillsLive struct {
	view       *chatView
	chat       *store.Chat
	promptFile string
}

func newChatSkillsLive(t *testing.T) *chatSkillsLive {
	t.Helper()
	const token = "chat-skills-token"

	// echo-prompt writes each run's prompt where this test can read it back
	// exactly (issue #323). The skill probe never reaches the scenario: its
	// first stdin line is claude's `initialize` control_request, which
	// fakeagent answers before any scenario runs.
	promptFile := filepath.Join(t.TempDir(), "prompts.jsonl")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)
	// Past the adapter's skill-listing floor (internal/agent/claude/list.go):
	// below it claude answers ErrSkillsUnsupported and there is no list to
	// pick from.
	t.Setenv("FAKEAGENT_VERSION", "2.1.277")

	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "skills.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	chat := &store.Chat{
		ProjectID: project.ID, Title: "skills", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, chat); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	fake := agenttest.BuildFakeAgent(t)
	agents := agent.NewRegistry(claude.New(func() string { return fake }))
	cache := agent.NewSkillCache()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Agents: agents, DataDir: dataDir, Logger: log,
		InvalidateSkills: cache.Invalidate,
	})
	chats.Start(t.Context())
	t.Cleanup(chats.Stop)

	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Broker: broker, Agents: agents,
		Catalog: agent.NewCatalogCache(agents), Chats: chats, Skills: cache,
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
	return &chatSkillsLive{view: v, chat: chat, promptFile: promptFile}
}

// prompts is every prompt the fake CLI has been handed so far.
func (h *chatSkillsLive) prompts(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(h.promptFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the prompt file: %v", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var one string
		if err := json.Unmarshal([]byte(line), &one); err != nil {
			t.Fatalf("prompt line %q: %v", line, err)
		}
		out = append(out, one)
	}
	return out
}

// TestChatSkillsPickedInvocationReachesTheAgentLive is the whole path: tab,
// filter, accept, send — and the exact text the daemon's adapter named is
// what the CLI was handed.
func TestChatSkillsPickedInvocationReachesTheAgentLive(t *testing.T) {
	h := newChatSkillsLive(t)
	v := h.view

	cmd := v.openSkills()
	if cmd == nil {
		t.Fatal("tab asked the daemon nothing")
	}
	answer, ok := runCmd(t, cmd, 60*time.Second).(chatSkillsMsg)
	if !ok {
		t.Fatalf("the fetch produced something other than a chatSkillsMsg")
	}
	if answer.err != nil {
		t.Fatalf("GET /v1/chats/%d/skills: %v", v.chatID, answer.err)
	}
	v.applySkills(answer)
	if !v.skills.open {
		t.Fatalf("the daemon's answer did not open a list: %q", v.note)
	}
	// The daemon's claude adapter drops the built-in and keeps the two
	// skills fakeagent's default `initialize` reply carries (task 124.7).
	if got := skillRowNames(v); len(got) != 2 {
		t.Fatalf("the list holds %v, want the two skills claude reported", got)
	}
	want := v.skills.rows[0].invocation
	if !strings.HasPrefix(want, "/") {
		t.Fatalf("claude's invocation is %q, want the adapter's leading sigil", want)
	}

	// Filter down to that one row, then take it with tab.
	for _, r := range "fake-skill" {
		v.updateKey(keyPress(string(r)))
	}
	if got := skillRowNames(v); len(got) != 1 || got[0] != want {
		t.Fatalf("filtering left %v, want just %q", got, want)
	}
	v.updateKey(registryKey(t, "tab"))
	if got := v.composer.Value(); got != want+" " {
		t.Fatalf("the draft is %q, want %q", got, want+" ")
	}

	v.composer.InsertString("the login page")
	sent := runCmd(t, v.sendCmd(), 60*time.Second)
	if msg, ok := sent.(chatSentMsg); !ok || msg.err != nil {
		t.Fatalf("sending produced %#v", sent)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		if got := h.prompts(t); len(got) > 0 {
			if got[0] != want+" the login page" {
				t.Fatalf("the CLI was handed %q, want %q byte for byte",
					got[0], want+" the login page")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never reached the fake CLI")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
