package tui

import (
	"context"
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
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestChatUsageCrossesTheWireLive holds task 157's wire against the real
// handlers: a turn's cache counts and context figures, and each adapter's
// compact_command, reach the chat workspace as the store recorded them — and a
// figure the store left null stays absent rather than arriving as zero.
func TestChatUsageCrossesTheWireLive(t *testing.T) {
	const token = "chat-usage-token"
	dir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dir, "usage.db"))
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
		ProjectID: project.ID, Title: "usage", State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, chat); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	turn, err := st.CreateChatTurn(ctx, chat.ID, "hello")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	ended := time.Now().UTC()
	ms, cr, cw, ctxTokens, window := int64(42_000), int64(16_000), int64(2_000), int64(170_000), int64(200_000)
	cost := 0.04
	turn.State, turn.EndedAt, turn.DurationMS = chatstate.TurnDone, &ended, &ms
	turn.InputTokens, turn.OutputTokens, turn.CostUSD = 200, 1100, &cost
	turn.CacheReadTokens, turn.CacheCreationTokens = &cr, &cw
	turn.ContextTokens, turn.ContextWindow = &ctxTokens, &window
	if err := st.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}

	missing := func() string { return filepath.Join(dir, "no-such-cli") }
	agents := agent.NewRegistry(claude.New(missing), codex.New(missing))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Broker: broker, Agents: agents,
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
	if len(v.turns) != 1 {
		t.Fatalf("loaded %d turns, want 1", len(v.turns))
	}
	got := v.turns[0]
	for name, p := range map[string]*int64{
		"cache_read_tokens": got.CacheReadTokens, "cache_creation_tokens": got.CacheCreationTokens,
		"context_tokens": got.ContextTokens, "context_window": got.ContextWindow,
	} {
		if p == nil {
			t.Errorf("%s did not cross the wire", name)
		}
	}
	if got.CacheReadTokens != nil && *got.CacheReadTokens != cr ||
		got.ContextTokens != nil && *got.ContextTokens != ctxTokens ||
		got.ContextWindow != nil && *got.ContextWindow != window {
		t.Errorf("the turn arrived as %+v", got)
	}

	agentsMsg, ok := runCmd(t, v.loadAgentsCmd(), 30*time.Second).(chatAgentsMsg)
	if !ok {
		t.Fatal("the agent list did not load")
	}
	v.updateMsg(agentsMsg)
	if v.compactCmds["claude"] != "/compact" {
		t.Errorf("claude's compact_command arrived as %q", v.compactCmds["claude"])
	}
	if c, have := v.compactCmds["codex"]; have {
		t.Errorf("codex has a compact command %q", c)
	}
	served, err := v.client.ListAgents(ctx, false)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	for _, a := range served {
		if a.CompactCommand == nil {
			t.Errorf("%s: compact_command is null with a registry to ask", a.Name)
		}
	}

	heading := ansi.Strip(chatTurnHeading("claude", got, ""))
	for _, want := range []string{"42.0s", "18.2k↑ 1.1k↓", "$0.04"} {
		if !strings.Contains(heading, want) {
			t.Errorf("the heading %q lacks %q", heading, want)
		}
	}
	gauge, hot := chatGauge(v.turns, true, v.gaugeCompactKey())
	if !hot || !strings.Contains(ansi.Strip(gauge), "85%") || !strings.Contains(ansi.Strip(gauge), "alt+k compact") {
		t.Errorf("the gauge reads %q (hot=%v)", ansi.Strip(gauge), hot)
	}

	// A turn whose adapter reported none of it: absent, never zero.
	other := &store.Chat{
		ProjectID: project.ID, Title: "bare", State: chatstate.Idle, Agent: "codex",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := st.CreateChat(ctx, other); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	bare, err := st.CreateChatTurn(ctx, other.ID, "again")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	bare.State, bare.EndedAt = chatstate.TurnDone, &ended
	if err := st.UpdateChatTurn(ctx, bare); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	_, turns, err := v.client.GetChat(ctx, other.ID)
	if err != nil || len(turns) != 1 {
		t.Fatalf("GetChat: %d turns, %v", len(turns), err)
	}
	if b := turns[0]; b.CacheReadTokens != nil || b.CacheCreationTokens != nil || b.ContextTokens != nil || b.ContextWindow != nil {
		t.Errorf("an unreported figure arrived as %+v", b)
	}
	if g, _ := chatGauge(turns, true, ""); g != "" {
		t.Errorf("a turn with no context figure drew a gauge %q", ansi.Strip(g))
	}
}
