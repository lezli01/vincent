package store

import (
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// wantMarker asserts an issue's handoff marker as GetIssue reads it: the
// state, and the handoff and its task when the marker names one.
func wantMarker(t *testing.T, s *Store, issueID int64, want HandoffMarker, h *Handoff) {
	t.Helper()
	got := mustGetIssue(t, s, issueID).Handoff
	if want == "" {
		if got != nil {
			t.Errorf("issue %d marker = %+v, want none", issueID, got)
		}
		return
	}
	if got == nil || got.State != want {
		t.Fatalf("issue %d marker = %+v, want %s", issueID, got, want)
	}
	if h == nil {
		if got.HandoffID != nil || got.TaskID != nil {
			t.Errorf("issue %d %s names handoff %v task %v, want none", issueID, want, got.HandoffID, got.TaskID)
		}
		return
	}
	if got.HandoffID == nil || *got.HandoffID != h.ID || got.TaskID == nil || *got.TaskID != h.TaskID {
		t.Errorf("issue %d %s names handoff %v task %v, want %d/%d", issueID, want, got.HandoffID, got.TaskID, h.ID, h.TaskID)
	}
}

func listIDs(t *testing.T, s *Store, f IssueFilter) []int64 {
	t.Helper()
	list, err := s.ListIssues(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(list))
	for _, iss := range list {
		ids = append(ids, iss.ID)
	}
	return ids
}

// restart re-admits a done task as its handoff's follow-up does and leaves
// it running.
func restart(t *testing.T, s *Store, task *Task, acting int64) {
	t.Helper()
	req := &FollowUpRequest{Form: FollowUpAgent, Prompt: "again", Workflow: "steps: []", Origin: TaskDone, Round: 1, ActingTaskID: &acting}
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskDone, TaskQueued, TaskChange{PendingFollowUp: req}); err != nil {
		t.Fatal(err)
	}
	moveTask(t, s, &Task{ID: task.ID, State: TaskQueued}, TaskRunning)
}

// TestHandoffMarker walks task 137.10's marker over real handoff rows.
func TestHandoffMarker(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	pending, _ := handedOffIssue(t, s, p.ID, "pending", "p1")
	wantMarker(t, s, pending.ID, HandoffMarkerPending, nil)

	claimedIss, claimedTask := handedOffIssue(t, s, p.ID, "claimed", "c1")
	waiting, _ := handedOffIssue(t, s, p.ID, "waiting", "w1")
	delivered, deliveredTask := handedOffIssue(t, s, p.ID, "delivered", "d1")
	returned, _ := handedOffIssue(t, s, p.ID, "returned", "r1")
	h, ht := mustHandoff(t, s, p.ID, "h", claimedIss.ID, waiting.ID, delivered.ID, returned.ID)
	for _, id := range []int64{claimedIss.ID, waiting.ID, delivered.ID, returned.ID} {
		wantMarker(t, s, id, HandoffMarkerClaimed, h)
	}
	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{waiting.ID}, Outcome: HandoffWaiting})
	wantMarker(t, s, waiting.ID, HandoffMarkerWaiting, h)

	// deliver --close none: still hand_off, not selectable, delivered.
	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{delivered.ID}, Outcome: HandoffDelivered})
	wantLane(t, s, delivered.ID, issuestate.LaneHandOff)
	wantMarker(t, s, delivered.ID, HandoffMarkerDelivered, h)
	// A follow-up that adds no commits keeps it; one that moves head_sha
	// makes it pending again.
	if err := followUp(t, s, deliveredTask, "d1", nil); err != nil {
		t.Fatal(err)
	}
	wantMarker(t, s, delivered.ID, HandoffMarkerDelivered, h)
	if err := followUp(t, s, deliveredTask, "d2", nil); err != nil {
		t.Fatal(err)
	}
	wantMarker(t, s, delivered.ID, HandoffMarkerPending, nil)

	// A return withdraws readiness: in_progress with nothing running.
	resolveItems(t, s, HandoffVerb{HandoffID: h.ID, IssueIDs: []int64{returned.ID}, Outcome: HandoffReturned})
	wantLane(t, s, returned.ID, issuestate.LaneInProgress)
	wantMarker(t, s, returned.ID, HandoffMarkerIdle, nil)

	// An explicit workflow's unready delivery is idle too.
	explicit := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "explicit"})
	finishedMain(t, s, p.ID, explicit, "explicit", readyDelivery("explicit", false, ""))
	wantMarker(t, s, explicit.ID, HandoffMarkerIdle, nil)

	// The claim beats the lane: the handoff's own task restarting work on
	// a claimed issue makes it in_progress, and it stays claimed.
	restart(t, s, claimedTask, ht.ID)
	wantLane(t, s, claimedIss.ID, issuestate.LaneInProgress)
	wantMarker(t, s, claimedIss.ID, HandoffMarkerClaimed, h)

	// No marker: the open lane, a running issue, and a closed one —
	// including one whose waiting item the close resolved.
	open := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "open"})
	wantMarker(t, s, open.ID, "", nil)
	running := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "running"})
	rt := newTask(p.ID, "running", TaskRunning)
	rt.IssueID = &running.ID
	mustCreate(t, s, rt)
	wantMarker(t, s, running.ID, "", nil)
	if _, err := s.TransitionIssue(ctx, waiting.ID, issuestate.Close, "", nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	if it := itemOf(t, s, h.ID, waiting.ID); it.Outcome != HandoffDelivered {
		t.Fatalf("closing a waiting issue left its item %q", it.Outcome)
	}
	wantMarker(t, s, waiting.ID, "", nil)

	// The filter: alone, with lanes, and paged.
	if got, want := listIDs(t, s, IssueFilter{Handoffs: []HandoffMarker{HandoffMarkerIdle}}),
		[]int64{explicit.ID, returned.ID}; !slices.Equal(got, want) {
		t.Errorf("idle = %v, want %v", got, want)
	}
	if got, want := listIDs(t, s, IssueFilter{Handoffs: []HandoffMarker{HandoffMarkerPending, HandoffMarkerClaimed}}),
		[]int64{delivered.ID, claimedIss.ID, pending.ID}; !slices.Equal(got, want) {
		t.Errorf("pending+claimed = %v, want %v", got, want)
	}
	if got, want := listIDs(t, s, IssueFilter{
		Handoffs: []HandoffMarker{HandoffMarkerPending, HandoffMarkerClaimed},
		Lanes:    []issuestate.Lane{issuestate.LaneInProgress},
	}), []int64{claimedIss.ID}; !slices.Equal(got, want) {
		t.Errorf("claimed in_progress = %v, want %v", got, want)
	}
	if got, want := listIDs(t, s, IssueFilter{
		Handoffs: []HandoffMarker{HandoffMarkerPending, HandoffMarkerClaimed}, Limit: 1, Offset: 1,
	}), []int64{claimedIss.ID}; !slices.Equal(got, want) {
		t.Errorf("paged = %v, want %v", got, want)
	}

	// Every pending issue is what a select: would claim, and nothing else.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectHandoffIssuesTx(ctx, tx, p.ID, &HandoffSelect{})
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	pendingIDs := listIDs(t, s, IssueFilter{ProjectID: p.ID, Handoffs: []HandoffMarker{HandoffMarkerPending}})
	slices.Sort(selected)
	slices.Sort(pendingIDs)
	if !slices.Equal(selected, pendingIDs) {
		t.Errorf("select: claims %v, ?handoff=pending lists %v", selected, pendingIDs)
	}
}

// TestHandoffMarkerOf holds the SQL to HandoffMarkerOf across the issue's
// state, its root task's state and readiness, the item's outcome and
// whether the delivered item still names the current delivery.
func TestHandoffMarkerOf(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	iss, task := handedOffIssue(t, s, p.ID, "x", "s1")
	h, _ := mustHandoff(t, s, p.ID, "h", iss.ID)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	outcomes := []HandoffOutcome{HandoffUnresolved, HandoffDelivered, HandoffReturned, HandoffWaiting, HandoffReleased}
	for _, st := range []issuestate.State{issuestate.Open, issuestate.Closed} {
		for _, ts := range []TaskState{TaskRunning, TaskDone, TaskAborted} {
			for _, ready := range []bool{true, false} {
				for _, head := range []string{"s1", "s2"} {
					for _, o := range outcomes {
						exec(`UPDATE issues SET state = ? WHERE id = ?`, string(st), iss.ID)
						exec(`UPDATE tasks SET state = ?, delivery_json = json_set(delivery_json, '$.ready', json(?), '$.head_sha', ?)
							WHERE id = ?`, string(ts), map[bool]string{true: "true", false: "false"}[ready], head, task.ID)
						exec(`UPDATE handoff_items SET outcome = ? WHERE handoff_id = ?`, nullString(string(o)), h.ID)
						got := mustGetIssue(t, s, iss.ID)
						var claim *HandoffOutcome
						if o.Claims() {
							claim = &o
						}
						want := HandoffMarkerOf(got.State, got.Lane, got.Active, claim, o == HandoffDelivered && head == "s1")
						var sqlMarker HandoffMarker
						if got.Handoff != nil {
							sqlMarker = got.Handoff.State
						}
						if sqlMarker != want {
							t.Errorf("%s issue, task %s, ready %v, head %s, item %q: SQL %q, Go %q",
								st, ts, ready, head, o, sqlMarker, want)
						}
					}
				}
			}
		}
	}
	h7 := HandoffWaiting
	for _, tc := range []struct {
		st        issuestate.State
		lane      issuestate.Lane
		active    bool
		claim     *HandoffOutcome
		delivered bool
		want      HandoffMarker
	}{
		{issuestate.Closed, issuestate.LaneDone, false, &h7, false, ""},
		{issuestate.Open, issuestate.LaneInProgress, true, &h7, false, HandoffMarkerWaiting},
		{issuestate.Open, issuestate.LaneHandOff, false, nil, false, HandoffMarkerPending},
		{issuestate.Open, issuestate.LaneHandOff, false, nil, true, HandoffMarkerDelivered},
		{issuestate.Open, issuestate.LaneInProgress, false, nil, false, HandoffMarkerIdle},
		{issuestate.Open, issuestate.LaneInProgress, true, nil, false, ""},
		{issuestate.Open, issuestate.LaneOpen, false, nil, false, ""},
	} {
		if got := HandoffMarkerOf(tc.st, tc.lane, tc.active, tc.claim, tc.delivered); got != tc.want {
			t.Errorf("HandoffMarkerOf(%+v) = %q, want %q", tc, got, tc.want)
		}
	}
}
