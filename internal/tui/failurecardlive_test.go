package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/store"
)

// blockWith runs a parked task and blocks it on reason, with run as its one
// attempt (nil for a step-less block) and detail as the daemon's sentence.
func (h *actionLiveHarness) blockWith(t *testing.T, id int64, run *store.StepRun, reason, detail string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskQueued, store.TaskRunning,
		store.TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if run != nil {
		run.TaskID = id
		if err := h.st.CreateStepRun(ctx, run); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskRunning, store.TaskBlocked,
		store.TaskChange{BlockReason: &reason, BlockDetail: &detail}); err != nil {
		t.Fatalf("block: %v", err)
	}
}

// overviewOf opens a task and waits for its Overview to say want.
func (h *actionLiveHarness) overviewOf(t *testing.T, id int64, want string) string {
	t.Helper()
	_, cmd := h.m.Update(selectTaskMsg{id: id})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the failure card of task "+fmt.Sprint(id), func() bool {
		return detailOf(h.m).taskID == id && strings.Contains(ansi.Strip(content(h.m)), want)
	})
	return ansi.Strip(content(h.m))
}

// TestFailureCardFromRealServer reads three failure cards off the screen
// against the real API handlers (task 129.13): an agent step's check_failed
// whose evidence is the check's output fetched from its transcript, a fan-out
// parent blocked on a lane, and a step-less worktree block explained by the
// daemon's block_detail.
func TestFailureCardFromRealServer(t *testing.T) {
	h := newActionLiveHarness(t)
	now := time.Now()

	checked := h.createParkedTask(t, "checked")
	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	transcript := `{"type":"agent.output","text":"All done, the tests pass."}` + "\n" +
		`{"type":"vincent.output","phase":"check","stream":"stderr","text":"FAIL TestThing"}` + "\n"
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	h.blockWith(t, checked.ID, &store.StepRun{
		StepIndex: 0, StepID: "implement", StepType: "agent", Attempt: 1,
		State: store.StepFailed, FailureReason: "check_failed", TranscriptPath: path,
		StatusMessage: "tests look fine", StartedAt: now, FinishedAt: &now,
	}, "check_failed", "")
	got := h.overviewOf(t, checked.ID, "FAIL TestThing")
	for _, want := range []string{"✗ Step 1 implement · attempt 1 · check failed  check_failed", "status  “tests look fine”", "E      edit retry"} {
		if !strings.Contains(got, want) {
			t.Errorf("check_failed card misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "All done, the tests pass.") {
		t.Errorf("check_failed evidence is the agent's message:\n%s", got)
	}

	parent := h.createParkedTask(t, "fanned")
	index, until := 0, now.Add(24*time.Hour)
	lane := &store.Task{
		ProjectID: h.projectID, Title: "api lane", WorkflowName: "ask", WorkflowSnapshot: askWorkflow,
		BaseBranch: "main", BranchName: "vincent/live-api-lane", State: store.TaskQueued,
		AdmitNotBefore: &until, ParentTaskID: &parent.ID, ParentStepIndex: &index, LaneID: "api",
	}
	if err := h.st.CreateTask(context.Background(), lane, nil); err != nil {
		t.Fatalf("CreateTask lane: %v", err)
	}
	h.blockWith(t, lane.ID, nil, "worktree_dirty", "")
	h.blockWith(t, parent.ID, &store.StepRun{
		StepIndex: 0, StepID: "lanes", StepType: stepTypeFanOut, Attempt: 1,
		State: store.StepFailed, FailureReason: reasonLaneFailed,
		ResultSummary: fmt.Sprintf(`lane "api" (task %d) is blocked, not done`, lane.ID),
		StartedAt:     now, FinishedAt: &now,
	}, reasonLaneFailed, "")
	got = h.overviewOf(t, parent.ID, "the lane is blocked on worktree_dirty")
	for _, want := range []string{`⚠ Step 1 implement · lane "api"`, fmt.Sprintf("task %d", lane.ID), "l open the lane"} {
		if !strings.Contains(got, want) {
			t.Errorf("lane_failed card misses %q:\n%s", want, got)
		}
	}

	dirty := h.createParkedTask(t, "dirty")
	h.blockWith(t, dirty.ID, nil, "worktree_dirty", "README.md is modified in the worktree")
	got = h.overviewOf(t, dirty.ID, "README.md is modified in the worktree")
	if !strings.Contains(got, "worktree has uncommitted changes  worktree_dirty") {
		t.Errorf("worktree_dirty card misses its headline:\n%s", got)
	}
}
