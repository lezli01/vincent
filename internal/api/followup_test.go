package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// finishedTask leaves a task in `done` or `aborted`, the two states a
// follow-up may be asked for from (§6, task 027).
func finishedTask(t *testing.T, h *taskHarness, end store.TaskState) taskResponse {
	t.Helper()
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskRunning)
	setState(t, h, task.ID, end)
	return task
}

func followUpPath(id int64) string { return fmt.Sprintf("/v1/tasks/%d/follow_up", id) }

// TestFollowUpFromDoneAndAborted: the request is persisted with the origin it
// was launched from and the task re-queues, so the scheduler admits it like
// anything else and both §11 caps apply.
func TestFollowUpFromDoneAndAborted(t *testing.T) {
	for _, origin := range []store.TaskState{store.TaskDone, store.TaskAborted} {
		t.Run(string(origin), func(t *testing.T) {
			h := newActionHarness(t)
			task := finishedTask(t, h, origin)

			resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
				map[string]any{"prompt": "rebase onto main"})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("follow_up: %d %s", resp.StatusCode, body)
			}
			if got := decodeTask(t, body); got.State != string(store.TaskQueued) {
				t.Errorf("state = %s, want queued", got.State)
			}

			stored, err := h.store.GetTask(t.Context(), task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			req := stored.PendingFollowUp
			if req == nil {
				t.Fatal("the follow-up request was not persisted")
			}
			if req.Form != store.FollowUpAgent || req.Prompt != "rebase onto main" {
				t.Errorf("request = %+v, want the agent form carrying what was posted", req)
			}
			if req.Origin != origin {
				t.Errorf("origin = %s, want %s — the task is returned there", req.Origin, origin)
			}
			if req.Round != 1 || req.Cursor != 0 {
				t.Errorf("round/cursor = %d/%d, want 1/0", req.Round, req.Cursor)
			}
			if !strings.Contains(req.Workflow, "type: agent") {
				t.Errorf("compiled workflow is not an agent step:\n%s", req.Workflow)
			}
			if stored.RetryCursorAt != nil {
				t.Error("follow-up moved the retry cursor; the workflow's budgets are not its business (§7.2)")
			}
		})
	}
}

// TestFollowUpIsOfferedExactlyOnDoneAndAborted: `available_actions` is derived
// from §6, so the endpoint and the bar a client renders cannot disagree.
func TestFollowUpIsOfferedExactlyOnDoneAndAborted(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	if hasAction(task.AvailableActions, "follow_up") {
		t.Errorf("queued actions = %v, must not offer follow_up", task.AvailableActions)
	}
	for _, state := range taskstate.All {
		setState(t, h, task.ID, state)
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", task.ID), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get task in %s: %d %s", state, resp.StatusCode, body)
		}
		want := state == store.TaskDone || state == store.TaskAborted
		if got := decodeTask(t, body); hasAction(got.AvailableActions, "follow_up") != want {
			t.Errorf("%s actions = %v, follow_up offered = %v, want %v",
				state, got.AvailableActions, !want, want)
		}
	}
}

// TestFollowUpFromEveryOtherStateIs409: 409 with the state actually found, so
// a client can re-issue against what it did not expect (§13.1).
func TestFollowUpFromEveryOtherStateIs409(t *testing.T) {
	for _, state := range taskstate.All {
		if state == store.TaskDone || state == store.TaskAborted {
			continue
		}
		t.Run(string(state), func(t *testing.T) {
			h := newActionHarness(t)
			task := queuedTask(t, h)
			setState(t, h, task.ID, state)

			resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
				map[string]any{"prompt": "do it"})
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("follow_up from %s: %d %s, want 409", state, resp.StatusCode, body)
			}
			if got := decodeError(t, body).Details["state"]; got != string(state) {
				t.Errorf("details.state = %v, want %s", got, state)
			}
		})
	}
}

// TestFollowUpNeedsExactlyOneForm is decision 3's shape at the wire: none
// says nothing to run, and two say two different things with no rule for
// which wins.
func TestFollowUpNeedsExactlyOneForm(t *testing.T) {
	cases := map[string]map[string]any{
		"none":                {},
		"blank":               {"prompt": "   \n\t "},
		"prompt and run":      {"prompt": "do it", "run": "git status"},
		"run and workflow":    {"run": "git status", "workflow": "adhoc"},
		"prompt and workflow": {"prompt": "do it", "workflow": "adhoc"},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			h := newActionHarness(t)
			task := finishedTask(t, h, store.TaskDone)

			resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID), payload)
			wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)

			stored, err := h.store.GetTask(t.Context(), task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if stored.State != store.TaskDone || stored.PendingFollowUp != nil {
				t.Errorf("a refused follow-up moved the task: %s, pending %+v",
					stored.State, stored.PendingFollowUp)
			}
		})
	}
}

// TestFollowUpRejectsAnUnknownWorkflow: the name is resolved against the
// registry now rather than at admission, so the person asking is told.
func TestFollowUpRejectsAnUnknownWorkflow(t *testing.T) {
	h := newActionHarness(t)
	task := finishedTask(t, h, store.TaskDone)

	resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
		map[string]any{"workflow": "no-such-workflow"})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if msg := decodeError(t, body).Message; !strings.Contains(msg, "no-such-workflow") {
		t.Errorf("message = %q, want it to name the workflow", msg)
	}
}

// TestFollowUpAcceptsARegistryWorkflow is the third form's happy path: the
// built-in `adhoc` workflow is spliced and stored as it will run, so a later
// edit to the file cannot mutate the run (§5.3).
func TestFollowUpAcceptsARegistryWorkflow(t *testing.T) {
	h := newActionHarness(t)
	task := finishedTask(t, h, store.TaskDone)

	resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
		map[string]any{"workflow": "adhoc", "agent": "claude"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("follow_up: %d %s", resp.StatusCode, body)
	}
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	req := stored.PendingFollowUp
	if req == nil || req.Form != store.FollowUpWorkflow || req.WorkflowName != "adhoc" {
		t.Fatalf("request = %+v, want the workflow form naming adhoc", req)
	}
	if req.Workflow == "" || !strings.Contains(req.Workflow, "steps:") {
		t.Errorf("the compiled workflow was not stored:\n%s", req.Workflow)
	}
	// The request stands in for the step level of §8.6's chain, written into
	// the steps that declare nothing of their own (decision 12).
	if !strings.Contains(req.Workflow, "agent: claude") {
		t.Errorf("the request's agent did not reach the compiled steps:\n%s", req.Workflow)
	}
}

// TestFollowUpSelectionValidationMatchesCreation applies §8.2 to the run's own
// triple the way POST /v1/tasks applies it to a task's: an unregistered agent
// is a 400.
func TestFollowUpSelectionValidationMatchesCreation(t *testing.T) {
	h := newActionHarness(t)
	task := finishedTask(t, h, store.TaskDone)

	resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
		map[string]any{"prompt": "fix it", "agent": "not-an-adapter"})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if msg := decodeError(t, body).Message; !strings.Contains(msg, "not-an-adapter") {
		t.Errorf("message = %q, want it to name the agent", msg)
	}
}

// TestFollowUpOnAMissingTaskIs404 keeps the endpoint's not-found shape the
// same as every other task action's.
func TestFollowUpOnAMissingTaskIs404(t *testing.T) {
	h := newActionHarness(t)
	resp, body := h.doJSON(t, http.MethodPost, followUpPath(9999),
		map[string]any{"prompt": "do it"})
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
}
