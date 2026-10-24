package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// recordedDelivery is an explicit delivery as a step would record one.
func recordedDelivery(title string) *Delivery {
	return &Delivery{
		Kind: DeliveryChange, Title: title, Body: "body of " + title, HeadSHA: "abc123",
		Branch: "b", Base: "main", RecordedAt: time.Now().UTC().Truncate(time.Second),
	}
}

// TestSetTaskDeliveryStates: a delivery is recorded on a running or a done
// task, refused with the state found on any other, and announced with a
// task.delivery_changed event that carries no text.
func TestSetTaskDeliveryStates(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	for _, tc := range []struct {
		state TaskState
		ok    bool
	}{
		{TaskRunning, true},
		{TaskDone, true},
		{TaskQueued, false},
		{TaskBlocked, false},
		{TaskPaused, false},
		{TaskAwaitingGate, false},
		{TaskAborted, false},
		{TaskArchived, false},
	} {
		task := newTask(p.ID, "deliver-"+string(tc.state), tc.state)
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatal(err)
		}
		got, err := s.SetTaskDelivery(ctx, task.ID, recordedDelivery("t"))
		if !tc.ok {
			var se *DeliveryStateError
			if !errors.As(err, &se) || se.State != tc.state || !errors.Is(err, ErrNotDeliverable) {
				t.Errorf("%s: err = %v, want a DeliveryStateError for %s", tc.state, err, tc.state)
			}
			reread, _ := s.GetTask(ctx, task.ID)
			if reread.Delivery != nil {
				t.Errorf("%s: a refused delivery was stored: %+v", tc.state, reread.Delivery)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: SetTaskDelivery: %v", tc.state, err)
		}
		if got.Delivery == nil || got.Delivery.Title != "t" {
			t.Fatalf("%s: returned delivery = %+v", tc.state, got.Delivery)
		}
		reread, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if reread.Delivery == nil || reread.Delivery.Body != "body of t" || reread.Delivery.HeadSHA != "abc123" {
			t.Errorf("%s: stored delivery = %+v", tc.state, reread.Delivery)
		}
		evs, err := s.ListEvents(ctx, EventFilter{TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		last := evs[len(evs)-1]
		if last.Type != EventTaskDeliveryChanged || last.TaskID == nil || *last.TaskID != task.ID {
			t.Fatalf("%s: last event = %s for %v, want %s", tc.state, last.Type, last.TaskID, EventTaskDeliveryChanged)
		}
		var payload map[string]any
		if err := json.Unmarshal(last.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["kind"] != string(DeliveryChange) || payload["body"] != nil || payload["title"] != nil {
			t.Errorf("%s: event payload = %v, want the kind and no text", tc.state, payload)
		}
	}
}

// TestDeliveryAcrossTransitions: the `→ done` writes TaskChange.Delivery; an
// archive and a restore keep it; a follow-up that re-admits the task drops
// it, so the next `→ done` describes its own run.
func TestDeliveryAcrossTransitions(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "across", TaskRunning)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	move := func(from, to TaskState, ch TaskChange) *Task {
		t.Helper()
		got, _, err := s.TransitionTask(ctx, task.ID, from, to, ch)
		if err != nil {
			t.Fatalf("%s → %s: %v", from, to, err)
		}
		return got
	}
	done := move(TaskRunning, TaskDone, TaskChange{Delivery: recordedDelivery("first run")})
	if done.Delivery == nil || done.Delivery.Title != "first run" {
		t.Fatalf("after → done: delivery = %+v", done.Delivery)
	}
	if got := move(TaskDone, TaskArchived, TaskChange{}); got.Delivery == nil {
		t.Fatal("archive dropped the delivery")
	}
	if got := move(TaskArchived, TaskDone, TaskChange{}); got.Delivery == nil || got.Delivery.Title != "first run" {
		t.Fatalf("restore: delivery = %+v", got.Delivery)
	}
	if got := move(TaskDone, TaskQueued, TaskChange{}); got.Delivery != nil {
		t.Fatalf("a follow-up kept the finished run's delivery: %+v", got.Delivery)
	}
	reread, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Delivery != nil {
		t.Fatalf("stored delivery after the follow-up = %+v", reread.Delivery)
	}
	move(TaskQueued, TaskRunning, TaskChange{})
	if got := move(TaskRunning, TaskDone, TaskChange{Delivery: recordedDelivery("second run")}); got.Delivery.Title != "second run" {
		t.Fatalf("second → done: delivery = %+v", got.Delivery)
	}
}

// TestIssueLatestDelivery: an issue carries the delivery of its latest
// finished main-line root task — by finished_at, archived-from-done
// included — and never a side task's, a lane's or an aborted one's.
func TestIssueLatestDelivery(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "latest"})
	add := func(title string, state, archivedFrom TaskState, role string, finished time.Time) {
		t.Helper()
		task := newTask(p.ID, title, state)
		task.ArchivedFrom = archivedFrom
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatal(err)
		}
		d, err := marshalDelivery(recordedDelivery(title))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE tasks SET issue_id = ?, issue_worktree = ?, finished_at = ?, delivery_json = ?
			WHERE id = ?`, iss.ID, nullString(role), formatTime(finished), d, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	read := func() *Delivery {
		t.Helper()
		got, err := s.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListIssues(ctx, IssueFilter{ProjectID: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("ListIssues = %d rows", len(rows))
		}
		if (got.Delivery == nil) != (rows[0].Delivery == nil) ||
			(got.Delivery != nil && got.Delivery.Title != rows[0].Delivery.Title) {
			t.Fatalf("GetIssue and ListIssues disagree: %+v vs %+v", got.Delivery, rows[0].Delivery)
		}
		return got.Delivery
	}
	if d := read(); d != nil {
		t.Fatalf("an issue with no task has delivery %+v", d)
	}
	base := time.Now().UTC().Add(-time.Hour)
	add("older main", TaskDone, "", IssueWorktreeMain, base)
	if d := read(); d == nil || d.Title != "older main" {
		t.Fatalf("delivery = %+v, want older main", d)
	}
	add("archived main", TaskArchived, TaskDone, IssueWorktreeMain, base.Add(time.Minute))
	if d := read(); d == nil || d.Title != "archived main" {
		t.Fatalf("delivery = %+v, want the newer one archived from done", d)
	}
	add("newer side", TaskDone, "", IssueWorktreeSide, base.Add(2*time.Minute))
	add("newer aborted", TaskAborted, "", IssueWorktreeMain, base.Add(3*time.Minute))
	add("archived aborted", TaskArchived, TaskAborted, IssueWorktreeMain, base.Add(4*time.Minute))
	if d := read(); d == nil || d.Title != "archived main" {
		t.Fatalf("delivery = %+v; a side, aborted or archived-from-aborted task must not count", d)
	}
	add("no role", TaskDone, "", "", base.Add(5*time.Minute))
	if d := read(); d == nil || d.Title != "no role" {
		t.Fatalf("delivery = %+v, want the newest role-less root task", d)
	}
}

// TestBackfillCandidatesAndFill: the backfill lists exactly the finished
// root tasks of issues with no delivery, and its write never overwrites one
// recorded meanwhile.
func TestBackfillCandidatesAndFill(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "backfill"})
	mk := func(title string, state, archivedFrom TaskState, onIssue bool) *Task {
		t.Helper()
		task := newTask(p.ID, title, state)
		task.ArchivedFrom = archivedFrom
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatal(err)
		}
		if onIssue {
			if _, err := s.db.Exec(`UPDATE tasks SET issue_id = ? WHERE id = ?`, iss.ID, task.ID); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}
	done := mk("done", TaskDone, "", true)
	archived := mk("archived", TaskArchived, TaskDone, true)
	mk("aborted", TaskAborted, "", true)
	mk("running", TaskRunning, "", true)
	mk("no issue", TaskDone, "", false)
	got, err := s.ListTasksMissingDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, task := range got {
		ids = append(ids, task.ID)
	}
	if len(ids) != 2 || ids[0] != done.ID || ids[1] != archived.ID {
		t.Fatalf("candidates = %v, want [%d %d]", ids, done.ID, archived.ID)
	}
	if _, err := s.SetTaskDelivery(ctx, done.ID, recordedDelivery("recorded")); err != nil {
		t.Fatal(err)
	}
	ok, err := s.FillTaskDelivery(ctx, done.ID, &Delivery{Kind: DeliveryNone, Implicit: true})
	if err != nil || ok {
		t.Fatalf("fill over a recorded delivery = %v, %v; want false", ok, err)
	}
	if reread, _ := s.GetTask(ctx, done.ID); reread.Delivery.Title != "recorded" {
		t.Fatalf("the backfill overwrote a recorded delivery: %+v", reread.Delivery)
	}
	if ok, err := s.FillTaskDelivery(ctx, archived.ID, &Delivery{Kind: DeliveryNone, Implicit: true}); err != nil || !ok {
		t.Fatalf("fill = %v, %v; want true", ok, err)
	}
	if left, _ := s.ListTasksMissingDelivery(ctx); len(left) != 0 {
		t.Fatalf("candidates after the fill = %d, want 0", len(left))
	}
}
