package store

import (
	"errors"
	"slices"
	"testing"
)

// deliveredIssue is handedOffIssue with the issue's priority, kind and
// labels, and a delivery of kind dk, finished in call order.
func deliveredIssue(t *testing.T, s *Store, in NewIssue, dk DeliveryKind) *Issue {
	t.Helper()
	iss := mustCreateIssue(t, s, in)
	task := newTask(in.ProjectID, in.Title, TaskRunning)
	task.IssueID, task.IssueWorktree = &iss.ID, IssueWorktreeMain
	mustCreate(t, s, task)
	d := recordedDelivery(in.Title)
	d.Kind = dk
	if _, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskDone, TaskChange{Delivery: d}); err != nil {
		t.Fatalf("finish %s: %v", in.Title, err)
	}
	return iss
}

func startSelect(t *testing.T, s *Store, projectID int64, title string, sel HandoffSelect) (*Handoff, *Task, []int64, error) {
	t.Helper()
	h := &Handoff{CreatedBy: HandoffByHuman}
	task := newTask(projectID, title, TaskQueued)
	ids, err := s.StartHandoff(t.Context(), NewHandoff{Handoff: h, Task: task, Select: &sel})
	return h, task, ids, err
}

// TestHandoffSelectFiltersAndOrder: a selection honours delivery, kind,
// labels (AND, case-insensitive) and max, and orders priority 1 before 4
// before 0, then the oldest delivery, then id.
func TestHandoffSelectFiltersAndOrder(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	none := deliveredIssue(t, s, NewIssue{ProjectID: p.ID, Title: "none", Priority: 0, Kind: "bug"}, DeliveryChange)
	low := deliveredIssue(t, s, NewIssue{ProjectID: p.ID, Title: "low", Priority: 4, Kind: "bug", Labels: []string{"Ship", "x"}}, DeliveryChange)
	urgentOld := deliveredIssue(t, s, NewIssue{ProjectID: p.ID, Title: "urgent old", Priority: 1, Kind: "feature", Labels: []string{"ship"}}, DeliveryChange)
	urgentNew := deliveredIssue(t, s, NewIssue{ProjectID: p.ID, Title: "urgent new", Priority: 1, Kind: "bug", Labels: []string{"ship", "x"}}, DeliveryChange)
	reply := deliveredIssue(t, s, NewIssue{ProjectID: p.ID, Title: "reply", Priority: 2, Kind: "question"}, DeliveryReply)
	other := testProject(t, s, "p2")
	deliveredIssue(t, s, NewIssue{ProjectID: other.ID, Title: "elsewhere", Priority: 1}, DeliveryChange)

	cases := []struct {
		name string
		sel  HandoffSelect
		want []int64
	}{
		{"everything, in order", HandoffSelect{}, []int64{urgentOld.ID, urgentNew.ID, reply.ID, low.ID, none.ID}},
		{"delivery", HandoffSelect{Delivery: []string{"reply"}}, []int64{reply.ID}},
		{"delivery list", HandoffSelect{Delivery: []string{"change", "none"}}, []int64{urgentOld.ID, urgentNew.ID, low.ID, none.ID}},
		{"kind", HandoffSelect{Kinds: []string{"bug"}}, []int64{urgentNew.ID, low.ID, none.ID}},
		{"labels are AND, any case", HandoffSelect{Labels: []string{"SHIP", "X"}}, []int64{urgentNew.ID, low.ID}},
		{"max", HandoffSelect{Max: 2}, []int64{urgentOld.ID, urgentNew.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			got, err := selectHandoffIssuesTx(t.Context(), tx, p.ID, &tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("selected %v, want %v", got, tc.want)
			}
		})
	}

	// The create claims the selection, and a second selection skips what
	// the first claimed.
	h, _, ids, err := startSelect(t, s, p.ID, "first", HandoffSelect{Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []int64{urgentOld.ID, urgentNew.ID}) {
		t.Fatalf("claimed %v", ids)
	}
	items, _, err := s.ListHandoffItems(t.Context(), h.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %v, %v", items, err)
	}
	_, _, ids, err = startSelect(t, s, p.ID, "second", HandoffSelect{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []int64{reply.ID, low.ID, none.ID}) {
		t.Fatalf("second selection claimed %v", ids)
	}
}

// TestHandoffSelectNothing: an empty selection is ErrNothingToHandOff and
// leaves no task or handoff behind.
func TestHandoffSelectNothing(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	deliveredIssue(t, s, NewIssue{ProjectID: p.ID, Title: "a bug", Kind: "bug"}, DeliveryChange)
	_, task, _, err := startSelect(t, s, p.ID, "empty", HandoffSelect{Kinds: []string{"feature"}})
	if !errors.Is(err, ErrNothingToHandOff) {
		t.Fatalf("err = %v, want ErrNothingToHandOff", err)
	}
	if task.ID != 0 {
		if _, err := s.GetTask(t.Context(), task.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the refused handoff's task survived: %v", err)
		}
	}
	if hs, err := s.ListHandoffs(t.Context(), HandoffFilter{}); err != nil || len(hs) != 0 {
		t.Errorf("handoffs = %v, %v; want none", hs, err)
	}
}

// TestListHandoffsFilters: by project, derived state and issue, newest
// first.
func TestListHandoffsFilters(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	q := testProject(t, s, "p2")
	a, _ := handedOffIssue(t, s, p.ID, "a", "s1")
	b, _ := handedOffIssue(t, s, p.ID, "b", "s2")
	c, _ := handedOffIssue(t, s, q.ID, "c", "s3")
	h1, t1 := mustHandoff(t, s, p.ID, "h1", a.ID)
	h2, _ := mustHandoff(t, s, p.ID, "h2", b.ID)
	h3, _ := mustHandoff(t, s, q.ID, "h3", c.ID)
	moveTask(t, s, t1, TaskAborted)

	ids := func(f HandoffFilter) []int64 {
		t.Helper()
		hs, err := s.ListHandoffs(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, h := range hs {
			out = append(out, h.ID)
		}
		return out
	}
	for name, tc := range map[string]struct {
		f    HandoffFilter
		want []int64
	}{
		"all":     {HandoffFilter{}, []int64{h3.ID, h2.ID, h1.ID}},
		"project": {HandoffFilter{ProjectID: p.ID}, []int64{h2.ID, h1.ID}},
		"live":    {HandoffFilter{States: []HandoffState{HandoffLive}}, []int64{h3.ID, h2.ID}},
		"settled": {HandoffFilter{ProjectID: p.ID, States: []HandoffState{HandoffSettled, HandoffWaitingState}}, []int64{h1.ID}},
		"issue":   {HandoffFilter{IssueID: b.ID}, []int64{h2.ID}},
	} {
		if got := ids(tc.f); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// TestGrantJSON: the vocabulary is closed, and a grant is stored
// deduplicated in the vocabulary's order.
func TestGrantJSON(t *testing.T) {
	got, err := GrantJSON([]string{"pull.merge", "issue.comment", "pull.merge"})
	if err != nil || got != `["issue.comment","pull.merge"]` {
		t.Errorf("GrantJSON = %q, %v", got, err)
	}
	if got, err := GrantJSON(nil); err != nil || got != `[]` {
		t.Errorf("empty grant = %q, %v", got, err)
	}
	if _, err := GrantJSON([]string{"repo.delete"}); err == nil {
		t.Error("an unknown capability was accepted")
	}
}
