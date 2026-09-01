package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// TestDetailTailJoinsTranscriptWithoutGapOrDuplicate is T3.3's headline: the
// live tail and the transcript catch-up are two sources for one pane, and
// joining them is the thing this PR invents. A unit test cannot prove it —
// the offsets come from the daemon writing the file and the client reading
// it — so this runs the real store, the real broker, the real handlers and
// the real client, and asserts the seam holds in both directions: a line the
// fetch already covered must not appear twice, and a line published after it
// must not be lost.
func TestDetailTailJoinsTranscriptWithoutGapOrDuplicate(t *testing.T) {
	h := newBoardLiveHarness(t)
	task := h.createTask(t, "tailed task")
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	covered := appendTranscript(t, path, "line-one", "line-two")

	// The task must actually be running: T3.10's subscription rule only
	// opens the live tail for a running task, which is the only state that
	// produces live output.
	if _, _, err := h.st.TransitionTask(ctx, task.ID,
		store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("transition to running: %v", err)
	}
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "one", StepType: "command",
		Attempt: 1, State: store.StepRunning, TranscriptPath: path,
		StartedAt: time.Now(),
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	// Open the task: the shell routes, the view fetches and subscribes.
	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	// Steps & Attempts is the default task tab; output is a separate full-screen
	// tab, so select it before asserting the transcript seam on screen.
	_, cmd = h.m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	h.p.push(cmd)
	h.p.until(20*time.Second, "the transcript window to render", func() bool {
		return strings.Contains(content(h.m), "line-two")
	})
	// The subscription must exist before publishing: live output with no
	// subscriber is dropped by design (§13.3), which would make this flaky
	// rather than failing honestly.
	h.p.until(10*time.Second, "the per-task subscription to attach", func() bool {
		return h.broker.OutputSubscribers(task.ID) > 0
	})

	// The daemon writes a third line and publishes it, exactly as the runner
	// does: transcript first, then the chunk carrying the position past it.
	next := appendTranscript(t, path, "line-three")

	// A chunk the fetch already covered — same line, offset at the boundary
	// the fetch reported. It must be recognized as already seen.
	h.broker.PublishOutput(task.ID, events.Chunk{
		Type: "command.output",
		Payload: map[string]any{
			"run_id": run.ID, "offset": covered,
			"phase": "run", "stream": "stdout", "text": "line-two",
		},
	})
	h.broker.PublishOutput(task.ID, events.Chunk{
		Type: "command.output",
		Payload: map[string]any{
			"run_id": run.ID, "offset": next,
			"phase": "run", "stream": "stdout", "text": "line-three",
		},
	})

	h.p.until(20*time.Second, "the live line to render", func() bool {
		return strings.Contains(content(h.m), "line-three")
	})
	if got := content(h.m); strings.Count(got, "line-two") != 1 {
		t.Errorf("line-two appears %d times; the catch-up seam duplicated it:\n%s",
			strings.Count(got, "line-two"), got)
	}
	if got := content(h.m); !strings.Contains(got, "line-one") {
		t.Errorf("the fetched window lost its first line:\n%s", got)
	}
}

// appendTranscript appends these records to the transcript and returns the
// file size afterwards — the offset the daemon stamps on the chunk it
// publishes right after the last write.
//
// It appends because the daemon does: a transcript is opened once and written
// to, never rebuilt (§12.2). Rewriting the file with os.WriteFile truncates it
// to zero first, and the per-task subscription's ConnectedNote re-reads the
// transcript (§13.3) at a moment this test does not synchronize with. A read
// landing inside that window returns an empty window, applyTranscript replaces
// the records already on screen with it, and the first line goes missing for a
// reason that has nothing to do with the seam under test.
func appendTranscript(t *testing.T, path string, texts ...string) int64 {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer func() { _ = f.Close() }()
	for _, text := range texts {
		if _, err := fmt.Fprintf(f,
			`{"type":"vincent.output","phase":"run","stream":"stdout","text":%q}`+"\n", text); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	return fi.Size()
}

// TestLiveSplitDocumentMatchesTheColdTranscript is #291's regression test for
// the two criteria that were already true and now have a document model
// underneath them: an assistant message an adapter delivered as several
// records renders the same live as it does from the persisted transcript, and
// the X-Next-Offset seam still neither duplicates a record nor loses one.
//
// It streams a table whose header, delimiter and body arrive as three
// separate chunks — the split that used to render as three broken documents —
// and compares the pane against a cold render of the same source.
func TestLiveSplitDocumentMatchesTheColdTranscript(t *testing.T) {
	h := newBoardLiveHarness(t)
	task := h.createTask(t, "split-document task")
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	appendAssistant(t, path, "Here is the table:\n")

	if _, _, err := h.st.TransitionTask(ctx, task.ID,
		store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("transition to running: %v", err)
	}
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "one", StepType: "agent",
		Agent:   "claude",
		Attempt: 1, State: store.StepRunning, TranscriptPath: path,
		StartedAt: time.Now(),
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	_, cmd = h.m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	h.p.push(cmd)
	h.p.until(20*time.Second, "the transcript window to render", func() bool {
		return strings.Contains(content(h.m), "Here is the table:")
	})
	h.p.until(10*time.Second, "the per-task subscription to attach", func() bool {
		return h.broker.OutputSubscribers(task.ID) > 0
	})

	// The rest of the one message, record by record, transcript first and
	// then the chunk past it — exactly the runner's order.
	rest := []string{"| Step | State |", "|---|---|", "| build | ok |"}
	for _, text := range rest {
		offset := appendAssistant(t, path, text)
		h.broker.PublishOutput(task.ID, events.Chunk{
			Type:    "agent.output",
			Payload: map[string]any{"run_id": run.ID, "offset": offset, "text": text},
		})
	}
	h.p.until(20*time.Second, "the live table to render", func() bool {
		return strings.Contains(content(h.m), "build")
	})

	got := content(h.m)
	if strings.Count(got, "| Step | State |") != 0 {
		t.Errorf("the delimiter row never joined its header — the table stayed source:\n%s", got)
	}
	if strings.Count(got, "build") != 1 {
		t.Errorf("the seam duplicated a record:\n%s", got)
	}
	if !strings.Contains(got, "Here is the table:") {
		t.Errorf("the fetched window lost its first record:\n%s", got)
	}
}

// appendAssistant is appendTranscript for assistant prose: one agent.output
// record per call, returning the offset the daemon would stamp on the chunk
// it publishes next.
func appendAssistant(t *testing.T, path string, text string) int64 {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer func() { _ = f.Close() }()
	// The transcript on disk holds the agent's own dialect (§13.2); the
	// server normalizes it on the way out, exactly as it does for a real run.
	if _, err := fmt.Fprintf(f,
		`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`+"\n",
		text); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	return fi.Size()
}
