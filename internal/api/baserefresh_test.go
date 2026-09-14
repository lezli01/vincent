package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// topLevelField returns one top-level field of a JSON object verbatim, and
// whether it was present at all — the difference between null and omitted is
// the point of the assertions that use it.
func topLevelField(t *testing.T, body []byte, name string) (string, bool) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	v, ok := m[name]
	return string(v), ok
}

// remoteTip is what origin's branch points at, asked of the remote itself
// rather than of a remote-tracking ref a fetch may or may not have moved.
func remoteTip(t *testing.T, repo, branch string) string {
	t.Helper()
	fields := strings.Fields(testrepo.Run(t, repo, "ls-remote", "origin", "refs/heads/"+branch))
	if len(fields) == 0 {
		t.Fatalf("origin has no %s", branch)
	}
	return fields[0]
}

// TestTaskDetailServesBaseRefresh: GET /v1/tasks/{id} carries base_sha and the
// base refresh record once a worktree claim wrote them (§13.2, task 099), and
// base_refresh is JSON null — not omitted — before it did.
func TestTaskDetailServesBaseRefresh(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	path := fmt.Sprintf("/v1/tasks/%d", task.ID)

	resp, body := h.doJSON(t, http.MethodGet, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	if raw, ok := topLevelField(t, body, "base_refresh"); !ok || raw != "null" {
		t.Errorf("base_refresh = %q (present %v), want null", raw, ok)
	}
	if raw, ok := topLevelField(t, body, "base_sha"); ok {
		t.Errorf("base_sha = %s on a task with none, want it omitted", raw)
	}

	sha := strings.Repeat("ab", 20)
	refresh := &store.BaseRefresh{
		Fetch: store.BaseFetch{Remote: "origin", Ref: "refs/heads/main", Result: "fetched"},
		FastForward: store.BaseFastForward{
			Result: "skipped", Reason: "error", Error: "cannot lock ref 'refs/heads/main'",
		},
	}
	if err := h.store.ClaimTaskWorktree(t.Context(), task.ID, t.TempDir(), sha, refresh); err != nil {
		t.Fatalf("ClaimTaskWorktree: %v", err)
	}

	resp, body = h.doJSON(t, http.MethodGet, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	got := decodeTask(t, body)
	if got.BaseSHA != sha {
		t.Errorf("base_sha = %q, want %q", got.BaseSHA, sha)
	}
	want := baseRefreshBody{
		Fetch: baseFetchBody{Result: "fetched", Remote: "origin", Ref: "refs/heads/main"},
		FastForward: baseFastForwardBody{
			Result: "skipped", Reason: "error", Error: "cannot lock ref 'refs/heads/main'",
		},
	}
	if got.BaseRefresh == nil || *got.BaseRefresh != want {
		t.Errorf("base_refresh = %+v, want %+v", got.BaseRefresh, want)
	}
}

// createChatBody posts a chat and decodes the typed body.
func (h *chatHarness) createChatBody(t *testing.T) chatBody {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats",
		map[string]any{"project_id": h.projectID, "title": "a talk", "agent": "claude"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create chat: %d %s", resp.StatusCode, body)
	}
	var out chatBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode chat: %v (%s)", err, body)
	}
	return out
}

// TestChatCreateBaseRefreshHonorsTheFetchKey: a chat's base is refreshed the
// way a task's is, which includes not refreshing it when `fetch_base_branch`
// is false (§10, task 099). The remote is ahead, so a fetch would have had
// something to do, and the local base staying put proves none ran.
func TestChatCreateBaseRefreshHonorsTheFetchKey(t *testing.T) {
	h := newChatHarness(t)
	h.cfg.FetchBaseBranch = false
	advanceRemoteBase(t, h.repo, "main")
	before := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/main")

	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats",
		map[string]any{"project_id": h.projectID, "title": "a talk", "agent": "claude"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create chat: %d %s", resp.StatusCode, body)
	}
	if raw, ok := topLevelField(t, body, "base_sha"); ok {
		t.Errorf("base_sha = %s with the fetch off, want it omitted", raw)
	}
	var chat chatBody
	if err := json.Unmarshal(body, &chat); err != nil {
		t.Fatalf("decode chat: %v", err)
	}
	want := baseRefreshBody{
		Fetch:       baseFetchBody{Result: "disabled"},
		FastForward: baseFastForwardBody{Result: "not_attempted"},
	}
	if chat.BaseRefresh == nil || *chat.BaseRefresh != want {
		t.Errorf("base_refresh = %+v, want %+v", chat.BaseRefresh, want)
	}

	stored, err := h.store.GetChat(t.Context(), chat.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.BaseSHA != "" {
		t.Errorf("stored base_sha = %q, want none", stored.BaseSHA)
	}
	if r := renderBaseRefresh(stored.BaseRefresh); r == nil || *r != want {
		t.Errorf("stored base_refresh = %+v, want %+v", stored.BaseRefresh, want)
	}
	if after := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/main"); after != before {
		t.Errorf("local main moved %s -> %s with the fetch off", before, after)
	}
}

// TestChatCreateBaseRefreshFetchesTheUpstreamTip: with the key on, against a
// base that tracks a remote which is ahead, the chat starts from the remote's
// tip and says so; the clean local base is fast-forwarded to it.
func TestChatCreateBaseRefreshFetchesTheUpstreamTip(t *testing.T) {
	h := newChatHarness(t)
	advanceRemoteBase(t, h.repo, "main")
	tip := remoteTip(t, h.repo, "main")

	chat := h.createChatBody(t)
	if chat.BaseSHA != tip {
		t.Errorf("base_sha = %q, want the remote tip %q", chat.BaseSHA, tip)
	}
	r := chat.BaseRefresh
	if r == nil {
		t.Fatal("base_refresh is null after a fetch")
	}
	if want := (baseFetchBody{Result: "fetched", Remote: "origin", Ref: "refs/heads/main"}); r.Fetch != want {
		t.Errorf("fetch = %+v, want %+v", r.Fetch, want)
	}
	if r.FastForward.Result != "advanced" || r.FastForward.Worktree == "" {
		t.Errorf("fast_forward = %+v, want advanced in the project checkout", r.FastForward)
	}

	stored, err := h.store.GetChat(t.Context(), chat.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.BaseSHA != tip {
		t.Errorf("stored base_sha = %q, want %q", stored.BaseSHA, tip)
	}
	if got := renderBaseRefresh(stored.BaseRefresh); got == nil || *got != *r {
		t.Errorf("stored base_refresh = %+v, want the served %+v", stored.BaseRefresh, *r)
	}
	if local := testrepo.Run(t, h.repo, "rev-parse", "refs/heads/main"); local != tip {
		t.Errorf("local main = %s, want it fast-forwarded to %s", local, tip)
	}
}

// TestHandoffCarriesBaseRefresh: a handed-off task shows what its workspace
// started from — the chat's base_sha and base refresh, read back from GET
// /v1/tasks/{id} rather than from the handoff response.
func TestHandoffCarriesBaseRefresh(t *testing.T) {
	h := newChatHarness(t)
	advanceRemoteBase(t, h.repo, "main")
	chat := h.createChatBody(t)
	if chat.BaseSHA == "" || chat.BaseRefresh == nil {
		t.Fatalf("fixture is wrong: chat has no base refresh (%+v)", chat)
	}

	code, body := h.handoff(t, chat.ID, map[string]any{"title": "finish it"})
	if code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	task, _ := body["task"].(map[string]any)
	id, ok := task["id"].(float64)
	if !ok {
		t.Fatalf("handoff body has no task id: %v", body)
	}

	resp, raw := h.doJSON(t, http.MethodGet, "/v1/tasks/"+strconv.FormatInt(int64(id), 10), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, raw)
	}
	got := decodeTask(t, raw)
	if got.BaseSHA != chat.BaseSHA {
		t.Errorf("task base_sha = %q, want the chat's %q", got.BaseSHA, chat.BaseSHA)
	}
	if got.BaseRefresh == nil || *got.BaseRefresh != *chat.BaseRefresh {
		t.Errorf("task base_refresh = %+v, want the chat's %+v", got.BaseRefresh, *chat.BaseRefresh)
	}
}
