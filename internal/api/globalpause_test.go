package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

func decodePause(t *testing.T, body []byte) pauseResponse {
	t.Helper()
	var out pauseResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode pause: %v (%s)", err, body)
	}
	return out
}

// TestGlobalPauseRoutes drives the three global pause routes (task 149):
// active by default, pausing while a task still runs, a repeat pause that
// keeps the first reason, the per-task mark on the wire, and a continue that
// withdraws the pending request.
func TestGlobalPauseRoutes(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskRunning)

	resp, body := h.doJSON(t, http.MethodGet, "/v1/daemon/pause", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	if got := decodePause(t, body); got.State != "active" || got.PausedAt != nil {
		t.Fatalf("fresh daemon = %+v, want active", got)
	}

	resp, body = h.doJSON(t, http.MethodPost, "/v1/daemon/pause", map[string]any{"reason": "line one\nline two"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("multi-line reason: %d %s, want 400", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodPost, "/v1/daemon/pause", map[string]any{"reason": strings.Repeat("x", 201)})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("long reason: %d %s, want 400", resp.StatusCode, body)
	}

	resp, body = h.doJSON(t, http.MethodPost, "/v1/daemon/pause", map[string]any{"reason": "  upgrading  "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pause: %d %s", resp.StatusCode, body)
	}
	got := decodePause(t, body)
	if got.State != "pausing" || got.Running != 1 || got.Reason != "upgrading" || got.PausedAt == nil {
		t.Fatalf("pause = %+v, want pausing, one running, reason upgrading", got)
	}
	resp, body = h.doJSON(t, http.MethodPost, "/v1/daemon/pause", nil)
	if resp.StatusCode != http.StatusOK || decodePause(t, body).Reason != "upgrading" {
		t.Fatalf("repeat pause: %d %s, want 200 keeping the first reason", resp.StatusCode, body)
	}

	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	if tr := decodeTask(t, body); !tr.PauseRequested || !tr.GlobalPause {
		t.Fatalf("running task under the pause: pause_requested=%v global_pause=%v, want both", tr.PauseRequested, tr.GlobalPause)
	}

	resp, body = h.doJSON(t, http.MethodPost, "/v1/daemon/continue", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("continue: %d %s", resp.StatusCode, body)
	}
	got = decodePause(t, body)
	if got.State != "active" || got.Resumed == nil || *got.Resumed != 0 {
		t.Fatalf("continue = %+v, want active with nothing resumed", got)
	}
	_, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", task.ID), nil)
	if tr := decodeTask(t, body); tr.PauseRequested || tr.GlobalPause {
		t.Fatalf("after continue: pause_requested=%v global_pause=%v, want the request withdrawn", tr.PauseRequested, tr.GlobalPause)
	}
}
