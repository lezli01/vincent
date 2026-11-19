package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// `vincent chat search` (task 158) against the real GET /v1/chats/search
// handler: the grouped text output, --json, the archived scope, and a
// too-short query's exit 1.
func TestChatSearchCommand(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	turn := h.addTurn(t, chat, chatstate.TurnDone, "")
	turn.ResultText = "the handler returns status429Body"
	if err := h.st.UpdateChatTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetChatState(t.Context(), chat.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}

	out, errOut, code := runCLI(t, "chat", "search", "429")
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, errOut)
	}
	out = strings.ReplaceAll(out, "\r\n", "\n")
	for _, want := range []string{"archived", "talk", "turn 1 answer", "the handler returns status429Body"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	out, errOut, code = runCLI(t, "chat", "search", "429", "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d (%s)", code, errOut)
	}
	var results []apiclient.ChatSearchResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].Chat.ID != chat.ID || results[0].Matches[0].Seq != 1 {
		t.Errorf("--json = %+v, want the chat matched at turn 1", results)
	}

	out, _, code = runCLI(t, "chat", "search", "429", "--archived", "false", "--json")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("--archived false = %d %q, want an empty array", code, out)
	}
	out, _, code = runCLI(t, "chat", "search", "nothing-like-this")
	if code != 0 || !strings.Contains(out, "No matches.") {
		t.Errorf("no match = %d %q", code, out)
	}
	_, errOut, code = runCLI(t, "chat", "search", "42")
	if code != 1 || !strings.Contains(errOut, "3 characters") {
		t.Errorf("short query = %d %q, want exit 1 naming the minimum", code, errOut)
	}
}
