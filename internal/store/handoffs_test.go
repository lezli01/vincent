package store

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// handedOffIssue is an issue in the `hand_off` lane: one main task that
// finished `done` with a change delivery at head.
func handedOffIssue(t *testing.T, s *Store, projectID int64, title, head string) (*Issue, *Task) {
	t.Helper()
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: projectID, Title: title})
	task := newTask(projectID, title, TaskRunning)
	task.IssueID, task.IssueWorktree = &iss.ID, IssueWorktreeMain
	mustCreate(t, s, task)
	d := recordedDelivery(title)
	d.HeadSHA = head
	done, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskDone, TaskChange{Delivery: d})
	if err != nil {
		t.Fatalf("finish %s: %v", title, err)
	}
	return iss, done
}

// newHandoff creates a handoff over issueIDs, with a queued task.
func newHandoff(t *testing.T, s *Store, projectID int64, title string, issueIDs ...int64) (*Handoff, *Task, error) {
	t.Helper()
	h := &Handoff{CreatedBy: HandoffByHuman}
	task := newTask(projectID, title, TaskQueued)
	err := s.CreateHandoff(t.Context(), h, issueIDs, task, nil)
	return h, task, err
}

func mustHandoff(t *testing.T, s *Store, projectID int64, title string, issueIDs ...int64) (*Handoff, *Task) {
	t.Helper()
	h, task, err := newHandoff(t, s, projectID, title, issueIDs...)
	if err != nil {
		t.Fatalf("CreateHandoff(%s): %v", title, err)
	}
	return h, task
}

// eligible reads handoffEligibleExpr for one issue.
func eligible(t *testing.T, s *Store, issueID int64) bool {
	t.Helper()
	e := handoffEligibleExpr()
	var ok bool
	if err := s.db.QueryRow(`SELECT `+e.sql+` FROM issues i WHERE i.id = ?`,
		append(slices.Clone(e.args), issueID)...).Scan(&ok); err != nil {
		t.Fatalf("eligibility of issue %d: %v", issueID, err)
	}
	return ok
}

func setOutcome(t *testing.T, s *Store, h *Handoff, issueID int64, o HandoffOutcome) {
	t.Helper()
	if _, err := s.SetHandoffItemOutcome(t.Context(), h.ID, issueID, o, issuestate.Human, ""); err != nil {
		t.Fatalf("set handoff %d issue %d %s: %v", h.ID, issueID, o, err)
	}
}

// followUp re-admits a done task as a follow-up does, then finishes it
// again at head.
func followUp(t *testing.T, s *Store, task *Task, head string, acting *int64) error {
	t.Helper()
	ctx := t.Context()
	req := &FollowUpRequest{Form: FollowUpAgent, Prompt: "again", Workflow: "steps: []", Origin: TaskDone, Round: 1, ActingTaskID: acting}
	if _, _, err := s.TransitionTask(ctx, task.ID, TaskDone, TaskQueued, TaskChange{PendingFollowUp: req}); err != nil {
		return err
	}
	moveTask(t, s, &Task{ID: task.ID, State: TaskQueued}, TaskRunning)
	d := recordedDelivery(task.Title)
	d.HeadSHA = head
	_, _, err := s.TransitionTask(ctx, task.ID, TaskRunning, TaskDone, TaskChange{Delivery: d})
	return err
}

// TestHandoffClaimIsExclusive: a live item — unresolved or waiting — holds
// its issue against every other handoff, at the create and at the index;
// once returned or released the issue may be claimed again.
func TestHandoffClaimIsExclusive(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss, _ := handedOffIssue(t, s, p.ID, "claimed", "s1")
	h1, _ := mustHandoff(t, s, p.ID, "h1", iss.ID)

	_, second, err := newHandoff(t, s, p.ID, "h2", iss.ID)
	var ne *IssueNotEligibleError
	if !errors.As(err, &ne) || ne.IssueID != iss.ID || !errors.Is(err, ErrIssueNotEligible) {
		t.Fatalf("second claim: err = %v, want IssueNotEligibleError{%d}", err, iss.ID)
	}
	if second.ID != 0 {
		if _, err := s.GetTask(ctx, second.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the refused handoff's task survived the rollback: %v", err)
		}
	}

	// The index itself refuses a second live row while the first waits.
	setOutcome(t, s, h1, iss.ID, HandoffWaiting)
	other := newTask(p.ID, "raw", TaskQueued)
	mustCreate(t, s, other)
	res, err := s.db.Exec(`INSERT INTO handoffs (project_id, task_id, created_by, created_at) VALUES (?, ?, 'human', ?)`,
		p.ID, other.ID, formatTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	rawID, _ := res.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO handoff_items (handoff_id, issue_id) VALUES (?, ?)`, rawID, iss.ID); err == nil {
		t.Fatal("the claim index allowed a second live item while the first is waiting")
	}

	for _, o := range []HandoffOutcome{HandoffReleased, HandoffReturned} {
		setOutcome(t, s, h1, iss.ID, o)
		h, _, err := newHandoff(t, s, p.ID, "after-"+string(o), iss.ID)
		if err != nil {
			t.Fatalf("claim after %s: %v", o, err)
		}
		setOutcome(t, s, h, iss.ID, HandoffReleased)
	}
}

// TestHandoffEligibility walks decision 2's rule.
func TestHandoffEligibility(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	open := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "open lane"})
	running := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "in progress"})
	rt := newTask(p.ID, "running", TaskRunning)
	rt.IssueID = &running.ID
	mustCreate(t, s, rt)
	closed, _ := handedOffIssue(t, s, p.ID, "closed", "c1")
	if _, err := s.TransitionIssue(ctx, closed.ID, issuestate.Close, "", nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	ready, readyTask := handedOffIssue(t, s, p.ID, "ready", "r1")

	for _, iss := range []*Issue{open, running, closed} {
		if eligible(t, s, iss.ID) {
			t.Errorf("issue %q is eligible", iss.Title)
		}
		if _, _, err := newHandoff(t, s, p.ID, "h-"+iss.Title, iss.ID); !errors.Is(err, ErrIssueNotEligible) {
			t.Errorf("claim of %q: err = %v, want ErrIssueNotEligible", iss.Title, err)
		}
	}
	if !eligible(t, s, ready.ID) {
		t.Fatal("an open issue in hand_off with no claim is not eligible")
	}
	h, _ := mustHandoff(t, s, p.ID, "h", ready.ID)
	if eligible(t, s, ready.ID) {
		t.Error("a claimed issue is eligible")
	}
	setOutcome(t, s, h, ready.ID, HandoffDelivered)
	if eligible(t, s, ready.ID) {
		t.Error("an issue whose current delivery was delivered is eligible")
	}
	// A follow-up that adds no commits leaves the same delivery current.
	if err := followUp(t, s, readyTask, "r1", nil); err != nil {
		t.Fatal(err)
	}
	if eligible(t, s, ready.ID) {
		t.Error("a follow-up with the same head made a delivered issue eligible")
	}
	if err := followUp(t, s, readyTask, "r2", nil); err != nil {
		t.Fatal(err)
	}
	if !eligible(t, s, ready.ID) {
		t.Error("a follow-up with a new head left the issue ineligible")
	}
	// Delivered and closed: the lane is done, so it is ineligible whatever.
	h2, _ := mustHandoff(t, s, p.ID, "h2", ready.ID)
	setOutcome(t, s, h2, ready.ID, HandoffDelivered)
	if _, err := s.TransitionIssue(ctx, ready.ID, issuestate.Close, "", nil, issuestate.Handoff); err != nil {
		t.Fatal(err)
	}
	if eligible(t, s, ready.ID) {
		t.Error("a delivered, closed issue is eligible")
	}
}

// TestHandoffItemSnapshotsDelivery: the item keeps the delivery it was
// handed, whatever the delivering task does next.
func TestHandoffItemSnapshotsDelivery(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss, task := handedOffIssue(t, s, p.ID, "snap", "s1")
	h, ht := mustHandoff(t, s, p.ID, "h", iss.ID)
	check := func() {
		t.Helper()
		items, _, err := s.ListHandoffItems(ctx, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].DeliveryTaskID == nil || *items[0].DeliveryTaskID != task.ID ||
			items[0].Delivery == nil || items[0].Delivery.HeadSHA != "s1" || items[0].Delivery.Title != "snap" {
			t.Fatalf("items = %+v", items[0])
		}
	}
	check()
	// The handoff's own task may follow up; the snapshot stays.
	if err := followUp(t, s, task, "s2", &ht.ID); err != nil {
		t.Fatal(err)
	}
	check()
}

// TestHandoffState: the SQL derivation agrees with HandoffStateOf across
// the task's states and the items' outcomes.
func TestHandoffState(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	a, _ := handedOffIssue(t, s, p.ID, "a", "a1")
	b, _ := handedOffIssue(t, s, p.ID, "b", "b1")
	h, ht := mustHandoff(t, s, p.ID, "h", a.ID, b.ID)
	outcomes := []HandoffOutcome{HandoffUnresolved, HandoffDelivered, HandoffReturned, HandoffWaiting, HandoffReleased}
	for _, st := range []TaskState{TaskQueued, TaskRunning, TaskBlocked, TaskDone, TaskAborted, TaskArchived} {
		for _, oa := range outcomes {
			for _, ob := range outcomes {
				if _, err := s.db.Exec(`UPDATE tasks SET state = ? WHERE id = ?`, string(st), ht.ID); err != nil {
					t.Fatal(err)
				}
				for id, o := range map[int64]HandoffOutcome{a.ID: oa, b.ID: ob} {
					if _, err := s.db.Exec(`UPDATE handoff_items SET outcome = ? WHERE handoff_id = ? AND issue_id = ?`,
						nullString(string(o)), h.ID, id); err != nil {
						t.Fatal(err)
					}
				}
				got, err := s.GetHandoff(ctx, h.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := HandoffStateOf(st, []HandoffOutcome{oa, ob})
				if got.State != want {
					t.Errorf("task %s, items %q/%q: SQL %s, Go %s", st, oa, ob, got.State, want)
				}
				_, listed, err := s.ListHandoffItems(ctx, h.ID)
				if err != nil || listed != want {
					t.Errorf("ListHandoffItems state = %s, %v; want %s", listed, err, want)
				}
			}
		}
	}
	for _, tc := range []struct {
		st   TaskState
		outs []HandoffOutcome
		want HandoffState
	}{
		{TaskRunning, []HandoffOutcome{HandoffWaiting}, HandoffLive},
		{TaskDone, []HandoffOutcome{HandoffWaiting, HandoffDelivered}, HandoffWaitingState},
		{TaskDone, []HandoffOutcome{HandoffUnresolved}, HandoffSettled},
		{TaskAborted, nil, HandoffSettled},
	} {
		if got := HandoffStateOf(tc.st, tc.outs); got != tc.want {
			t.Errorf("HandoffStateOf(%s, %v) = %s, want %s", tc.st, tc.outs, got, tc.want)
		}
	}
}

// TestHandoffTaskMovesNoLane: the handoff task carries no issue, and
// neither its create nor its transitions write issue.lane_changed.
func TestHandoffTaskMovesNoLane(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	iss, _ := handedOffIssue(t, s, p.ID, "lane", "l1")
	w := watchEvents(s)
	_, ht := mustHandoff(t, s, p.ID, "h", iss.ID)
	if ht.IssueID != nil {
		t.Fatalf("handoff task issue_id = %d", *ht.IssueID)
	}
	moveTask(t, s, ht, TaskRunning, TaskDone)
	if lc := laneChanges(w.take()); len(lc) != 0 {
		t.Fatalf("the handoff task moved a lane: %v", eventTypes(lc))
	}
	if _, _, err := newHandoff(t, s, p.ID, "bad", iss.ID); err == nil {
		t.Fatal("a claim survived its handoff task settling")
	}
	if err := s.CreateHandoff(t.Context(), &Handoff{CreatedBy: HandoffByHuman}, []int64{iss.ID},
		&Task{ProjectID: p.ID, IssueID: &iss.ID}, nil); err == nil {
		t.Fatal("CreateHandoff took a task with an issue")
	}
}

func handoffEventsIn(evs []*Event) []*Event {
	var out []*Event
	for _, e := range evs {
		switch e.Type {
		case EventHandoffCreated, EventHandoffItemChanged, EventHandoffSettled:
			out = append(out, e)
		}
	}
	return out
}

// TestHandoffEvents: created, item_changed and settled carry their
// payloads; settled fires at the task's settle when nothing waits, and at
// the item write that ends the wait otherwise; a rolled-back create
// records nothing.
func TestHandoffEvents(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	a, _ := handedOffIssue(t, s, p.ID, "a", "a1")
	b, _ := handedOffIssue(t, s, p.ID, "b", "b1")
	w := watchEvents(s)

	h, ht := mustHandoff(t, s, p.ID, "h", a.ID, b.ID)
	evs := handoffEventsIn(w.take())
	if len(evs) != 1 || evs[0].Type != EventHandoffCreated {
		t.Fatalf("create published %v", eventTypes(evs))
	}
	pl := payloadOf(t, *evs[0])
	if pl["id"] != float64(h.ID) || pl["task_id"] != float64(ht.ID) || pl["created_by"] != "human" ||
		fmt.Sprint(pl["issue_ids"]) != fmt.Sprintf("[%d %d]", a.ID, b.ID) {
		t.Errorf("handoff.created payload = %v", pl)
	}
	if evs[0].TaskID == nil || *evs[0].TaskID != ht.ID {
		t.Errorf("handoff.created task_id = %v, want %d", evs[0].TaskID, ht.ID)
	}

	// A rolled-back create records nothing.
	before, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := newHandoff(t, s, p.ID, "dup", a.ID); err == nil {
		t.Fatal("duplicate claim succeeded")
	}
	after, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || len(w.take()) != 0 {
		t.Fatalf("a rolled-back create recorded %d events", len(after)-len(before))
	}

	setOutcome(t, s, h, a.ID, HandoffDelivered)
	evs = handoffEventsIn(w.take())
	if len(evs) != 1 || evs[0].Type != EventHandoffItemChanged {
		t.Fatalf("item write published %v", eventTypes(evs))
	}
	pl = payloadOf(t, *evs[0])
	if pl["handoff_id"] != float64(h.ID) || pl["issue_id"] != float64(a.ID) || pl["from"] != "" ||
		pl["to"] != "delivered" || pl["by"] != "human" {
		t.Errorf("handoff.item_changed payload = %v", pl)
	}
	setOutcome(t, s, h, a.ID, HandoffDelivered)
	if evs := handoffEventsIn(w.take()); len(evs) != 0 {
		t.Errorf("a no-op outcome write published %v", eventTypes(evs))
	}

	// b waits, so the task settling leaves the handoff waiting: no settled.
	setOutcome(t, s, h, b.ID, HandoffWaiting)
	w.take()
	moveTask(t, s, ht, TaskRunning, TaskDone)
	if evs := handoffEventsIn(w.take()); len(evs) != 0 {
		t.Fatalf("settling with a waiting item published %v", eventTypes(evs))
	}
	if got, _ := s.GetHandoff(ctx, h.ID); got.State != HandoffWaitingState {
		t.Fatalf("state = %s, want waiting", got.State)
	}
	setOutcome(t, s, h, b.ID, HandoffReleased)
	evs = handoffEventsIn(w.take())
	if len(evs) != 2 || evs[0].Type != EventHandoffItemChanged || evs[1].Type != EventHandoffSettled {
		t.Fatalf("the wait ending published %v", eventTypes(evs))
	}
	if pl := payloadOf(t, *evs[1]); pl["id"] != float64(h.ID) || pl["task_id"] != float64(ht.ID) {
		t.Errorf("handoff.settled payload = %v", pl)
	}

	// Nothing waiting: the task's own settle records handoff.settled after
	// its state change, in the same commit.
	c, _ := handedOffIssue(t, s, p.ID, "c", "c1")
	_, ht2 := mustHandoff(t, s, p.ID, "h2", c.ID)
	moveTask(t, s, ht2, TaskRunning)
	w.take()
	moveTask(t, s, ht2, TaskAborted)
	all := w.take()
	if got := eventTypes(all); len(all) != 2 || all[0].Type != EventTaskStateChanged || all[1].Type != EventHandoffSettled {
		t.Fatalf("settling the task published %v", got)
	}
}

// TestIssueInHandoffRefusal: new work on a claimed issue — a task, a chat
// handoff, a follow-up — is refused naming the handoff, except from the
// handoff's own task and a merge-back of a side task it created; a
// returned or released item lets it through.
func TestIssueInHandoffRefusal(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss, main := handedOffIssue(t, s, p.ID, "guarded", "g1")
	h, ht := mustHandoff(t, s, p.ID, "h", iss.ID)
	wantRefused := func(what string, err error) {
		t.Helper()
		var ih *IssueInHandoffError
		if !errors.As(err, &ih) || ih.HandoffID != h.ID || ih.IssueID != iss.ID || !errors.Is(err, ErrIssueInHandoff) {
			t.Errorf("%s: err = %v, want IssueInHandoffError{%d, %d}", what, err, iss.ID, h.ID)
		}
	}

	task := newTask(p.ID, "new work", TaskQueued)
	task.IssueID = &iss.ID
	wantRefused("create", s.CreateTask(ctx, task, nil))

	c := testChat(t, s, p.ID)
	ct := newTask(p.ID, "from chat", TaskQueued)
	ct.IssueID = &iss.ID
	ct.BranchName, ct.WorktreePath = c.Branch, c.WorktreePath
	_, err := s.HandoffChat(ctx, c.ID, ct)
	wantRefused("chat handoff", err)

	req := &FollowUpRequest{Form: FollowUpAgent, Prompt: "x", Workflow: "steps: []", Origin: TaskDone, Round: 1}
	_, _, err = s.TransitionTask(ctx, main.ID, TaskDone, TaskQueued, TaskChange{PendingFollowUp: req})
	wantRefused("follow-up", err)

	// A fan-out lane is part of its parent's work.
	lane := newTask(p.ID, "lane", TaskQueued)
	lane.IssueID, lane.ParentTaskID = &iss.ID, &ht.ID
	mustCreate(t, s, lane)

	// The handoff's own task: a side task, merged back on its finish.
	side := newTask(p.ID, "fix", TaskQueued)
	side.IssueID, side.IssueWorktree, side.MergeOnConflict = &iss.ID, IssueWorktreeSide, MergeOnConflictBlock
	side.ActingTaskID, side.CreatedByTaskID = &ht.ID, &ht.ID
	mustCreate(t, s, side)
	moveTask(t, s, side, TaskRunning)
	mb := &Task{Title: "Merge", WorkflowName: "__merge_back", WorkflowSnapshot: "name: x"}
	if _, _, err := s.TransitionTask(ctx, side.ID, TaskRunning, TaskDone, TaskChange{MergeBack: mb}); err != nil {
		t.Fatalf("merge-back of the handoff's side task: %v", err)
	}
	if mb.ID == 0 {
		t.Fatal("no merge-back was inserted")
	}
	// Its own follow-up, through the marker.
	req.ActingTaskID = &ht.ID
	if _, _, err := s.TransitionTask(ctx, side.ID, TaskDone, TaskQueued, TaskChange{PendingFollowUp: req}); err != nil {
		t.Fatalf("the handoff's own follow-up: %v", err)
	}

	setOutcome(t, s, h, iss.ID, HandoffReturned)
	again := newTask(p.ID, "after return", TaskQueued)
	again.IssueID = &iss.ID
	if err := s.CreateTask(ctx, again, nil); err != nil {
		t.Fatalf("create after the item returned: %v", err)
	}
}

// TestIssueEventsCarryHandoff: a state change and a comment written under
// WithIssueHandoff name the handoff, as the handoff actor.
func TestIssueEventsCarryHandoff(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "x"})
	w := watchEvents(s)
	ctx := WithIssueHandoff(t.Context(), 42)
	if _, err := s.AddIssueComment(ctx, iss.ID, "vincent", "done", "", issuestate.Handoff); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, "", nil, issuestate.Handoff); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, "", nil, issuestate.Handoff); !errors.Is(err, ErrInvalidIssueAction) {
		t.Errorf("handoff closing a closed issue: err = %v, want ErrInvalidIssueAction", err)
	}
	var seen int
	for _, e := range w.take() {
		if e.Type != EventIssueCommentAdded && e.Type != EventIssueStateChanged {
			continue
		}
		seen++
		if pl := payloadOf(t, *e); pl["handoff_id"] != float64(42) || pl["by"] != "handoff" {
			t.Errorf("%s payload = %v", e.Type, pl)
		}
	}
	if seen != 2 {
		t.Errorf("saw %d issue events, want 2", seen)
	}
}
