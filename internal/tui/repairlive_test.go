package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// blockTask puts a task into `blocked` with a reason and the failed row that
// put it there, the way the engine leaves one (§6, §7.2).
func (h *actionLiveHarness) blockTask(t *testing.T, id int64, reason string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskQueued, store.TaskRunning,
		store.TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	now := time.Now()
	run := &store.StepRun{
		TaskID: id, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: store.StepFailed, FailureReason: "nonzero_exit",
		ResultSummary: "the step did not work", StartedAt: now, FinishedAt: &now,
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskRunning, store.TaskBlocked,
		store.TaskChange{BlockReason: &reason}); err != nil {
		t.Fatalf("block: %v", err)
	}
}

// TestDetailRepairsBlockedTaskLive drives the §6 repair the way a human does:
// the bar offers it only when the daemon does, `R` opens the form, and what
// was typed is what the daemon receives (task 025).
//
// The repair agent hangs, so the request is still on the task when this
// asserts it — a repair drains at the re-block, which a hung agent never
// reaches.
func TestDetailRepairsBlockedTaskLive(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newActionLiveHarness(t)
	task := h.createTask(t, "repairable")

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail view to open the task", func() bool {
		return detailOf(h.m).taskID == task.ID && detailOf(h.m).loaded
	})
	// Queued: the daemon does not offer repair, so neither does the bar.
	if detailOf(h.m).target().has(apiclient.ActionRepair) {
		t.Error("a queued task offers repair")
	}
	if hintsContain(detailOf(h.m).detailHints(), "repair") {
		t.Error("the bar advertises repair on a queued task")
	}
	h.press(t, "R")
	if detailOf(h.m).repair != nil {
		t.Fatal("R opened the repair form on a task the daemon does not offer it for")
	}

	h.blockTask(t, task.ID, "check_failed")
	h.p.until(30*time.Second, "the daemon to offer repair", func() bool {
		return detailOf(h.m).target().has(apiclient.ActionRepair)
	})
	if !hintsContain(detailOf(h.m).detailHints(), "repair") {
		t.Error("the bar does not advertise repair on a blocked task")
	}

	h.press(t, "R")
	form := detailOf(h.m).repair
	if form == nil {
		t.Fatal("R did not open the repair form")
	}
	if !h.m.views[viewHome].(*shell).popup {
		t.Fatal("the repair form did not take the popup")
	}
	if !strings.Contains(content(h.m), "Repair") {
		t.Errorf("the popup is not on screen: %q", content(h.m))
	}

	// Type a prompt: enter opens the field, ctrl+s keeps it, ctrl+s again
	// starts the repair.
	const prompt = "add the missing file"
	h.press(t, "enter")
	if !form.editing {
		t.Fatal("enter did not open the prompt field")
	}
	h.typeText(t, prompt)
	h.pressCtrlS(t)
	if form.prompt != prompt {
		t.Fatalf("form prompt = %q, want %q", form.prompt, prompt)
	}
	h.pressCtrlS(t)

	h.p.until(60*time.Second, "the repair to be admitted", func() bool {
		return h.state(t, task.ID) != store.TaskBlocked
	})
	stored, err := h.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.PendingRepair == nil || stored.PendingRepair.Prompt != prompt {
		t.Fatalf("the daemon received %+v, want the prompt that was typed", stored.PendingRepair)
	}

	// The form closes on the reply, so the popup goes with it.
	h.p.until(30*time.Second, "the submitted form to close", func() bool {
		return detailOf(h.m).repair == nil && !h.m.views[viewHome].(*shell).popup
	})
}

// TestTimelineRendersARepairAsItsOwnEntry: a repair row sits at the blocked
// step's index under a reserved id, and the timeline must not render it as an
// attempt of that step — "distinct from the blocked step's own attempts" is
// the issue's own criterion (task 025).
func TestTimelineRendersARepairAsItsOwnEntry(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newActionLiveHarness(t)
	task := h.createTask(t, "repairable")
	h.blockTask(t, task.ID, "check_failed")

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail view to open the task", func() bool {
		return detailOf(h.m).taskID == task.ID && detailOf(h.m).loaded
	})
	h.press(t, "R")
	h.press(t, "enter")
	h.typeText(t, "fix it")
	h.pressCtrlS(t)
	h.pressCtrlS(t)

	h.p.until(60*time.Second, "the repair row to reach the timeline", func() bool {
		for _, r := range detailOf(h.m).task.Steps {
			if r.StepID == apiclient.RepairStepID {
				return true
			}
		}
		return false
	})
	h.p.until(30*time.Second, "the timeline to label the repair", func() bool {
		return strings.Contains(content(h.m), "repair (ad-hoc agent)")
	})

	// The step it sits beside keeps its own shape: one header, its own
	// attempts, and no `parallel` tier invented by two step ids sharing an
	// index.
	if strings.Contains(content(h.m), "(parallel)") {
		t.Errorf("the repair row made the blocked step read as a group:\n%s", content(h.m))
	}
}

// typeText sends one key per rune, the way a terminal delivers typing.
func (h *actionLiveHarness) typeText(t *testing.T, text string) {
	t.Helper()
	for _, r := range text {
		_, cmd := h.m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		h.p.push(cmd)
	}
}

// pressCtrlS sends the repair form's submit chord.
func (h *actionLiveHarness) pressCtrlS(t *testing.T) {
	t.Helper()
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	h.p.push(cmd)
}

func hintsContain(hints []string, want string) bool {
	for _, hint := range hints {
		if strings.Contains(hint, want) {
			return true
		}
	}
	return false
}
