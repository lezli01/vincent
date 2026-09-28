package api

// A task that blocks before any step runs has no step row to explain it, so
// until issue #594 its explanation lived only in daemon.log: the API served a
// bare `block_reason` code and nothing else. `block_detail` is the
// daemon-authored sentence that goes with the code, served on both the list
// rows and the detail endpoint.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/worktree"
)

func TestWorktreePathOccupiedBlockServesADetail(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	created := h.createTask(t, map[string]any{"title": "occupied"})

	// Squat on the path the task's worktree will be created at, before the
	// runner admits it: creation refuses a non-empty target.
	target := h.wt.Path(worktree.TaskOwner(created.ID))
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "squatter.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write squatter: %v", err)
	}

	h.runner.Start(t.Context())
	h.sched.Start(t.Context())
	t.Cleanup(h.runner.Stop)
	t.Cleanup(h.sched.Stop)
	h.sched.Wake()

	blocked := h.waitForState(t, created.ID, "blocked")
	if got := sval(blocked.BlockReason); got != worktree.ReasonWorktreePathOccupied {
		t.Fatalf("block_reason = %s, want %s", got, worktree.ReasonWorktreePathOccupied)
	}

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	var detail map[string]any
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("task body: %v", err)
	}
	assertOccupiedDetail(t, "GET /v1/tasks/{id}", detail, target)

	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks?project_id=%d", h.projectID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("list = %v, %d rows, want 1", err, len(rows))
	}
	assertOccupiedDetail(t, "GET /v1/tasks", rows[0], target)
}

func assertOccupiedDetail(t *testing.T, where string, task map[string]any, target string) {
	t.Helper()
	got, _ := task["block_detail"].(string)
	if got == "" {
		t.Errorf("%s: block_detail = %v, want a sentence saying why the worktree could not be created "+
			"(the explanation otherwise exists only in daemon.log)", where, task["block_detail"])
		return
	}
	if !strings.Contains(got, target) {
		t.Errorf("%s: block_detail = %q, want it to name the occupied path %s", where, got, target)
	}
}
