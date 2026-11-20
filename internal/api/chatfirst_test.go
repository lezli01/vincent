package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// TestDeriveChatTitle is task 156 decision 2's rule on its own.
func TestDeriveChatTitle(t *testing.T) {
	long := strings.Repeat("word ", 30)
	for _, tc := range []struct{ name, title, message, want string }{
		{"typed title wins", "  mine  ", "the message", "mine"},
		{"first non-empty line", "", "\n\n   fix   the\tbuild  \nsecond line", "fix the build"},
		{"both empty", " ", " \n\t", ""},
		{"exact fit", "", strings.Repeat("a", ChatTitleMaxRunes), strings.Repeat("a", ChatTitleMaxRunes)},
	} {
		if got := DeriveChatTitle(tc.title, tc.message); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	got := DeriveChatTitle("", long)
	if !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) > ChatTitleMaxRunes {
		t.Fatalf("long message title = %q (%d runes)", got, utf8.RuneCountInString(got))
	}
	if strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") || !strings.HasSuffix(strings.TrimSuffix(got, "…"), "word") {
		t.Fatalf("long message title %q is not cut at a word boundary", got)
	}
	one := DeriveChatTitle("", strings.Repeat("é", 200))
	if utf8.RuneCountInString(one) != ChatTitleMaxRunes {
		t.Fatalf("one long word cut to %d runes, want %d", utf8.RuneCountInString(one), ChatTitleMaxRunes)
	}
}

type createdChat struct {
	chatBody
	FirstTurn *chatTurnBody `json:"first_turn"`
}

func (h *chatHarness) createWith(t *testing.T, req map[string]any) (int, createdChat, []byte) {
	t.Helper()
	req["project_id"] = h.projectID
	resp, body := h.doJSON(t, http.MethodPost, "/v1/chats", req)
	var out createdChat
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out, body
}

// TestChatCreateWithMessage is task 156 decision 1 and 2: one request makes
// the chat and starts turn 1 with the message byte for byte, and the empty
// title is derived from it — the branch slug with it.
func TestChatCreateWithMessage(t *testing.T) {
	h := newChatHarness(t)
	msg := "  Fix the flaky build on Windows\nit times out in CI"
	code, c, body := h.createWith(t, map[string]any{"agent": "claude", "message": msg})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if c.Title != "Fix the flaky build on Windows" {
		t.Fatalf("title = %q", c.Title)
	}
	if !strings.HasSuffix(c.Branch, "-fix-the-flaky-build-on-windows") {
		t.Fatalf("branch %q does not follow the derived title", c.Branch)
	}
	if c.FirstTurn == nil || c.FirstTurn.Seq != 1 {
		t.Fatalf("first_turn = %+v (%s)", c.FirstTurn, body)
	}
	if c.FirstTurn.Prompt != msg {
		t.Fatalf("stored prompt = %q, want the message byte for byte", c.FirstTurn.Prompt)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings = %v", c.Warnings)
	}
	turns, err := h.store.ListChatTurns(t.Context(), c.ID)
	if err != nil || len(turns) != 1 || turns[0].Prompt != msg {
		t.Fatalf("turns = %+v, %v", turns, err)
	}
	waitUntil(t, func() bool { return !h.runner.Running(c.ID) })

	// Without a message, first_turn is present and null.
	code, _, body = h.createWith(t, map[string]any{"agent": "claude", "title": "plain"})
	if code != http.StatusCreated || !strings.Contains(string(body), `"first_turn":null`) {
		t.Fatalf("create without message = %d %s", code, body)
	}
	// Neither a title nor a message is the 400.
	code, _, body = h.createWith(t, map[string]any{"agent": "claude", "title": " ", "message": "\n "})
	if code != http.StatusBadRequest || !strings.Contains(string(body), "a title or a message is required") {
		t.Fatalf("empty create = %d %s", code, body)
	}
}

// TestChatCreateKeepsTheChatWhenTheFirstSendIsRefused: over the cap, the
// chat is still created, first_turn is null and a warning says why.
func TestChatCreateKeepsTheChatWhenTheFirstSendIsRefused(t *testing.T) {
	h := newChatHarness(t)
	h.cfg.MaxParallelChats = 1
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	code, busy, body := h.createWith(t, map[string]any{"agent": "claude", "message": "hold the line"})
	if code != http.StatusCreated || busy.FirstTurn == nil {
		t.Fatalf("first create = %d %s", code, body)
	}
	waitUntil(t, func() bool { return h.runner.Running(busy.ID) })
	defer h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/cancel", busy.ID), nil)

	code, other, body := h.createWith(t, map[string]any{"agent": "claude", "message": "me too"})
	if code != http.StatusCreated {
		t.Fatalf("create over the cap = %d %s, want 201", code, body)
	}
	if other.FirstTurn != nil {
		t.Fatalf("first_turn = %+v, want null", other.FirstTurn)
	}
	if len(other.Warnings) != 1 || !strings.Contains(other.Warnings[0], "not sent") ||
		!strings.Contains(other.Warnings[0], CodeChatCapReached) {
		t.Fatalf("warnings = %v", other.Warnings)
	}
	stored, err := h.store.GetChat(t.Context(), other.ID)
	if err != nil || stored.State != chatstate.Idle || stored.Title != "me too" {
		t.Fatalf("kept chat = %+v, %v", stored, err)
	}
}

// TestChatRename is task 156 decision 4: legal in idle and terminal states,
// never renames the branch, refuses an empty title and unknown fields, and
// publishes chat.updated.
func TestChatRename(t *testing.T) {
	h := newChatHarness(t)
	_, created := h.create(t, "claude")
	id := int64(created["id"].(float64))
	branch := created["branch"].(string)
	path := fmt.Sprintf("/v1/chats/%d", id)

	resp, body := h.doJSON(t, http.MethodPatch, path, map[string]any{"title": "  a better name "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename = %d %s", resp.StatusCode, body)
	}
	var got chatBody
	_ = json.Unmarshal(body, &got)
	if got.Title != "a better name" || got.Branch != branch {
		t.Fatalf("renamed = %q on %q, want the branch %q untouched", got.Title, got.Branch, branch)
	}
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{Types: []string{store.EventChatUpdated}})
	if err != nil || len(events) != 1 || !strings.Contains(string(events[0].Payload), "a better name") {
		t.Fatalf("chat.updated events = %+v, %v", events, err)
	}

	for _, bad := range []map[string]any{
		{"title": "   "},
		{},
		{"title": strings.Repeat("x", ChatTitleMaxRunes+1)},
		{"title": "x", "model": "opus"},
	} {
		if resp, body := h.doJSON(t, http.MethodPatch, path, bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("rename %v = %d %s, want 400", bad, resp.StatusCode, body)
		}
	}

	if resp, body := h.doJSON(t, http.MethodPost, path+"/archive", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d %s", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodPatch, path, map[string]any{"title": "renamed after archive"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename archived = %d %s", resp.StatusCode, body)
	}
	_ = json.Unmarshal(body, &got)
	if got.State != string(chatstate.Archived) || got.Title != "renamed after archive" {
		t.Fatalf("archived rename = %+v", got)
	}
	if resp, _ := h.doJSON(t, http.MethodPatch, "/v1/chats/9999", map[string]any{"title": "x"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("rename missing = %d, want 404", resp.StatusCode)
	}
}

// TestLinkedChatOpensWithAMessage: on the task-chat route, `message` runs
// turn 1 with the opening context prepended, as any first turn has it.
func TestLinkedChatOpensWithAMessage(t *testing.T) {
	h := newLinkedHarness(t)
	task := h.blockedWithWorktree(t)
	code, body := h.post(t, fmt.Sprintf("/v1/tasks/%d/chat", task.ID), map[string]any{"message": "why did it fail?"})
	if code != http.StatusCreated {
		t.Fatalf("open chat: %d %s", code, body)
	}
	var c createdChat
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if c.FirstTurn == nil || c.FirstTurn.Prompt != "why did it fail?" {
		t.Fatalf("first_turn = %+v", c.FirstTurn)
	}
	if c.Title != task.Title {
		t.Fatalf("title = %q, want the task's %q ahead of derivation", c.Title, task.Title)
	}
	turns := h.waitIdle(t, c.ID, 1)
	if !strings.Contains(turns[0].ResultText, "<task id=") {
		t.Errorf("turn 1 was not sent the opening context: %q", turns[0].ResultText)
	}
}
