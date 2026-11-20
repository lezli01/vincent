package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

func (h *chatHarness) patchChat(t *testing.T, id int64, req map[string]any) (int, string) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPatch, fmt.Sprintf("/v1/chats/%d", id), req)
	return resp.StatusCode, string(body)
}

func (h *chatHarness) newChatID(t *testing.T, agentName string) int64 {
	t.Helper()
	code, out := h.create(t, agentName)
	if code != http.StatusCreated {
		t.Fatalf("create on %s = %d (%v)", agentName, code, out)
	}
	return int64(out["id"].(float64))
}

// TestChatPatchChangesEachField is task 162 decision 5: each field changes on
// its own, and the chat.updated event is written for it.
func TestChatPatchChangesEachField(t *testing.T) {
	h := newChatHarness(t)
	id := h.newChatID(t, "claude")
	for _, req := range []map[string]any{
		{"title": "renamed"}, {"model": "sonnet"}, {"effort": "high"}, {"permission_mode": "restricted"},
	} {
		if code, body := h.patchChat(t, id, req); code != http.StatusOK {
			t.Fatalf("PATCH %v = %d %s", req, code, body)
		}
	}
	c, err := h.store.GetChat(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "renamed" || c.Model != "sonnet" || c.Effort != "high" || c.PermissionMode != "restricted" {
		t.Errorf("chat = %q %q %q %q, want renamed sonnet high restricted", c.Title, c.Model, c.Effort, c.PermissionMode)
	}
	evs, err := h.store.ListEvents(t.Context(), store.EventFilter{Types: []string{store.EventChatUpdated}})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == store.EventChatUpdated {
			n++
		}
	}
	if n != 4 {
		t.Errorf("chat.updated events = %d, want 4", n)
	}
	if code, body := h.patchChat(t, id, map[string]any{"title": "  "}); code != http.StatusBadRequest {
		t.Errorf("PATCH empty title = %d %s, want 400", code, body)
	}
	if code, body := h.patchChat(t, id, map[string]any{"permission_mode": "plan"}); code != http.StatusBadRequest {
		t.Errorf("PATCH permission_mode plan = %d %s, want 400: the vocabulary does not grow", code, body)
	}
}

// TestChatPatchValidatesAgainstTheCatalog: a model from another adapter's
// catalog is a 400, through the same Check task creation uses.
func TestChatPatchValidatesAgainstTheCatalog(t *testing.T) {
	h := newChatHarness(t)
	id := h.newChatID(t, "claude")
	cats := agent.NewCatalogCache(agent.NewRegistry(claude.New(nil), codex.New(nil))).Catalogs()
	var foreign string
	for _, m := range cats["codex"].Models {
		if !slices.ContainsFunc(cats["claude"].Models, func(o agent.Option) bool { return o.Value == m.Value }) {
			foreign = m.Value
			break
		}
	}
	if foreign == "" {
		t.Skip("no codex-only model in the curated catalogs")
	}
	if code, body := h.patchChat(t, id, map[string]any{"model": foreign}); code != http.StatusBadRequest {
		t.Errorf("PATCH a codex model onto a claude chat = %d %s, want 400", code, body)
	}
}

// TestChatPatchRefusesATerminalChat: an archived chat's settings are history.
func TestChatPatchRefusesATerminalChat(t *testing.T) {
	h := newChatHarness(t)
	id := h.newChatID(t, "claude")
	if _, err := h.store.SetChatState(t.Context(), id, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	if code, body := h.patchChat(t, id, map[string]any{"model": "sonnet"}); code != http.StatusConflict {
		t.Errorf("PATCH archived chat = %d %s, want 409", code, body)
	}
}

// TestChatPatchModelNeedsTheCapability is decision 8: before the first turn
// any adapter may change model; after it, only one whose resumed run is
// captured honouring a change — claude, not codex.
func TestChatPatchModelNeedsTheCapability(t *testing.T) {
	h := newChatHarness(t)
	codexID, claudeID := h.newChatID(t, "codex"), h.newChatID(t, "claude")
	if code, body := h.patchChat(t, codexID, map[string]any{"model": "gpt-5"}); code != http.StatusOK {
		t.Fatalf("PATCH codex model before the first turn = %d %s, want 200", code, body)
	}
	for _, id := range []int64{codexID, claudeID} {
		if _, err := h.store.SetChatSession(t.Context(), id, "sess"); err != nil {
			t.Fatal(err)
		}
	}
	code, body := h.patchChat(t, codexID, map[string]any{"model": "gpt-5-mini"})
	if code != http.StatusBadRequest || !strings.Contains(body, CodeAgentCannotChangeOnResume) {
		t.Errorf("PATCH codex model after a turn = %d %s, want 400 %s", code, body, CodeAgentCannotChangeOnResume)
	}
	resp, rb := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/send", codexID),
		map[string]any{"message": "hi", "effort": "low"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(rb), CodeAgentCannotChangeOnResume) {
		t.Errorf("codex effort override after a turn = %d %s, want 400", resp.StatusCode, rb)
	}
	// Restating the current value is not a change.
	if code, body := h.patchChat(t, codexID, map[string]any{"model": "gpt-5", "permission_mode": "full-auto"}); code != http.StatusOK {
		t.Errorf("PATCH codex unchanged model = %d %s, want 200", code, body)
	}
	if code, body := h.patchChat(t, claudeID, map[string]any{"model": "opus"}); code != http.StatusOK {
		t.Errorf("PATCH claude model after a turn = %d %s, want 200", code, body)
	}
	resp, rb = h.doJSON(t, http.MethodGet, "/v1/agents", nil)
	var agents struct {
		Agents []struct {
			Name   string `json:"name"`
			Change *bool  `json:"supports_resume_settings_change"`
			// Restricted is decision 1's adapter half, published so the
			// TUI keeps no copy of which adapter it is (review F3).
			Restricted *bool `json:"supports_restricted_resume"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rb, &agents); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/agents = %d %v", resp.StatusCode, err)
	}
	for _, a := range agents.Agents {
		want := a.Name == "claude"
		if a.Change == nil || *a.Change != want {
			t.Errorf("%s supports_resume_settings_change = %v, want %v", a.Name, a.Change, want)
		}
		if wantR := a.Name != "codex"; a.Restricted == nil || *a.Restricted != wantR {
			t.Errorf("%s supports_restricted_resume = %v, want %v", a.Name, a.Restricted, wantR)
		}
	}
}

// TestLinkedRestrictedChatCannotRise is decision 3: a chat linked to a
// restricted task is never raised to full-auto, by PATCH or by an override,
// and the 409 names the task.
func TestLinkedRestrictedChatCannotRise(t *testing.T) {
	h := newChatHarness(t)
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.projectID, Title: "clamped", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/1-clamped", State: store.TaskBlocked,
		WorktreePath: h.repo, Restricted: true,
	}
	if err := h.store.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	c := &store.Chat{Title: "why", Agent: "claude", PermissionMode: string(agent.Restricted)}
	if err := h.store.OpenLinkedChat(ctx, task.ID, store.TaskBlocked, c); err != nil {
		t.Fatal(err)
	}
	code, body := h.patchChat(t, c.ID, map[string]any{"permission_mode": "full-auto"})
	if code != http.StatusConflict || !strings.Contains(body, fmt.Sprintf(`"linked_task_id":"%d"`, task.ID)) {
		t.Errorf("PATCH linked restricted chat to full-auto = %d %s, want 409 naming task %d", code, body, task.ID)
	}
	resp, rb := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/send", c.ID),
		map[string]any{"message": "hi", "permission_mode": "full-auto"})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("full-auto override on a linked restricted chat = %d %s, want 409", resp.StatusCode, rb)
	}
}
