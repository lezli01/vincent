package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// stepDiffFixture gives a task a worktree whose history has three parts: a
// commit before the step, the step's own commit, and a commit after it, plus
// an uncommitted edit on top. It returns the shas around the step's commit —
// what the engine would have journaled as its range.
func stepDiffFixture(t *testing.T, h *taskHarness, id int64) (start, end string) {
	t.Helper()
	stored, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	created, err := h.wt.CreateAndClaim(t.Context(), h.repo, worktree.TaskOwner(id),
		stored.BranchName, stored.BaseBranch, true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if err := h.store.SetTaskProgress(t.Context(), id, nil, &created.Path, &created.BaseSHA); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	wt := created.Path
	commit := func(name, body string) string {
		testrepo.WriteFile(t, wt, name, body)
		testrepo.Run(t, wt, "add", ".")
		testrepo.Run(t, wt, "commit", "-q", "-m", name)
		return strings.TrimSpace(testrepo.Run(t, wt, "rev-parse", "HEAD"))
	}
	start = commit("before.txt", "an earlier step's work\n")
	end = commit("step.txt", "this step's work\n")
	commit("after.txt", "a later step's work\n")
	testrepo.WriteFile(t, wt, "step.txt", "edited after the step, never committed\n")
	return start, end
}

// addStepRun inserts a step run of task id with the given range and state.
func addStepRun(t *testing.T, h *taskHarness, id int64, state store.StepRunState, start, end *string) int64 {
	t.Helper()
	r := &store.StepRun{
		TaskID: id, StepIndex: 0, StepID: "implement", StepType: "command",
		Attempt: 1, State: state, HeadStart: start, HeadEnd: end,
	}
	if state != store.StepRunning {
		now := time.Now()
		r.FinishedAt = &now
	}
	if err := h.store.CreateStepRun(t.Context(), r); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return r.ID
}

func stepDiff(t *testing.T, h *taskHarness, taskID, runID int64, extra string) (*http.Response, []byte) {
	t.Helper()
	return h.doJSON(t, http.MethodGet,
		fmt.Sprintf("/v1/tasks/%d/diff?step_run=%d%s", taskID, runID, extra), nil)
}

// A finished run diffs exactly its own range: the step's commit, and neither
// the commit before nor the one after nor the uncommitted edit (task 168.6).
// The body is the whole-task diff's text/plain shape, and `by=lane` wraps the
// same bytes in a single remainder section.
func TestDiffStepRunFinishedIsItsRange(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	start, end := stepDiffFixture(t, h, task.ID)
	runID := addStepRun(t, h, task.ID, store.StepSucceeded, &start, &end)

	resp, body := stepDiff(t, h, task.ID, runID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff?step_run: %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want the whole-task diff's text/plain", ct)
	}
	got := string(body)
	if !strings.Contains(got, "+this step's work") {
		t.Errorf("the step's own commit is missing:\n%s", got)
	}
	for _, not := range []string{"before.txt", "after.txt", "never committed"} {
		if strings.Contains(got, not) {
			t.Errorf("the range diff carries %q, which is outside it:\n%s", not, got)
		}
	}

	resp, grouped := stepDiff(t, h, task.ID, runID, "&by=lane")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff?step_run&by=lane: %d %s", resp.StatusCode, grouped)
	}
	var out diffLanesResponse
	if err := json.Unmarshal(grouped, &out); err != nil {
		t.Fatalf("decode %s: %v", grouped, err)
	}
	if len(out.Sections) != 1 || !out.Sections[0].Remainder || out.Sections[0].Diff != got {
		t.Errorf("by=lane = %+v, want one remainder section holding the flat body", out.Sections)
	}
}

// A running run diffs its start against the live worktree, as the whole-task
// diff does from the merge-base: every commit since it started, and what is
// uncommitted.
func TestDiffStepRunRunningIsStartToWorktree(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	start, _ := stepDiffFixture(t, h, task.ID)
	runID := addStepRun(t, h, task.ID, store.StepRunning, &start, nil)

	resp, body := stepDiff(t, h, task.ID, runID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff?step_run: %d %s", resp.StatusCode, body)
	}
	got := string(body)
	for _, want := range []string{"step.txt", "after.txt", "+edited after the step, never committed"} {
		if !strings.Contains(got, want) {
			t.Errorf("the live diff is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "before.txt") {
		t.Errorf("the live diff reaches back before the run started:\n%s", got)
	}
}

// A run with no range — a row from before the record, and one recovery
// finalized without an end — is a 409 that says so, never an empty diff that
// would read as "this step changed nothing".
func TestDiffStepRunUnrecordedIsConflict(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	start, _ := stepDiffFixture(t, h, task.ID)

	for name, runID := range map[string]int64{
		"no range at all":     addStepRun(t, h, task.ID, store.StepSucceeded, nil, nil),
		"interrupted, no end": addStepRun(t, h, task.ID, store.StepInterrupted, &start, nil),
		"running, no start":   addStepRun(t, h, task.ID, store.StepRunning, nil, nil),
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := stepDiff(t, h, task.ID, runID, "")
			wantError(t, resp, body, http.StatusConflict, CodeStepRangeUnrecorded)
		})
	}
}

// Another task's run, an unknown one, and a malformed id: the run must be this
// task's, and a run of another task is as unknown here as one that never
// existed.
func TestDiffStepRunOfAnotherTaskIsNotFound(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	start, end := stepDiffFixture(t, h, task.ID)
	other := queuedTask(t, h)
	foreign := addStepRun(t, h, other.ID, store.StepSucceeded, &start, &end)

	resp, body := stepDiff(t, h, task.ID, foreign, "")
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
	resp, body = stepDiff(t, h, task.ID, foreign+1000, "")
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)

	for _, bad := range []string{"abc", "0", "-3"} {
		resp, body := h.doJSON(t, http.MethodGet,
			fmt.Sprintf("/v1/tasks/%d/diff?step_run=%s", task.ID, bad), nil)
		wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	}
}
