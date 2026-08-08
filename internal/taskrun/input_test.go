package taskrun

// Engine-level §7.4 coverage (T2.12 Done-when): the fake agent drives
// request → awaiting_input → answer → resume → done; input_timeout expiry
// fails the attempt; deny mode auto-answers with no state change; an
// unparseable control request fails input_protocol_error without hanging.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

const askQuestionText = "Which color do you prefer?"

// askSnapshot is a single agent step with no retries, so a failed wait
// lands in blocked with its reason visible.
func askSnapshot(extra ...string) string {
	s := `name: ask
steps:
  - id: implement
    type: agent
    max_retries: 0
`
	for _, line := range extra {
		s += "    " + line + "\n"
	}
	return s + `    prompt: |
      Do {{.Task.Title}}
`
}

// statePayloads returns the task's state-change event payloads, in order.
func (h *engineHarness) statePayloads(t *testing.T, id int64) []map[string]any {
	t.Helper()
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var out []map[string]any
	for _, e := range events {
		if e.TaskID == nil || *e.TaskID != id || e.Type != store.EventTaskStateChanged {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("state payload: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func payloadTo(payloads []map[string]any, to string) map[string]any {
	for _, p := range payloads {
		if p["to"] == to {
			return p
		}
	}
	return nil
}

func TestEngineInputRequestRoundTrip(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, askSnapshot())

	waiting := h.waitForState(t, task.ID, store.TaskAwaitingInput, store.TaskBlocked, store.TaskDone)
	if waiting.State != store.TaskAwaitingInput {
		t.Fatalf("task reached %s (block_reason %q), want awaiting_input", waiting.State, waiting.BlockReason)
	}
	// The normalized request is on the task (§13.2)…
	var pending PendingInput
	if err := json.Unmarshal([]byte(waiting.PendingInputJSON), &pending); err != nil {
		t.Fatalf("pending_input_json %q: %v", waiting.PendingInputJSON, err)
	}
	if pending.Kind != "question" || len(pending.Questions) != 1 ||
		pending.Questions[0].Text != askQuestionText {
		t.Fatalf("pending = %+v, want the fake's question", pending)
	}
	// …and the state-change event carries the alert fields (§13.3).
	entered := payloadTo(h.statePayloads(t, task.ID), string(store.TaskAwaitingInput))
	if entered == nil {
		t.Fatal("no state_changed event with to=awaiting_input")
	}
	if entered["kind"] != "question" || entered["summary"] != askQuestionText {
		t.Errorf("event payload = %v, want kind=question and the question as summary", entered)
	}

	// Let some wall clock pass so input_wait_ms has something to record.
	time.Sleep(30 * time.Millisecond)
	if _, err := h.runner.Answer(t.Context(), task.ID,
		AnswerInput{Answers: map[string][]string{askQuestionText: {"Red"}}}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("task reached %s (block_reason %q), want done", final.State, final.BlockReason)
	}
	if final.PendingInputJSON != "" {
		t.Errorf("pending_input_json survived the answer: %s", final.PendingInputJSON)
	}

	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepSucceeded {
		t.Fatalf("step runs = %+v, want one succeeded attempt", runs)
	}
	if runs[0].ResultSummary == "" || !containsAll(runs[0].ResultSummary, `"Red"`) {
		t.Errorf("result %q does not echo the answer", runs[0].ResultSummary)
	}
	if runs[0].InputWaitMS <= 0 {
		t.Errorf("input_wait_ms = %d, want > 0 (the task waited)", runs[0].InputWaitMS)
	}
	transcript, err := os.ReadFile(runs[0].TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	for _, want := range []string{
		`"vincent.input_request"`, `"vincent.input_response"`, `"source":"human"`,
	} {
		if !containsAll(string(transcript), want) {
			t.Errorf("transcript lacks %s", want)
		}
	}
}

func TestEngineInputPermissionAnswer(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "ask-permission")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, askSnapshot())

	waiting := h.waitForState(t, task.ID, store.TaskAwaitingInput, store.TaskBlocked, store.TaskDone)
	if waiting.State != store.TaskAwaitingInput {
		t.Fatalf("task reached %s, want awaiting_input", waiting.State)
	}
	var pending PendingInput
	if err := json.Unmarshal([]byte(waiting.PendingInputJSON), &pending); err != nil {
		t.Fatalf("pending_input_json: %v", err)
	}
	if pending.Kind != "permission" || pending.Permission == nil || pending.Permission.Tool != "Write" {
		t.Fatalf("pending = %+v, want a Write permission request", pending)
	}
	allow := true
	if _, err := h.runner.Answer(t.Context(), task.ID, AnswerInput{Allow: &allow}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("task reached %s (block_reason %q), want done", final.State, final.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || !containsAll(runs[0].ResultSummary, "allow") {
		t.Errorf("result %q does not echo the allow verdict", runs[0].ResultSummary)
	}
}

func TestEngineInputTimeoutFailsAttempt(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, askSnapshot("input_timeout: 250ms"))

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked || final.BlockReason != ReasonInputTimeout {
		t.Fatalf("task = %s (block_reason %q), want blocked/input_timeout", final.State, final.BlockReason)
	}
	if final.PendingInputJSON != "" {
		t.Errorf("pending_input_json survived the timeout: %s", final.PendingInputJSON)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].State != store.StepFailed || runs[0].FailureReason != ReasonInputTimeout {
		t.Fatalf("step runs = %+v, want one failed attempt with input_timeout", runs)
	}
	if runs[0].InputWaitMS < 200 {
		t.Errorf("input_wait_ms = %d, want ≳ the 250ms wait", runs[0].InputWaitMS)
	}
	// The wait ended through input_closed: awaiting_input → running → blocked.
	payloads := h.statePayloads(t, task.ID)
	if payloadTo(payloads, string(store.TaskAwaitingInput)) == nil {
		t.Error("no transition into awaiting_input recorded")
	}
}

func TestEngineDenyModeAutoAnswersQuestion(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, askSnapshot("on_input: deny"))

	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("task reached %s (block_reason %q), want done", final.State, final.BlockReason)
	}
	// Deny mode never leaves running (§7.4).
	if p := payloadTo(h.statePayloads(t, task.ID), string(store.TaskAwaitingInput)); p != nil {
		t.Errorf("deny-mode task entered awaiting_input: %v", p)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || !containsAll(runs[0].ResultSummary, "no user is available") {
		t.Errorf("result %q does not echo the canned answer", runs[0].ResultSummary)
	}
	if runs[0].InputWaitMS != 0 {
		t.Errorf("input_wait_ms = %d for a deny-mode run, want 0", runs[0].InputWaitMS)
	}
}

func TestEngineDenyModeDeniesPermission(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "ask-permission")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, askSnapshot("on_input: deny"))

	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("task reached %s (block_reason %q), want done", final.State, final.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || !containsAll(runs[0].ResultSummary, "deny: no user is available; permission denied") {
		t.Errorf("result %q does not echo the §7.4 permission denial", runs[0].ResultSummary)
	}
}

func TestEngineBadInputRequestFailsProtocol(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "bad-input-request")
	h := newEngineHarness(t)
	h.start(t)
	task := h.createTask(t, askSnapshot())

	// The fake never exits on its own — reaching blocked at all proves the
	// engine killed it rather than waiting on a request it can't render.
	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked || final.BlockReason != ReasonInputProtocolError {
		t.Fatalf("task = %s (block_reason %q), want blocked/input_protocol_error",
			final.State, final.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 || runs[0].FailureReason != ReasonInputProtocolError {
		t.Fatalf("step runs = %+v, want one failed attempt with input_protocol_error", runs)
	}
	if p := payloadTo(h.statePayloads(t, task.ID), string(store.TaskAwaitingInput)); p != nil {
		t.Errorf("protocol error must not park the task, but it entered awaiting_input: %v", p)
	}
}

// TestStepClockArithmetic pins the pause/resume bookkeeping with a fake
// clock; the timer itself rides real time and is stopped before it fires.
func TestStepClockArithmetic(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := newStepClock(time.Hour, func() time.Time { return now }, func() {
		t.Error("clock fired; the test never lets real time reach it")
	})
	defer clock.stop()

	now = now.Add(10 * time.Minute)
	clock.pause()
	if clock.remaining != 50*time.Minute {
		t.Errorf("remaining after 10m active = %s, want 50m", clock.remaining)
	}
	clock.pause() // idempotent while paused
	if clock.remaining != 50*time.Minute {
		t.Errorf("second pause changed remaining to %s", clock.remaining)
	}

	now = now.Add(24 * time.Hour) // waiting time must not consume budget
	clock.resume()
	now = now.Add(20 * time.Minute)
	clock.pause()
	if clock.remaining != 30*time.Minute {
		t.Errorf("remaining after another 20m active = %s, want 30m", clock.remaining)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
