package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// blockedTask puts a queued task into `blocked` with a reason, which is the
// only state a repair may be asked for from (§6, task 025).
func blockedTask(t *testing.T, h *taskHarness, reason string) taskResponse {
	t.Helper()
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskRunning)
	if _, _, err := h.store.TransitionTask(t.Context(), task.ID,
		store.TaskRunning, store.TaskBlocked, store.TaskChange{BlockReason: &reason}); err != nil {
		t.Fatalf("block: %v", err)
	}
	return task
}

func TestRepairFromBlocked(t *testing.T) {
	h := newActionHarness(t)
	task := blockedTask(t, h, "check_failed")

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/repair", task.ID),
		map[string]any{"prompt": "fix the failing check"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repair: %d %s", resp.StatusCode, body)
	}
	got := decodeTask(t, body)
	if got.State != string(store.TaskQueued) {
		t.Errorf("state = %s, want queued — the scheduler admits a repair like anything else", got.State)
	}

	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.PendingRepair == nil {
		t.Fatal("the repair request was not persisted")
	}
	if stored.PendingRepair.Prompt != "fix the failing check" {
		t.Errorf("prompt = %q, want what was posted", stored.PendingRepair.Prompt)
	}
	if stored.PendingRepair.BlockReason != "check_failed" {
		t.Errorf("block reason = %q, want the one the task carried",
			stored.PendingRepair.BlockReason)
	}
	if stored.RetryCursorAt != nil {
		t.Error("repair moved the retry cursor; a repair is not a retry (§7.2)")
	}
}

// TestRepairIsOfferedExactlyWhileBlocked: `available_actions` is derived from
// §6, so the endpoint and the bar a client renders cannot disagree.
func TestRepairIsOfferedExactlyWhileBlocked(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	if hasAction(task.AvailableActions, "repair") {
		t.Errorf("queued actions = %v, must not offer repair", task.AvailableActions)
	}
	for _, state := range []store.TaskState{
		store.TaskRunning, store.TaskAwaitingGate, store.TaskBlocked, store.TaskPaused,
	} {
		setState(t, h, task.ID, state)
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", task.ID), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get: %d %s", resp.StatusCode, body)
		}
		got := decodeTask(t, body)
		want := state == store.TaskBlocked
		if hasAction(got.AvailableActions, "repair") != want {
			t.Errorf("%s actions = %v, repair offered = %v, want %v",
				state, got.AvailableActions, !want, want)
		}
	}
}

// TestRepairFromEveryOtherStateIs409: 409 with the state actually found, so a
// client can re-issue against what it did not expect (§13.1).
func TestRepairFromEveryOtherStateIs409(t *testing.T) {
	for _, state := range taskstate.All {
		if state == store.TaskBlocked {
			continue
		}
		t.Run(string(state), func(t *testing.T) {
			h := newActionHarness(t)
			task := queuedTask(t, h)
			setState(t, h, task.ID, state)

			resp, body := h.doJSON(t, http.MethodPost,
				fmt.Sprintf("/v1/tasks/%d/repair", task.ID), map[string]any{"prompt": "fix it"})
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("repair from %s: %d %s, want 409", state, resp.StatusCode, body)
			}
			if got := decodeError(t, body).Details["state"]; got != string(state) {
				t.Errorf("details.state = %v, want %s", got, state)
			}
		})
	}
}

func TestRepairRejectsAnEmptyPrompt(t *testing.T) {
	h := newActionHarness(t)
	task := blockedTask(t, h, "check_failed")

	for _, prompt := range []any{"", "   \n\t "} {
		resp, body := h.doJSON(t, http.MethodPost,
			fmt.Sprintf("/v1/tasks/%d/repair", task.ID), map[string]any{"prompt": prompt})
		wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	}
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.State != store.TaskBlocked || stored.PendingRepair != nil {
		t.Errorf("a refused repair moved the task: %s, pending %+v", stored.State, stored.PendingRepair)
	}
}

// TestRepairSelectionValidationMatchesCreation applies §8.2 to the repair's
// own triple the way POST /v1/tasks applies it to a task's: an unregistered
// agent and a known-invalid value are 400s, an unknown one is a warning.
func TestRepairSelectionValidationMatchesCreation(t *testing.T) {
	h := newActionHarness(t)
	task := blockedTask(t, h, "check_failed")

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/repair", task.ID),
		map[string]any{"prompt": "fix it", "agent": "not-an-adapter"})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "not-an-adapter") {
		t.Errorf("message %s must name the agent that was asked for", body)
	}

	// `minimal` is a codex-only effort, and the repair resolves to claude.
	resp, body = h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/repair", task.ID),
		map[string]any{"prompt": "fix it", "agent": "claude", "effort": "minimal"})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "codex") {
		t.Errorf("message %s must name the owning adapter", body)
	}

	// A model no catalog knows starts the repair, with a warning on the body.
	resp, body = h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/repair", task.ID),
		map[string]any{"prompt": "fix it", "agent": "claude", "model": "made-up-model-x"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repair with an unknown model: %d %s", resp.StatusCode, body)
	}
	got := decodeTask(t, body)
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "made-up-model-x") {
		t.Errorf("warnings = %v, want one naming the unknown model", got.Warnings)
	}
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.PendingRepair == nil || stored.PendingRepair.Model != "made-up-model-x" {
		t.Errorf("the selection did not reach the request: %+v", stored.PendingRepair)
	}
}

func TestRepairOnAMissingTaskIs404(t *testing.T) {
	h := newActionHarness(t)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks/9999/repair",
		map[string]any{"prompt": "fix it"})
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
}
