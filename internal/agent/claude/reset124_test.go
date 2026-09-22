package claude

import (
	"encoding/json"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The reset capture (task 124.20) was recorded on 2026-09-22 against claude
// 2.1.278 with vincent's own chat-turn argv — the input-mode one plus
// --replay-user-messages — over two runs in a throwaway git repository
// outside this one: an ordinary prompt, then `/clear` written to stdin as one
// stream-json user line with `--resume` carrying the first run's session id.
// The hook lines the machine's own settings produced are removed, paths
// rewritten under /work/repo, the init line's personal inventory (plugins,
// MCP servers, agents, skills) replaced with the probe's own and its model
// with a plain one; the line structure, the field names and the ids' shape
// are verbatim.
//
// What the capture settles, and what no committed evidence proved before it:
// the line exists in `--print` stream-json output at all, it is a *top-level
// type* rather than the `system` subtype §9.2's 124.2 bullet implied, and it
// carries `new_conversation_id` — a third id, equal to neither the session it
// leaves nor the one every line after it stamps.
const resetFixture = "stream_conversation_reset_2.1.278.jsonl"

// resetLine is the capture's reset, by index: the first line, because on a
// resumed run claude writes it before the new conversation's own header.
const resetLine = 0

// TestConversationResetParses is the mapping: claude's own line becomes the
// normalized record and not an unrecognized one, with the verbatim line
// still riding along for format=raw.
func TestConversationResetParses(t *testing.T) {
	lines := fixtureLines(t, resetFixture)
	events := parseAll(lines...)

	ev := events[resetLine]
	if ev.Type != agent.EventConversationReset {
		t.Fatalf("reset line = %q, want %q", ev.Type, agent.EventConversationReset)
	}
	if string(ev.Raw) != string(lines[resetLine]) {
		t.Errorf("raw = %q, want the line verbatim", ev.Raw)
	}
	// Nothing else on the line is a payload: the record is its type.
	if ev.Text != "" || ev.Message != "" || ev.Skill != nil || ev.Header != nil {
		t.Errorf("record = %+v, want an empty one", ev)
	}
}

// TestConversationResetIsNotUnmodeled is what stops a verbose tail counting
// the line among the unrecognized ones — the complement api.normalizeLine
// calls agent.raw (task 071).
func TestConversationResetIsNotUnmodeled(t *testing.T) {
	ev := parseAll(fixtureLines(t, resetFixture)...)[resetLine]
	if agent.UnmodeledLine(ev) {
		t.Error("UnmodeledLine = true, want the reset modeled")
	}
}

// TestConversationResetPublishesAChunk is the one way this record differs
// from agent.input_echo (task 124.20): an echoed prompt is already on screen,
// a reset is news, so it goes live — with an empty payload, because the
// record is its type.
func TestConversationResetPublishesAChunk(t *testing.T) {
	ev := parseAll(fixtureLines(t, resetFixture)...)[resetLine]
	chunks := agent.LiveChunks(ev)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want one", len(chunks))
	}
	if chunks[0].Type != "agent.conversation_reset" {
		t.Errorf("chunk = %q, want agent.conversation_reset", chunks[0].Type)
	}
	if len(chunks[0].Payload) != 0 {
		t.Errorf("payload = %+v, want none", chunks[0].Payload)
	}
}

// TestConversationResetLeavesResumeAlone is the behaviour this change does
// not touch (§9.2): the session id vincent resumes next turn still comes from
// the lines, last-wins, and never from the reset's `new_conversation_id`.
// The capture is the proof that reading the reset's id instead would resume
// the wrong conversation — the two differ.
func TestConversationResetLeavesResumeAlone(t *testing.T) {
	lines := fixtureLines(t, resetFixture)

	var reset struct {
		SessionID         string `json:"session_id"`
		NewConversationID string `json:"new_conversation_id"`
	}
	if err := json.Unmarshal(lines[resetLine], &reset); err != nil {
		t.Fatalf("decode reset line: %v", err)
	}
	var last streamLine
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatalf("decode last line: %v", err)
	}
	// claude.run stores the last session_id a line stamped, and that is what
	// the next turn resumes.
	switch last.SessionID {
	case "":
		t.Fatal("the capture's last line carries no session id")
	case reset.SessionID:
		t.Error("the run ended in the session it reset away from; the capture says it does not")
	case reset.NewConversationID:
		t.Error("new_conversation_id is the resumable id after all; the record's silence about it " +
			"is then a choice, not a safeguard")
	}
}
