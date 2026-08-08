package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

func decodeTaskList(t *testing.T, body []byte) []listTaskResponse {
	t.Helper()
	var out []listTaskResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode task list: %v (%s)", err, body)
	}
	return out
}

func listTasks(t *testing.T, h *taskHarness, query string) []listTaskResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tasks%s: %d %s", query, resp.StatusCode, body)
	}
	return decodeTaskList(t, body)
}

// TestTaskListCarriesBoardColumns is the anti-N+1 guarantee: everything §15's
// board renders comes back in this one call.
func TestTaskListCarriesBoardColumns(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	rows := listTasks(t, h, "")
	if len(rows) != 1 {
		t.Fatalf("listed %d tasks, want 1", len(rows))
	}
	got := rows[0]
	if got.ID != task.ID {
		t.Fatalf("id = %d, want %d", got.ID, task.ID)
	}
	if got.ProjectName == "" {
		t.Error("project_name is empty; a board would have to join /v1/projects itself")
	}
	// The built-in adhoc workflow is one step, `id: run`, with no name — so
	// the id is the fallback a board can always render.
	if got.StepTotal != 1 {
		t.Errorf("step_total = %d, want 1 (adhoc has one step)", got.StepTotal)
	}
	if got.StepName != "run" {
		t.Errorf("step_name = %q, want %q", got.StepName, "run")
	}
	// No attempts yet: no cost reported is not the same as free.
	if got.CostUSD != nil {
		t.Errorf("cost_usd = %v, want null before any attempt ran", *got.CostUSD)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("tokens = %d/%d, want 0/0", got.InputTokens, got.OutputTokens)
	}
	// The embedded task representation still carries everything it did.
	if len(got.AvailableActions) == 0 {
		t.Error("available_actions missing; the embedded taskResponse regressed")
	}
}

// TestTaskListCostSumsEveryAttempt mirrors the store rule at the HTTP edge:
// the board reports what the task actually cost, retries included.
func TestTaskListCostSumsEveryAttempt(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	for i, c := range []float64{0.12, 0.15, 0.14} {
		cost := c
		in, out := int64(100), int64(10)
		run := &store.StepRun{
			TaskID: task.ID, StepIndex: 0, StepID: "run", StepType: "agent",
			Attempt: i + 1, State: store.StepFailed,
			CostUSD: &cost, InputTokens: &in, OutputTokens: &out,
		}
		if err := h.store.CreateStepRun(t.Context(), run); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}

	rows := listTasks(t, h, "")
	if len(rows) != 1 {
		t.Fatalf("listed %d tasks, want 1", len(rows))
	}
	if rows[0].CostUSD == nil {
		t.Fatal("cost_usd is null though three attempts reported a cost")
	}
	if diff := *rows[0].CostUSD - 0.41; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost_usd = %v, want 0.41 (all three attempts)", *rows[0].CostUSD)
	}
	if rows[0].InputTokens != 300 || rows[0].OutputTokens != 30 {
		t.Errorf("tokens = %d/%d, want 300/30", rows[0].InputTokens, rows[0].OutputTokens)
	}
}

// TestTaskListStepNamePastLastStep covers the finished-task case: the cursor
// legitimately sits one past the end, and an out-of-range index must render
// blank rather than panic.
func TestTaskListStepNamePastLastStep(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	step := 1 // adhoc has exactly one step, so index 1 is past the end
	if err := h.store.SetTaskProgress(t.Context(), task.ID, &step, nil); err != nil {
		t.Fatalf("SetTaskProgress: %v", err)
	}

	rows := listTasks(t, h, "")
	if rows[0].StepName != "" {
		t.Errorf("step_name = %q, want empty past the last step", rows[0].StepName)
	}
	if rows[0].StepTotal != 1 {
		t.Errorf("step_total = %d, want 1", rows[0].StepTotal)
	}
}

// TestTaskListArchivedParam covers the default a board depends on, and the
// escapes from it.
func TestTaskListArchivedParam(t *testing.T) {
	h := newActionHarness(t)
	live := queuedTask(t, h)
	gone := queuedTask(t, h)
	setState(t, h, gone.ID, store.TaskArchived)

	ids := func(query string) []int64 {
		t.Helper()
		rows := listTasks(t, h, query)
		out := make([]int64, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}

	if got := ids(""); len(got) != 1 || got[0] != live.ID {
		t.Errorf("default = %v, want only the live task %d", got, live.ID)
	}
	if got := ids("?archived=false"); len(got) != 1 || got[0] != live.ID {
		t.Errorf("archived=false = %v, want only %d", got, live.ID)
	}
	if got := ids("?archived=true"); len(got) != 1 || got[0] != gone.ID {
		t.Errorf("archived=true = %v, want only %d", got, gone.ID)
	}
	if got := ids("?archived=all"); len(got) != 2 {
		t.Errorf("archived=all = %v, want both", got)
	}
	if got := ids("?state=archived"); len(got) != 1 || got[0] != gone.ID {
		t.Errorf("state=archived = %v, want %d — an explicit state must win", got, gone.ID)
	}
}

func TestTaskListArchivedParamRejectsGarbage(t *testing.T) {
	h := newActionHarness(t)
	resp, body := h.doJSON(t, http.MethodGet, "/v1/tasks?archived=yes", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", resp.StatusCode, body)
	}
	if code := decodeError(t, body).Code; code != CodeValidationFailed {
		t.Errorf("code = %q, want %q", code, CodeValidationFailed)
	}
}

// TestSnapshotCacheParsesOnce guards the reason the cache exists: listing is
// on the board's refresh path, and re-parsing immutable YAML per row per
// request is the cost it was added to avoid.
func TestSnapshotCacheParsesOnce(t *testing.T) {
	c := newSnapshotCache()
	const yaml = "name: x\nsteps:\n  - id: a\n    type: agent\n    prompt: hi\n  - id: b\n    name: second\n    type: agent\n    prompt: yo\n"

	first := c.get(7, yaml)
	if first.stepTotal != 2 {
		t.Fatalf("step_total = %d, want 2", first.stepTotal)
	}
	if first.stepName(0) != "a" {
		t.Errorf("step 0 name = %q, want the id %q as fallback", first.stepName(0), "a")
	}
	if first.stepName(1) != "second" {
		t.Errorf("step 1 name = %q, want the explicit name", first.stepName(1))
	}
	if first.stepName(2) != "" || first.stepName(-1) != "" {
		t.Error("out-of-range step index should render blank")
	}

	// A second get with deliberately different YAML must return the cached
	// parse: the key is the task id, and a task's snapshot cannot change.
	again := c.get(7, "name: y\nsteps: []\n")
	if again.stepTotal != 2 {
		t.Errorf("step_total = %d after re-get, want the cached 2", again.stepTotal)
	}

	c.forget(7)
	if after := c.get(7, "name: y\nsteps: []\n"); after.stepTotal != 0 {
		t.Errorf("step_total = %d after forget, want a fresh parse (0)", after.stepTotal)
	}
}

// TestSnapshotCacheToleratesUnparsableSnapshot keeps a corrupt snapshot from
// failing the whole board: the row renders without step columns.
func TestSnapshotCacheToleratesUnparsableSnapshot(t *testing.T) {
	c := newSnapshotCache()
	got := c.get(1, "this: is: not: a: workflow")
	if got.stepTotal != 0 || got.stepName(0) != "" {
		t.Errorf("got %+v, want an empty summary", got)
	}
}
