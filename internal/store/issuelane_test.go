package store

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// rootSpec is one root task of a lane case: its state, and for an archived
// one the state it was archived from. lanes are fan-out children under it.
type rootSpec struct {
	state, archivedFrom TaskState
	lanes               []TaskState
}

// issueWithRoots creates an issue whose root tasks (and their lanes) are roots.
func issueWithRoots(t *testing.T, s *Store, projectID int64, title string, roots ...rootSpec) *Issue {
	t.Helper()
	ctx := t.Context()
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: projectID, Title: title})
	link := func(taskID int64) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE tasks SET issue_id = ? WHERE id = ?`, iss.ID, taskID); err != nil {
			t.Fatal(err)
		}
	}
	for n, r := range roots {
		task := newTask(projectID, fmt.Sprintf("%s-%d-%d", title, iss.ID, n), r.state)
		task.ArchivedFrom = r.archivedFrom
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatal(err)
		}
		link(task.ID)
		for n, ls := range r.lanes {
			l := lane(t, s, projectID, task.ID, string(rune('a'+n)), n, ls)
			link(l.ID)
		}
	}
	return iss
}

// wantLaneOf is the Go rule over the same roots the SQL reads, so a row's
// stored-and-derived lane can be checked against issuestate.LaneOf.
func wantLaneOf(st issuestate.State, roots []rootSpec) (lane issuestate.Lane, attention bool) {
	var unsettled, done bool
	for _, r := range roots {
		unsettled = unsettled || !taskstate.Settled(r.state)
		done = done || r.state == TaskDone || (r.state == taskstate.Archived && r.archivedFrom == TaskDone)
		attention = attention || taskstate.NeedsHuman(r.state)
	}
	return issuestate.LaneOf(st, unsettled, done), attention
}

// TestIssueLaneAndAttention: one issue per case, its lane and attention read
// back through GetIssue and ListIssues equal issuestate.LaneOf over the same
// root tasks, and for an open issue Active is exactly Lane == in_progress.
func TestIssueLaneAndAttention(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	type laneCase struct {
		name      string
		roots     []rootSpec
		close     issuestate.Reason // "" = stays open
		reopen    bool
		want      issuestate.Lane
		attention bool
	}
	var cases []laneCase
	// Every §6 state of a single root task, so a new state is covered.
	for _, st := range taskstate.All {
		r := rootSpec{state: st}
		if st == taskstate.Archived {
			r.archivedFrom = TaskDone
		}
		want, att := wantLaneOf(issuestate.Open, []rootSpec{r})
		cases = append(cases, laneCase{name: "single " + string(st), roots: []rootSpec{r}, want: want, attention: att})
	}
	cases = append(cases,
		laneCase{name: "no tasks", want: issuestate.LaneOpen},
		laneCase{name: "aborted only", roots: []rootSpec{{state: TaskAborted}, {state: TaskAborted}}, want: issuestate.LaneOpen},
		laneCase{name: "archived from done", roots: []rootSpec{{state: taskstate.Archived, archivedFrom: TaskDone}}, want: issuestate.LaneHandOff},
		laneCase{name: "archived from aborted", roots: []rootSpec{{state: taskstate.Archived, archivedFrom: TaskAborted}}, want: issuestate.LaneOpen},
		laneCase{name: "done root, running lane", roots: []rootSpec{{state: TaskDone, lanes: []TaskState{TaskRunning, TaskBlocked}}}, want: issuestate.LaneHandOff},
		laneCase{name: "done and aborted", roots: []rootSpec{{state: TaskDone}, {state: TaskAborted}}, want: issuestate.LaneHandOff},
		laneCase{name: "done and paused", roots: []rootSpec{{state: TaskDone}, {state: taskstate.Paused}}, want: issuestate.LaneInProgress},
		laneCase{name: "closed completed", close: issuestate.Completed, want: issuestate.LaneDone},
		laneCase{name: "closed not_planned", close: issuestate.NotPlanned, roots: []rootSpec{{state: TaskDone}}, want: issuestate.LaneDone},
		laneCase{name: "closed duplicate", close: issuestate.Duplicate, roots: []rootSpec{{state: TaskAborted}}, want: issuestate.LaneDone},
		laneCase{name: "closed running", close: issuestate.Completed, roots: []rootSpec{{state: TaskRunning}}, want: issuestate.LaneDone},
		laneCase{name: "closed blocked", close: issuestate.Completed, roots: []rootSpec{{state: TaskBlocked}}, want: issuestate.LaneDone, attention: true},
		laneCase{name: "reopened done", close: issuestate.Completed, reopen: true, roots: []rootSpec{{state: TaskDone}}, want: issuestate.LaneHandOff},
	)

	orig := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "original"})
	ids := map[int64]laneCase{orig.ID: {name: "original", want: issuestate.LaneOpen}}
	for _, c := range cases {
		iss := issueWithRoots(t, s, p.ID, c.name, c.roots...)
		if c.close != "" {
			var dup *int64
			if c.close == issuestate.Duplicate {
				dup = &orig.ID
			}
			if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, c.close, dup, issuestate.Human); err != nil {
				t.Fatalf("%s: close: %v", c.name, err)
			}
			if c.reopen {
				if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Reopen, "", nil, issuestate.Human); err != nil {
					t.Fatalf("%s: reopen: %v", c.name, err)
				}
			}
		}
		ids[iss.ID] = c
	}

	check := func(where string, got *Issue) {
		t.Helper()
		c := ids[got.ID]
		if got.Lane != c.want || got.Attention != c.attention {
			t.Errorf("%s %s: lane %s attention %v; want %s %v", where, c.name, got.Lane, got.Attention, c.want, c.attention)
		}
		if want, att := wantLaneOf(got.State, c.roots); got.Lane != want || got.Attention != att {
			t.Errorf("%s %s: lane %s attention %v; LaneOf says %s %v", where, c.name, got.Lane, got.Attention, want, att)
		}
		if got.State == issuestate.Open && got.Active != (got.Lane == issuestate.LaneInProgress) {
			t.Errorf("%s %s: open issue with active %v in lane %s", where, c.name, got.Active, got.Lane)
		}
	}
	list, err := s.ListIssues(ctx, IssueFilter{})
	if err != nil || len(list) != len(ids) {
		t.Fatalf("ListIssues = %d, %v; want %d", len(list), err, len(ids))
	}
	for _, iss := range list {
		check("list", iss)
		got, err := s.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatal(err)
		}
		check("get", got)
	}
	// A closed issue with a running root task is done but still active.
	for _, iss := range list {
		if ids[iss.ID].name == "closed running" && !iss.Active {
			t.Errorf("closed running: active false, want true")
		}
	}

	// The lane filter returns exactly the issues whose row says that lane,
	// so the filter and the column are one rule.
	for _, l := range issuestate.Lanes {
		got, err := s.ListIssues(ctx, IssueFilter{Lanes: []issuestate.Lane{l}})
		if err != nil {
			t.Fatal(err)
		}
		var want []int64
		for _, iss := range list {
			if iss.Lane == l {
				want = append(want, iss.ID)
			}
		}
		if !slices.Equal(issueIDs(got), want) {
			t.Errorf("lane %s: filter %v, rows %v", l, issueIDs(got), want)
		}
	}
}

func issueIDs(list []*Issue) []int64 {
	out := make([]int64, 0, len(list))
	for _, iss := range list {
		out = append(out, iss.ID)
	}
	return out
}

// TestListIssuesLaneFilter: lanes OR among themselves, AND with every other
// filter (a contradiction is empty), and are applied before LIMIT/OFFSET.
func TestListIssuesLaneFilter(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	var handOff []int64
	for range 3 {
		handOff = append(handOff, issueWithRoots(t, s, p.ID, "h", rootSpec{state: TaskDone}).ID)
	}
	running := issueWithRoots(t, s, p.ID, "r", rootSpec{state: TaskRunning})
	_ = issueWithRoots(t, s, p.ID, "o")
	closed := issueWithRoots(t, s, p.ID, "c", rootSpec{state: TaskDone})
	if _, err := s.TransitionIssue(ctx, closed.ID, issuestate.Close, "", nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(handOff) // ListIssues is newest-updated first

	list := func(f IssueFilter) []int64 {
		t.Helper()
		got, err := s.ListIssues(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return issueIDs(got)
	}
	if got := list(IssueFilter{Lanes: []issuestate.Lane{issuestate.LaneHandOff}}); !slices.Equal(got, handOff) {
		t.Errorf("hand_off = %v, want %v", got, handOff)
	}
	if got := list(IssueFilter{Lanes: []issuestate.Lane{issuestate.LaneHandOff}, Limit: 1, Offset: 1}); !slices.Equal(got, handOff[1:2]) {
		t.Errorf("hand_off page 2 = %v, want %v", got, handOff[1:2])
	}
	if got := list(IssueFilter{Lanes: []issuestate.Lane{issuestate.LaneInProgress, issuestate.LaneDone}}); !slices.Equal(got, []int64{closed.ID, running.ID}) {
		t.Errorf("in_progress|done = %v, want %v", got, []int64{closed.ID, running.ID})
	}
	if got := list(IssueFilter{States: []issuestate.State{issuestate.Open}, Lanes: []issuestate.Lane{issuestate.LaneDone}}); len(got) != 0 {
		t.Errorf("open ∧ done = %v, want none", got)
	}
	if got := list(IssueFilter{Lanes: []issuestate.Lane{issuestate.LaneDone}}); !slices.Equal(got, list(IssueFilter{States: []issuestate.State{issuestate.Closed}})) {
		t.Errorf("lane done %v != state closed", got)
	}
}

// TestProjectStatsIssueLanes: per-lane counts include closed issues as done,
// while open, open_imported and active keep counting open issues only.
func TestProjectStatsIssueLanes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p := testProject(t, s, "p1")
	issueWithRoots(t, s, p.ID, "o")
	issueWithRoots(t, s, p.ID, "r", rootSpec{state: TaskRunning})
	issueWithRoots(t, s, p.ID, "h", rootSpec{state: TaskDone})
	c := issueWithRoots(t, s, p.ID, "c", rootSpec{state: TaskRunning})
	if _, err := s.TransitionIssue(ctx, c.ID, issuestate.Close, "", nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	stats, err := s.ProjectStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st := stats[p.ID]
	want := map[issuestate.Lane]int{issuestate.LaneOpen: 1, issuestate.LaneInProgress: 1, issuestate.LaneHandOff: 1, issuestate.LaneDone: 1}
	if len(st.IssuesByLane) != len(want) {
		t.Errorf("IssuesByLane = %v, want %v", st.IssuesByLane, want)
	}
	for l, n := range want {
		if st.IssuesByLane[l] != n {
			t.Errorf("IssuesByLane[%s] = %d, want %d", l, st.IssuesByLane[l], n)
		}
	}
	if st.IssuesOpen != 3 || st.IssuesActive != 1 || st.IssuesOpenImported != 0 {
		t.Errorf("open %d active %d imported %d; want 3 1 0", st.IssuesOpen, st.IssuesActive, st.IssuesOpenImported)
	}
}
