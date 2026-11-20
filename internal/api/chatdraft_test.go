package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// TestChatDraftRoutes: PUT then GET round-trips, a write may carry one field
// alone, the chat read embeds both and the list carries has_draft, the write
// publishes chat.draft_changed, and an ended chat answers 409.
func TestChatDraftRoutes(t *testing.T) {
	h := newProjectHarness(t)
	ctx := t.Context()
	p := &store.Project{Name: "p1", Path: "/p1", DefaultBranch: "main"}
	if err := h.store.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	id := seedSearchChat(t, h, p.ID, "draft", "first prompt", "ok", chatstate.Idle)
	path := fmt.Sprintf("/v1/chats/%d/draft", id)

	if resp, body := h.doJSON(t, http.MethodPut, path, map[string]any{"draft": "half", "stash": "aside"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d %s", resp.StatusCode, body)
	}
	if resp, body := h.doJSON(t, http.MethodPut, path, map[string]any{"draft": "half done"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT draft alone = %d %s", resp.StatusCode, body)
	}
	resp, body := h.doJSON(t, http.MethodGet, path, nil)
	var d chatDraftBody
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &d) != nil {
		t.Fatalf("GET = %d %s", resp.StatusCode, body)
	}
	if d.Draft != "half done" || d.Stash != "aside" || d.UpdatedAt == nil {
		t.Fatalf("GET = %+v", d)
	}

	_, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", id), nil)
	var got struct {
		Chat chatBody `json:"chat"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Chat.Draft != "half done" || got.Chat.Stash != "aside" || !got.Chat.HasDraft {
		t.Fatalf("chat read = %+v", got.Chat)
	}
	// The read carries the write's stamp, which is what orders it against a
	// client's own saves (review F1).
	if got.Chat.DraftUpdatedAt == nil || !got.Chat.DraftUpdatedAt.Equal(*d.UpdatedAt) {
		t.Fatalf("chat read draft_updated_at = %v, want %v", got.Chat.DraftUpdatedAt, d.UpdatedAt)
	}
	_, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats?project_id=%d", p.ID), nil)
	var list struct {
		Chats []chatBody `json:"chats"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Chats) != 1 || !list.Chats[0].HasDraft || list.Chats[0].Draft != "" || list.Chats[0].DraftUpdatedAt != nil {
		t.Fatalf("list = %s", body)
	}

	evs, err := h.store.ListEvents(ctx, store.EventFilter{Types: []string{store.EventChatDraft}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("%d chat.draft_changed events, want 2", len(evs))
	}

	if resp, body := h.doJSON(t, http.MethodPut, path, map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty PUT = %d %s", resp.StatusCode, body)
	}
	if _, err := h.store.SetChatState(ctx, id, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.doJSON(t, http.MethodPut, path, map[string]any{"draft": "x"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT on an archived chat = %d %s", resp.StatusCode, body)
	}
}

// TestChatPromptsRoute: "prompts" is not read as a chat id, a short query is
// answered by recency rather than query_too_short, a long one narrows, and
// archived chats are included.
func TestChatPromptsRoute(t *testing.T) {
	h := newProjectHarness(t)
	ctx := t.Context()
	p := &store.Project{Name: "p1", Path: "/p1", DefaultBranch: "main"}
	if err := h.store.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	seedSearchChat(t, h, p.ID, "live", "ask about walrus", "fine", chatstate.Idle)
	seedSearchChat(t, h, p.ID, "old", "explain the lexer", "done", chatstate.Archived)

	prompts := func(q string) []chatPromptBody {
		t.Helper()
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/prompts?project_id=%d&q=%s", p.ID, q), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET prompts q=%q = %d %s", q, resp.StatusCode, body)
		}
		var out struct {
			Prompts []chatPromptBody `json:"prompts"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Prompts
	}
	if got := prompts("ab"); len(got) != 2 || got[0].Prompt != "explain the lexer" || got[0].ChatState != "archived" {
		t.Fatalf("short query = %+v", got)
	}
	if got := prompts("walr"); len(got) != 1 || got[0].Prompt != "ask about walrus" {
		t.Fatalf("q=walr = %+v", got)
	}
	if resp, body := h.doJSON(t, http.MethodGet, "/v1/chats/prompts", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no project_id = %d %s", resp.StatusCode, body)
	}
}
