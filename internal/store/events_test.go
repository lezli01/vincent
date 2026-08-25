package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAppendEventDefaults(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	e := &Event{Type: "daemon.shutting_down"}
	if err := s.AppendEvent(ctx, e); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if e.ID == 0 {
		t.Error("AppendEvent did not assign an ID")
	}
	if e.TS.IsZero() {
		t.Error("AppendEvent did not default TS")
	}

	got, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if string(got[0].Payload) != "{}" {
		t.Errorf("Payload = %s, want {}", got[0].Payload)
	}
	if got[0].TaskID != nil || got[0].ProjectID != nil {
		t.Errorf("unset ids came back non-nil: %+v", got[0])
	}
}

func TestEventRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	taskID, projectID := int64(7), int64(3)
	in := &Event{
		Type:      "task.state_changed",
		TaskID:    &taskID,
		ProjectID: &projectID,
		Payload:   json.RawMessage(`{"state":"running"}`),
	}
	if err := s.AppendEvent(ctx, in); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	got, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	e := got[0]
	if e.Type != in.Type || e.TaskID == nil || *e.TaskID != taskID ||
		e.ProjectID == nil || *e.ProjectID != projectID {
		t.Errorf("got %+v, want %+v", e, in)
	}
	if string(e.Payload) != `{"state":"running"}` {
		t.Errorf("Payload = %s", e.Payload)
	}
	if !e.TS.Equal(in.TS) {
		t.Errorf("TS = %v, want %v", e.TS, in.TS)
	}
}

func TestListEventsFilters(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	p1, p2 := int64(1), int64(2)
	mk := func(typ string, projectID *int64) *Event {
		e := &Event{Type: typ, ProjectID: projectID}
		if err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		return e
	}
	e1 := mk("task.created", &p1)
	e2 := mk("task.state_changed", &p1)
	e3 := mk("task.created", &p2)
	e4 := mk("gate.waiting", nil)

	ids := func(events []Event) []int64 {
		out := make([]int64, len(events))
		for i, e := range events {
			out[i] = e.ID
		}
		return out
	}

	cases := []struct {
		name   string
		filter EventFilter
		want   []int64 // ascending id order
	}{
		{"all", EventFilter{}, []int64{e1.ID, e2.ID, e3.ID, e4.ID}},
		{"after id (SSE resume)", EventFilter{AfterID: e2.ID}, []int64{e3.ID, e4.ID}},
		{"one type", EventFilter{Types: []string{"task.created"}}, []int64{e1.ID, e3.ID}},
		{"two types", EventFilter{Types: []string{"task.created", "gate.waiting"}}, []int64{e1.ID, e3.ID, e4.ID}},
		{"by project", EventFilter{ProjectID: p1}, []int64{e1.ID, e2.ID}},
		{"limit", EventFilter{Limit: 2}, []int64{e1.ID, e2.ID}},
		{"combined", EventFilter{AfterID: e1.ID, Types: []string{"task.created"}}, []int64{e3.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListEvents(ctx, tc.filter)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if !reflect.DeepEqual(ids(got), tc.want) {
				t.Errorf("ListEvents(%+v) ids = %v, want %v", tc.filter, ids(got), tc.want)
			}
		})
	}
}

// TestMaxEventID pins the high-water mark an SSE replay pages up to: zero on
// an empty table, so a resume on a fresh daemon walks nowhere, and the last
// assigned id once events exist.
func TestMaxEventID(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	got, err := s.MaxEventID(ctx)
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	if got != 0 {
		t.Errorf("MaxEventID on an empty table = %d, want 0", got)
	}

	var last int64
	for range 3 {
		e := &Event{Type: "daemon.shutting_down"}
		if err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		last = e.ID
	}
	if got, err = s.MaxEventID(ctx); err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	if got != last {
		t.Errorf("MaxEventID = %d, want the last appended id %d", got, last)
	}
}
