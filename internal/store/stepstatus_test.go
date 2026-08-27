package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// countStatusEvents is how many `task.status_changed` rows the database
// holds — the figure that says whether the dedup rule actually held.
func countStatusEvents(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM events WHERE type = ?`, EventTaskStatusChanged).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

func runningStepRun(t *testing.T, s *Store, taskID int64, stepID string) *StepRun {
	t.Helper()
	r := &StepRun{
		TaskID: taskID, StepIndex: 0, StepID: stepID, StepType: "agent",
		Attempt: 1, State: StepRunning,
	}
	if err := s.CreateStepRun(t.Context(), r); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return r
}

// The column is nothing but a value the store carries: what goes in comes
// back, and an empty message is stored as NULL rather than as "".
func TestStepRunStatusRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	in := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "impl", StepType: "agent",
		Attempt: 1, State: StepRunning, StatusMessage: "rebasing onto master",
	}
	if err := s.CreateStepRun(ctx, in); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	got, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.StatusMessage != "rebasing onto master" {
		t.Fatalf("StatusMessage = %q, want the message the insert carried", got.StatusMessage)
	}

	var raw sql.NullString
	if err := s.db.QueryRow(
		`SELECT status_message FROM step_runs WHERE id = ?`, in.ID).Scan(&raw); err != nil {
		t.Fatalf("read status_message: %v", err)
	}
	if !raw.Valid {
		t.Error("a message that was set stored as NULL")
	}

	// The engine's own final write must not carry the column: the step sets
	// its status from another process while the actor is blocked in Wait, so
	// an UpdateStepRun with the actor's stale struct would erase it. This is
	// the property that makes the last live value survive onto the finished
	// row (task 036).
	stale := *got
	stale.StatusMessage = ""
	stale.State = StepSucceeded
	if err := s.UpdateStepRun(ctx, &stale); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	after, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun after update: %v", err)
	}
	if after.StatusMessage != "rebasing onto master" {
		t.Errorf("StatusMessage after UpdateStepRun = %q, want it untouched", after.StatusMessage)
	}
	if after.State != StepSucceeded {
		t.Errorf("state = %s, want the update to have applied everything else", after.State)
	}
}

// The dedup rule of §13.3: a changed write appends exactly one event, and a
// re-write of the identical message appends none. A board that refetches on
// the event must not be woken by news it already has.
func TestSetStepRunStatusDedupesEvents(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)
	run := runningStepRun(t, s, task.ID, "impl")

	runID, changed, err := s.SetStepRunStatus(ctx, task.ID, "impl", "3 tests red in internal/store")
	if err != nil {
		t.Fatalf("SetStepRunStatus: %v", err)
	}
	if runID != run.ID || !changed {
		t.Fatalf("first write = (%d, %v), want (%d, true)", runID, changed, run.ID)
	}
	if n := countStatusEvents(t, s); n != 1 {
		t.Fatalf("events after the first write = %d, want exactly 1", n)
	}

	_, changed, err = s.SetStepRunStatus(ctx, task.ID, "impl", "3 tests red in internal/store")
	if err != nil {
		t.Fatalf("identical re-write: %v", err)
	}
	if changed {
		t.Error("an identical re-write reported a change")
	}
	if n := countStatusEvents(t, s); n != 1 {
		t.Errorf("events after an identical re-write = %d, want still 1", n)
	}

	if _, changed, err = s.SetStepRunStatus(ctx, task.ID, "impl", "1 test red"); err != nil {
		t.Fatalf("changed write: %v", err)
	}
	if !changed {
		t.Error("a changed write reported no change")
	}
	if n := countStatusEvents(t, s); n != 2 {
		t.Errorf("events after a changed write = %d, want 2", n)
	}

	// The payload is what §13.3 promises, and it carries the project so the
	// /v1/events project filter works on this type like every other.
	evs, err := s.ListEvents(ctx, EventFilter{Types: []string{EventTaskStatusChanged}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	last := evs[len(evs)-1]
	var payload struct {
		TaskID  int64  `json:"task_id"`
		StepID  string `json:"step_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatalf("payload: %v (%s)", err, last.Payload)
	}
	if payload.TaskID != task.ID || payload.StepID != "impl" || payload.Message != "1 test red" {
		t.Errorf("payload = %+v, want the task, step and message", payload)
	}
	if last.ProjectID == nil || *last.ProjectID != task.ProjectID {
		t.Errorf("event project = %v, want %d", last.ProjectID, task.ProjectID)
	}
}

// A write addressed at a step that is not running is refused, never applied
// silently — and the refusal is its own error, so the API can answer 409
// rather than 404.
func TestSetStepRunStatusRefusesAFinishedStep(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)
	run := runningStepRun(t, s, task.ID, "impl")

	run.State = StepSucceeded
	if err := s.UpdateStepRun(ctx, run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	if _, _, err := s.SetStepRunStatus(ctx, task.ID, "impl", "too late"); !errors.Is(err, ErrStepNotRunning) {
		t.Fatalf("write against a finished step = %v, want ErrStepNotRunning", err)
	}
	if _, err := s.RunningStepRunID(ctx, task.ID, "impl"); !errors.Is(err, ErrStepNotRunning) {
		t.Errorf("RunningStepRunID on a finished step = %v, want ErrStepNotRunning", err)
	}
	if n := countStatusEvents(t, s); n != 0 {
		t.Errorf("a refused write appended %d events, want 0", n)
	}
	if got, _ := s.GetStepRun(ctx, run.ID); got.StatusMessage != "" {
		t.Errorf("a refused write reached the row: %q", got.StatusMessage)
	}
}

// Two `parallel` sub-steps share one task id and run at once (§7.5), which is
// the whole reason the endpoint is keyed by step id: each must address its
// own row.
func TestSetStepRunStatusAddressesOneSubStep(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)
	left := runningStepRun(t, s, task.ID, "lint")
	right := runningStepRun(t, s, task.ID, "test")

	if _, _, err := s.SetStepRunStatus(ctx, task.ID, "lint", "checking imports"); err != nil {
		t.Fatalf("lint status: %v", err)
	}
	if _, _, err := s.SetStepRunStatus(ctx, task.ID, "test", "running ./internal/..."); err != nil {
		t.Fatalf("test status: %v", err)
	}
	gotLeft, _ := s.GetStepRun(ctx, left.ID)
	gotRight, _ := s.GetStepRun(ctx, right.ID)
	if gotLeft.StatusMessage != "checking imports" {
		t.Errorf("lint status = %q", gotLeft.StatusMessage)
	}
	if gotRight.StatusMessage != "running ./internal/..." {
		t.Errorf("test status = %q", gotRight.StatusMessage)
	}
}

// The board reads GET /v1/tasks and never fetches step rows, so the list
// endpoint denormalizes the newest row's status. Newest row and not newest
// *message*: a step that spoke and finished must not have its line linger
// beside the next step, which is doing something else.
func TestLatestStepStatuses(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	first := runningStepRun(t, s, task.ID, "build")
	if _, _, err := s.SetStepRunStatus(ctx, task.ID, "build", "compiling"); err != nil {
		t.Fatalf("build status: %v", err)
	}
	got, err := s.LatestStepStatuses(ctx, []int64{task.ID})
	if err != nil {
		t.Fatalf("LatestStepStatuses: %v", err)
	}
	if got[task.ID] != "compiling" {
		t.Fatalf("status = %q, want the running step's", got[task.ID])
	}

	first.State = StepSucceeded
	if err := s.UpdateStepRun(ctx, first); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	runningStepRun(t, s, task.ID, "publish") // says nothing
	got, err = s.LatestStepStatuses(ctx, []int64{task.ID})
	if err != nil {
		t.Fatalf("LatestStepStatuses: %v", err)
	}
	if _, ok := got[task.ID]; ok {
		t.Errorf("status = %q, want none: the newest row said nothing", got[task.ID])
	}

	if got, err := s.LatestStepStatuses(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("LatestStepStatuses(nil) = %v, %v; want an empty map", got, err)
	}
}

// A database written before 0014 has rows with no status column at all. They
// must survive the upgrade readable, with the status reading as "said
// nothing" rather than as an error.
func TestMigrateAddsStatusToPreexistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 13)

	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0013: %v", err)
		}
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatalf("disable foreign keys: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO step_runs
			(task_id, step_index, step_id, step_type, attempt, state, result_summary, started_at)
			VALUES (1, 0, 'impl', 'agent', 1, 'succeeded', 'all green', ?)`,
			formatTime(time.Now())); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('step_runs') WHERE name = 'status_message'`).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info: %v", err)
		}
		if n != 0 {
			t.Fatalf("status_message exists at schema 13; the fixture is not a pre-0014 database")
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 13 -> %d): %v", latestSchemaVersion, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := schemaVersion(t, s); v != latestSchemaVersion {
		t.Errorf("schema version = %d, want %d", v, latestSchemaVersion)
	}
	runs, err := s.ListStepRuns(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want the legacy row", len(runs))
	}
	if runs[0].StatusMessage != "" {
		t.Errorf("legacy StatusMessage = %q, want empty", runs[0].StatusMessage)
	}
	if runs[0].ResultSummary != "all green" {
		t.Errorf("the migration disturbed an existing column: %q", runs[0].ResultSummary)
	}
}
