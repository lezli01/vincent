package chatrun

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// Task 124.20 over a real chat turn: a `/clear` reaches claude verbatim
// (124.6, decision 78), claude throws its own conversation away, and the mark
// has to survive the wire rather than only the parser.

// TestConversationResetReachesTheChat: the turn publishes the record live, so
// a reader watching the tail sees the point at which the agent's memory
// restarted instead of scrolling past it.
func TestConversationResetReachesTheChat(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "conversation-reset")
	c := h.chat(t)

	chunks := h.chunks(t, c.ID, func() { h.sendAndWait(t, c.ID, "/clear") })

	var marks int
	for _, chunk := range chunks {
		switch chunk.Type {
		case "agent.conversation_reset":
			marks++
			// The runner's own envelope (chat_id, offset, the verbatim
			// line) is all it carries: the record itself has no payload, so
			// none of claude's ids is promoted out of the raw line.
			for _, key := range []string{"new_conversation_id", "session_id"} {
				if _, ok := chunk.Payload[key]; ok {
					t.Errorf("reset chunk promoted %q out of the raw line: %+v", key, chunk.Payload)
				}
			}
		case "agent.raw":
			if raw, _ := chunk.Payload["raw"].(string); strings.Contains(raw, "conversation_reset") {
				t.Errorf("the reset is still an unrecognized line: %s", raw)
			}
		}
	}
	if marks != 1 {
		t.Fatalf("reset chunks = %d, want the one mark", marks)
	}
	// And in the finished turn's transcript, so it shows what the live tail
	// showed (task 071 decision 1).
	if body := h.transcript(t, c.ID, 1); !strings.Contains(body, `"conversation_reset"`) {
		t.Errorf("the transcript kept no reset line:\n%s", body)
	}
}

// TestConversationResetKeepsLastWinsResume is the behaviour the mark does not
// change (§9.2): the id the next turn resumes is still the last one the
// stream stamped, which after a reset is the new conversation's — not the one
// the run was resuming, and not the reset line's own `new_conversation_id`.
func TestConversationResetKeepsLastWinsResume(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)

	first := h.sendAndWait(t, c.ID, "remember heliotrope")
	if first.State != chatstate.TurnDone {
		t.Fatalf("turn 1 = %s (%s: %s)", first.State, first.FailReason, first.ErrorMessage)
	}
	before := h.waitIdle(t, c.ID).SessionID
	if before == "" {
		t.Fatal("turn 1 recorded no session id, so there is nothing to reset away from")
	}

	t.Setenv("FAKEAGENT_SCENARIO", "conversation-reset")
	second := h.sendAndWait(t, c.ID, "/clear")
	if second.State != chatstate.TurnDone {
		t.Fatalf("turn 2 = %s (%s: %s)", second.State, second.FailReason, second.ErrorMessage)
	}

	after := h.waitIdle(t, c.ID).SessionID
	if after == before {
		t.Fatalf("chat session id = %q, unchanged; the reset's own lines stamped a new one", after)
	}
	if after != before+"-after-reset" {
		t.Errorf("chat session id = %q, want the id the lines after the reset carried", after)
	}
}
