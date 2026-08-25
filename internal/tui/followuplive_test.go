package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// finishTask leaves a task in `done` with the rows a finished run leaves
// behind, so its cursor sits one past the last step — the position a
// follow-up round's rows go after (§5.4, task 027 decision 2).
func (h *actionLiveHarness) finishTask(t *testing.T, id int64, end store.TaskState) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskQueued, store.TaskRunning,
		store.TaskChange{}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	now := time.Now()
	run := &store.StepRun{
		TaskID: id, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: store.StepSucceeded, ResultSummary: "done",
		StartedAt: now, FinishedAt: &now,
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	next := 1
	if _, _, err := h.st.TransitionTask(ctx, id, store.TaskRunning, end,
		store.TaskChange{CurrentStep: &next}); err != nil {
		t.Fatalf("finish as %s: %v", end, err)
	}
}

// TestDetailFollowsUpFinishedTaskLive drives the §6 follow-up the way a human
// does: the bar offers it only when the daemon does, `F` opens the form, the
// run form is chosen from a list, and what was typed is what the daemon
// receives (task 027).
//
// The follow-up agent hangs, so the request is still on the task when this
// asserts it — the request drains at the restore, which a hung agent never
// reaches.
func TestDetailFollowsUpFinishedTaskLive(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newActionLiveHarness(t)
	task := h.createTask(t, "finishable")

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail view to open the task", func() bool {
		return detailOf(h.m).taskID == task.ID && detailOf(h.m).loaded
	})
	// Queued: the daemon does not offer follow-up, so neither does the bar.
	if detailOf(h.m).target().has(apiclient.ActionFollowUp) {
		t.Error("a queued task offers follow_up")
	}
	if hintsContain(detailOf(h.m).detailHints(), "follow-up") {
		t.Error("the bar advertises follow-up on a queued task")
	}
	h.press(t, "F")
	if detailOf(h.m).followUp != nil {
		t.Fatal("F opened the follow-up form on a task the daemon does not offer it for")
	}

	h.finishTask(t, task.ID, store.TaskDone)
	h.p.until(30*time.Second, "the daemon to offer follow-up", func() bool {
		return detailOf(h.m).target().has(apiclient.ActionFollowUp)
	})
	if !hintsContain(detailOf(h.m).detailHints(), "follow-up") {
		t.Error("the bar does not advertise follow-up on a finished task")
	}

	h.press(t, "F")
	form := detailOf(h.m).followUp
	if form == nil {
		t.Fatal("F did not open the follow-up form")
	}
	if !h.m.views[viewHome].(*shell).popup {
		t.Fatal("the follow-up form did not take the popup")
	}
	if !strings.Contains(content(h.m), "Follow-up") {
		t.Errorf("the popup is not on screen: %q", content(h.m))
	}

	// The cursor starts on the run-form chooser, which defaults to `agent`.
	// One row down is the prompt: enter opens it, ctrl+s keeps it, ctrl+s
	// again starts the run.
	const prompt = "rebase this branch onto main"
	h.press(t, "j")
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

	h.p.until(60*time.Second, "the follow-up to be admitted", func() bool {
		return h.state(t, task.ID) != store.TaskDone
	})
	stored, err := h.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	req := stored.PendingFollowUp
	if req == nil || req.Prompt != prompt {
		t.Fatalf("the daemon received %+v, want the prompt that was typed", req)
	}
	if req.Form != store.FollowUpAgent {
		t.Errorf("form = %q, want %q — the chooser's default", req.Form, store.FollowUpAgent)
	}
	if req.Origin != store.TaskDone {
		t.Errorf("origin = %s, want done — the task is returned there", req.Origin)
	}

	// The form closes on the reply, so the popup goes with it.
	h.p.until(30*time.Second, "the submitted form to close", func() bool {
		return detailOf(h.m).followUp == nil && !h.m.views[viewHome].(*shell).popup
	})
}

// TestFollowUpFormPostsEachRunForm: the chooser decides which field is sent,
// and the daemon refuses a request that names two things to run — so exactly
// one has to arrive.
func TestFollowUpFormPostsEachRunForm(t *testing.T) {
	cases := []struct {
		form string
		set  func(*followUpForm)
		want apiclient.FollowUpInput
	}{
		{
			form: apiclient.FollowUpFormAgent,
			set:  func(f *followUpForm) { f.prompt = "tidy it" },
			want: apiclient.FollowUpInput{Prompt: "tidy it"},
		},
		{
			form: apiclient.FollowUpFormCommand,
			set:  func(f *followUpForm) { f.run = "git rebase origin/main" },
			want: apiclient.FollowUpInput{Run: "git rebase origin/main"},
		},
		{
			form: apiclient.FollowUpFormWorkflow,
			set:  func(f *followUpForm) { f.workflow = "review" },
			want: apiclient.FollowUpInput{Workflow: "review"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.form, func(t *testing.T) {
			f := newFollowUpForm(1, 1, "done")
			f.form = tc.form
			tc.set(f)
			if got := f.request(); got != tc.want {
				t.Errorf("request = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestTimelineRendersAFollowUpAsItsOwnRound: a follow-up's rows sit past the
// workflow's last index, and the timeline must not number them as steps of a
// workflow that never grew (task 027 decision 1).
func TestTimelineRendersAFollowUpAsItsOwnRound(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	h := newActionLiveHarness(t)
	task := h.createTask(t, "finishable")
	h.finishTask(t, task.ID, store.TaskDone)

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	h.p.until(30*time.Second, "the detail view to open the task", func() bool {
		return detailOf(h.m).taskID == task.ID && detailOf(h.m).loaded
	})
	h.press(t, "F")
	h.press(t, "j")
	h.press(t, "enter")
	h.typeText(t, "one more commit")
	h.pressCtrlS(t)
	h.pressCtrlS(t)

	h.p.until(60*time.Second, "the follow-up row to reach the timeline", func() bool {
		total := detailOf(h.m).task.StepTotal
		for _, r := range detailOf(h.m).task.Steps {
			if total > 0 && r.StepIndex >= total {
				return true
			}
		}
		return false
	})
	h.p.until(30*time.Second, "the timeline to label the round", func() bool {
		return strings.Contains(content(h.m), "follow-up 1")
	})
	if strings.Contains(content(h.m), "(parallel)") {
		t.Errorf("the follow-up round read as a group:\n%s", content(h.m))
	}
}
