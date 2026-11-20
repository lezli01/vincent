package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// seedSearchChat writes a chat with one finished turn straight into the
// store, then moves it to st.
func seedSearchChat(t *testing.T, h *projectHarness, projectID int64, title, prompt, answer string, st chatstate.State) int64 {
	t.Helper()
	ctx := t.Context()
	c := &store.Chat{
		ProjectID: projectID, Title: title, Agent: "claude",
		Branch: "vincent/s-" + url.PathEscape(title), BaseBranch: "main", PermissionMode: "full_auto",
	}
	if err := h.store.CreateChat(ctx, c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	turn, err := h.store.CreateChatTurn(ctx, c.ID, prompt)
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	turn.State = chatstate.TurnDone
	turn.ResultText = answer
	if err := h.store.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	if _, err := h.store.SetChatState(ctx, c.ID, st); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	return c.ID
}

func (h *projectHarness) searchChatIDs(t *testing.T, query string) []int64 {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, "/v1/chats/search?"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/chats/search?%s = %d %s", query, resp.StatusCode, body)
	}
	var out struct {
		Results []struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("search body: %v", err)
	}
	ids := []int64{}
	for _, r := range out.Results {
		ids = append(ids, r.Chat.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestChatSearchRoute: the route is not read as a chat id, `archived`
// defaults to all and splits the way GET /v1/chats does, project_id
// filters, the matches carry segments, and bad input is a typed 400.
func TestChatSearchRoute(t *testing.T) {
	h := newProjectHarness(t)
	ctx := t.Context()
	p1 := &store.Project{Name: "p1", Path: "/p1", DefaultBranch: "main"}
	p2 := &store.Project{Name: "p2", Path: "/p2", DefaultBranch: "main"}
	for _, p := range []*store.Project{p1, p2} {
		if err := h.store.CreateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	live := seedSearchChat(t, h, p1.ID, "live", "ask about walrus", "the walrus is fine", chatstate.Idle)
	archived := seedSearchChat(t, h, p1.ID, "old", "walrus again", "answered status429Body", chatstate.Archived)
	other := seedSearchChat(t, h, p2.ID, "elsewhere", "walrus elsewhere", "ok", chatstate.Idle)

	if got := h.searchChatIDs(t, "q=walrus"); !slices.Equal(got, []int64{live, archived, other}) {
		t.Errorf("default scope = %v, want every chat, archived included", got)
	}
	if got := h.searchChatIDs(t, "q=walrus&archived=false"); !slices.Equal(got, []int64{live, other}) {
		t.Errorf("archived=false = %v", got)
	}
	if got := h.searchChatIDs(t, "q=walrus&archived=true"); !slices.Equal(got, []int64{archived}) {
		t.Errorf("archived=true = %v", got)
	}
	if got := h.searchChatIDs(t, fmt.Sprintf("q=walrus&project_id=%d", p2.ID)); !slices.Equal(got, []int64{other}) {
		t.Errorf("project_id = %v", got)
	}

	resp, body := h.doJSON(t, http.MethodGet, "/v1/chats/search?q=429", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search = %d %s", resp.StatusCode, body)
	}
	var out struct {
		Results []struct {
			Chat struct {
				ID    int64  `json:"id"`
				State string `json:"state"`
			} `json:"chat"`
			Matches []struct {
				Seq      int    `json:"seq"`
				Field    string `json:"field"`
				Snippet  string `json:"snippet"`
				Segments []struct {
					Text  string `json:"text"`
					Match bool   `json:"match"`
				} `json:"segments"`
			} `json:"matches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].Chat.ID != archived || out.Results[0].Chat.State != "archived" {
		t.Fatalf("429 = %s, want the archived chat", body)
	}
	m := out.Results[0].Matches[0]
	var marked string
	for _, s := range m.Segments {
		if s.Match {
			marked += s.Text
		}
	}
	if m.Seq != 1 || m.Field != "result" || marked != "429" || m.Snippet != "answered status429Body" {
		t.Errorf("match = %+v, want turn 1's answer with 429 marked", m)
	}

	// Query syntax is only ever text: never a 500.
	for _, q := range []string{`"wal`, `walrus*`, `-walrus`, `NEAR(walrus)`, `AND NOT`, `(((`, `wal"rus`} {
		resp, body := h.doJSON(t, http.MethodGet, "/v1/chats/search?q="+url.QueryEscape(q), nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("q=%s = %d %s, want 200", q, resp.StatusCode, body)
		}
	}

	for _, tc := range []struct{ query, code string }{
		{"", CodeValidationFailed},
		{"q=", CodeValidationFailed},
		{"q=ab", CodeQueryTooShort},
		{"q=walrus+ab", CodeQueryTooShort},
		{"q=walrus&archived=include", CodeValidationFailed},
		{"q=walrus&project_id=x", CodeValidationFailed},
		{"q=walrus&limit=-1", CodeValidationFailed},
	} {
		resp, body := h.doJSON(t, http.MethodGet, "/v1/chats/search?"+tc.query, nil)
		var e map[string]any
		_ = json.Unmarshal(body, &e)
		if resp.StatusCode != http.StatusBadRequest || errCode(e) != tc.code {
			t.Errorf("%q = %d %s, want 400 %s", tc.query, resp.StatusCode, body, tc.code)
		}
	}

	// A POST to the literal path is a 405 for the search route, not a
	// chat-id route's answer.
	resp, _ = h.doJSON(t, http.MethodDelete, "/v1/chats/search", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /v1/chats/search = %d, want 405: the literal must not fall through to /v1/chats/{id}", resp.StatusCode)
	}
}
