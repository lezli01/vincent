package store

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

func resolveItems(t *testing.T, s *Store, v HandoffVerb) *HandoffVerbResult {
	t.Helper()
	if v.By == "" {
		v.By = HandoffByHumanActor
	}
	if v.IssueActor == "" {
		v.IssueActor = issuestate.Human
	}
	res, err := s.ResolveHandoffItems(t.Context(), v)
	if err != nil {
		t.Fatalf("ResolveHandoffItems(%s %v): %v", v.Outcome, v.IssueIDs, err)
	}
	return res
}

func itemOf(t *testing.T, s *Store, handoffID, issueID int64) *HandoffItem {
	t.Helper()
	items, _, err := s.ListHandoffItems(t.Context(), handoffID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.IssueID == issueID {
			return it
		}
	}
	t.Fatalf("handoff %d has no item for issue %d", handoffID, issueID)
	return nil
}

func claimed(t *testing.T, s *Store, projectID, issueID int64) bool {
	t.Helper()
	_, ok, err := s.IssueClaim(t.Context(), projectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestHandoffDeliver: deliver closes with the asked reason, the delivery's
// suggestion, or completed; `none` leaves the issue open and ineligible;
// a comment lands before the close.
func TestHandoffDeliver(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	a, _ := handedOffIssue(t, s, p.ID, "a", "a1")
	b, _ := handedOffIssue(t, s, p.ID, "b", "b1")
	c, _ := handedOffIssue(t, s, p.ID, "c", "c1")
	h, _ := mustHandoff(t, s, p.ID, "h", a.ID, b.ID, c.ID)

	resolveItems(t, s, HandoffVerb{
		HandoffID: h.ID, IssueIDs: []int64{a.ID}, Outcome: HandoffDelivered,
		Close: true, CloseReason: issuestate.NotPlanned, Note: "shipped", Comment: "merged", CommentAuthor: "me",
	})
	got := mustGetIssue(t, s, a.ID)
	if got.State != issuestate.Closed || got.CloseReason != issuestate.NotPlanned {
		t.Errorf("issue a = %s/%s, want closed/not_planned", got.State, got.CloseReason)
	}
	if it := itemOf(t, s, h.ID, a.ID); it.Outcome != HandoffDelivered || it.OutcomeBy != HandoffByHumanActor || it.Note != "shipped" {
		t.Errorf("item a = %+v", it)
	}
	comments, _ := s.ListIssueComments(ctx, a.ID)
	if len(comments) != 1 || comments[0].Body != "merged" {
		t.Errorf("issue a comments = %+v", comments)
	}
	evs := issueEvents(t, s, EventIssueCommentAdded, EventIssueStateChanged)
	if len(evs) != 2 || evs[0].Type != EventIssueCommentAdded || payloadOf(t, evs[1])["handoff_id"] != float64(h.ID) {
		t.Errorf("issue events = %v", evs)
	}

	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{b.ID}, Outcome: HandoffDelivered, Close: true})
	if got := mustGetIssue(t, s, b.ID); got.State != issuestate.Closed || got.CloseReason != issuestate.Completed {
		t.Errorf("issue b = %s/%s, want closed/completed", got.State, got.CloseReason)
	}

	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{c.ID}, Outcome: HandoffDelivered})
	if got := mustGetIssue(t, s, c.ID); got.State == issuestate.Closed {
		t.Error("close none closed the issue")
	}
	if claimed(t, s, p.ID, c.ID) || eligible(t, s, c.ID) {
		t.Error("a delivered open issue is still claimed or eligible")
	}

	// Final: another verb on a delivered item is refused, naming it.
	_, err := s.ResolveHandoffItems(ctx, HandoffVerb{
		HandoffID: h.ID, IssueIDs: []int64{c.ID},
		Outcome: HandoffReleased, By: HandoffByHumanActor, IssueActor: issuestate.Human,
	})
	var resolved *HandoffItemResolvedError
	if !errors.As(err, &resolved) || resolved.Outcome != HandoffDelivered || !errors.Is(err, ErrHandoffItemResolved) {
		t.Errorf("release of a delivered item: err = %v", err)
	}
	// The same outcome again is a no-op.
	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{c.ID}, Outcome: HandoffDelivered})
}

// TestHandoffReturnReleaseWait: return releases the claim and withdraws
// readiness; release leaves the issue eligible; wait keeps the claim past
// the task's settle, which releases what is still unresolved.
func TestHandoffReturnReleaseWait(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	ret, _ := handedOffIssue(t, s, p.ID, "ret", "r1")
	rel, _ := handedOffIssue(t, s, p.ID, "rel", "l1")
	wt, _ := handedOffIssue(t, s, p.ID, "wait", "w1")
	left, _ := handedOffIssue(t, s, p.ID, "left", "x1")
	h, ht := mustHandoff(t, s, p.ID, "h", ret.ID, rel.ID, wt.ID, left.ID)

	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{ret.ID}, Outcome: HandoffReturned})
	if claimed(t, s, p.ID, ret.ID) {
		t.Error("a returned item kept its claim")
	}
	wantLane(t, s, ret.ID, issuestate.LaneInProgress)

	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{rel.ID}, Outcome: HandoffReleased})
	if claimed(t, s, p.ID, rel.ID) || !eligible(t, s, rel.ID) {
		t.Error("a released issue is not eligible again")
	}

	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{wt.ID}, Outcome: HandoffWaiting})
	moveTask(t, s, ht, TaskRunning, TaskDone)
	if !claimed(t, s, p.ID, wt.ID) {
		t.Error("a waiting item lost its claim when the task settled")
	}
	if it := itemOf(t, s, h.ID, left.ID); it.Outcome != HandoffReleased || it.OutcomeBy != HandoffBySettle {
		t.Errorf("unresolved item after settle = %s by %s, want released by settle", it.Outcome, it.OutcomeBy)
	}
	// On a settled handoff only the waiting item may still move.
	got, _ := s.GetHandoff(t.Context(), h.ID)
	if got.State != HandoffWaitingState {
		t.Fatalf("state = %s, want waiting", got.State)
	}
	res := resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{wt.ID}, Outcome: HandoffReleased})
	if res.Handoff.State != HandoffSettled {
		t.Errorf("state after the wait ended = %s", res.Handoff.State)
	}
}

// TestHandoffAllSkipsWaitingAndIsAllOrNothing: --all moves only unresolved
// items, and one refused item refuses the batch with nothing written.
func TestHandoffAllSkipsWaitingAndIsAllOrNothing(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	a, _ := handedOffIssue(t, s, p.ID, "a", "a1")
	b, _ := handedOffIssue(t, s, p.ID, "b", "b1")
	c, _ := handedOffIssue(t, s, p.ID, "c", "c1")
	h, _ := mustHandoff(t, s, p.ID, "h", a.ID, b.ID, c.ID)
	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{a.ID}, Outcome: HandoffWaiting})

	refuse := errors.New("refused")
	_, err := s.ResolveHandoffItems(ctx, HandoffVerb{
		HandoffID: h.ID, All: true, Outcome: HandoffDelivered, Close: true,
		By: HandoffByAgentActor, IssueActor: issuestate.Agent,
		Check: func(iss *Issue) error {
			if iss.ID == c.ID {
				return refuse
			}
			return nil
		},
	})
	if !errors.Is(err, refuse) {
		t.Fatalf("err = %v, want the check's refusal", err)
	}
	for _, id := range []int64{b.ID, c.ID} {
		if it := itemOf(t, s, h.ID, id); it.Outcome != HandoffUnresolved {
			t.Errorf("issue %d outcome = %s after a refused batch", id, it.Outcome)
		}
		if mustGetIssue(t, s, id).State == issuestate.Closed {
			t.Errorf("issue %d closed by a refused batch", id)
		}
	}

	res := resolveItems(t, s, HandoffVerb{
		HandoffID: h.ID, All: true, Outcome: HandoffReleased,
		By: HandoffByHandoff, IssueActor: issuestate.Agent,
	})
	if len(res.Items) != 2 {
		t.Errorf("--all moved %d items, want 2", len(res.Items))
	}
	if it := itemOf(t, s, h.ID, a.ID); it.Outcome != HandoffWaiting {
		t.Errorf("--all moved the waiting item to %s", it.Outcome)
	}
	if it := itemOf(t, s, h.ID, b.ID); it.OutcomeBy != HandoffByHandoff {
		t.Errorf("outcome_by = %s, want handoff", it.OutcomeBy)
	}
	if _, err := s.ResolveHandoffItems(ctx, HandoffVerb{
		HandoffID: h.ID, IssueIDs: []int64{999},
		Outcome: HandoffReleased, By: HandoffByHumanActor, IssueActor: issuestate.Human,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an issue that is not an item: err = %v, want not found", err)
	}
}

// TestHandoffReturnFollowUpAndSide: a return's follow-up re-admits the
// issue's latest main task in the same transaction — the claim released
// first, so issue_in_handoff never trips — and a side task is created with
// merge_back; a follow-up that loses its compare-and-swap refuses the whole
// return with the claim intact.
func TestHandoffReturnFollowUpAndSide(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	a, mainA := handedOffIssue(t, s, p.ID, "a", "a1")
	b, _ := handedOffIssue(t, s, p.ID, "b", "b1")
	h, _ := mustHandoff(t, s, p.ID, "h", a.ID, b.ID)

	latest, err := s.LatestIssueMainTask(ctx, a.ID)
	if err != nil || latest.ID != mainA.ID {
		t.Fatalf("LatestIssueMainTask = %v, %v; want task %d", latest, err, mainA.ID)
	}
	req := &FollowUpRequest{Form: FollowUpAgent, Prompt: "again", Workflow: "steps: []", Origin: TaskDone, Round: 1}
	stale := &HandoffFollowUp{TaskID: mainA.ID, From: TaskBlocked, To: TaskQueued, Change: TaskChange{PendingFollowUp: req}}
	_, err = s.ResolveHandoffItems(ctx, HandoffVerb{
		HandoffID: h.ID, IssueIDs: []int64{a.ID}, Outcome: HandoffReturned,
		By: HandoffByHumanActor, IssueActor: issuestate.Human, FollowUps: map[int64]*HandoffFollowUp{a.ID: stale},
	})
	if _, lost := AsStateConflict(err); !lost {
		t.Fatalf("a refused follow-up: err = %v, want a state conflict", err)
	}
	if !claimed(t, s, p.ID, a.ID) {
		t.Fatal("a refused return released the claim")
	}

	fu := &HandoffFollowUp{TaskID: mainA.ID, From: TaskDone, To: TaskQueued, Change: TaskChange{PendingFollowUp: req}}
	resolveItems(t, s, HandoffVerb{
		HandoffID: h.ID, IssueIDs: []int64{a.ID}, Outcome: HandoffReturned,
		FollowUps: map[int64]*HandoffFollowUp{a.ID: fu},
	})
	if got, _ := s.GetTask(ctx, mainA.ID); got.State != TaskQueued || got.PendingFollowUp == nil {
		t.Errorf("main task after return = %s, follow-up %v", got.State, got.PendingFollowUp)
	}

	side := newTask(p.ID, "side", TaskQueued)
	side.IssueID, side.IssueWorktree, side.MergeOnConflict = &b.ID, IssueWorktreeSide, MergeOnConflictBlock
	resolveItems(t, s, HandoffVerb{
		HandoffID: h.ID, IssueIDs: []int64{b.ID}, Outcome: HandoffReturned,
		SideTasks: map[int64]*HandoffSideTask{b.ID: {Task: side}},
	})
	if side.ID == 0 {
		t.Fatal("no side task was created")
	}
	if got, _ := s.GetTask(ctx, side.ID); got.IssueWorktree != IssueWorktreeSide || got.MergeOnConflict != MergeOnConflictBlock {
		t.Errorf("side task = %s/%s", got.IssueWorktree, got.MergeOnConflict)
	}
}

// TestHandoffWaitResolvedBySyncClose: an imported issue closed by the
// importer resolves its waiting item delivered, by sync, and settles the
// handoff when it was the last thing waiting.
func TestHandoffWaitResolvedBySyncClose(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	in := RemoteIssue{
		ProjectID: p.ID, Provider: "github", RemoteKey: "gh-1", Repo: "o/r", Number: 1,
		Title: "imported", State: issuestate.Open,
	}
	iss := mustUpsertRemote(t, s, in)
	task := newTask(p.ID, "work", TaskRunning)
	task.IssueID, task.IssueWorktree = &iss.ID, IssueWorktreeMain
	mustCreate(t, s, task)
	if _, _, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskDone, TaskChange{Delivery: recordedDelivery("w")}); err != nil {
		t.Fatal(err)
	}
	h, ht := mustHandoff(t, s, p.ID, "h", iss.ID)
	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{iss.ID}, Outcome: HandoffWaiting})
	moveTask(t, s, ht, TaskRunning, TaskDone)

	w := watchEvents(s)
	in.State = issuestate.Closed
	mustUpsertRemote(t, s, in)
	if it := itemOf(t, s, h.ID, iss.ID); it.Outcome != HandoffDelivered || it.OutcomeBy != HandoffBySync {
		t.Errorf("item after the sync close = %s by %s, want delivered by sync", it.Outcome, it.OutcomeBy)
	}
	evs := handoffEventsIn(w.take())
	if len(evs) != 2 || evs[0].Type != EventHandoffItemChanged || evs[1].Type != EventHandoffSettled {
		t.Errorf("the sync close published %v", eventTypes(evs))
	}
}
