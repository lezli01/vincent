package apiclient_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// TestStreamTaskDeliversOutputAndEvents covers the frame split the per-task
// stream depends on: a durable event carries an id and decodes as an
// envelope, while a live-output chunk has no id and its data *is* the
// payload. Before T3.3 the client dropped the `event:` field entirely and
// decoded a chunk as a zero-valued event.
func TestStreamTaskDeliversOutputAndEvents(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notes := h.client().StreamTask(ctx, h.taskID, testStreamOptions())
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}

	// Live output: no id, the chunk type rides `event:`, identity rides the
	// payload.
	waitForSubscriber(t, h)
	h.broker.PublishOutput(h.taskID, events.Chunk{
		Type: "agent.output",
		Payload: map[string]any{
			"run_id": int64(31), "offset": int64(4096), "text": "Reading token.go",
		},
	})
	out, ok := nextNote(t, notes).(apiclient.OutputNote)
	if !ok {
		t.Fatalf("expected an OutputNote, got %T", out)
	}
	if out.Type != "agent.output" {
		t.Errorf("chunk type = %q, want agent.output", out.Type)
	}
	if out.RunID != 31 || out.Offset != 4096 {
		t.Errorf("chunk identity = run %d offset %d, want 31/4096", out.RunID, out.Offset)
	}
	if out.Text() != "Reading token.go" {
		t.Errorf("chunk text = %q", out.Text())
	}

	// Durable events keep arriving on the same stream, still as EventNotes.
	want := h.append(t, "task.state_changed")
	ev, ok := nextNote(t, notes).(apiclient.EventNote)
	if !ok {
		t.Fatalf("expected an EventNote, got %T", ev)
	}
	if ev.Event.ID != want.ID || ev.Event.Type != "task.state_changed" {
		t.Errorf("event = %+v, want id %d", ev.Event, want.ID)
	}
}

// TestStreamTaskOutputDoesNotAdvanceCursor guards the resume rule: live
// output is not replayable, so believing it had been "seen" would make a
// reconnect skip durable events it never received.
func TestStreamTaskOutputDoesNotAdvanceCursor(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := h.append(t, "task.created")
	notes := h.client().StreamTask(ctx, h.taskID, apiclient.StreamOptions{
		LastEventID:    first.ID - 1,
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	})
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}
	// The replay delivers the durable event that already existed.
	if ev, ok := nextNote(t, notes).(apiclient.EventNote); !ok || ev.Event.ID != first.ID {
		t.Fatalf("replay note = %+v, want event %d", ev, first.ID)
	}

	waitForSubscriber(t, h)
	h.broker.PublishOutput(h.taskID, events.Chunk{
		Type:    "command.output",
		Payload: map[string]any{"run_id": int64(1), "offset": int64(10), "text": "x"},
	})
	if _, ok := nextNote(t, notes).(apiclient.OutputNote); !ok {
		t.Fatal("expected an OutputNote")
	}
	// A second durable event still arrives: the chunk did not move the
	// cursor past it.
	second := h.append(t, "task.step_advanced")
	if ev, ok := nextNote(t, notes).(apiclient.EventNote); !ok || ev.Event.ID != second.ID {
		t.Errorf("second event = %+v, want id %d", ev, second.ID)
	}
}

// TestTranscriptNormalizedTailAndResume drives the fetch half of the
// catch-up seam against the real handler: a tail window, then a resume from
// the offset it reported.
func TestTranscriptNormalizedTailAndResume(t *testing.T) {
	h := newHarness(t)
	lines := []string{
		`{"type":"vincent.step_started","step":"implement"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","id":"toolu_01",` +
			`"input":{"file_path":"internal/auth/token.go","old_string":"a","new_string":"b"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"weighing the options"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01",` +
			`"content":"File created successfully"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	run := &store.StepRun{
		TaskID: h.taskID, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: store.StepRunning, Agent: "claude",
		TranscriptPath: path, StartedAt: time.Now(),
	}
	if err := h.st.CreateStepRun(context.Background(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	c := h.client()
	ctx := context.Background()

	all, next, err := c.Transcript(ctx, h.taskID, run.ID, apiclient.TranscriptOptions{})
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(all) != len(lines) {
		t.Fatalf("got %d records, want %d", len(all), len(lines))
	}
	if next != int64(len(body)) {
		t.Errorf("next offset = %d, want %d", next, len(body))
	}
	if all[0].Type != "vincent.step_started" {
		t.Errorf("vincent annotation not preserved: %+v", all[0])
	}
	if all[1].Type != "agent.output" || all[1].Text != "first" {
		t.Errorf("agent output not normalized: %+v", all[1])
	}
	if all[2].Type != "agent.tool_use" || len(all[2].Tools) != 1 || all[2].Tools[0].Name != "Edit" {
		t.Errorf("tool use not normalized: %+v", all[2])
	}
	// The subject and the call id survive the round trip through the real
	// handler — the fixture line carries an `input` and an `id`, and a
	// client that only ever sees the name is what T4.14 was filed about.
	if got := all[2].Tools[0].Summary; got != "internal/auth/token.go" {
		t.Errorf("tool summary = %q, want the edited file", got)
	}
	if got := all[2].Tools[0].CallID; got != "toolu_01" {
		t.Errorf("tool call id = %q, want toolu_01", got)
	}
	// T4.16: reasoning and outcomes survive the same round trip. These are
	// the records the pane needs to show what a run is doing, and the client
	// and the handler declare their shapes independently — which is exactly
	// what this test exists to keep in agreement.
	if all[3].Type != "agent.thinking" || all[3].Text != "weighing the options" {
		t.Errorf("thinking not normalized: %+v", all[3])
	}
	if all[4].Type != "agent.tool_result" || len(all[4].Results) != 1 {
		t.Fatalf("tool result not normalized: %+v", all[4])
	}
	if res := all[4].Results[0]; res.CallID != "toolu_01" ||
		!strings.Contains(res.Summary, "File created") || res.IsError {
		t.Errorf("tool result = %+v, want toolu_01 succeeding", res)
	}

	// A tail smaller than the file returns a suffix, still whole records.
	tailRecs, tailNext, err := c.Transcript(ctx, h.taskID, run.ID,
		apiclient.TranscriptOptions{Tail: int64(len(lines[3]) + 1)})
	if err != nil {
		t.Fatalf("Transcript(tail): %v", err)
	}
	if len(tailRecs) == 0 || len(tailRecs) >= len(all) {
		t.Fatalf("tail returned %d records of %d", len(tailRecs), len(all))
	}
	if last := tailRecs[len(tailRecs)-1]; last.Text != "second" {
		t.Errorf("tail does not end at the last record: %+v", last)
	}
	if tailNext != next {
		t.Errorf("tail next offset = %d, want %d", tailNext, next)
	}

	// Resuming at the reported offset yields nothing new — the cursor is a
	// record boundary, so the follow-up starts cleanly.
	rest, restNext, err := c.Transcript(ctx, h.taskID, run.ID, apiclient.TranscriptOptions{Offset: next})
	if err != nil {
		t.Fatalf("Transcript(resume): %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("resume returned %d records, want 0", len(rest))
	}
	if restNext != next {
		t.Errorf("resume next offset = %d, want %d", restNext, next)
	}
}

// TestGetTaskCarriesSteps proves the detail view needs exactly one call.
func TestGetTaskCarriesSteps(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	finished := time.Now()
	run := &store.StepRun{
		TaskID: h.taskID, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: store.StepSucceeded, Agent: "claude",
		InputWaitMS: 3000, StartedAt: finished.Add(-10 * time.Second), FinishedAt: &finished,
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	detail, err := h.client().GetTask(ctx, h.taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(detail.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(detail.Steps))
	}
	step := detail.Steps[0]
	if step.State != "succeeded" || step.Attempt != 1 {
		t.Errorf("step = %+v", step)
	}
	// §17 active time: the three seconds spent waiting on a human are not
	// work the step did.
	d, ok := step.Duration(finished)
	if !ok || d != 7*time.Second {
		t.Errorf("duration = %v (ok=%v), want 7s", d, ok)
	}
	if step.Live() {
		t.Error("a finished attempt reported itself live")
	}
	if detail.BranchName == "" {
		t.Error("detail is missing the branch name")
	}
	if detail.PendingInput != nil && !json.Valid(detail.PendingInput) {
		t.Error("pending_input is not valid JSON")
	}
}

// waitForSubscriber gives the server's output subscription time to register.
// Publishing before the handler subscribes drops the chunk, which is correct
// behavior (live output is lossy by design) but would make the test flaky.
func waitForSubscriber(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(noteTimeout)
	for h.broker.OutputSubscribers(h.taskID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no output subscriber registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
