package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

// listChatIDs is GET /v1/chats?{query} as the listed ids, newest first.
func (h *chatHarness) listChatIDs(t *testing.T, query string) []int64 {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, "/v1/chats?"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/chats?%s = %d %s", query, resp.StatusCode, body)
	}
	var out struct {
		Chats []struct {
			ID int64 `json:"id"`
		} `json:"chats"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("chats body: %v", err)
	}
	ids := []int64{}
	for _, c := range out.Chats {
		ids = append(ids, c.ID)
	}
	return ids
}

// TestChatListByIssue is #931 over the API: `issue_id` is parsed the way
// GET /v1/tasks parses it, an unknown issue lists nothing, and a well-formed
// one lists the chats opened on it and handed off into its work, with
// `archived` keeping its default.
func TestChatListByIssue(t *testing.T) {
	h := newChatHarness(t)
	for _, bad := range []string{"abc", "0", "-3"} {
		resp, body := h.doJSON(t, http.MethodGet, "/v1/chats?issue_id="+bad, nil)
		var e map[string]any
		_ = json.Unmarshal(body, &e)
		if resp.StatusCode != http.StatusBadRequest || errCode(e) != CodeValidationFailed {
			t.Errorf("issue_id=%s = %d %s, want 400 %s", bad, resp.StatusCode, body, CodeValidationFailed)
		}
	}

	onIssue := h.newIssue(t, "talked about")
	handedTo := h.newIssue(t, "worked on")
	issueChat, _, _ := h.openIssueChat(t, onIssue.ID)
	free, _ := handoffFixture(t, h)
	if code, body := h.handoff(t, free, map[string]any{"issue_id": handedTo.ID}); code != http.StatusCreated {
		t.Fatalf("handoff = %d (%v)", code, body)
	}
	unrelated, _ := handoffFixture(t, h)

	if got := h.listChatIDs(t, "issue_id=9999"); len(got) != 0 {
		t.Errorf("unknown issue = %v, want []", got)
	}
	if got := h.listChatIDs(t, fmt.Sprintf("issue_id=%d", onIssue.ID)); !reflect.DeepEqual(got, []int64{issueChat}) {
		t.Errorf("issue_id=%d = %v, want [%d]", onIssue.ID, got, issueChat)
	}
	// The handed-off chat is terminal, so the default hides it.
	if got := h.listChatIDs(t, fmt.Sprintf("issue_id=%d", handedTo.ID)); len(got) != 0 {
		t.Errorf("issue_id=%d by default = %v, want the handed-off chat hidden", handedTo.ID, got)
	}
	if got := h.listChatIDs(t, fmt.Sprintf("issue_id=%d&archived=all", handedTo.ID)); !reflect.DeepEqual(got, []int64{free}) {
		t.Errorf("issue_id=%d&archived=all = %v, want [%d]", handedTo.ID, got, free)
	}
	for _, id := range h.listChatIDs(t, fmt.Sprintf("issue_id=%d&archived=all&project_id=%d", onIssue.ID, h.projectID)) {
		if id == unrelated {
			t.Errorf("issue filter listed the unrelated chat %d", unrelated)
		}
	}
}
