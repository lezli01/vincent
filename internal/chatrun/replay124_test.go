package chatrun

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
)

// Task 124.10 over a real chat turn: the runner sets RunSpec.ReportInvocations
// on every turn, so the CLI reports the commands it expanded from the message
// and each line it adds reaches a client as something other than agent.raw.

// chunks collects every live-output chunk one turn publishes.
func (h *harness) chunks(t *testing.T, chatID int64, send func()) []events.Chunk {
	t.Helper()
	broker := events.New()
	t.Cleanup(broker.Close)
	h.runner.deps.Events = broker
	sub := broker.SubscribeOutput(ChatOutputKey(chatID), 256)
	t.Cleanup(sub.Close)

	done := make(chan []events.Chunk, 1)
	go func() {
		var got []events.Chunk
		for chunk := range sub.C {
			got = append(got, chunk)
		}
		done <- got
	}()
	send()
	h.waitIdle(t, chatID)
	sub.Close()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the output subscription never drained")
		return nil
	}
}

// rawLines is every verbatim line a chunk of the named type carried.
func rawLines(chunks []events.Chunk, typ string) []string {
	var out []string
	for _, c := range chunks {
		if c.Type != typ {
			continue
		}
		raw, _ := c.Payload["raw"].(string)
		out = append(out, raw)
	}
	return out
}

// TestHumanSkillReachesTheChat: a turn whose message is `/probe x` publishes
// the human's invocation as an agent.skill chunk, which before the replay
// flag was nothing at all — the CLI said nothing about it on stdout.
func TestHumanSkillReachesTheChat(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "skill-human")
	c := h.chat(t)

	chunks := h.chunks(t, c.ID, func() { h.sendAndWait(t, c.ID, "/probe x") })

	var loads []agent.SkillInvocation
	for _, chunk := range chunks {
		if chunk.Type != "agent.skill" {
			continue
		}
		payload, err := json.Marshal(chunk.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var load agent.SkillInvocation
		if err := json.Unmarshal(payload, &load); err != nil {
			t.Fatal(err)
		}
		loads = append(loads, load)
	}
	want := agent.SkillInvocation{Name: "probe", Args: "x", By: "human"}
	if len(loads) != 1 || loads[0] != want {
		t.Fatalf("published loads = %+v, want [%+v]", loads, want)
	}
	// The invocation is in the turn's transcript too, so the finished turn
	// shows what the live tail showed (task 071 decision 1).
	if body := h.transcript(t, c.ID, 1); !strings.Contains(body, `"isReplay":true`) {
		t.Errorf("the transcript kept no replayed line:\n%s", body)
	}
}

// TestPlainMessageEchoIsNotRaw: the same flag echoes the turn's own message,
// and an echo is already on screen as the human's bubble. It publishes no
// chunk and is never the agent.raw a client would dim into the tail.
func TestPlainMessageEchoIsNotRaw(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "skill-human")
	c := h.chat(t)

	chunks := h.chunks(t, c.ID, func() { h.sendAndWait(t, c.ID, "just a message") })

	for _, raw := range rawLines(chunks, "agent.raw") {
		if strings.Contains(raw, `"isReplay"`) {
			t.Errorf("the echo published an agent.raw chunk: %s", raw)
		}
	}
	for _, chunk := range chunks {
		if chunk.Type == "agent.input_echo" {
			t.Errorf("the echo published a chunk at all: %+v", chunk)
		}
		if chunk.Type == "agent.skill" {
			t.Errorf("a plain message reported a skill: %+v", chunk)
		}
	}
}

// TestAnsweredQuestionEchoIsNotRaw: the flag also hands vincent's own §7.4
// answer back on stdout. It is not an unrecognized line either.
func TestAnsweredQuestionEchoIsNotRaw(t *testing.T) {
	h := newHarness(t)
	h.cfg.Defaults.AgentTimeout = config.Duration(30 * time.Second)
	h.cfg.Defaults.InputTimeout = config.Duration(30 * time.Second)
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	c := h.chat(t)

	chunks := h.chunks(t, c.ID, func() {
		if _, err := h.runner.Send(t.Context(), c.ID, "ask me something"); err != nil {
			t.Errorf("Send: %v", err)
			return
		}
		h.waitState(t, c.ID, chatstate.AwaitingInput)
		if err := h.runner.Answer(t.Context(), c.ID, agent.InputResponse{
			Answers: map[string][]string{"Which color do you prefer?": {"Blue"}},
		}); err != nil {
			t.Errorf("Answer: %v", err)
		}
	})

	var echoed bool
	for _, raw := range rawLines(chunks, "agent.raw") {
		if strings.Contains(raw, `"control_response"`) {
			t.Errorf("the echoed answer published an agent.raw chunk: %s", raw)
		}
	}
	if body := h.transcript(t, c.ID, 1); strings.Contains(body, `"type":"control_response"`) {
		echoed = true
	}
	if !echoed {
		t.Fatal("the fake CLI never echoed the control_response; the flag did not reach it")
	}
}
