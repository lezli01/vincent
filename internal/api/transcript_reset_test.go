package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
)

// TestNormalizeConversationReset: claude's `/clear` line is an
// agent.conversation_reset record carrying nothing but its type (task
// 124.20), and the turn's transcript holds no agent.raw for it. The verbatim
// line is still reachable through format=raw, which is where its ids live.
func TestNormalizeConversationReset(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..", "agent", "claude", "testdata", "stream_conversation_reset_2.1.278.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")
	records := normalizedFixture(t, lines, claude.New(func() string { return "" }).NewLineParser())

	var marks int
	for _, rec := range records {
		switch rec["type"] {
		case "agent.conversation_reset":
			marks++
			if len(rec) != 1 {
				t.Errorf("reset record = %v, want the type alone", rec)
			}
		case "agent.raw":
			if strings.Contains(rec["line"].(string), "conversation_reset") {
				t.Errorf("the reset is still raw: %v", rec)
			}
		}
	}
	if marks != 1 {
		t.Errorf("reset records = %d, want 1", marks)
	}
}

// TestConversationResetChunkMatchesTheRecord is the seam between the live
// route and the transcript route (task 071): a client renders both through
// one path, so agent.LiveChunks and normalizeLine must agree on the record
// this event becomes, down to it having no payload.
func TestConversationResetChunkMatchesTheRecord(t *testing.T) {
	ev := agent.Event{Type: agent.EventConversationReset, Raw: []byte(`{"type":"conversation_reset"}`)}

	chunks := agent.LiveChunks(ev)
	if len(chunks) != 1 || chunks[0].Type != "agent.conversation_reset" {
		t.Fatalf("live chunks = %+v, want one agent.conversation_reset", chunks)
	}
	out := normalizedEvent(ev, ev.Raw)
	if len(out) != 1 {
		t.Fatalf("records = %d, want one", len(out))
	}
	b, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"type":"agent.conversation_reset"}` {
		t.Errorf("reset record = %s", got)
	}
	if len(chunks[0].Payload) != 0 {
		t.Errorf("chunk payload = %+v, want none, as the record has none", chunks[0].Payload)
	}
}
