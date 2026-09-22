package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// Task 124.20 prints the agent.conversation_reset record, which both
// transcript commands used to drop in renderTranscriptRecord's default arm.
// The mark has to survive outside the TUI: a reader piping
// `vincent chat transcript` sees the same point the pane marks.

const resetLine = "# conversation reset · the agent no longer sees the turns above"

func TestRenderTranscriptConversationReset(t *testing.T) {
	got, ok := renderTranscriptRecord(
		apiclient.TranscriptRecord{Type: "agent.conversation_reset"}, false, nil)
	if !ok || got != resetLine {
		t.Errorf("rendered %q (%v), want %q", got, ok, resetLine)
	}
}

// TestChatTranscriptPrintsTheConversationReset drives claude's own captured
// `/clear` through the real route: the record prints as the mark in text, and
// as its type under --json, which it gets for free from the normalizer.
func TestChatTranscriptPrintsTheConversationReset(t *testing.T) {
	h := newLiveHarness(t)
	lines := fixtureLines(t, filepath.Join("..", "agent", "claude", "testdata",
		"stream_conversation_reset_2.1.278.jsonl"))
	chat := h.addChat(t)
	h.addTurn(t, chat, chatstate.TurnDone, "", lines...)

	out, errOut, code := runCLI(t, chatArgs(chat)...)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	if !strings.Contains(out, resetLine) {
		t.Errorf("text output does not mark the reset:\n%s", out)
	}

	out, _, code = runCLI(t, chatArgs(chat, "--json")...)
	if code != 0 {
		t.Fatalf("--json exit = %d, want 0", code)
	}
	var marks int
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		var rec apiclient.TranscriptRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %q is not a TranscriptRecord: %v", line, err)
		}
		if rec.Type == "agent.conversation_reset" {
			marks++
		}
		if rec.Type == "agent.raw" && strings.Contains(rec.Line, "conversation_reset") {
			t.Errorf("the reset is still raw: %s", line)
		}
	}
	if marks != 1 {
		t.Errorf("--json reset records = %d, want 1:\n%s", marks, out)
	}
}
