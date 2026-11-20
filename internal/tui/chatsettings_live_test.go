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

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestChatSettingsLive is task 162's TUI against the real handlers: the
// alt+p picker PATCHes the chat's model, shift+tab lowers to restricted at
// once and asks before raising back to full-auto, the composer border's chip
// follows, and a turn's heading names the model it ran on.
func TestChatSettingsLive(t *testing.T) {
	const token = "chat-settings-token"
	dir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dir, "settings.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	chat := &store.Chat{
		ProjectID: project.ID, Title: "settings", State: chatstate.Idle, Agent: "claude",
		Model: "haiku", PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, chat); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	turn, err := st.CreateChatTurn(ctx, chat.ID, "hello")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	ended := time.Now().UTC()
	turn.State, turn.EndedAt = chatstate.TurnDone, &ended
	if err := st.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}

	missing := func() string { return filepath.Join(dir, "no-such-cli") }
	agents := agent.NewRegistry(claude.New(missing))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Agents: agents,
		Catalog: agent.NewCatalogCache(agents),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = apiclient.New(ts.URL, token)
	v.chatID = chat.ID
	v.now = time.Now
	loaded, ok := runCmd(t, v.loadCmd(), 30*time.Second).(chatLoadedMsg)
	if !ok || loaded.err != nil {
		t.Fatalf("loading the chat produced %#v", loaded)
	}
	v.applyLoaded(loaded)
	if h := ansi.Strip(chatTurnHeading("claude", v.turns[0], "")); !strings.Contains(h, "haiku") {
		t.Errorf("turn heading %q does not name the model the turn ran on", h)
	}
	agentsMsg, ok := runCmd(t, v.loadAgentsCmd(), 30*time.Second).(chatAgentsMsg)
	if !ok {
		t.Fatal("the agent list did not load")
	}
	v.updateMsg(agentsMsg)
	if got := ansi.Strip(v.render(120, 30)); !strings.Contains(got, "haiku · default effort · full-auto") {
		t.Errorf("composer border carries no settings chip:\n%s", got)
	}

	// alt+p, then a model row, then enter: a PATCH.
	v.updateKey(registryKey(t, "alt+p"))
	if v.picker == nil {
		t.Fatal("alt+p opened no picker")
	}
	want := ""
	for i, r := range v.picker.rows {
		if r.field == "model" && r.value != "" && r.value != "haiku" {
			v.picker.cursor, want = i, r.value
			break
		}
	}
	if want == "" {
		t.Fatal("the picker offers no other model from claude's catalog")
	}
	_, cmd := v.updateKey(registryKey(t, "enter"))
	v.updateMsg(runCmd(t, cmd, 30*time.Second))
	if got, _ := st.GetChat(ctx, chat.ID); got.Model != want || v.chat.Model != want {
		t.Errorf("after the picker the chat runs on %q (view %q), want %q", got.Model, v.chat.Model, want)
	}

	// shift+tab lowers at once.
	_, cmd = v.updateKey(registryKey(t, "shift+tab"))
	if v.raising || cmd == nil {
		t.Fatal("shift+tab on a full-auto chat asked instead of lowering it")
	}
	v.updateMsg(runCmd(t, cmd, 30*time.Second))
	if got, _ := st.GetChat(ctx, chat.ID); got.PermissionMode != "restricted" {
		t.Fatalf("after shift+tab the chat is %q, want restricted", got.PermissionMode)
	}
	// And asks before raising: n keeps it restricted, y raises it.
	v.updateKey(registryKey(t, "shift+tab"))
	if !v.raising {
		t.Fatal("shift+tab on a restricted chat raised it without asking")
	}
	v.updateKey(keyPress("n"))
	if got, _ := st.GetChat(ctx, chat.ID); got.PermissionMode != "restricted" {
		t.Fatalf("declining the confirmation left the chat %q", got.PermissionMode)
	}
	v.updateKey(registryKey(t, "shift+tab"))
	_, cmd = v.updateKey(keyPress("y"))
	v.updateMsg(runCmd(t, cmd, 30*time.Second))
	if got, _ := st.GetChat(ctx, chat.ID); got.PermissionMode != "full-auto" {
		t.Fatalf("confirming left the chat %q, want full-auto", got.PermissionMode)
	}
}

// TestChatModeToggleSaysWhyLive is decision 2 against the real handlers:
// where only one mode is legal, shift+tab says why and changes nothing.
// Which adapter cannot keep a resumed turn restricted comes from GET
// /v1/agents rather than a name the TUI knows (review F3), and a chat linked
// to a restricted task, once the daemon has refused its raise naming the
// task, is not asked about again.
func TestChatModeToggleSaysWhyLive(t *testing.T) {
	const token = "chat-mode-token"
	dir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dir, "mode.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	codexChat := &store.Chat{
		ProjectID: project.ID, Title: "codex", State: chatstate.Idle, Agent: "codex",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, codexChat); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	task := &store.Task{
		ProjectID: project.ID, Title: "clamped", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/1-clamped", State: store.TaskBlocked,
		WorktreePath: t.TempDir(), Restricted: true,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	linked := &store.Chat{Title: "why", Agent: "claude", PermissionMode: string(agent.Restricted)}
	if err := st.OpenLinkedChat(ctx, task.ID, store.TaskBlocked, linked); err != nil {
		t.Fatal(err)
	}

	missing := func() string { return filepath.Join(dir, "no-such-cli") }
	agents := agent.NewRegistry(claude.New(missing), codex.New(missing))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Agents: agents,
		Catalog: agent.NewCatalogCache(agents),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	open := func(id int64) *chatView {
		v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
		v.client = apiclient.New(ts.URL, token)
		v.chatID = id
		v.now = time.Now
		loaded, ok := runCmd(t, v.loadCmd(), 30*time.Second).(chatLoadedMsg)
		if !ok || loaded.err != nil {
			t.Fatalf("loading chat %d produced %#v", id, loaded)
		}
		v.applyLoaded(loaded)
		agentsMsg, ok := runCmd(t, v.loadAgentsCmd(), 30*time.Second).(chatAgentsMsg)
		if !ok {
			t.Fatal("the agent list did not load")
		}
		v.updateMsg(agentsMsg)
		return v
	}

	// codex: the daemon says it cannot keep a resumed turn restricted.
	v := open(codexChat.ID)
	if _, cmd := v.updateKey(registryKey(t, "shift+tab")); cmd != nil || v.raising {
		t.Fatal("shift+tab on a codex chat tried to change its mode")
	}
	if !strings.Contains(v.note, "codex cannot keep a resumed turn restricted") {
		t.Errorf("shift+tab on codex said %q", v.note)
	}

	// A chat on a restricted task: the first raise asks and the daemon
	// refuses, naming the task; the next press says so without asking.
	v = open(linked.ID)
	v.updateKey(registryKey(t, "shift+tab"))
	_, cmd := v.updateKey(keyPress("y"))
	v.updateMsg(runCmd(t, cmd, 30*time.Second))
	want := fmt.Sprintf("task #%d runs restricted", task.ID)
	if !strings.Contains(v.note, want) {
		t.Errorf("the refused raise said %q, want %q", v.note, want)
	}
	if _, cmd := v.updateKey(registryKey(t, "shift+tab")); cmd != nil || v.raising {
		t.Fatal("shift+tab asked again on a chat the daemon said can never run full-auto")
	}
	if !strings.Contains(v.note, want) {
		t.Errorf("the second shift+tab said %q, want %q", v.note, want)
	}
	if got, _ := st.GetChat(ctx, linked.ID); got.PermissionMode != "restricted" {
		t.Fatalf("the linked chat is %q, want restricted", got.PermissionMode)
	}
}
