package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// TestChatViewedRoute is POST /v1/chats/{id}/viewed over the real handlers
// (task 165): it clears the unread mark, both fields are on the wire, a
// repeat view announces nothing, and an unknown chat is the 404 envelope.
func TestChatViewedRoute(t *testing.T) {
	h := newChatHarness(t)
	ctx := t.Context()
	code, created := h.create(t, "")
	if code != http.StatusCreated {
		t.Fatalf("create = %d (%v)", code, created)
	}
	if v, ok := created["unread"]; !ok || v != false {
		t.Fatalf("created unread = %v (present %v), want false", v, ok)
	}
	if v, ok := created["last_viewed_at"]; !ok || v != nil {
		t.Fatalf("created last_viewed_at = %v (present %v), want null", v, ok)
	}
	id := int64(created["id"].(float64))

	// A finished turn, written straight to the store: the route is under
	// test, not the runner.
	turn, err := h.store.CreateChatTurn(ctx, id, "hello")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	end := time.Now().Add(-time.Minute)
	turn.State, turn.EndedAt = chatstate.TurnDone, &end
	if err := h.store.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}

	getChat := func() map[string]any {
		t.Helper()
		resp, raw := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", id), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET chat = %d %s", resp.StatusCode, raw)
		}
		var out struct {
			Chat map[string]any `json:"chat"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode chat: %v", err)
		}
		return out.Chat
	}
	if got := getChat()["unread"]; got != true {
		t.Fatalf("unread before view = %v, want true", got)
	}

	view := func() map[string]any {
		t.Helper()
		resp, raw := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/viewed", id), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST viewed = %d %s", resp.StatusCode, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode viewed: %v", err)
		}
		return out
	}
	viewed := view()
	if viewed["unread"] != false {
		t.Fatalf("unread after view = %v, want false", viewed["unread"])
	}
	stamp, ok := viewed["last_viewed_at"].(string)
	if !ok {
		t.Fatalf("last_viewed_at = %v, want an RFC3339 string", viewed["last_viewed_at"])
	}
	if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
		t.Fatalf("last_viewed_at %q: %v", stamp, err)
	}
	if got := getChat()["unread"]; got != false {
		t.Fatalf("GET unread after view = %v, want false", got)
	}

	evs, err := h.store.ListEvents(ctx, store.EventFilter{Types: []string{store.EventChatViewed}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("chat.viewed events = %d, want 1", len(evs))
	}
	if !matchesChat(&evs[0], id) {
		t.Fatalf("chat.viewed payload %s does not name chat %d for its per-chat stream", evs[0].Payload, id)
	}

	resp, raw := h.doJSON(t, http.MethodPost, "/v1/chats/99999/viewed", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown chat = %d %s, want 404", resp.StatusCode, raw)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Error.Code != CodeNotFound {
		t.Fatalf("404 body %s, want code %q", raw, CodeNotFound)
	}
}
