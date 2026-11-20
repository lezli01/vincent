package cli

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// `vincent chat find` against the real GET /v1/chats/{id}/search handler
// (task 160): a turn whose transcript mentions the text is listed with its
// count, case-insensitively, one whose transcript is gone is searched
// through its result text, and an empty query is refused.
func TestChatFindLive(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	h.addTurn(t, chat, chatstate.TurnDone, "", lineOutput)
	gone := h.addTurn(t, chat, chatstate.TurnDone, "", lineOutput)
	gone.ResultText = "the FAILING test again"
	if err := h.st.UpdateChatTurn(t.Context(), gone); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.turnTranscriptPath(chat.ID, gone.Seq)); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(chat.ID, 10)

	out, errOut, code := runCLI(t, "chat", "find", id, "Failing")
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, errOut)
	}
	got := strings.Split(strings.TrimSpace(strings.ReplaceAll(out, "\r\n", "\n")), "\n")
	if len(got) != 2 || got[0] != "turn 1  1 match" ||
		got[1] != "turn 2  1 match  (result text only: the transcript is gone)" {
		t.Fatalf("output:\n%s", out)
	}

	out, _, code = runCLI(t, "chat", "find", id, "nowhere", "--json")
	if code != 0 || !strings.Contains(out, `"turns": []`) {
		t.Fatalf("--json with no match = %d %s, want an empty turns array", code, out)
	}

	if _, _, code = runCLI(t, "chat", "find", id, ""); code != 1 {
		t.Fatalf("empty query exit = %d, want 1", code)
	}
}
