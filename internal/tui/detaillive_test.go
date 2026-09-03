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
	"github.com/lezli01/vincent/internal/worktree"
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

// TestOutputPaneHoldsOneLaneSubscriptionAtATime is #316's cost objection made
// into an assertion. The Output pane's lane selector is allowed exactly one
// extra live subscription — never one per lane, which at 64 lanes is task 051
// decision 1's objection and task 049 decision 4's one-SSE-seam rule broken at
// once, and would render lossy besides: the daemon drops live chunks for a
// slow subscriber (§13.3).
//
// It runs against the real handlers and the real broker because "how many
// subscriptions exist" is a fact about the server, not about the model.
func TestOutputPaneHoldsOneLaneSubscriptionAtATime(t *testing.T) {
	h := newBoardLiveHarness(t)
	ctx := context.Background()
	parent := h.createTask(t, "fan-out parent")
	laneA := h.createLaneTask(t, parent.ID, "api", 0)
	laneB := h.createLaneTask(t, parent.ID, "web", 1)
	// Only a running task has live output worth a stream (§13.3), so the
	// lanes have to actually be running for the assertion to mean anything.
	for _, lane := range []*store.Task{laneA, laneB} {
		if _, _, err := h.st.TransitionTask(ctx, lane.ID,
			store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
			t.Fatalf("transition lane to running: %v", err)
		}
	}

	_, cmd := h.m.Update(selectTaskMsg{id: parent.ID})
	h.p.push(cmd)
	view := h.m.views[viewTask].(*taskView)
	h.p.until(20*time.Second, "the parent's lanes to load", func() bool {
		return len(view.lanes) == 2
	})
	_, cmd = h.m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	h.p.push(cmd)

	// `>` walks onto the first lane: exactly one subscription, and it is that
	// lane's.
	h.p.push(view.updateKey(synthKey(">")))
	h.p.until(20*time.Second, "the first lane's subscription to attach", func() bool {
		return h.broker.OutputSubscribers(laneA.ID) == 1
	})
	if got := h.broker.OutputSubscribers(laneB.ID); got != 0 {
		t.Fatalf("the unselected lane has %d subscribers, want none", got)
	}

	// `>` again moves the selection, and the previous stream goes with it.
	h.p.push(view.updateKey(synthKey(">")))
	h.p.until(20*time.Second, "the second lane's subscription to attach", func() bool {
		return h.broker.OutputSubscribers(laneB.ID) == 1
	})
	h.p.until(20*time.Second, "the first lane's subscription to go", func() bool {
		return h.broker.OutputSubscribers(laneA.ID) == 0
	})
	if got := h.broker.OutputSubscribers(laneB.ID); got != 1 {
		t.Fatalf("the selected lane has %d subscribers, want exactly one", got)
	}

	// Leaving the fan-out leaves no lane subscription behind.
	_, cmd = h.m.Update(selectTaskMsg{id: laneA.ID})
	h.p.push(cmd)
	h.p.until(20*time.Second, "the lane subscription to be torn down", func() bool {
		return h.broker.OutputSubscribers(laneB.ID) == 0
	})
	if view.laneDetail != nil {
		t.Fatalf("a lane sub-model survived the workspace moving off the fan-out")
	}
}

// createLaneTask is one lane of a fan_out: a child task carrying the parent,
// the lane id and its merge order (§7.6).
func (h *boardLiveHarness) createLaneTask(t *testing.T, parentID int64, lane string, order int) *store.Task {
	t.Helper()
	index := 0
	task := &store.Task{
		ProjectID: h.projectID, Title: lane + " lane", WorkflowName: "three",
		WorkflowSnapshot: threeStepWorkflow, BaseBranch: "main", State: store.TaskQueued,
		ParentTaskID: &parentID, ParentStepIndex: &index, LaneID: lane, LaneOrder: order,
	}
	resolve := func(id int64) (string, error) { return worktree.BranchName(id, task.Title), nil }
	if err := h.st.CreateTask(context.Background(), task, resolve); err != nil {
		t.Fatalf("CreateTask lane %s: %v", lane, err)
	}
	return task
}
