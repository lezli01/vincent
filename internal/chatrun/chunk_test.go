package chatrun

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/events"
)

// TestChatChunksAreNormalized is task 071 decision 1 on the wire: a chat's
// live-output chunks carry §13.3's typed names and normalized fields — the
// same ones a step's chunks carry — with the agent's verbatim line kept
// beside them as `raw`.
//
// Before this, `record` published one type, `output`, whose only content was
// the raw stream line. A client had to parse the dialect to render anything,
// which no client does: the chat's tail was dimmed JSON end to end.
func TestChatChunksAreNormalized(t *testing.T) {
	h := newHarness(t)
	broker := events.New()
	t.Cleanup(broker.Close)
	h.runner.deps.Events = broker

	c := h.chat(t)
	sub := broker.SubscribeOutput(ChatOutputKey(c.ID), 256)
	t.Cleanup(sub.Close)

	done := make(chan []events.Chunk, 1)
	go func() {
		var got []events.Chunk
		for chunk := range sub.C {
			got = append(got, chunk)
		}
		done <- got
	}()

	h.sendAndWait(t, c.ID, "hello")
	h.waitIdle(t, c.ID)
	sub.Close()

	var chunks []events.Chunk
	select {
	case chunks = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the output subscription never drained")
	}
	if len(chunks) == 0 {
		t.Fatal("a finished turn published no live output")
	}

	sawOutput := false
	for _, chunk := range chunks {
		if chunk.Type == "output" {
			t.Fatalf("chunk kept the pre-071 catch-all type: %+v", chunk)
		}
		var body struct {
			ChatID int64  `json:"chat_id"`
			TurnID int64  `json:"turn_id"`
			Raw    string `json:"raw"`
			Text   string `json:"text"`
		}
		payload, err := json.Marshal(chunk.Payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if body.ChatID != c.ID || body.TurnID == 0 {
			t.Fatalf("chunk lost its identity: %s", payload)
		}
		// The additive half of decision 1: `raw` is still there, so nothing
		// that read it before reads less now.
		if body.Raw == "" {
			t.Fatalf("chunk dropped the verbatim line: %s", payload)
		}
		if chunk.Type == "agent.output" && body.Text != "" {
			sawOutput = true
		}
	}
	if !sawOutput {
		t.Fatalf("no agent.output chunk carried text; got %d chunks", len(chunks))
	}
}
