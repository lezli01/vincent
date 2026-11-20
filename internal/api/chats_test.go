package api

import (
	"context"
	"encoding/json"
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
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// chatHarness is the chat spine over the *real* handlers: server + store +
// worktrees + a chat runner, with all three shipped adapters pointed at
// fakeagent so what a chat may be created on is decided by the adapters
// themselves rather than by a test double. agenttest.StubNonResuming rides
// alongside them for the one thing none of the three says any more: no.
type chatHarness struct {
	*projectHarness
	runner    *chatrun.Runner
	repo      string
	dataDir   string
	projectID int64
	cfg       config.Config
}

func newChatHarness(t *testing.T) *chatHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "chats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	bin := func() string { return fake }
	reg := agent.NewRegistry(
		claude.New(bin), codex.New(bin), cursor.New(bin),
		agenttest.StubNonResuming{},
	)
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
		Dirs:        config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	h.projectHarness = &projectHarness{ts: ts, store: st, wt: wt}
	h.runner = runner
	h.dataDir = dataDir

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

// TestChatCreateRefusesAdaptersThatCannotResume is task 063 decision 3: the
// capability is stated, never emulated. The refusal happens at creation, so a
// human never gets a chat that cannot hold a conversation.
//
// The subject is the stub, not a shipped adapter. Task 070 taught codex and
// cursor to resume and the two names this used to iterate then asserted the
// opposite of the truth — which is exactly the failure mode a refusal pinned
// to whichever CLI happens to lack the capability today will keep having.
func TestChatCreateRefusesAdaptersThatCannotResume(t *testing.T) {
	h := newChatHarness(t)
	code, body := h.create(t, agenttest.NonResumingName)
	if code != http.StatusBadRequest {
		t.Fatalf("create on %s = %d, want 400 (%v)", agenttest.NonResumingName, code, body)
	}
	errObj, _ := body["error"].(map[string]any)
	if got := errObj["code"]; got != CodeAgentCannotResume {
		t.Fatalf("error code = %v, want %q", got, CodeAgentCannotResume)
	}
	// And every shipped adapter is now on the other side of it (task 070).
	for _, name := range []string{"claude", "codex", "cursor"} {
		if code, body := h.create(t, name); code != http.StatusCreated {
			t.Fatalf("create on %s = %d, want 201 (%v)", name, code, body)
		}
	}
}

// TestChatsAreAlwaysFullAuto was task 072 decision 1's structural guard: a
// chat's mode was not requestable, because `codex exec resume` has no
// --sandbox and so no argv spelling for `restricted` (§9.3). Task 160
// decision 1 supersedes that decision explicitly, and this is the guard for
// the rule that replaced it: the mode is requestable, the default is still
// full-auto, and the hazard is refused where the mode is chosen — a
// restricted codex chat, PATCH or turn is a typed 400, never a resumed turn
// that quietly runs full-auto.
func TestChatsAreAlwaysFullAuto(t *testing.T) {
	h := newChatHarness(t)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats", map[string]any{
		"project_id": h.projectID, "title": "a talk",
		"agent": "codex", "permission_mode": string(agent.Restricted),
	})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), CodeAgentCannotRestrictResume) {
		t.Fatalf("create a restricted codex chat = %d %s, want 400 %s", resp.StatusCode, body, CodeAgentCannotRestrictResume)
	}
	// The default, on every adapter, is still full-auto.
	ids := map[string]int64{}
	for _, name := range []string{"claude", "codex", "cursor"} {
		code, out := h.create(t, name)
		if code != http.StatusCreated {
			t.Fatalf("create on %s = %d (%v)", name, code, out)
		}
		id := int64(out["id"].(float64))
		ids[name] = id
		c, err := h.store.GetChat(t.Context(), id)
		if err != nil {
			t.Fatalf("GetChat: %v", err)
		}
		if c.PermissionMode != string(agent.FullAuto) {
			t.Fatalf("chat on %s = %q, want the default %q", name, c.PermissionMode, agent.FullAuto)
		}
	}
	// PATCH and a per-turn override refuse a restricted codex chat the same way.
	path := fmt.Sprintf("/v1/chats/%d", ids["codex"])
	resp, body = h.doJSON(t, http.MethodPatch, path, map[string]any{"permission_mode": "restricted"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), CodeAgentCannotRestrictResume) {
		t.Errorf("PATCH codex restricted = %d %s, want 400 %s", resp.StatusCode, body, CodeAgentCannotRestrictResume)
	}
	resp, body = h.doJSON(t, http.MethodPost, path+"/send", map[string]any{"message": "hi", "permission_mode": "restricted"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), CodeAgentCannotRestrictResume) {
		t.Errorf("restricted codex turn = %d %s, want 400 %s", resp.StatusCode, body, CodeAgentCannotRestrictResume)
	}
	if c, _ := h.store.GetChat(t.Context(), ids["codex"]); c.PermissionMode != string(agent.FullAuto) {
		t.Errorf("codex chat mode = %q after refusals, want full-auto", c.PermissionMode)
	}
	// claude can keep a resumed turn restricted, so it may be lowered.
	resp, body = h.doJSON(t, http.MethodPatch, fmt.Sprintf("/v1/chats/%d", ids["claude"]),
		map[string]any{"permission_mode": "restricted"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"permission_mode":"restricted"`) {
		t.Errorf("PATCH claude restricted = %d %s, want 200", resp.StatusCode, body)
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

	// Both reads say how many turns the chat has and what it runs under.
	var read struct {
		Chat struct {
			TurnCount      *int   `json:"turn_count"`
			PermissionMode string `json:"permission_mode"`
		} `json:"chat"`
		Chats []struct {
			ID        int64 `json:"id"`
			TurnCount *int  `json:"turn_count"`
		} `json:"chats"`
	}
	_, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", id), nil)
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatal(err)
	}
	if read.Chat.TurnCount == nil || *read.Chat.TurnCount != 1 || read.Chat.PermissionMode != string(agent.FullAuto) {
		t.Fatalf("GET /v1/chats/%d = %s", id, body)
	}
	_, body = h.doJSON(t, http.MethodGet, "/v1/chats", nil)
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Chats) != 1 || read.Chats[0].TurnCount == nil || *read.Chats[0].TurnCount != 1 {
		t.Fatalf("GET /v1/chats = %s", body)
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

// TestChatArchiveRefusalNamesTheRealState is issue #298's third defect.
// `handleChatArchive` consults `chatstate.Allowed(state, Archive)` — legal
// from `idle` alone — and then writes one hardcoded sentence, "a chat with a
// live turn cannot be archived", for every state that is not `idle`. Both
// terminal states are among them (§5.5, task 074 decision 5), and for both the
// sentence asserts something false: an `archived` or `handed_off` chat holds
// no process at all (§11), so there is no live turn to name. The `state` in
// the error details carries the truth the message contradicts.
//
// Statuses are already pinned by TestChatActionsIn409WhenTheFSMSaysNo and
// TestHandoffIsTerminal, which is how a wrong message survived both: this
// reads the sentence a human is shown.
func TestChatArchiveRefusalNamesTheRealState(t *testing.T) {
	h := newChatHarness(t)

	archived, _ := handoffFixture(t, h)
	if resp, body := h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/chats/%d/archive", archived), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d %s", resp.StatusCode, body)
	}

	handed, _ := handoffFixture(t, h)
	if code, body := h.handoff(t, handed, map[string]any{"title": "finish it"}); code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}

	for _, tc := range []struct {
		id    int64
		state string
		// names is vocabulary the refusal must use to be about this state
		// rather than about a running turn; the exact sentence is the fix's
		// to choose.
		names string
	}{
		{archived, "archived", "archiv"},
		{handed, "handed_off", "hand"},
	} {
		resp, raw := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/archive", tc.id), nil)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("archive on a %s chat = %d %s, want 409", tc.state, resp.StatusCode, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s refusal: %v", tc.state, err)
		}
		errObj, _ := out["error"].(map[string]any)
		msg, _ := errObj["message"].(string)
		details, _ := errObj["details"].(map[string]any)
		if got := details["state"]; got != tc.state {
			t.Fatalf("details.state = %v, want %q", got, tc.state)
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "live turn") {
			t.Errorf("archive on a %s chat says %q — it claims a live turn it never checked for, and none can be running in a terminal state", tc.state, msg)
		}
		if !strings.Contains(low, tc.names) {
			t.Errorf("archive on a %s chat says %q — the message never names the state that actually blocked it, which details.state = %q reports", tc.state, msg, tc.state)
		}
	}
}

// TestChatListArchivedParam is issue #298's first defect. `store.ChatFilter`
// carries only ProjectID and States and `handleChatList` reads only
// `project_id` and repeated `state`, so nothing anywhere can ask for a chat
// listing without its terminal rows — which is why they accumulate on the
// chats board forever (§15). Tasks have had `?archived=false|true|all`,
// defaulting to exclusion, since §13.2; chats get the same parameter and the
// same default, and it covers **both** terminal states, `archived` and
// `handed_off` alike (§5.5, task 074 decision 5). An explicit `state=` still
// wins, exactly as it does for tasks.
func TestChatListArchivedParam(t *testing.T) {
	h := newChatHarness(t)

	live, _ := handoffFixture(t, h)
	gone, _ := handoffFixture(t, h)
	if resp, body := h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/chats/%d/archive", gone), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d %s", resp.StatusCode, body)
	}
	handed, _ := handoffFixture(t, h)
	if code, body := h.handoff(t, handed, map[string]any{"title": "finish it"}); code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}

	ids := func(query string) []int64 {
		t.Helper()
		resp, raw := h.doJSON(t, http.MethodGet, "/v1/chats"+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/chats%s = %d %s", query, resp.StatusCode, raw)
		}
		var out struct {
			Chats []struct {
				ID int64 `json:"id"`
			} `json:"chats"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode listing: %v", err)
		}
		got := make([]int64, 0, len(out.Chats))
		for _, c := range out.Chats {
			got = append(got, c.ID)
		}
		return got
	}
	has := func(got []int64, want int64) bool {
		for _, id := range got {
			if id == want {
				return true
			}
		}
		return false
	}

	for _, q := range []string{"", "?archived=false"} {
		got := ids(q)
		if len(got) != 1 || got[0] != live {
			t.Errorf("GET /v1/chats%q = %v, want only the idle chat %d — archived %d and handed-off %d must be excluded by default",
				q, got, live, gone, handed)
		}
	}
	if got := ids("?archived=true"); len(got) != 2 || !has(got, gone) || !has(got, handed) {
		t.Errorf("archived=true = %v, want both terminal chats %d and %d", got, gone, handed)
	}
	if got := ids("?archived=all"); len(got) != 3 {
		t.Errorf("archived=all = %v, want all three", got)
	}
	if got := ids("?state=archived"); len(got) != 1 || got[0] != gone {
		t.Errorf("state=archived = %v, want %d — an explicit state must win", got, gone)
	}
	resp, raw := h.doJSON(t, http.MethodGet, "/v1/chats?archived=yes", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("archived=yes = %d %s, want 400", resp.StatusCode, raw)
	}
}

// TestChatCreateOutlivesItsCaller: the chat row is written before its
// worktree exists, so a client that gives up mid-create — its deadline, an
// Esc in the TUI — must not cancel the git work and strand a chat with no
// worktree. The request arrives already cancelled, which is the worst case of
// a caller leaving: the create still finishes whole.
func TestChatCreateOutlivesItsCaller(t *testing.T) {
	h := newChatHarness(t)
	body := fmt.Sprintf(`{"project_id":%d,"title":"a talk"}`, h.projectID)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chats", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ts.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create under a cancelled request: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		ID           int64  `json:"id"`
		WorktreePath string `json:"worktree_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.WorktreePath == "" {
		t.Fatal("created chat has no worktree")
	}
	if _, err := os.Stat(out.WorktreePath); err != nil {
		t.Fatalf("worktree %s: %v", out.WorktreePath, err)
	}
	chat, err := h.store.GetChat(t.Context(), out.ID)
	if err != nil {
		t.Fatalf("get chat: %v", err)
	}
	if chat.WorktreePath != out.WorktreePath {
		t.Fatalf("stored worktree %q, answered %q", chat.WorktreePath, out.WorktreePath)
	}
}
