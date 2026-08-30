package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// chatHarness is the chat spine over the *real* handlers: server + store +
// worktrees + a chat runner, with all three adapters pointed at fakeagent so
// the refusal of codex and cursor is decided by the adapters themselves rather
// than by a test double.
type chatHarness struct {
	*projectHarness
	runner    *chatrun.Runner
	repo      string
	projectID int64
	cfg       config.Config
}

func newChatHarness(t *testing.T) *chatHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "chats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	bin := func() string { return fake }
	reg := agent.NewRegistry(claude.New(bin), codex.New(bin), cursor.New(bin))
	h := &chatHarness{cfg: config.Default()}
	cfg := func() config.Config { return h.cfg }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())

	runner := chatrun.New(chatrun.Deps{
		Store: st, Config: cfg, Worktrees: wt, Agents: reg,
		DataDir: dataDir, Logger: log,
	})
	runner.Start(t.Context())
	t.Cleanup(runner.Stop)

	s := New(Deps{
		Token:       testToken,
		Config:      cfg,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      log,
		Store:       st,
		Git:         git,
		Worktrees:   wt,
		Agents:      reg,
		Catalog:     agent.NewCatalogCache(reg),
		Chats:       runner,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	h.projectHarness = &projectHarness{ts: ts, store: st, wt: wt}
	h.runner = runner

	h.repo = testrepo.Init(t, "main")
	resp, body := h.doJSON(t, http.MethodPost, "/v1/projects", map[string]any{"path": h.repo})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register project: %d %s", resp.StatusCode, body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("project body: %v", err)
	}
	h.projectID = p.ID
	return h
}

// create posts a chat and returns the status and the decoded body.
func (h *chatHarness) create(t *testing.T, agentName string) (int, map[string]any) {
	t.Helper()
	req := map[string]any{"project_id": h.projectID, "title": "a talk"}
	if agentName != "" {
		req["agent"] = agentName
	}
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats", req)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

// TestChatCreateRefusesAdaptersThatCannotResume is decision 3: the capability
// is stated, never emulated. The refusal happens at creation, so a human never
// gets a chat that cannot hold a conversation.
func TestChatCreateRefusesAdaptersThatCannotResume(t *testing.T) {
	h := newChatHarness(t)
	for _, name := range []string{"codex", "cursor"} {
		code, body := h.create(t, name)
		if code != http.StatusBadRequest {
			t.Fatalf("create on %s = %d, want 400 (%v)", name, code, body)
		}
		errObj, _ := body["error"].(map[string]any)
		if got := errObj["code"]; got != CodeAgentCannotResume {
			t.Fatalf("create on %s error code = %v, want %q", name, got, CodeAgentCannotResume)
		}
	}
	if code, body := h.create(t, "claude"); code != http.StatusCreated {
		t.Fatalf("create on claude = %d, want 201 (%v)", code, body)
	}
}

// TestChatsAreNotTasks: the two families never mix. A chat on the board would
// undo the whole reason it is a separate entity.
func TestChatsAreNotTasks(t *testing.T) {
	h := newChatHarness(t)
	if code, body := h.create(t, "claude"); code != http.StatusCreated {
		t.Fatalf("create = %d (%v)", code, body)
	}
	resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/tasks = %d", resp.StatusCode)
	}
	var tasks []json.RawMessage
	if err := json.Unmarshal(body, &tasks); err != nil {
		t.Fatalf("tasks body: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("GET /v1/tasks returned %d rows with only a chat in the store", len(tasks))
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/chats", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/chats = %d", resp.StatusCode)
	}
	var chats struct {
		Chats []json.RawMessage `json:"chats"`
	}
	if err := json.Unmarshal(body, &chats); err != nil {
		t.Fatalf("chats body: %v", err)
	}
	if len(chats.Chats) != 1 {
		t.Fatalf("GET /v1/chats = %d rows, want 1", len(chats.Chats))
	}
}

// TestChatSendOverTheCapIs409 is decision 1's wire half: refused immediately,
// with the §11 vocabulary, and never queued.
func TestChatSendOverTheCapIs409(t *testing.T) {
	h := newChatHarness(t)
	h.cfg.MaxParallelChats = 1
	t.Setenv("FAKEAGENT_SCENARIO", "hang")

	_, busy := h.create(t, "claude")
	_, other := h.create(t, "claude")
	busyID, otherID := int64(busy["id"].(float64)), int64(other["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/chats/%d/send", busyID), map[string]any{"message": "hold the line"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first send = %d %s", resp.StatusCode, body)
	}
	waitUntil(t, func() bool { return h.runner.Running(busyID) })

	resp, body = h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/chats/%d/send", otherID), map[string]any{"message": "me too"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("send over the cap = %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), CodeChatCapReached) {
		t.Fatalf("409 body %s does not carry %q", body, CodeChatCapReached)
	}
	// Refused, not parked: the chat is still idle and owns no turn.
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", otherID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET chat = %d", resp.StatusCode)
	}
	var got struct {
		Chat struct {
			State string `json:"state"`
		} `json:"chat"`
		Turns []json.RawMessage `json:"turns"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("chat body: %v", err)
	}
	if got.Chat.State != "idle" {
		t.Fatalf("refused chat state = %q, want idle", got.Chat.State)
	}
	if len(got.Turns) != 0 {
		t.Fatalf("refused send left %d turns behind", len(got.Turns))
	}
	h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/cancel", busyID), nil)
}

// TestChatTurnRendersThroughTheWire walks one turn end to end over the real
// handlers and checks the turn is readable afterwards — the result text, the
// accounting columns and the session the turn ran in.
func TestChatTurnRendersThroughTheWire(t *testing.T) {
	h := newChatHarness(t)
	_, created := h.create(t, "claude")
	id := int64(created["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/chats/%d/send", id), map[string]any{"message": "hello there"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send = %d %s", resp.StatusCode, body)
	}
	var turns struct {
		Turns []struct {
			State        string  `json:"state"`
			Prompt       string  `json:"prompt"`
			ResultText   string  `json:"result_text"`
			SessionID    string  `json:"session_id"`
			InputTokens  int64   `json:"input_tokens"`
			OutputTokens int64   `json:"output_tokens"`
			CostUSD      float64 `json:"cost_usd"`
		} `json:"turns"`
	}
	waitUntil(t, func() bool {
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", id), nil)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		turns.Turns = turns.Turns[:0]
		if json.Unmarshal(body, &turns) != nil || len(turns.Turns) == 0 {
			return false
		}
		return turns.Turns[0].State != "running"
	})
	got := turns.Turns[0]
	if got.State != "done" {
		t.Fatalf("turn state = %q, want done", got.State)
	}
	if got.Prompt != "hello there" {
		t.Fatalf("prompt = %q", got.Prompt)
	}
	if !strings.Contains(got.ResultText, "hello there") {
		t.Fatalf("result text %q did not round-trip the prompt", got.ResultText)
	}
	if got.SessionID == "" {
		t.Fatal("turn recorded no session id, so the next turn has nothing to resume")
	}
	// The accounting half of the gap the issue named: a conversation outside
	// vincent has no token or cost record at all.
	if got.InputTokens == 0 || got.OutputTokens == 0 {
		t.Fatalf("turn recorded no tokens: in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.CostUSD == 0 {
		t.Fatal("turn recorded no cost")
	}
}

// TestChatActionsIn409WhenTheFSMSaysNo: the API and the runner consult one
// definition of what may happen next (§5.5), the way §6's is consulted.
func TestChatActionsIn409WhenTheFSMSaysNo(t *testing.T) {
	h := newChatHarness(t)
	_, created := h.create(t, "claude")
	id := int64(created["id"].(float64))
	for _, path := range []string{"answer", "cancel"} {
		resp, body := h.doJSON(t, http.MethodPost,
			fmt.Sprintf("/v1/chats/%d/%s", id, path), map[string]any{"value": "x"})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s on an idle chat = %d %s, want 409", path, resp.StatusCode, body)
		}
	}
	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/archive", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d %s", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/chats/%d/send", id), map[string]any{"message": "hello?"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("send to an archived chat = %d %s, want 409", resp.StatusCode, body)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
