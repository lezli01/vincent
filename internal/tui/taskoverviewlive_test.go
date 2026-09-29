package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/store"
)

// runTask leaves a task running with one live attempt that has said
// something about itself, driven by hand as blockTask and finishTask are.
func (h *actionLiveHarness) runTask(t *testing.T, id int64, status string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskQueued, store.TaskRunning,
		store.TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	run := &store.StepRun{
		TaskID: id, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: store.StepRunning, StatusMessage: status, StartedAt: time.Now(),
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
}

// TestOverviewFramesFromRealServer opens a blocked, an aborted, a running and
// a done task against the real API handlers and reads each one's frame off the
// screen (task 129.12): the landing tab is the Overview, and what it says is
// derived from what the daemon served, with no call of its own.
func TestOverviewFramesFromRealServer(t *testing.T) {
	h := newActionLiveHarness(t)
	blocked := h.createParkedTask(t, "blocked-one")
	h.blockTask(t, blocked.ID, "timeout")
	running := h.createParkedTask(t, "running-one")
	h.runTask(t, running.ID, "rebasing onto main")
	done := h.createParkedTask(t, "done-one")
	h.finishTask(t, done.ID, store.TaskDone)
	aborted := h.createParkedTask(t, "aborted-one")
	h.finishTask(t, aborted.ID, store.TaskAborted)

	for _, c := range []struct {
		task *store.Task
		want []string
	}{
		{blocked, []string{"Blocked", "timeout", "retry", "3 output of attempt 1", "6 what it was given"}},
		{aborted, []string{"Aborted", "3 output of attempt 1", "6 what it was given"}},
		{running, []string{"Running", "rebasing onto main", "3 full output"}},
		{done, []string{"Done", "Outcome", "4 diff"}},
	} {
		_, cmd := h.m.Update(selectTaskMsg{id: c.task.ID})
		h.p.push(cmd)
		id := c.task.ID
		h.p.until(30*time.Second, "the overview of "+c.task.Title, func() bool {
			return detailOf(h.m).taskID == id && detailOf(h.m).loaded &&
				strings.Contains(ansi.Strip(content(h.m)), c.want[0])
		})
		got := ansi.Strip(content(h.m))
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: the overview misses %q:\n%s", c.task.Title, want, got)
			}
		}
	}
}
