package tui

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/taskstate"
)

// gitErr is the archive refusal no retry fixes (task 153).
func gitErr() error {
	return &apiclient.Error{
		Status: http.StatusConflict, Code: "invalid_state",
		Message: "git_error: git status failed: exit 128",
		Details: map[string]string{"reason": "git_error"},
	}
}

// TestStuckArchiveOffersForceDelete: an archive that failed on git becomes
// the force-delete question instead of an error the task is stuck behind.
func TestStuckArchiveOffersForceDelete(t *testing.T) {
	var bar actionBar
	target := targetFor(taskstate.Done)
	bar.applyResult(actionResultMsg{taskID: target.id, action: apiclient.ActionArchive, err: gitErr()})
	if !bar.capturing() || bar.pending != actionForceDelete {
		t.Fatalf("a git_error archive did not offer the force delete (pending %q)", bar.pending)
	}
	got := bar.render(target)
	for _, want := range []string{"force delete #7?", "(y/n)", "git_error", "branch stays"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt = %q, want it to contain %q", got, want)
		}
	}
	if _, _ = bar.handleKey("n", nil, target); bar.capturing() {
		t.Error("n did not dismiss the force-delete question")
	}
}

// TestStateConflictDoesNotOfferForceDelete: a 409 naming a state is the task
// having moved on, not a stuck one; nor is a dirty worktree, whose answer is
// force.
func TestStateConflictDoesNotOfferForceDelete(t *testing.T) {
	for name, err := range map[string]error{
		"state": &apiclient.Error{Status: http.StatusConflict, Details: map[string]string{"state": "running"}},
		"dirty": &apiclient.Error{Status: http.StatusConflict, Details: map[string]string{"reason": "worktree_dirty"}},
		"plain": errors.New("connection refused"),
	} {
		var bar actionBar
		bar.applyResult(actionResultMsg{taskID: 7, action: apiclient.ActionArchive, err: err})
		if bar.pending == actionForceDelete {
			t.Errorf("%s: offered a force delete", name)
		}
	}
	if isStuckArchive(apiclient.ActionCancel, gitErr()) {
		t.Error("a cancel's failure was read as a stuck archive")
	}
}

// TestForceDeleteReportsWhatWasLeftBehind: the cleanup's warnings are the
// one consequence a human cannot see afterwards — the row is gone.
func TestForceDeleteReportsWhatWasLeftBehind(t *testing.T) {
	var bar actionBar
	bar.applyResult(actionResultMsg{
		taskID: 7, action: actionForceDelete,
		warnings: []string{"task 7: worktree /x was kept", "task 8: other"},
	})
	if !strings.Contains(bar.status, "#7 force-deleted") || !strings.Contains(bar.status, "left behind") ||
		!strings.Contains(bar.status, "1 more") || !bar.statusBad {
		t.Errorf("status = %q (bad %v), want the delete and what it left", bar.status, bar.statusBad)
	}
	bar = actionBar{}
	bar.applyResult(actionResultMsg{taskID: 7, action: actionForceDelete})
	if bar.status != "#7 force-deleted" || bar.statusBad {
		t.Errorf("clean status = %q (bad %v)", bar.status, bar.statusBad)
	}
}

// TestBulkStuckArchiveOffersForceDeleteForThoseOnly: of a batch, only the
// archives git refused are offered the force delete.
func TestBulkStuckArchiveOffersForceDeleteForThoseOnly(t *testing.T) {
	var bar actionBar
	bar.applyBulkResult(bulkResultMsg{
		action: apiclient.ActionArchive, total: 3, done: []int64{1},
		failed: []bulkFailure{
			{id: 2, err: gitErr()},
			{id: 3, err: &apiclient.Error{Status: http.StatusConflict, Details: map[string]string{"state": "running"}}},
		},
	})
	if bar.pending != actionForceDelete || len(bar.pendingIDs) != 1 || bar.pendingIDs[0] != 2 {
		t.Fatalf("pending %q over %v, want a force delete of task 2 only", bar.pending, bar.pendingIDs)
	}
	if got := bar.confirmPrompt(taskActions{id: 9}); !strings.Contains(got, "1 of 3") {
		t.Errorf("prompt = %q, want it to name the subset", got)
	}
}

// TestRefusedDeleteOffersForce: the archived boards re-ask a refused delete
// by force — but never one refused for a live process, and never a force
// delete's own refusals.
func TestRefusedDeleteOffersForce(t *testing.T) {
	refused := func(reason string) bulkFailure {
		return bulkFailure{id: 4, err: &apiclient.Error{
			Status: http.StatusConflict, Message: "task 4 still has fan-out lanes",
			Details: map[string]string{"reason": reason},
		}}
	}
	offer := deleteResultMsg{total: 1, refused: []bulkFailure{refused("has_lanes")}}.forceOffer()
	if offer == nil || !offer.force || len(offer.ids) != 1 || offer.ids[0] != 4 {
		t.Fatalf("offer = %+v, want a force delete of task 4", offer)
	}
	if q := offer.question(); !strings.Contains(q, "force delete task 4?") || strings.Contains(q, "[b]") {
		t.Errorf("question = %q, want a two-answer force question", q)
	}
	if offer := (deleteResultMsg{refused: []bulkFailure{refused("live_process")}}).forceOffer(); offer != nil {
		t.Errorf("a live process was offered a force delete: %+v", offer)
	}
	if offer := (deleteResultMsg{forced: true, refused: []bulkFailure{refused("has_lanes")}}).forceOffer(); offer != nil {
		t.Errorf("a force delete's refusal was re-offered: %+v", offer)
	}
}

// TestChatsStuckArchiveOffersForceDelete: the chats board's archive takes the
// same way out as the task board's.
func TestChatsStuckArchiveOffersForceDelete(t *testing.T) {
	v := chatsFixture()
	v.applyArchived(chatArchivedMsg{id: 2, err: gitErr()})
	if v.delPrompt == nil || !v.delPrompt.force || !v.delPrompt.chats {
		t.Fatalf("a git_error archive did not offer the force delete: %+v", v.delPrompt)
	}
	if cmd := v.answerDelete("b"); cmd != nil || v.delPrompt == nil {
		t.Error("b answered a force delete, which never deletes a branch")
	}
}

// TestIssueDeleteHeldOffersForce: a delete refused because a main task holds
// the issue is re-asked by force; any other failure is not.
func TestIssueDeleteHeldOffersForce(t *testing.T) {
	prev := &issueAction{issue: apiclient.Issue{ID: 5}, action: issueActDelete}
	held := &apiclient.Error{
		Status: http.StatusConflict, Code: "issue_has_live_main_task",
		Message: "issue 5 has an unsettled main task 9", Details: map[string]string{"task_id": "9"},
	}
	offer := issueForceOffer(nil, prev, issueActedMsg{id: 5, action: issueActDelete, err: held})
	if offer == nil || offer.action != issueActForceDelete || offer.stage != iaConfirm {
		t.Fatalf("offer = %+v, want the force-delete confirmation", offer)
	}
	text := strings.Join(offer.confirmText(), "\n")
	if !strings.Contains(text, "unsettled main task 9") {
		t.Errorf("confirmation %q does not say what holds the issue", text)
	}
	// It is drawn on every frame, so reading it must not consume anything.
	if again := strings.Join(offer.confirmText(), "\n"); again != text {
		t.Errorf("the confirmation changed between renders: %q then %q", text, again)
	}
	if issueForceOffer(nil, prev, issueActedMsg{id: 5, action: issueActDelete, err: errors.New("boom")}) != nil {
		t.Error("a plain failure was offered a force delete")
	}
	note := actedNote(issueActedMsg{
		id: 5, action: issueActForceDelete,
		forced: apiclient.ForceDeletedIssue{TaskIDs: []int64{9}},
	})
	if !strings.Contains(note, "force-deleted issue #5") || !strings.Contains(note, "1 task") {
		t.Errorf("note = %q", note)
	}
}
