package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// TestLiveSubagentReachesThePane is task 109's wire proven end to end: a
// subagent's records fetched from the transcript and its chunks published
// live both reach the pane through the real handlers with parent_call_id and
// the subagent fields intact — the label's description rides only on a
// subagent_started chunk, so the second agent's label cannot appear without
// it.
func TestLiveSubagentReachesThePane(t *testing.T) {
	h := newBoardLiveHarness(t)
	task := h.createTask(t, "subagent task")
	ctx := context.Background()

	data, err := os.ReadFile(filepath.Join("..", "agent", "claude", "testdata", "stream_subagent_async_2.1.268.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fixture := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")

	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	// The first agent is on disk before the pane opens: its spawn, its start,
	// its launch, and its first lines.
	cold, hot := fixture[:10], fixture[10:21]
	for _, line := range cold {
		appendRawLine(t, path, line)
	}

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
	h.p.until(20*time.Second, "the fetched subagent to render", func() bool {
		return strings.Contains(plainContent(h.m), "┊ ↳ Verify 022 gate walkthrough")
	})
	h.p.until(10*time.Second, "the per-task subscription to attach", func() bool {
		return h.broker.OutputSubscribers(task.ID) > 0
	})

	// The second agent arrives live, transcript first and then its chunks,
	// in the runner's order and through the runner's own mapping.
	parse := claude.New(func() string { return "" }).NewLineParser()
	for _, line := range cold {
		parse([]byte(line))
	}
	for _, line := range hot {
		offset := appendRawLine(t, path, line)
		for _, c := range agent.LiveChunks(parse([]byte(line))) {
			payload := map[string]any{"run_id": run.ID, "offset": offset}
			for k, v := range c.Payload {
				payload[k] = v
			}
			h.broker.PublishOutput(task.ID, events.Chunk{Type: c.Type, Payload: payload})
		}
	}
	h.p.until(20*time.Second, "the live subagent to render", func() bool {
		got := plainContent(h.m)
		return strings.Contains(got, "┊ ↳ Verify 088 gate walkthrough") &&
			strings.Contains(got, "✓ started in background")
	})
	if got := plainContent(h.m); strings.Count(got, "┊ ▸ Bash") < 2 {
		t.Errorf("both agents' tool calls should render on the rail:\n%s", got)
	}
}

// plainContent is the rendered view with its styling stripped: the rail and
// the record's gutter are styled separately, so an escape sequence sits
// between them.
func plainContent(m *root) string {
	return escapes.ReplaceAllString(content(m), "")
}

// appendRawLine appends one verbatim dialect line to a transcript, returning
// the offset the daemon would stamp on the chunks it publishes for it.
func appendRawLine(t *testing.T, path, line string) int64 {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	return fi.Size()
}
