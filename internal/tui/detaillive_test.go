package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	covered := writeTranscript(t, path, "line-one", "line-two")

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
	next := writeTranscript(t, path, "line-one", "line-two", "line-three")

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

// writeTranscript writes a transcript holding exactly these records and
// returns the file size — the offset the daemon stamps on the chunk it
// publishes right after the last write.
func writeTranscript(t *testing.T, path string, texts ...string) int64 {
	t.Helper()
	var sb strings.Builder
	for _, text := range texts {
		fmt.Fprintf(&sb,
			`{"type":"vincent.output","phase":"run","stream":"stdout","text":%q}`+"\n", text)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return int64(len(sb.String()))
}
