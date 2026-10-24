package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Raw delivery_json values a seeded row can carry (task 137 decision 13):
// the legacy shapes — NULL, and a delivery from before readiness existed —
// and the two a readiness write leaves.
const (
	rawNoDelivery = ""
	rawKeyless    = `{"kind":"change","title":"t","recorded_at":"2026-01-01T00:00:00Z"}`
	rawReady      = `{"kind":"change","title":"t","ready":true,"ready_by":"workflow","recorded_at":"2026-01-01T00:00:00Z"}`
	rawNotReady   = `{"kind":"change","title":"t","ready":false,"ready_by":"task","recorded_at":"2026-01-01T00:00:00Z"}`
)

// seedTask is one root task of a seeded issue, written with raw SQL so a
// test can build rows CreateTask would refuse — a side task with no main
// branch — and every legacy delivery shape.
type seedTask struct {
	state, archivedFrom TaskState
	side                bool
	raw                 string // delivery_json; rawNoDelivery for NULL
	finished            int    // finished_at, in minutes after a fixed epoch; 0 = unset
}

var seedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// seedIssue creates an issue whose root tasks are tasks, closed when closed.
func seedIssue(t *testing.T, s *Store, projectID int64, title string, closed bool, tasks ...seedTask) *Issue {
	t.Helper()
	ctx := t.Context()
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: projectID, Title: title})
	for n, st := range tasks {
		task := newTask(projectID, fmt.Sprintf("seed-%d-%d", iss.ID, n), st.state)
		task.ArchivedFrom = st.archivedFrom
		mustCreate(t, s, task)
		role := IssueWorktreeMain
		if st.side {
			role = IssueWorktreeSide
		}
		raw := sql.NullString{String: st.raw, Valid: st.raw != rawNoDelivery}
		var finished sql.NullString
		if st.finished != 0 {
			finished = sql.NullString{String: formatTime(seedEpoch.Add(time.Duration(st.finished) * time.Minute)), Valid: true}
		}
		if _, err := s.db.Exec(`UPDATE tasks SET issue_id = ?, issue_worktree = ?, delivery_json = ?, finished_at = ? WHERE id = ?`,
			iss.ID, role, raw, finished, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if closed {
		if _, err := s.TransitionIssue(ctx, iss.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
			t.Fatal(err)
		}
	}
	return mustGetIssue(t, s, iss.ID)
}

func mustGetIssue(t *testing.T, s *Store, id int64) *Issue {
	t.Helper()
	iss, err := s.GetIssue(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

func finishedState(st seedTask) bool {
	return st.state == TaskDone || (st.state == TaskArchived && st.archivedFrom == TaskDone)
}

// seedLaneOf is issuestate.LaneOf over seeded tasks, reduced in Go the way
// laneExpr reduces them in SQL: the latest finished main-line task is the
// one with the greatest finished_at (ties to the greater id, which is the
// later in tasks), and its delivery reads ready unless it says otherwise.
func seedLaneOf(closed bool, tasks []seedTask) issuestate.Lane {
	var unsettled, finished bool
	latest := -1
	for n, st := range tasks {
		unsettled = unsettled || !taskstate.Settled(st.state)
		if !finishedState(st) {
			continue
		}
		finished = true
		if !st.side && (latest < 0 || st.finished >= tasks[latest].finished) {
			latest = n
		}
	}
	ready := latest >= 0 && tasks[latest].raw != rawNotReady
	state := issuestate.Open
	if closed {
		state = issuestate.Closed
	}
	return issuestate.LaneOf(state, unsettled, finished, ready)
}

// TestLaneOfMatchesLaneExpr seeds an issue for every combination of the
// facts the lane reads — closed, an unsettled root, a finished side task,
// and the latest main-line delivery's shape — and holds laneExpr to
// issuestate.LaneOf on each, so the two spellings of the rule cannot drift.
func TestLaneOfMatchesLaneExpr(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	mains := map[string][]seedTask{
		"no main":            nil,
		"aborted main":       {{state: TaskAborted, finished: 1}},
		"null delivery":      {{state: TaskDone, finished: 1}},
		"keyless":            {{state: TaskDone, raw: rawKeyless, finished: 1}},
		"ready":              {{state: TaskDone, raw: rawReady, finished: 1}},
		"not ready":          {{state: TaskDone, raw: rawNotReady, finished: 1}},
		"archived not ready": {{state: TaskArchived, archivedFrom: TaskDone, raw: rawNotReady, finished: 1}},
		"archived ready":     {{state: TaskArchived, archivedFrom: TaskDone, raw: rawReady, finished: 1}},
		"newer not ready":    {{state: TaskDone, raw: rawReady, finished: 1}, {state: TaskDone, raw: rawNotReady, finished: 2}},
		"newer ready":        {{state: TaskDone, raw: rawNotReady, finished: 1}, {state: TaskDone, raw: rawKeyless, finished: 2}},
	}
	names := make([]string, 0, len(mains))
	for name := range mains {
		names = append(names, name)
	}
	slices.Sort(names)
	seen := map[issuestate.Lane]bool{}
	for _, name := range names {
		for _, closed := range []bool{false, true} {
			for _, running := range []bool{false, true} {
				for _, side := range []bool{false, true} {
					tasks := slices.Clone(mains[name])
					if running {
						tasks = append(tasks, seedTask{state: TaskRunning})
					}
					if side {
						// Newest of all, and not ready: a side task is never
						// the latest delivery, whatever it says.
						tasks = append(tasks, seedTask{state: TaskDone, side: true, raw: rawNotReady, finished: 9})
					}
					label := fmt.Sprintf("%s closed=%v running=%v side=%v", name, closed, running, side)
					iss := seedIssue(t, s, p.ID, label, closed, tasks...)
					want := seedLaneOf(closed, tasks)
					if iss.Lane != want {
						t.Errorf("%s: laneExpr %s, LaneOf %s", label, iss.Lane, want)
					}
					seen[iss.Lane] = true
				}
			}
		}
	}
	for _, l := range issuestate.Lanes {
		if !seen[l] {
			t.Errorf("no combination landed in %s", l)
		}
	}
}

// oldLaneExpr is the lane rule before task 137.15, kept here only to prove
// the upgrade: any finished root task — main or side — was `hand_off`.
func oldLaneExpr() sqlFrag {
	unsettled, handedOff := activeExpr(), rootTaskExists(handedOffCond())
	args := []any{string(issuestate.Closed), string(issuestate.LaneDone)}
	args = append(args, unsettled.args...)
	args = append(args, string(issuestate.LaneInProgress))
	args = append(args, handedOff.args...)
	args = append(args, string(issuestate.LaneHandOff), string(issuestate.LaneOpen))
	return sqlFrag{
		sql: `(CASE WHEN i.state = ? THEN ?
			WHEN ` + unsettled.sql + ` THEN ?
			WHEN ` + handedOff.sql + ` THEN ?
			ELSE ? END)`,
		args: args,
	}
}

// TestUpgradeKeepsEveryLaneButSideOnly: a database as the previous release
// left it — no delivery carries a `ready` key, many are NULL — reads the
// same lane under the old rule and the new one for every issue shape,
// except the one documented edge: an issue whose only finished work is a
// side task's moves from `hand_off` to an idle `in_progress`.
func TestUpgradeKeepsEveryLaneButSideOnly(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	shapes := []struct {
		name     string
		closed   bool
		sideOnly bool
		tasks    []seedTask
	}{
		{name: "no tasks"},
		{name: "aborted", tasks: []seedTask{{state: TaskAborted}}},
		{name: "running", tasks: []seedTask{{state: TaskRunning}}},
		{name: "blocked", tasks: []seedTask{{state: TaskBlocked}}},
		{name: "done null", tasks: []seedTask{{state: TaskDone, finished: 1}}},
		{name: "done keyless", tasks: []seedTask{{state: TaskDone, raw: rawKeyless, finished: 1}}},
		{name: "archived null", tasks: []seedTask{{state: TaskArchived, archivedFrom: TaskDone, finished: 1}}},
		{name: "archived aborted", tasks: []seedTask{{state: TaskArchived, archivedFrom: TaskAborted, finished: 1}}},
		{name: "done and running", tasks: []seedTask{{state: TaskDone, finished: 1}, {state: TaskRunning}}},
		{name: "done and aborted", tasks: []seedTask{{state: TaskDone, raw: rawKeyless, finished: 1}, {state: TaskAborted, finished: 2}}},
		{name: "main and side done", tasks: []seedTask{{state: TaskDone, finished: 1}, {state: TaskDone, side: true, raw: rawKeyless, finished: 2}}},
		{name: "side running", tasks: []seedTask{{state: TaskDone, finished: 1}, {state: TaskRunning, side: true}}},
		{name: "closed done", closed: true, tasks: []seedTask{{state: TaskDone, finished: 1}}},
		{name: "closed side only", closed: true, tasks: []seedTask{{state: TaskDone, side: true, finished: 1}}},
		{name: "side only", sideOnly: true, tasks: []seedTask{{state: TaskDone, side: true, raw: rawKeyless, finished: 1}}},
		{name: "side only, merge-back cancelled", sideOnly: true, tasks: []seedTask{
			{state: TaskDone, side: true, finished: 1}, {state: TaskAborted, finished: 2},
		}},
	}
	old := oldLaneExpr()
	for _, sh := range shapes {
		iss := seedIssue(t, s, p.ID, sh.name, sh.closed, sh.tasks...)
		var was string
		if err := s.db.QueryRow(`SELECT `+old.sql+` FROM issues i WHERE i.id = ?`,
			append(slices.Clone(old.args), iss.ID)...).Scan(&was); err != nil {
			t.Fatal(err)
		}
		switch {
		case sh.sideOnly:
			if was != string(issuestate.LaneHandOff) || iss.Lane != issuestate.LaneInProgress || iss.Active {
				t.Errorf("%s: %s → %s (active %v), want hand_off → idle in_progress", sh.name, was, iss.Lane, iss.Active)
			}
		case was != string(iss.Lane):
			t.Errorf("%s: lane moved on upgrade, %s → %s", sh.name, was, iss.Lane)
		}
	}
}

// finishedMain is a main task of iss that finished `done` with d.
func finishedMain(t *testing.T, s *Store, projectID int64, iss *Issue, title string, d *Delivery) *Task {
	t.Helper()
	task := newTask(projectID, title, TaskRunning)
	task.IssueID, task.IssueWorktree = &iss.ID, IssueWorktreeMain
	mustCreate(t, s, task)
	done, _, err := s.TransitionTask(t.Context(), task.ID, TaskRunning, TaskDone, TaskChange{Delivery: d})
	if err != nil {
		t.Fatalf("finish %s: %v", title, err)
	}
	return done
}

func readyDelivery(title string, ready bool, by ReadyBy) *Delivery {
	d := recordedDelivery(title)
	d.Ready, d.ReadyBy = &ready, by
	return d
}

func wantLane(t *testing.T, s *Store, issueID int64, want issuestate.Lane) {
	t.Helper()
	if got := mustGetIssue(t, s, issueID).Lane; got != want {
		t.Fatalf("issue %d lane = %s, want %s", issueID, got, want)
	}
}

func wantReady(t *testing.T, s *Store, taskID int64, ready *bool, by ReadyBy) {
	t.Helper()
	task, err := s.GetTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	d := task.Delivery
	if d == nil {
		t.Fatalf("task %d has no delivery", taskID)
	}
	if (d.Ready == nil) != (ready == nil) || (ready != nil && *d.Ready != *ready) || d.ReadyBy != by {
		t.Errorf("task %d readiness = %v by %q, want %v by %q", taskID, d.Ready, d.ReadyBy, ready, by)
	}
}

func boolPtr(v bool) *bool { return &v }

// TestReadinessMovesTheLane: an auto-style ready delivery is `hand_off` as
// before; a not-ready one waits in `in_progress` until a person marks it
// ready, which announces the move by that person, once; unready takes it
// back, by whoever did it.
func TestReadinessMovesTheLane(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	auto := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "auto"})
	finishedMain(t, s, p.ID, auto, "auto", readyDelivery("auto", true, ReadyByWorkflow))
	wantLane(t, s, auto.ID, issuestate.LaneHandOff)

	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "explicit"})
	task := finishedMain(t, s, p.ID, iss, "explicit", readyDelivery("explicit", false, ""))
	wantLane(t, s, iss.ID, issuestate.LaneInProgress)
	if got := mustGetIssue(t, s, iss.ID); got.Active {
		t.Errorf("a not-ready issue is active; it is idle")
	}

	w := watchEvents(s)
	got, err := s.MarkIssueReady(ctx, iss.ID, issuestate.Human)
	if err != nil {
		t.Fatalf("MarkIssueReady: %v", err)
	}
	if got.Lane != issuestate.LaneHandOff {
		t.Errorf("returned lane %s, want hand_off", got.Lane)
	}
	wantLaneMove(t, s, w.take(), EventTaskDeliveryChanged, iss.ID, issuestate.LaneInProgress, issuestate.LaneHandOff, 0, issuestate.Human)
	wantReady(t, s, task.ID, boolPtr(true), ReadyByHuman)

	// Already ready: no write, no event.
	if _, err := s.MarkIssueReady(ctx, iss.ID, issuestate.Agent); err != nil {
		t.Fatal(err)
	}
	if evs := w.take(); len(evs) != 0 {
		t.Errorf("marking a ready issue ready published %v", eventTypes(evs))
	}
	wantReady(t, s, task.ID, boolPtr(true), ReadyByHuman)

	if _, err := s.MarkIssueUnready(ctx, iss.ID, issuestate.Agent); err != nil {
		t.Fatalf("MarkIssueUnready: %v", err)
	}
	evs := w.take()
	wantLaneMove(t, s, evs, EventTaskDeliveryChanged, iss.ID, issuestate.LaneHandOff, issuestate.LaneInProgress, 0, issuestate.Agent)
	if p := payloadOf(t, *evs[0]); p["ready"] != false || p["kind"] != string(DeliveryChange) {
		t.Errorf("delivery_changed payload = %v, want ready false", p)
	}
	wantReady(t, s, task.ID, boolPtr(false), ReadyByAgent)
	// The text is untouched by a readiness write.
	if reread, _ := s.GetTask(ctx, task.ID); reread.Delivery.Title != "explicit" || reread.Delivery.HeadSHA != "abc123" {
		t.Errorf("delivery text changed: %+v", reread.Delivery)
	}

	if _, err := s.MarkIssueUnready(ctx, iss.ID, issuestate.Human); !errors.Is(err, ErrIssueNotHandOff) {
		t.Errorf("unready an in_progress issue: err = %v, want ErrIssueNotHandOff", err)
	}
}

// TestReadinessRefusals: each refusal, in the documented order.
func TestReadinessRefusals(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	both := func(name string, issueID int64, want error) {
		t.Helper()
		for _, op := range []struct {
			name string
			fn   func() (*Issue, error)
		}{
			{"ready", func() (*Issue, error) { return s.MarkIssueReady(ctx, issueID, issuestate.Human) }},
			{"unready", func() (*Issue, error) { return s.MarkIssueUnready(ctx, issueID, issuestate.Human) }},
		} {
			if _, err := op.fn(); !errors.Is(err, want) {
				t.Errorf("%s %s: err = %v, want %v", op.name, name, err, want)
			}
		}
	}
	both("unknown", 999, ErrNotFound)

	closed := seedIssue(t, s, p.ID, "closed", true, seedTask{state: TaskDone, finished: 1})
	both("closed", closed.ID, ErrInvalidIssueAction)

	// A root task running beside finished work: the run will deliver.
	running := seedIssue(t, s, p.ID, "running", false, seedTask{state: TaskDone, finished: 1}, seedTask{state: TaskRunning})
	both("running", running.ID, ErrIssueWorkRunning)

	none := seedIssue(t, s, p.ID, "none", false, seedTask{state: TaskAborted, finished: 1})
	both("only aborted", none.ID, ErrIssueNoFinishedWork)
	empty := seedIssue(t, s, p.ID, "empty", false)
	both("no tasks", empty.ID, ErrIssueNoFinishedWork)

	// The side-only edge: in_progress, and nothing a person can mark.
	side := seedIssue(t, s, p.ID, "side only", false, seedTask{state: TaskDone, side: true, finished: 1})
	if side.Lane != issuestate.LaneInProgress || side.Active {
		t.Errorf("side only: lane %s active %v, want idle in_progress", side.Lane, side.Active)
	}
	both("side only", side.ID, ErrIssueNoFinishedWork)

	// Claimed by a live handoff item: the handoff decides now.
	claimed, _ := handedOffIssue(t, s, p.ID, "claimed", "h1")
	h, _ := mustHandoff(t, s, p.ID, "handoff", claimed.ID)
	for _, fn := range []func() (*Issue, error){
		func() (*Issue, error) { return s.MarkIssueReady(ctx, claimed.ID, issuestate.Human) },
		func() (*Issue, error) { return s.MarkIssueUnready(ctx, claimed.ID, issuestate.Human) },
	} {
		_, err := fn()
		var ih *IssueInHandoffError
		if !errors.As(err, &ih) || ih.IssueID != claimed.ID || ih.HandoffID != h.ID || !errors.Is(err, ErrIssueInHandoff) {
			t.Errorf("claimed: err = %v, want IssueInHandoffError for handoff %d", err, h.ID)
		}
	}

	if _, err := s.MarkIssueReady(ctx, empty.ID, "robot"); err == nil {
		t.Errorf("an unknown actor was accepted")
	}
}

// TestNewerFinishedWorkResetsReadiness: readiness belongs to the latest
// delivery, so a newer finished main task that is not ready takes the issue
// out of `hand_off`, and marking it ready marks that one.
func TestNewerFinishedWorkResetsReadiness(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "i"})
	first := finishedMain(t, s, p.ID, iss, "first", readyDelivery("first", true, ReadyByWorkflow))
	wantLane(t, s, iss.ID, issuestate.LaneHandOff)
	second := finishedMain(t, s, p.ID, iss, "second", readyDelivery("second", false, ""))
	wantLane(t, s, iss.ID, issuestate.LaneInProgress)
	if _, err := s.MarkIssueReady(ctx, iss.ID, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	wantReady(t, s, second.ID, boolPtr(true), ReadyByHuman)
	wantReady(t, s, first.ID, boolPtr(true), ReadyByWorkflow)
	wantLane(t, s, iss.ID, issuestate.LaneHandOff)
}

// TestSetTaskDeliveryReadiness is decision 2's "a correction keeps
// readiness": on a running task the last call wins and --ready is the
// task's; on a done task a correction replaces only the text, unless it
// marks the delivery ready, which can move the lane.
func TestSetTaskDeliveryReadiness(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")

	iss := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "running"})
	task := newTask(p.ID, "running", TaskRunning)
	task.IssueID, task.IssueWorktree = &iss.ID, IssueWorktreeMain
	mustCreate(t, s, task)
	w := watchEvents(s)
	if _, err := s.SetTaskDelivery(ctx, task.ID, readyDelivery("a", true, ReadyByHuman)); err != nil {
		t.Fatal(err)
	}
	wantReady(t, s, task.ID, boolPtr(true), ReadyByTask)
	if evs := w.take(); len(evs) != 1 || payloadOf(t, *evs[0])["ready"] != true {
		t.Errorf("running ready: published %v", eventTypes(evs))
	}
	if _, err := s.SetTaskDelivery(ctx, task.ID, recordedDelivery("b")); err != nil {
		t.Fatal(err)
	}
	wantReady(t, s, task.ID, nil, "")
	if evs := w.take(); len(evs) != 1 {
		t.Errorf("running clear: published %v", eventTypes(evs))
	} else if _, has := payloadOf(t, *evs[0])["ready"]; has {
		t.Errorf("a delivery with no readiness announced one: %s", evs[0].Payload)
	}
	if _, err := s.SetTaskDelivery(ctx, task.ID, readyDelivery("c", false, ReadyByHuman)); err != nil {
		t.Fatal(err)
	}
	wantReady(t, s, task.ID, nil, "")

	// Done, ready by its workflow: a correction keeps that.
	done := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "done"})
	dt := finishedMain(t, s, p.ID, done, "done", readyDelivery("done", true, ReadyByWorkflow))
	w.take()
	for _, d := range []*Delivery{recordedDelivery("fix"), readyDelivery("fix2", false, ReadyByHuman)} {
		got, err := s.SetTaskDelivery(ctx, dt.ID, d)
		if err != nil {
			t.Fatal(err)
		}
		if got.Delivery.Title != d.Title {
			t.Errorf("correction text = %q, want %q", got.Delivery.Title, d.Title)
		}
		wantReady(t, s, dt.ID, boolPtr(true), ReadyByWorkflow)
	}
	wantLane(t, s, done.ID, issuestate.LaneHandOff)
	if evs := w.take(); len(laneChanges(evs)) != 0 {
		t.Errorf("a correction moved the lane: %v", eventTypes(evs))
	}

	// Done, not ready: a correction keeps that too, and one marking it
	// ready moves the lane, by the actor it names.
	idle := mustCreateIssue(t, s, NewIssue{ProjectID: p.ID, Title: "idle"})
	it := finishedMain(t, s, p.ID, idle, "idle", readyDelivery("idle", false, ""))
	if _, err := s.SetTaskDelivery(ctx, it.ID, recordedDelivery("fix")); err != nil {
		t.Fatal(err)
	}
	wantReady(t, s, it.ID, boolPtr(false), "")
	wantLane(t, s, idle.ID, issuestate.LaneInProgress)
	w.take()
	if _, err := s.SetTaskDelivery(ctx, it.ID, readyDelivery("ok", true, ReadyByAgent)); err != nil {
		t.Fatal(err)
	}
	wantLaneMove(t, s, w.take(), EventTaskDeliveryChanged, idle.ID, issuestate.LaneInProgress, issuestate.LaneHandOff, 0, issuestate.Agent)
	wantReady(t, s, it.ID, boolPtr(true), ReadyByAgent)
}

// TestLegacyDeliveriesReadReady: a NULL delivery and one with no `ready`
// key read ready, as every lane did before readiness. Withdrawing a NULL
// one writes a readiness-only delivery, which the backfill still lists and
// fills without losing the decision.
func TestLegacyDeliveriesReadReady(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	null := seedIssue(t, s, p.ID, "null", false, seedTask{state: TaskDone, finished: 1})
	keyless := seedIssue(t, s, p.ID, "keyless", false, seedTask{state: TaskDone, raw: rawKeyless, finished: 1})
	for _, iss := range []*Issue{null, keyless} {
		if iss.Lane != issuestate.LaneHandOff {
			t.Errorf("%s: lane %s, want hand_off", iss.Title, iss.Lane)
		}
		w := watchEvents(s)
		if _, err := s.MarkIssueReady(ctx, iss.ID, issuestate.Human); err != nil {
			t.Fatal(err)
		}
		if evs := w.take(); len(evs) != 0 {
			t.Errorf("%s: marking a legacy delivery ready published %v", iss.Title, eventTypes(evs))
		}
	}

	if _, err := s.MarkIssueUnready(ctx, null.ID, issuestate.Human); err != nil {
		t.Fatalf("unready a NULL delivery: %v", err)
	}
	wantLane(t, s, null.ID, issuestate.LaneInProgress)
	missing, err := s.ListTasksMissingDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].Delivery == nil || missing[0].Delivery.Kind != "" {
		t.Fatalf("missing = %d tasks (%+v), want the readiness-only one and none other", len(missing), missing)
	}
	wantReady(t, s, missing[0].ID, boolPtr(false), ReadyByHuman)
	if ok, err := s.FillTaskDelivery(ctx, missing[0].ID, &Delivery{Kind: DeliveryChange, Implicit: true, Title: "filled"}); err != nil || !ok {
		t.Fatalf("FillTaskDelivery = %v, %v", ok, err)
	}
	wantReady(t, s, missing[0].ID, boolPtr(false), ReadyByHuman)
	filled, _ := s.GetTask(ctx, missing[0].ID)
	if filled.Delivery.Kind != DeliveryChange || filled.Delivery.Title != "filled" {
		t.Errorf("filled delivery = %+v", filled.Delivery)
	}
	wantLane(t, s, null.ID, issuestate.LaneInProgress)
	if ok, _ := s.FillTaskDelivery(ctx, missing[0].ID, &Delivery{Kind: DeliveryNone}); ok {
		t.Errorf("a second fill overwrote a delivery")
	}
}
