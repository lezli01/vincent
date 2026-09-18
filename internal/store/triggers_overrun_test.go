package store

import (
	"strings"
	"testing"
	"time"
)

// Task 122: the widened CHECK, the supersede column and the backlog table.

func TestMigration0033WidensOutcomesAndKeepsRows(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{
		TriggerID: "t1", EventID: "e1", ConcurrencyKey: "V-1", Outcome: DeliveryQueued,
	}); err != nil {
		t.Errorf("queued refused: %v", err)
	}
	if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{
		TriggerID: "t1", EventID: "e2", ConcurrencyKey: "V-1", Outcome: DeliverySuperseded,
	}); err != nil {
		t.Errorf("superseded refused: %v", err)
	}
	if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{
		TriggerID: "t1", Outcome: "bogus",
	}); err == nil {
		t.Error("an unknown outcome was admitted")
	}
	rows, err := s.ListTriggerDeliveries(ctx, "t1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ConcurrencyKey != "V-1" {
		t.Errorf("rows = %+v", rows)
	}
	// A queued row is not delivered: a repeat event must reach the overrun
	// step rather than being deduped away (task 122 decision 8).
	if done, err := s.TriggerKeyDelivered(ctx, "t1", "e1"); err != nil || done {
		t.Errorf("queued counted as delivered (%v, %v)", done, err)
	}
}

func TestTriggerGroupInFlightAndSupersedeLink(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := &Project{Name: "p", Path: "/p", DefaultBranch: "main"}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	mk := func(state TaskState) int64 {
		task := &Task{
			ProjectID: p.ID, Title: "t", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: "b" + string(state), State: TaskPaused,
		}
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatal(err)
		}
		if state != TaskPaused {
			if _, _, err := s.TransitionTask(ctx, task.ID, TaskPaused, state, TaskChange{}); err != nil {
				t.Fatal(err)
			}
		}
		return task.ID
	}
	live, gone := mk(TaskRunning), mk(TaskDone)
	for _, id := range []int64{live, gone} {
		if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{
			TriggerID: "t1", ConcurrencyKey: "g", Outcome: DeliveryFired, TaskID: &id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.TriggerGroupInFlight(ctx, "t1", "g")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != live {
		t.Errorf("in flight = %v, want [%d]: a done task has settled", ids, live)
	}
	if flight, err := s.TaskInFlight(ctx, live); err != nil || !flight {
		t.Errorf("TaskInFlight(running) = %v, %v", flight, err)
	}
	if flight, err := s.TaskInFlight(ctx, gone); err != nil || flight {
		t.Errorf("TaskInFlight(done) = %v, %v", flight, err)
	}
	if flight, err := s.TaskInFlight(ctx, 9999); err != nil || flight {
		t.Errorf("TaskInFlight(missing) = %v, %v", flight, err)
	}
	// The supersede link round-trips.
	if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{
		TriggerID: "t1", ConcurrencyKey: "g", Outcome: DeliveryFired,
		TaskID: &live, SupersededTaskID: &gone,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListTriggerDeliveries(ctx, "t1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].SupersededTaskID == nil || *rows[0].SupersededTaskID != gone {
		t.Errorf("supersede link = %v, want %d", rows[0].SupersededTaskID, gone)
	}
}

func TestTriggerBacklogRoundTripAndPrune(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	for _, key := range []string{"a", "a", "b"} {
		if _, err := s.AppendTriggerBacklog(ctx, &TriggerBacklogItem{
			TriggerID: "t1", ConcurrencyKey: key, EventID: "e-" + key,
			EventJSON: []byte(`{"id":"e-` + key + `"}`), CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListTriggerBacklog(ctx, "t1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || string(items[0].EventJSON) != `{"id":"e-a"}` {
		t.Fatalf("group a = %+v", items)
	}
	all, err := s.ListTriggerBacklogFor(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID >= all[2].ID {
		t.Errorf("per-trigger list = %+v, want three oldest first", all)
	}
	groups, err := s.ListTriggerBacklogGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].ConcurrencyKey != "a" || groups[1].ConcurrencyKey != "b" {
		t.Errorf("groups = %+v", groups)
	}
	if err := s.DeleteTriggerBacklog(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if left, err := s.ListTriggerBacklog(ctx, "t1", "a"); err != nil || len(left) != 1 {
		t.Errorf("after delete = %+v, %v", left, err)
	}
	// Deleting a row that is not there is not an error.
	if err := s.DeleteTriggerBacklog(ctx, items[0].ID); err != nil {
		t.Errorf("second delete: %v", err)
	}
	n, err := s.PruneTriggerBacklog(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("pruned %d, want 2", n)
	}
}

// TestSettledStatesCoverTaskState keeps the SQL predicate honest: every state
// taskstate.Settled reports true for is in the list the group query excludes.
func TestSettledStatesCoverTaskState(t *testing.T) {
	var got []string
	for _, s := range settledStates {
		got = append(got, string(s))
	}
	if want := "aborted,archived,done"; !equalSorted(got, want) {
		t.Errorf("settled states = %v, want %s", got, want)
	}
	if n := strings.Count(settledStatePlaceholders, "?"); n != len(settledStates) {
		t.Errorf("placeholders = %q for %d states", settledStatePlaceholders, len(settledStates))
	}
}

func equalSorted(got []string, want string) bool {
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range strings.Split(want, ",") {
		if !set[w] {
			return false
		}
		delete(set, w)
	}
	return len(set) == 0
}
