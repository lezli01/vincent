package store

import (
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// laneEventWatch records every published event, in order, so a test can
// check what a write announced and in which order (task 134.6).
type laneEventWatch struct{ evs []*Event }

func watchEvents(s *Store) *laneEventWatch {
	w := &laneEventWatch{}
	s.SetEventHook(func(e *Event) { w.evs = append(w.evs, e) })
	return w
}

// take returns the events published since the last take.
func (w *laneEventWatch) take() []*Event {
	out := w.evs
	w.evs = nil
	return out
}

func eventTypes(evs []*Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

// laneChanges is the issue.lane_changed events among evs.
func laneChanges(evs []*Event) []*Event {
	var out []*Event
	for _, e := range evs {
		if e.Type == EventIssueLaneChanged {
			out = append(out, e)
		}
	}
	return out
}

// wantLaneMove asserts evs is exactly cause followed by one lane change
// from → to, carrying task_id or by (never both), with the row's task_id
// NULL; and that the published order matches the stored ids.
func wantLaneMove(t *testing.T, s *Store, evs []*Event, cause string, issueID int64, from, to issuestate.Lane, taskID int64, by issuestate.Actor) {
	t.Helper()
	got := eventTypes(evs)
	if len(evs) != 2 || evs[0].Type != cause || evs[1].Type != EventIssueLaneChanged {
		t.Fatalf("published %v, want [%s %s]", got, cause, EventIssueLaneChanged)
	}
	if evs[1].ID != evs[0].ID+1 {
		t.Errorf("lane event id %d does not follow its cause %d", evs[1].ID, evs[0].ID)
	}
	lc := evs[1]
	if lc.TaskID != nil {
		t.Errorf("lane event row task_id = %d, want NULL", *lc.TaskID)
	}
	p := payloadOf(t, *lc)
	if p["id"] != float64(issueID) || p["from"] != string(from) || p["to"] != string(to) {
		t.Errorf("lane payload = %v, want id %d %s → %s", p, issueID, from, to)
	}
	_, hasTask := p["task_id"]
	_, hasBy := p["by"]
	switch {
	case taskID != 0 && (p["task_id"] != float64(taskID) || hasBy):
		t.Errorf("lane payload = %v, want task_id %d and no by", p, taskID)
	case by != "" && (p["by"] != string(by) || hasTask):
		t.Errorf("lane payload = %v, want by %s and no task_id", p, by)
	}
	// Durable too: the same two rows are in the table, in this order.
	stored, err := s.ListEvents(t.Context(), EventFilter{AfterID: evs[0].ID - 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) < 2 || stored[0].Type != cause || stored[1].Type != EventIssueLaneChanged {
		t.Errorf("stored %v, want %s then %s", stored, cause, EventIssueLaneChanged)
	}
}

func issueVersion(t *testing.T, s *Store, id int64) (int64, string) {
	t.Helper()
	iss, err := s.GetIssue(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return iss.Version, formatTime(iss.UpdatedAt)
}

func transition(t *testing.T, s *Store, id int64, from, to TaskState) {
	t.Helper()
	if _, _, err := s.TransitionTask(t.Context(), id, from, to, TaskChange{}); err != nil {
		t.Fatalf("task %d %s → %s: %v", id, from, to, err)
	}
}

// TestTaskWritesAnnounceLaneMoves: a root task's create, transitions,
// delete and restore write issue.lane_changed right after their own event,
// in the same commit, exactly when the lane moves — and never touch the
// issue's version or updated_at (task 134.6 decisions 2, 3).
func TestTaskWritesAnnounceLaneMoves(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := testIssue(t, s, p.ID, "the issue")
	version, updated := issueVersion(t, s, iss.ID)
	w := watchEvents(s)

	task := newTask(p.ID, "work", TaskQueued)
	task.IssueID = &iss.ID
	mustCreate(t, s, task)
	wantLaneMove(t, s, w.take(), EventTaskCreated, iss.ID, issuestate.LaneOpen, issuestate.LaneInProgress, task.ID, "")

	// Within in_progress: no lane event.
	transition(t, s, task.ID, TaskQueued, TaskRunning)
	transition(t, s, task.ID, TaskRunning, TaskAwaitingInput)
	transition(t, s, task.ID, TaskAwaitingInput, TaskRunning)
	if lc := laneChanges(w.take()); len(lc) != 0 {
		t.Fatalf("in-lane transitions wrote %d lane events", len(lc))
	}

	transition(t, s, task.ID, TaskRunning, TaskDone)
	wantLaneMove(t, s, w.take(), EventTaskStateChanged, iss.ID, issuestate.LaneInProgress, issuestate.LaneHandOff, task.ID, "")

	// Archiving from done keeps the hand-off (task 134 decision 20).
	transition(t, s, task.ID, TaskDone, TaskArchived)
	if lc := laneChanges(w.take()); len(lc) != 0 {
		t.Fatalf("done → archived wrote %d lane events", len(lc))
	}

	exp, err := s.ExportTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ExportTask: %v", err)
	}
	if err := s.DeleteTaskCascade(ctx, task.ID); err != nil {
		t.Fatalf("DeleteTaskCascade: %v", err)
	}
	evs := w.take()
	wantLaneMove(t, s, evs, EventTaskDeleted, iss.ID, issuestate.LaneHandOff, issuestate.LaneOpen, task.ID, "")
	if got := payloadOf(t, *evs[0])["issue_id"]; got != float64(iss.ID) {
		t.Errorf("task.deleted issue_id = %v, want %d", got, iss.ID)
	}

	if _, err := s.ImportTask(ctx, exp, ImportOptions{}); err != nil {
		t.Fatalf("ImportTask: %v", err)
	}
	evs = w.take()
	wantLaneMove(t, s, evs, EventTaskRestored, iss.ID, issuestate.LaneOpen, issuestate.LaneHandOff, task.ID, "")
	if got := payloadOf(t, *evs[0])["issue_id"]; got != float64(iss.ID) {
		t.Errorf("task.restored issue_id = %v, want %d", got, iss.ID)
	}

	// The only root aborting moves in_progress back to open.
	other := newTask(p.ID, "second", TaskQueued)
	other.IssueID = &iss.ID
	mustCreate(t, s, other)
	wantLaneMove(t, s, w.take(), EventTaskCreated, iss.ID, issuestate.LaneHandOff, issuestate.LaneInProgress, other.ID, "")
	transition(t, s, other.ID, TaskQueued, TaskAborted)
	wantLaneMove(t, s, w.take(), EventTaskStateChanged, iss.ID, issuestate.LaneInProgress, issuestate.LaneHandOff, other.ID, "")

	if v, u := issueVersion(t, s, iss.ID); v != version || u != updated {
		t.Errorf("issue version/updated_at = %d/%s, want unchanged %d/%s", v, u, version, updated)
	}
}

// TestOnlyRootAbortReturnsToOpen: an issue whose one root task aborts goes
// in_progress → open.
func TestOnlyRootAbortReturnsToOpen(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	iss := testIssue(t, s, p.ID, "the issue")
	task := newTask(p.ID, "work", TaskQueued)
	task.IssueID = &iss.ID
	mustCreate(t, s, task)
	w := watchEvents(s)
	transition(t, s, task.ID, TaskQueued, TaskAborted)
	wantLaneMove(t, s, w.take(), EventTaskStateChanged, iss.ID, issuestate.LaneInProgress, issuestate.LaneOpen, task.ID, "")
}

// TestTaskEventsCarryIssueID: the four task events carry issue_id when the
// task has one — a fan-out lane included — and omit it otherwise; a lane's
// own writes never move its issue's lane (decisions 1, 3).
func TestTaskEventsCarryIssueID(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := testIssue(t, s, p.ID, "the issue")

	root := newTask(p.ID, "root", TaskRunning)
	root.IssueID = &iss.ID
	mustCreate(t, s, root)
	w := watchEvents(s)

	l := newTask(p.ID, "lane a", TaskQueued)
	l.IssueID = &iss.ID
	l.ParentTaskID = &root.ID
	idx := 0
	l.ParentStepIndex = &idx
	l.LaneID = "a"
	mustCreate(t, s, l)
	transition(t, s, l.ID, TaskQueued, TaskDone)
	transition(t, s, l.ID, TaskDone, TaskArchived)
	if err := s.DeleteTaskCascade(ctx, l.ID); err != nil {
		t.Fatalf("delete lane: %v", err)
	}
	evs := w.take()
	if lc := laneChanges(evs); len(lc) != 0 {
		t.Fatalf("a fan-out lane's writes wrote %d lane events", len(lc))
	}
	if got := eventTypes(evs); len(got) != 4 {
		t.Fatalf("lane writes published %v, want 4 task events", got)
	}
	for _, e := range evs {
		if got := payloadOf(t, *e)["issue_id"]; got != float64(iss.ID) {
			t.Errorf("%s issue_id = %v, want %d", e.Type, got, iss.ID)
		}
	}

	plain := newTask(p.ID, "no issue", TaskQueued)
	mustCreate(t, s, plain)
	transition(t, s, plain.ID, TaskQueued, TaskAborted)
	transition(t, s, plain.ID, TaskAborted, TaskArchived)
	if err := s.DeleteTaskCascade(ctx, plain.ID); err != nil {
		t.Fatalf("delete plain: %v", err)
	}
	for _, e := range w.take() {
		if _, ok := payloadOf(t, *e)["issue_id"]; ok {
			t.Errorf("%s of an issue-less task carries issue_id", e.Type)
		}
	}
}

// TestIssueCloseAndReopenAnnounceLaneMoves: a close moves the lane to done
// right after issue.state_changed, for every actor; a reopen moves it back
// to the derived lane; a task transition on a closed issue writes none
// (decision 4).
func TestIssueCloseAndReopenAnnounceLaneMoves(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	for _, by := range []issuestate.Actor{issuestate.Human, issuestate.Agent, issuestate.Sync} {
		t.Run(string(by), func(t *testing.T) {
			iss := testIssue(t, s, p.ID, "close by "+string(by))
			task := newTask(p.ID, "work "+string(by), TaskRunning)
			task.IssueID = &iss.ID
			mustCreate(t, s, task)
			w := watchEvents(s)

			mustTransition(t, s, iss.ID, issuestate.Close, issuestate.Completed, by)
			wantLaneMove(t, s, w.take(), EventIssueStateChanged, iss.ID, issuestate.LaneInProgress, issuestate.LaneDone, 0, by)

			transition(t, s, task.ID, TaskRunning, TaskDone)
			if lc := laneChanges(w.take()); len(lc) != 0 {
				t.Fatalf("a task transition on a closed issue wrote %d lane events", len(lc))
			}

			if by == issuestate.Sync {
				return // sync reopens only through the import path
			}
			mustTransition(t, s, iss.ID, issuestate.Reopen, "", by)
			wantLaneMove(t, s, w.take(), EventIssueStateChanged, iss.ID, issuestate.LaneDone, issuestate.LaneHandOff, 0, by)
		})
	}
}

// TestImportRefreshAnnouncesLaneMoves: a sync refresh that closes or
// reopens an imported issue writes the lane event after issue.updated; one
// that changes only the title writes none; creating an issue, even closed,
// writes none (decisions 4, 5).
func TestImportRefreshAnnouncesLaneMoves(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	w := watchEvents(s)

	closed := remoteIn(p.ID, "I_closed", 2)
	closed.State = issuestate.Closed
	mustUpsertRemote(t, s, closed)
	in := remoteIn(p.ID, "I_1", 1)
	iss := mustUpsertRemote(t, s, in)
	if lc := laneChanges(w.take()); len(lc) != 0 {
		t.Fatalf("importing issues wrote %d lane events", len(lc))
	}

	in.Title = "renamed"
	mustUpsertRemote(t, s, in)
	if lc := laneChanges(w.take()); len(lc) != 0 {
		t.Fatalf("a title-only refresh wrote %d lane events", len(lc))
	}

	in.State = issuestate.Closed
	mustUpsertRemote(t, s, in)
	wantLaneMove(t, s, w.take(), EventIssueUpdated, iss.ID, issuestate.LaneOpen, issuestate.LaneDone, 0, issuestate.Sync)

	in.State = issuestate.Open
	mustUpsertRemote(t, s, in)
	wantLaneMove(t, s, w.take(), EventIssueUpdated, iss.ID, issuestate.LaneDone, issuestate.LaneOpen, 0, issuestate.Sync)
}
