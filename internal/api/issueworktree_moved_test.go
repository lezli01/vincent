package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// Task 134.12: a main task that handed the issue's main worktree to the next
// main task, as the API sees it. The hand-off itself is the engine's; these
// build it with the store's TransferIssueWorktree.

// movedMainTask is a done main task that worked in the issue's main worktree
// and handed it to a queued successor, after which the successor committed
// on the shared branch. It returns both tasks and the predecessor's end_sha.
func movedMainTask(t *testing.T, h *taskHarness) (pred, succ taskResponse, endSHA string) {
	t.Helper()
	iss := h.issue(t)
	pred = h.createTask(t, map[string]any{"issue_id": iss.ID})
	succ = h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "next"})
	dir := commitsWorktree(t, h, pred.ID)
	commitFile(t, dir, "pred.txt", "predecessor work")
	setState(t, h, pred.ID, store.TaskRunning)
	setState(t, h, pred.ID, store.TaskDone)
	endSHA = strings.TrimSpace(testrepo.Run(t, dir, "rev-parse", "HEAD"))
	if err := h.store.TransferIssueWorktree(t.Context(), pred.ID, succ.ID, dir, endSHA); err != nil {
		t.Fatalf("TransferIssueWorktree: %v", err)
	}
	// The branch moves on under the successor.
	commitFile(t, dir, "succ.txt", "successor work")
	return pred, succ, endSHA
}

// TestMovedMainTaskRefusesFollowUpAndChat is task 134 decision 14: both
// actions that would work in the task's directory are a 409 naming the task
// that holds it now.
func TestMovedMainTaskRefusesFollowUpAndChat(t *testing.T) {
	h := newLinkedHarness(t)
	pred, succ, _ := movedMainTask(t, h.taskHarness)
	for name, req := range map[string]struct {
		path string
		body map[string]any
	}{
		"follow_up": {followUpPath(pred.ID), map[string]any{"prompt": "more"}},
		"chat":      {fmt.Sprintf("/v1/tasks/%d/chat", pred.ID), map[string]any{}},
	} {
		code, body := h.post(t, req.path, req.body)
		if code != http.StatusConflict {
			t.Errorf("%s = %d %s, want 409", name, code, body)
			continue
		}
		e := decodeError(t, body)
		if e.Code != CodeIssueWorktreeMoved {
			t.Errorf("%s code = %q, want %q", name, e.Code, CodeIssueWorktreeMoved)
		}
		if e.Details["holder_task_id"] != fmt.Sprint(succ.ID) || e.Details["state"] != string(store.TaskDone) {
			t.Errorf("%s details = %v, want holder %d in state done", name, e.Details, succ.ID)
		}
		if !strings.Contains(e.Message, fmt.Sprintf("task %d", succ.ID)) {
			t.Errorf("%s message = %q, want it to name task %d", name, e.Message, succ.ID)
		}
	}
	stored, err := h.store.GetTask(t.Context(), pred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != store.TaskDone || stored.PendingFollowUp != nil {
		t.Errorf("a refused follow-up wrote: state %s, pending %+v", stored.State, stored.PendingFollowUp)
	}
}

// TestDoneMainTaskHoldingItsWorktreeIsNotRefused: until the hand-off, a
// finished main task still owns the directory and may be followed up.
func TestDoneMainTaskHoldingItsWorktreeIsNotRefused(t *testing.T) {
	h := newActionHarness(t)
	iss := h.issue(t)
	pred := h.createTask(t, map[string]any{"issue_id": iss.ID})
	h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "next"})
	commitsWorktree(t, h, pred.ID)
	setState(t, h, pred.ID, store.TaskRunning)
	setState(t, h, pred.ID, store.TaskDone)

	resp, body := h.doJSON(t, http.MethodPost, followUpPath(pred.ID), map[string]any{"prompt": "more"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("follow_up = %d %s, want 200", resp.StatusCode, body)
	}
}

// TestIssueDeleteWaitsForItsMainTasks: an issue whose main task has not
// settled is a 409 naming it; once every main task is settled or archived
// the delete goes through.
func TestIssueDeleteWaitsForItsMainTasks(t *testing.T) {
	h := newActionHarness(t)
	iss := h.issue(t)
	first := h.createTask(t, map[string]any{"issue_id": iss.ID})
	second := h.createTask(t, map[string]any{"issue_id": iss.ID, "title": "second"})
	path := fmt.Sprintf("/v1/issues/%d", iss.ID)

	resp, body := h.doJSON(t, http.MethodDelete, path, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete with queued main tasks = %d %s, want 409", resp.StatusCode, body)
	}
	if e := decodeError(t, body); e.Code != CodeIssueHasLiveMainTask || e.Details["task_id"] != fmt.Sprint(first.ID) {
		t.Errorf("error = %+v, want %s naming task %d", e, CodeIssueHasLiveMainTask, first.ID)
	}

	setState(t, h, first.ID, store.TaskArchived)
	resp, body = h.doJSON(t, http.MethodDelete, path, nil)
	if e := decodeError(t, body); resp.StatusCode != http.StatusConflict || e.Details["task_id"] != fmt.Sprint(second.ID) {
		t.Fatalf("delete with one main task left = %d %s, want 409 naming task %d", resp.StatusCode, body, second.ID)
	}

	setState(t, h, second.ID, store.TaskRunning)
	setState(t, h, second.ID, store.TaskDone)
	if resp, body := h.doJSON(t, http.MethodDelete, path, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete with every main task settled = %d %s, want 204", resp.StatusCode, body)
	}
}

// TestMovedMainTaskCommitsStopAtEndSHA: the shared branch went on under the
// successor, but the predecessor's commits are its own.
func TestMovedMainTaskCommitsStopAtEndSHA(t *testing.T) {
	h := newActionHarness(t)
	pred, _, endSHA := movedMainTask(t, h)
	got := taskCommitsOK(t, h, pred.ID)
	if s := strings.Join(subjects(got), ","); s != "predecessor work" {
		t.Fatalf("subjects = %q, want only the predecessor's commit", s)
	}
	if got[0].SHA != endSHA {
		t.Errorf("newest sha = %q, want end_sha %q", got[0].SHA, endSHA)
	}
}

// TestMovedMainTaskDiffIsBaseToEndSHA: with no worktree of its own left, the
// predecessor's diff is read from the project repository and ends at its
// end_sha, in both the plain and the by-lane form.
func TestMovedMainTaskDiffIsBaseToEndSHA(t *testing.T) {
	h := newActionHarness(t)
	pred, _, _ := movedMainTask(t, h)

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", pred.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff = %d %s", resp.StatusCode, body)
	}
	if diff := string(body); !strings.Contains(diff, "pred.txt") || strings.Contains(diff, "succ.txt") {
		t.Errorf("diff is not base..end_sha:\n%s", diff)
	}

	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff?by=lane", pred.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff by lane = %d %s", resp.StatusCode, body)
	}
	var lanes diffLanesResponse
	if err := json.Unmarshal(body, &lanes); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	var all strings.Builder
	for _, s := range lanes.Sections {
		all.WriteString(s.Diff)
	}
	if !strings.Contains(all.String(), "pred.txt") || strings.Contains(all.String(), "succ.txt") {
		t.Errorf("lane sections are not base..end_sha: %+v", lanes.Sections)
	}
}
