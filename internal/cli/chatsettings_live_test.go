package cli

import (
	"strconv"
	"strings"
	"testing"
)

// TestChatSetChangesSettings is `vincent chat set` against the real handlers
// (task 162): it changes the model, lowers to restricted without asking, and
// refuses to raise to full-auto off a terminal unless --yes is given.
func TestChatSetChangesSettings(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	id := strconv.FormatInt(chat.ID, 10)

	out, errOut, code := runCLI(t, "chat", "set", id, "--model", "sonnet", "--permission-mode", "restricted")
	if code != 0 || !strings.Contains(out, "sonnet · default effort · restricted") {
		t.Fatalf("chat set = %d %q (%s)", code, out, errOut)
	}
	if _, errOut, code := runCLI(t, "chat", "set", id, "--permission-mode", "full-auto"); code != 1 ||
		!strings.Contains(errOut, "--yes") {
		t.Errorf("raise without --yes = %d (%s), want a refusal naming --yes", code, errOut)
	}
	if got, _ := h.st.GetChat(t.Context(), chat.ID); got.PermissionMode != "restricted" {
		t.Errorf("mode after a refused raise = %q, want restricted", got.PermissionMode)
	}
	if out, errOut, code := runCLI(t, "chat", "set", id, "--permission-mode", "full-auto", "--yes"); code != 0 ||
		!strings.Contains(out, "full-auto") {
		t.Errorf("raise with --yes = %d %q (%s)", code, out, errOut)
	}
	if _, errOut, code := runCLI(t, "chat", "set", id); code == 0 || !strings.Contains(errOut, "nothing to change") {
		t.Errorf("chat set with no flags = %d (%s), want a refusal", code, errOut)
	}
}

// TestChatSendFullAutoOverrideAsksFirst: a per-turn full-auto override on a
// restricted chat is a raise for that turn, and one turn is enough for the
// agent to run any command as you — so off a terminal it is refused unless
// --yes is given, exactly as `chat set` refuses (§16, review F5). Nothing is
// sent when it is refused.
func TestChatSendFullAutoOverrideAsksFirst(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	id := strconv.FormatInt(chat.ID, 10)
	if _, errOut, code := runCLI(t, "chat", "set", id, "--permission-mode", "restricted"); code != 0 {
		t.Fatalf("lowering to restricted = %d (%s)", code, errOut)
	}
	_, errOut, code := runCLI(t, "chat", "send", id, "hello", "--permission-mode", "full-auto")
	if code != 1 || !strings.Contains(errOut, "--yes") || !strings.Contains(errOut, "full-auto") {
		t.Errorf("full-auto override without --yes = %d (%s), want a refusal naming --yes", code, errOut)
	}
	turns, err := h.st.ListChatTurns(t.Context(), chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Errorf("a refused override still sent a turn: %d turns", len(turns))
	}
}
