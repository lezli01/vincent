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

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// The `@` file picker against the real handlers (task 126.11): the chat
// workspace asks GET /v1/chats/{id}/files, the daemon enumerates the chat's
// worktree with git and builds each row's mention with the claude adapter's
// own quoting rule, and the text the human picks reaches that CLI byte for
// byte on the next turn.
//
// It is the seam the model tests cannot cover — they hand chatFileList an
// answer — and it is §5.5's and task 124 decision 9's claim: vincent sends
// the message as typed, so a picked mention is **not rewritten** anywhere
// between the list and the agent's stdin. The path it picks has a space in
// it, because that is the one form where a client that rebuilt the insert
// text would produce something different from the adapter.

// chatFilesLive is the workspace wired to a real daemon stack over httptest.
type chatFilesLive struct {
	view       *chatView
	promptFile string
}

func newChatFilesLive(t *testing.T) *chatFilesLive {
	t.Helper()
	const token = "chat-files-token"

	// echo-prompt writes each turn's prompt where this test can read it back
	// exactly (issue #323).
	promptFile := filepath.Join(t.TempDir(), "prompts.jsonl")
	t.Setenv("FAKEAGENT_SCENARIO", "echo-prompt")
	t.Setenv("FAKEAGENT_PROMPT_FILE", promptFile)

	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "files.db"))
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
	work := liveFilesRepo(t)
	chat := &store.Chat{
		ProjectID: project.ID, Title: "files", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: work,
	}
	if err := st.CreateChat(ctx, chat); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	fake := agenttest.BuildFakeAgent(t)
	agents := agent.NewRegistry(claude.New(func() string { return fake }))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Agents: agents, DataDir: dataDir, Logger: log,
	})
	chats.Start(t.Context())
	t.Cleanup(chats.Stop)

	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Broker: broker, Agents: agents,
		Catalog: agent.NewCatalogCache(agents), Chats: chats,
		Worktrees: worktree.NewManager(gitx.New(), dataDir),
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
	return &chatFilesLive{view: v, promptFile: promptFile}
}

// liveFilesRepo is a repository holding one path with a space in it, with the
// developer's global core.excludesFile taken out of the answer the way
// internal/worktree's own file tests do it: a personal global ignore rule
// would otherwise remove the row this test picks.
func liveFilesRepo(t *testing.T) string {
	t.Helper()
	dir := testrepo.Init(t, "main")
	excludes := filepath.Join(t.TempDir(), "empty-excludes")
	if err := os.WriteFile(excludes, nil, 0o644); err != nil {
		t.Fatalf("write empty excludes file: %v", err)
	}
	testrepo.Run(t, dir, "config", "core.excludesFile", excludes)
	if err := os.MkdirAll(filepath.Join(dir, "dir with space"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	testrepo.WriteFile(t, dir, filepath.Join("dir with space", "notes.md"), "notes\n")
	return dir
}

// prompts is every prompt the fake CLI has been handed so far.
func (h *chatFilesLive) prompts(t *testing.T) []string {
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

// TestChatFilesPickedMentionReachesTheAgentLive is the whole path: type `@dir`,
// accept, send — and the exact text the daemon's adapter named is what the CLI
// was handed, quotes and interior space included.
func TestChatFilesPickedMentionReachesTheAgentLive(t *testing.T) {
	h := newChatFilesLive(t)
	v := h.view

	// The typed token is what asks for the listing: the picker is opened by
	// the draft and by no key at all.
	var answer chatFilesMsg
	for _, r := range "@dir" {
		_, cmd := v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		for _, msg := range chatCmdMsgs(cmd) {
			if m, ok := msg.(chatFilesMsg); ok {
				answer = m
			}
		}
	}
	if answer.chatID == 0 {
		t.Fatal("typing @dir asked the daemon for no listing")
	}
	if answer.err != nil {
		t.Fatalf("GET /v1/chats/%d/files: %v", v.chatID, answer.err)
	}
	v.applyFiles(answer)
	if !v.files.open {
		t.Fatalf("the daemon's listing did not open a picker: %q", v.note)
	}

	// The daemon's claude adapter quotes a path with a space in it, and the
	// row carries its bytes. Nothing in the TUI built this string.
	const want = `@"dir with space/notes.md"`
	row, _ := v.files.pick()
	if row.insert != want {
		t.Fatalf("the top row inserts %q, want the adapter's %q", row.insert, want)
	}

	v.updateKey(registryKey(t, "tab"))
	if got := v.composer.Value(); got != want+" " {
		t.Fatalf("the draft is %q, want %q", got, want+" ")
	}
	v.composer.InsertString("please read it")
	sent := runCmd(t, v.sendCmd(), 60*time.Second)
	if msg, ok := sent.(chatSentMsg); !ok || msg.err != nil {
		t.Fatalf("sending produced %#v", sent)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		if got := h.prompts(t); len(got) > 0 {
			if got[0] != want+" please read it" {
				t.Fatalf("the CLI was handed %q, want %q byte for byte",
					got[0], want+" please read it")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never reached the fake CLI")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
