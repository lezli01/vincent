package store

import (
	"errors"
	"testing"
	"time"
)

// TestTriggerCursorLifecycle: an absent row is ErrNotFound, a put inserts and
// then updates the whole row, and a delete is what re-seeds a trigger.
func TestTriggerCursorLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	if _, err := s.GetTriggerCursor(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpolled trigger: %v, want ErrNotFound", err)
	}

	now := time.Now()
	seed := "w1"
	if err := s.PutTriggerCursor(ctx, &TriggerCursor{
		TriggerID: "t1", Cursor: &seed, LastPollAt: &now, LastPollOK: true,
	}); err != nil {
		t.Fatalf("PutTriggerCursor: %v", err)
	}
	got, err := s.GetTriggerCursor(ctx, "t1")
	if err != nil {
		t.Fatalf("GetTriggerCursor: %v", err)
	}
	if got.Cursor == nil || *got.Cursor != "w1" || !got.LastPollOK || got.LastPollAt == nil ||
		!got.LastPollAt.Equal(now.UTC()) || got.LastFireAt != nil {
		t.Errorf("after seed = %+v", got)
	}

	// A failed poll keeps the cursor and records why.
	later := now.Add(time.Minute)
	got.LastPollAt, got.LastPollOK, got.LastPollError = &later, false, "exit status 3"
	if err := s.PutTriggerCursor(ctx, got); err != nil {
		t.Fatalf("PutTriggerCursor update: %v", err)
	}
	all, err := s.ListTriggerCursors(ctx)
	if err != nil {
		t.Fatalf("ListTriggerCursors: %v", err)
	}
	c := all["t1"]
	if c == nil || c.LastPollOK || c.LastPollError != "exit status 3" || *c.Cursor != "w1" {
		t.Errorf("after failure = %+v", c)
	}

	// An unseeded row round-trips its NULL cursor.
	if err := s.PutTriggerCursor(ctx, &TriggerCursor{TriggerID: "t2"}); err != nil {
		t.Fatalf("PutTriggerCursor unseeded: %v", err)
	}
	if c, err := s.GetTriggerCursor(ctx, "t2"); err != nil || c.Cursor != nil {
		t.Errorf("unseeded row = %+v, %v; want a nil cursor", c, err)
	}

	if err := s.DeleteTriggerCursor(ctx, "t1"); err != nil {
		t.Fatalf("DeleteTriggerCursor: %v", err)
	}
	if _, err := s.GetTriggerCursor(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted cursor: %v, want ErrNotFound", err)
	}
	if err := s.DeleteTriggerCursor(ctx, "t1"); err != nil {
		t.Errorf("deleting an absent cursor: %v", err)
	}
}

// TestTriggerDeliveriesLedger: only `fired` rows make a key delivered or count
// against the hourly limit, the list is newest first and bounded, and a task
// deleted after it fired leaves its dedupe row behind.
func TestTriggerDeliveriesLedger(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "fired", TaskArchived)
	if err := s.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	now := time.Now()
	rows := []TriggerDelivery{
		{TriggerID: "t1", EventID: "e0", DedupeKey: "k0", Outcome: DeliveryFired, CreatedAt: now.Add(-2 * time.Hour)},
		{TriggerID: "t1", EventID: "e1", DedupeKey: "k1", Outcome: DeliveryFired, TaskID: &task.ID, CreatedAt: now.Add(-time.Minute)},
		{TriggerID: "t1", EventID: "e2", DedupeKey: "k2", Outcome: DeliveryFiltered, CreatedAt: now.Add(-50 * time.Second)},
		{TriggerID: "t1", EventID: "e3", DedupeKey: "k3", Outcome: DeliveryRefused, Detail: `{"error":{}}`, CreatedAt: now.Add(-40 * time.Second)},
		{TriggerID: "t1", EventID: "e1", DedupeKey: "k1", Outcome: DeliveryDeduped, CreatedAt: now.Add(-30 * time.Second)},
		{TriggerID: "t2", EventID: "e1", DedupeKey: "k1", Outcome: DeliveryError, CreatedAt: now},
	}
	for i := range rows {
		got, err := s.RecordTriggerDelivery(ctx, &rows[i])
		if err != nil {
			t.Fatalf("RecordTriggerDelivery[%d]: %v", i, err)
		}
		if got.ID == 0 {
			t.Fatalf("RecordTriggerDelivery[%d]: no id", i)
		}
	}

	for _, tc := range []struct {
		trigger, key string
		want         bool
	}{
		{"t1", "k1", true},
		{"t1", "k0", true},
		{"t1", "k2", false}, // filtered created nothing
		{"t1", "k3", false}, // refused created nothing
		{"t2", "k1", false}, // keys are per trigger
	} {
		got, err := s.TriggerKeyDelivered(ctx, tc.trigger, tc.key)
		if err != nil || got != tc.want {
			t.Errorf("TriggerKeyDelivered(%s, %s) = %v, %v; want %v", tc.trigger, tc.key, got, err, tc.want)
		}
	}

	n, err := s.CountTriggerFiredSince(ctx, "t1", now.Add(-time.Hour))
	if err != nil || n != 1 {
		t.Errorf("CountTriggerFiredSince = %d, %v; want 1 (the two-hour-old fire is outside)", n, err)
	}

	list, err := s.ListTriggerDeliveries(ctx, "t1", 3)
	if err != nil {
		t.Fatalf("ListTriggerDeliveries: %v", err)
	}
	if len(list) != 3 || list[0].Outcome != DeliveryDeduped || list[1].Outcome != DeliveryRefused ||
		list[1].Detail != `{"error":{}}` || list[2].Outcome != DeliveryFiltered {
		t.Errorf("newest-first page = %+v", list)
	}

	if err := s.DeleteTaskCascade(ctx, task.ID); err != nil {
		t.Fatalf("DeleteTaskCascade: %v", err)
	}
	if got, _ := s.TriggerKeyDelivered(ctx, "t1", "k1"); !got {
		t.Error("deleting the task a delivery created un-delivered its key")
	}
	list, err = s.ListTriggerDeliveries(ctx, "t1", 10)
	if err != nil {
		t.Fatalf("ListTriggerDeliveries: %v", err)
	}
	for _, d := range list {
		if d.EventID == "e1" && d.Outcome == DeliveryFired && d.TaskID != nil {
			t.Errorf("fired row still names deleted task %d", *d.TaskID)
		}
	}
}

// TestPruneTriggerDeliveries: rows past the window go, rows inside it stay,
// and a second pass removes nothing.
func TestPruneTriggerDeliveries(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	now := time.Now()
	for _, age := range []time.Duration{31 * 24 * time.Hour, 29 * 24 * time.Hour} {
		if _, err := s.RecordTriggerDelivery(ctx, &TriggerDelivery{
			TriggerID: "t1", DedupeKey: age.String(), Outcome: DeliveryFired, CreatedAt: now.Add(-age),
		}); err != nil {
			t.Fatalf("RecordTriggerDelivery: %v", err)
		}
	}
	cutoff := now.Add(-30 * 24 * time.Hour)
	n, err := s.PruneTriggerDeliveries(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("first pass: removed %d, err %v; want 1, nil", n, err)
	}
	if got, _ := s.TriggerKeyDelivered(ctx, "t1", (29 * 24 * time.Hour).String()); !got {
		t.Error("a delivery inside the window was pruned")
	}
	n, err = s.PruneTriggerDeliveries(ctx, cutoff)
	if err != nil || n != 0 {
		t.Fatalf("second pass: removed %d, err %v; want 0, nil", n, err)
	}
}
