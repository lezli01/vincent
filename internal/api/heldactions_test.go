package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Held follow-up and retry (task 096 decision C): `paused: true` on either
// action lands the task in `paused` instead of `queued`, and `resume` is what
// admits it. The affordance is on the route for every client — a trigger's
// `propose` reaction replays it and adds no semantics of its own.

func retryPathOf(id int64) string { return fmt.Sprintf("/v1/tasks/%d/retry", id) }

func resumePathOf(id int64) string { return fmt.Sprintf("/v1/tasks/%d/resume", id) }

// assertNotAdmissible is the store's own admission query as the assertion: a
// paused row is invisible to it by construction, so there is no window to race.
func assertNotAdmissible(t *testing.T, h *taskHarness, id int64) {
	t.Helper()
	cands, err := h.store.ListAdmissible(t.Context())
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	for _, c := range cands {
		if c.Task.ID == id {
			t.Fatalf("a held task is admissible: %+v", c.Task)
		}
	}
}

// TestFollowUpPausedLandsPausedAndResumeQueues: the request is persisted with
// its origin exactly as an unheld one is, the task waits in `paused`, and
// `resume` re-queues it with the request intact — from both origins.
func TestFollowUpPausedLandsPausedAndResumeQueues(t *testing.T) {
	for _, origin := range []store.TaskState{store.TaskDone, store.TaskAborted} {
		t.Run(string(origin), func(t *testing.T) {
			h := newActionHarness(t)
			task := finishedTask(t, h, origin)

			resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
				map[string]any{"prompt": "rebase onto main", "paused": true})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("follow_up paused: %d %s", resp.StatusCode, body)
			}
			if got := decodeTask(t, body); got.State != string(store.TaskPaused) {
				t.Fatalf("state = %s, want paused", got.State)
			}
			stored, err := h.store.GetTask(t.Context(), task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if req := stored.PendingFollowUp; req == nil || req.Origin != origin || req.Round != 1 {
				t.Fatalf("pending follow-up = %+v, want round 1 from %s", req, origin)
			}
			assertNotAdmissible(t, h, task.ID)

			resp, body = h.doJSON(t, http.MethodPost, resumePathOf(task.ID), nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("resume: %d %s", resp.StatusCode, body)
			}
			if got := decodeTask(t, body); got.State != string(store.TaskQueued) {
				t.Errorf("state after resume = %s, want queued", got.State)
			}
			resumed, err := h.store.GetTask(t.Context(), task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if req := resumed.PendingFollowUp; req == nil || req.Origin != origin {
				t.Errorf("resume dropped the follow-up: %+v", req)
			}
		})
	}
}

// TestFollowUpPausedFromEveryOtherStateIs409: the held table shares §6's
// from-set for follow_up, so a held request is refused exactly where a plain
// one is, with the state actually found.
func TestFollowUpPausedFromEveryOtherStateIs409(t *testing.T) {
	for _, state := range taskstate.All {
		if state == store.TaskDone || state == store.TaskAborted {
			continue
		}
		t.Run(string(state), func(t *testing.T) {
			h := newActionHarness(t)
			task := queuedTask(t, h)
			setState(t, h, task.ID, state)

			resp, body := h.doJSON(t, http.MethodPost, followUpPath(task.ID),
				map[string]any{"prompt": "do it", "paused": true})
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("held follow_up from %s: %d %s, want 409", state, resp.StatusCode, body)
			}
			if got := decodeError(t, body).Details["state"]; got != string(state) {
				t.Errorf("details.state = %v, want %s", got, state)
			}
		})
	}
}

// TestRetryPausedFromBlockedLandsPaused: the retry is recorded — the cursor
// stamp that hands the step a fresh budget — and only the admission waits.
func TestRetryPausedFromBlockedLandsPaused(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskBlocked)

	resp, body := h.doJSON(t, http.MethodPost, retryPathOf(task.ID), map[string]any{"paused": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry paused: %d %s", resp.StatusCode, body)
	}
	got := decodeRetry(t, body)
	if got.State != string(store.TaskPaused) || got.RetriedDescendants != 0 {
		t.Fatalf("retry = state %s, retried %d; want paused, 0", got.State, got.RetriedDescendants)
	}
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.RetryCursorAt == nil {
		t.Error("a held retry did not stamp retry_cursor_at; resume would re-run on a spent budget")
	}
	assertNotAdmissible(t, h, task.ID)

	resp, body = h.doJSON(t, http.MethodPost, resumePathOf(task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d %s", resp.StatusCode, body)
	}
	if s := decodeTask(t, body).State; s != string(store.TaskQueued) {
		t.Errorf("state after resume = %s, want queued", s)
	}

	// An explicit false is the plain retry, not a refusal.
	setState(t, h, task.ID, store.TaskBlocked)
	resp, body = h.doJSON(t, http.MethodPost, retryPathOf(task.ID), map[string]any{"paused": false})
	if resp.StatusCode != http.StatusOK || decodeTask(t, body).State != string(store.TaskQueued) {
		t.Errorf("retry paused:false = %d %s, want 200 queued", resp.StatusCode, body)
	}
}

// TestRetryPausedHoldsABlockedParentsLanes: a blocked parent's retry cascades
// to its blocked lanes, and a held one holds them too — `paused: true` starts
// nothing, anywhere in the tree.
func TestRetryPausedHoldsABlockedParentsLanes(t *testing.T) {
	h := newActionHarness(t)
	parent, lanes := parkedParent(t, h, store.TaskBlocked, store.TaskRunning)
	setState(t, h, parent.ID, store.TaskBlocked)

	resp, body := h.doJSON(t, http.MethodPost, retryPathOf(parent.ID), map[string]any{"paused": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("held retry of a blocked parent: %d %s", resp.StatusCode, body)
	}
	got := decodeRetry(t, body)
	if got.State != string(store.TaskPaused) || got.RetriedDescendants != 1 {
		t.Errorf("retry = state %s, retried %d; want paused, 1", got.State, got.RetriedDescendants)
	}
	if s := laneState(t, h, lanes[0].ID); s != store.TaskPaused {
		t.Errorf("blocked lane = %s, want paused — the cascade must hold too", s)
	}
	if s := laneState(t, h, lanes[1].ID); s != store.TaskRunning {
		t.Errorf("running lane = %s, want running — only blocked lanes cascade", s)
	}
}

// TestRetryPausedFromParkedParentIs400: the retry legal from
// `awaiting_children` re-admits the lanes and never queues the parent, so a
// hold has nothing to replace. It is a request to correct — a 400 — and it
// changes nothing, lanes included.
func TestRetryPausedFromParkedParentIs400(t *testing.T) {
	h := newActionHarness(t)
	parent, lanes := parkedParent(t, h, store.TaskBlocked)

	resp, body := h.doJSON(t, http.MethodPost, retryPathOf(parent.ID), map[string]any{"paused": true})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if s := laneState(t, h, parent.ID); s != store.TaskAwaitingChildren {
		t.Errorf("parent = %s, want awaiting_children", s)
	}
	if s := laneState(t, h, lanes[0].ID); s != store.TaskBlocked {
		t.Errorf("lane = %s, want blocked — a refused retry cascades nothing", s)
	}
}

// TestRetryPausedFromInvalidStateIs409: outside retry's own from-set the held
// request gets the route's existing 409, not the parked parent's 400.
func TestRetryPausedFromInvalidStateIs409(t *testing.T) {
	for _, state := range []store.TaskState{store.TaskQueued, store.TaskPaused, store.TaskDone} {
		t.Run(string(state), func(t *testing.T) {
			h := newActionHarness(t)
			task := queuedTask(t, h)
			setState(t, h, task.ID, state)

			resp, body := h.doJSON(t, http.MethodPost, retryPathOf(task.ID), map[string]any{"paused": true})
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("held retry from %s: %d %s, want 409", state, resp.StatusCode, body)
			}
			if got := decodeError(t, body).Details["state"]; got != string(state) {
				t.Errorf("details.state = %v, want %s", got, state)
			}
		})
	}
}

// TestHeldAbortedFollowUpRestoresAborted runs task 027 decision 7 through a
// hold with the real scheduler: an aborted-origin follow-up waits in `paused`,
// runs once resumed, and ends through Restore back in `aborted` with its
// request drained — the hold changes when the run starts and nothing about how
// it ends.
func TestHeldAbortedFollowUpRestoresAborted(t *testing.T) {
	h := newTaskHarness(t, 0, true)
	// Created paused and cancelled, so it is `aborted` without ever running:
	// started_at stays nil until the follow-up is admitted, which is the proof
	// below that the run happened.
	task := h.createTask(t, map[string]any{"title": "held follow-up", "paused": true})
	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/cancel", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: %d %s", resp.StatusCode, body)
	}

	resp, body = h.doJSON(t, http.MethodPost, followUpPath(task.ID),
		map[string]any{"run": "git --version", "paused": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("follow_up paused: %d %s", resp.StatusCode, body)
	}
	if s := decodeTask(t, body).State; s != string(store.TaskPaused) {
		t.Fatalf("state = %s, want paused", s)
	}
	// The live scheduler, given time to tick, leaves it alone.
	time.Sleep(300 * time.Millisecond)
	if got := h.getTask(t, task.ID); got.State != string(store.TaskPaused) {
		t.Fatalf("state after scheduler ticks = %s, want paused", got.State)
	}

	resp, body = h.doJSON(t, http.MethodPost, resumePathOf(task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d %s", resp.StatusCode, body)
	}
	h.waitForState(t, task.ID, string(store.TaskAborted))

	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.StartedAt == nil {
		t.Error("started_at is nil: the held follow-up never ran")
	}
	if stored.PendingFollowUp != nil {
		t.Errorf("the follow-up request survived its Restore: %+v", stored.PendingFollowUp)
	}
}
