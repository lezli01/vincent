package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The three `POST /v1/tasks` widenings event triggers need (task 096
// decisions 9, 17, 18). Each is a field of the route for every client, not a
// trigger-only path: a trigger replays this route and adds no execution
// semantics of its own (decision 1).

// TestTaskCreatePausedWaitsForResume is decision 9: `paused: true` inserts the
// row `paused`, the running scheduler never admits it, and `resume` does.
func TestTaskCreatePausedWaitsForResume(t *testing.T) {
	h := newTaskHarness(t, 0, true)
	created := h.createTask(t, map[string]any{
		"project_id": h.projectID, "title": "held", "paused": true,
	})
	if created.State != "paused" {
		t.Fatalf("created state = %q, want paused", created.State)
	}
	// Admission is invisible to a paused row by construction, so there is no
	// window to race; the store's own admission query is the assertion.
	cands, err := h.store.ListAdmissible(t.Context())
	if err != nil {
		t.Fatalf("ListAdmissible: %v", err)
	}
	for _, c := range cands {
		if c.Task.ID == created.ID {
			t.Fatalf("a task created paused is admissible: %+v", c.Task)
		}
	}
	// And the live scheduler, given time to tick, leaves it alone.
	time.Sleep(300 * time.Millisecond)
	if got := h.getTask(t, created.ID); got.State != "paused" {
		t.Fatalf("state after scheduler ticks = %q, want paused", got.State)
	}

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/resume", created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d %s", resp.StatusCode, body)
	}
	h.waitForState(t, created.ID, "done")
}

// TestTaskCreateStoresTheLimits: `restricted` and `max_task_cost_usd` are
// snapshotted on the row and served back, and an absent pair is the old task
// exactly — unclamped, no cap of its own.
func TestTaskCreateStoresTheLimits(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	limited := h.createTask(t, map[string]any{
		"project_id": h.projectID, "title": "limited",
		"restricted": true, "max_task_cost_usd": 2.5,
	})
	if !limited.Restricted {
		t.Errorf("restricted = false, want true")
	}
	if limited.MaxTaskCostUSD == nil || *limited.MaxTaskCostUSD != 2.5 {
		t.Errorf("max_task_cost_usd = %v, want 2.5", limited.MaxTaskCostUSD)
	}
	row, err := h.store.GetTask(t.Context(), limited.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !row.Restricted || row.MaxTaskCostUSD != 2.5 {
		t.Errorf("row = restricted %v, cap %v; want true, 2.5", row.Restricted, row.MaxTaskCostUSD)
	}

	plain := h.createTask(t, map[string]any{"project_id": h.projectID, "title": "plain"})
	if plain.Restricted || plain.MaxTaskCostUSD != nil || plain.State != "queued" {
		t.Errorf("plain task = restricted %v, cap %v, state %s; want false, nil, queued",
			plain.Restricted, plain.MaxTaskCostUSD, plain.State)
	}
}

func TestTaskCreateRefusesANegativeCostCap(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "title": "x", "max_task_cost_usd": -1,
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "max_task_cost_usd") {
		t.Errorf("message %s does not name the field", body)
	}
}

// TestIdempotencyDigestCoversTheLimits: the same key with a body differing
// only in one of the three new fields is a second operation, not a replay.
func TestIdempotencyDigestCoversTheLimits(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"paused":            {"paused": true},
		"restricted":        {"restricted": true},
		"max_task_cost_usd": {"max_task_cost_usd": 1.0},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTaskHarness(t, 0, false)
			base := map[string]any{"project_id": h.projectID, "title": "same"}
			h.createWithKey(t, base, "key-limits")

			changed := map[string]any{"project_id": h.projectID, "title": "same"}
			for k, v := range extra {
				changed[k] = v
			}
			resp, body := h.postTaskKey(t, changed, "key-limits")
			wantError(t, resp, body, http.StatusConflict, CodeInvalidState)
			if !strings.Contains(string(body), "idempotency_key_reused") {
				t.Errorf("409 body %s does not say the key was reused", body)
			}
		})
	}
}

// TestTaskCreateClampRefusedWhereItCannotRestrict is decision 17's correction
// of decision 12: a clamped task on an adapter that cannot restrict here is
// refused at creation by task 041's gate — through the same clamped
// resolution the engine runs — even though its workflow never asked for
// `restricted`.
func TestTaskCreateClampRefusedWhereItCannotRestrict(t *testing.T) {
	t.Cleanup(cursor.SetSandboxAvailable(false))
	h := newRestrictedHarness(t, agenttest.BuildFakeAgent(t))
	const loose = "name: loose\n" +
		"steps:\n" +
		"  - {id: free, type: agent, permission_mode: full-auto, prompt: anything}\n"
	writeWorkflowFile(t, h.globalDir, "loose", loose)
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	id := int64(p["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "clamped", "workflow": "loose", "agent": "cursor", "restricted": true,
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	for _, want := range []string{"free", "cursor", "restrict"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("message %s missing %q", body, want)
		}
	}
	// Unclamped, the same workflow on the same agent is what it always was.
	resp, body = h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "unclamped", "workflow": "loose", "agent": "cursor",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("unclamped task: %d %s", resp.StatusCode, body)
	}
}
