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

// checkFailedTranscript is an attempt whose agent talked at length and whose
// check then failed — long on both sides, so landing at the first check line
// is a different place from the top and from the end.
func checkFailedTranscript(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for i := range 80 {
		fmt.Fprintf(&b, `{"type":"agent.output","text":"agent line %d"}`+"\n", i)
	}
	b.WriteString(`{"type":"vincent.output","phase":"check","stream":"stderr","text":"FAIL TestThing"}` + "\n")
	for i := range 80 {
		fmt.Fprintf(&b, `{"type":"vincent.output","phase":"check","stream":"stderr","text":"check line %d"}`+"\n", i)
	}
	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func workspaceOf(m *root) *taskView { return m.views[viewTask].(*taskView) }

// landedAtCheck waits for the Output tab to show attempt runID opened at its
// first check line: FAIL TestThing on screen, the viewport neither at the top
// nor following to the end.
func (h *actionLiveHarness) landedAtCheck(t *testing.T, what string, taskID, runID int64) {
	t.Helper()
	h.p.until(30*time.Second, what, func() bool {
		d := detailOf(h.m)
		return d.taskID == taskID && workspaceOf(h.m).tab == taskTabOutput &&
			d.displayRun == runID && len(d.records) > 0 &&
			strings.Contains(ansi.Strip(content(h.m)), "FAIL TestThing")
	})
	d := detailOf(h.m)
	if d.following || d.vp.YOffset() == 0 || d.vp.AtBottom() {
		t.Errorf("%s: following %v, offset %d, at bottom %v; want paused at the first check line",
			what, d.following, d.vp.YOffset(), d.vp.AtBottom())
	}
}

// TestNextFailureFromRealServer is task 129.18 against the real handlers: `N`
// on a check_failed task lands on Output at the first check line, and the
// Overview failure card's `3` takes the same path to the same place.
func TestNextFailureFromRealServer(t *testing.T) {
	h := newActionLiveHarness(t)
	now := time.Now()
	checked := h.createParkedTask(t, "checked")
	run := &store.StepRun{
		StepIndex: 0, StepID: "implement", StepType: "agent", Attempt: 1,
		State: store.StepFailed, FailureReason: "check_failed", TranscriptPath: checkFailedTranscript(t),
		StartedAt: now, FinishedAt: &now,
	}
	h.blockWith(t, checked.ID, run, "check_failed", "")

	h.overviewOf(t, checked.ID, "check failed")
	h.press(t, "N")
	h.landedAtCheck(t, "N on a check_failed task", checked.ID, run.ID)

	h.press(t, "0")
	h.p.until(10*time.Second, "back on the Overview", func() bool {
		return workspaceOf(h.m).tab == taskTabOverview
	})
	detailOf(h.m).vp.GotoTop()
	h.press(t, "3")
	h.landedAtCheck(t, "the failure card's 3", checked.ID, run.ID)
}

// TestNextFailureOpensABlockedLaneFromRealServer: on a fan-out parent whose
// lane is blocked, `N` opens the lane with the parent pushed on the back
// stack, and lands on the lane's failing attempt once it has loaded.
func TestNextFailureOpensABlockedLaneFromRealServer(t *testing.T) {
	h := newActionLiveHarness(t)
	now := time.Now()
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
	laneRun := &store.StepRun{
		StepIndex: 0, StepID: "implement", StepType: "agent", Attempt: 1,
		State: store.StepFailed, FailureReason: "check_failed", TranscriptPath: checkFailedTranscript(t),
		StartedAt: now, FinishedAt: &now,
	}
	h.blockWith(t, lane.ID, laneRun, "check_failed", "")
	h.blockWith(t, parent.ID, &store.StepRun{
		StepIndex: 0, StepID: "lanes", StepType: stepTypeFanOut, Attempt: 1,
		State: store.StepFailed, FailureReason: reasonLaneFailed,
		ResultSummary: fmt.Sprintf(`lane "api" (task %d) is blocked, not done`, lane.ID),
		StartedAt:     now, FinishedAt: &now,
	}, reasonLaneFailed, "")

	h.overviewOf(t, parent.ID, "l open the lane")
	h.p.until(10*time.Second, "the parent's lane rows", func() bool {
		return len(detailOf(h.m).laneRows) == 1
	})
	h.press(t, "N")
	h.landedAtCheck(t, "N on the parent", lane.ID, laneRun.ID)
	if st := workspaceOf(h.m).stack; len(st) != 1 || st[0] != parent.ID {
		t.Errorf("back stack = %v, want the parent %d", st, parent.ID)
	}
}

// TestBoardAttentionFilterFromRealServer: the list route serves the parent's
// rollup, `H` keeps the parent only once a lane needs a human, and the lane's
// own state event — for an id not on the board — is what refreshes it.
func TestBoardAttentionFilterFromRealServer(t *testing.T) {
	h := newActionLiveHarness(t)
	parent := h.parkedParent(t, "fanned")
	index := 0
	lane := &store.Task{
		ProjectID: h.projectID, Title: "lane", WorkflowName: "lane", WorkflowSnapshot: laneWorkflow,
		BaseBranch: "main", BranchName: "vincent/live-lane", State: store.TaskRunning,
		ParentTaskID: &parent.ID, ParentStepIndex: &index,
	}
	if err := h.st.CreateTask(context.Background(), lane, nil); err != nil {
		t.Fatalf("CreateTask lane: %v", err)
	}
	b := h.m.views[viewHome].(*shell).board
	h.p.until(10*time.Second, "the parent's row with its rollup", func() bool {
		for _, task := range b.tasks {
			if task.ID == parent.ID && task.Children != nil && task.Children.Total == 1 {
				return true
			}
		}
		return false
	})
	h.press(t, "H")
	if ids := shownIDs(b); len(ids) != 0 {
		t.Fatalf("H with every lane running shows %v, want nothing", ids)
	}

	reason := "nonzero_exit"
	if _, _, err := h.st.TransitionTask(context.Background(), lane.ID, store.TaskRunning, store.TaskBlocked,
		store.TaskChange{BlockReason: &reason}); err != nil {
		t.Fatalf("block lane: %v", err)
	}
	h.p.until(10*time.Second, "the lane's block to reach the parent's row", func() bool {
		ids := shownIDs(b)
		return len(ids) == 1 && ids[0] == parent.ID
	})
}
