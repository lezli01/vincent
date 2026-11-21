package store

import (
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// TestHandoffDeliverAllKeepsAPartialDeliveryOpen is issue #1042. A
// resolve run that delivers only part of an issue records a delivery whose
// suggested close is `none` — leave the issue open (author's decision on
// #1042, amending §5.3's close_reason vocabulary). A merge train then
// resolves every item with one `vincent handoff deliver --all` and no
// explicit `--close`, which the API turns into Close with an empty reason,
// so each item falls back to its own delivery's suggestion (§5.7).
//
// The partial item must stay open, take the "merged in …" comment, and read
// the `open` lane (author's decision on #1042, amending §5.6: a delivered
// item left open does not keep the issue in `hand_off`, where nothing would
// ever pick it up again), while a complete item beside it still closes as
// completed. Before the fix the delivery cannot say `none`, so #1017 was
// closed as completed with eight of its ten sub-tasks undone.
func TestHandoffDeliverAllKeepsAPartialDeliveryOpen(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	whole, _ := handedOffIssue(t, s, p.ID, "whole", "w1")

	part := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "part"})
	task := newTask(p.ID, "part", TaskRunning)
	task.IssueID, task.IssueWorktree = &part.ID, IssueWorktreeMain
	mustCreate(t, s, task)
	d := recordedDelivery("part")
	d.HeadSHA = "p1"
	d.CloseReason = "none" // `vincent task deliver --close-reason none`
	if _, _, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskDone, TaskChange{Delivery: d}); err != nil {
		t.Fatalf("finish part: %v", err)
	}

	h, _ := mustHandoff(t, s, p.ID, "train", whole.ID, part.ID)

	// `deliver --all --comment-file merged.md`, no --close: Close with no
	// reason, so each item's delivery decides.
	_, err := s.ResolveHandoffItems(ctx, HandoffVerb{
		HandoffID: h.ID, All: true, Outcome: HandoffDelivered, Close: true,
		Comment: "Merged in #1 as abc1234.", CommentAuthor: "me",
		By: HandoffByHumanActor, IssueActor: issuestate.Human,
	})
	if err != nil {
		t.Fatalf("deliver --all over a partial delivery: %v", err)
	}

	if got := mustGetIssue(t, s, whole.ID); got.State != issuestate.Closed || got.CloseReason != issuestate.Completed {
		t.Errorf("complete item = %s/%s, want closed/completed", got.State, got.CloseReason)
	}
	got := mustGetIssue(t, s, part.ID)
	if got.State == issuestate.Closed {
		t.Fatalf("partial delivery closed its issue as %q; a delivery suggesting close_reason none must leave it open", got.CloseReason)
	}
	if got.Lane != issuestate.LaneOpen {
		t.Errorf("partial delivery's open issue reads lane %q, want %q so the resolve sweep can pick it up again", got.Lane, issuestate.LaneOpen)
	}
	if it := itemOf(t, s, h.ID, part.ID); it.Outcome != HandoffDelivered {
		t.Errorf("partial item outcome = %q, want delivered", it.Outcome)
	}
	if comments, _ := s.ListIssueComments(ctx, part.ID); len(comments) != 1 {
		t.Errorf("partial issue comments = %+v, want the one merge comment", comments)
	}
}
