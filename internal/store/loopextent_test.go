package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A body row carries the extent the admission that wrote it planned, beside
// the item it ran on (migration 0026, issue #317). It is written once at
// insert and read back by every path a reader reaches a row through.
func TestStepRunLoopTotalRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	in := &StepRun{
		TaskID: task.ID, StepIndex: 1, StepID: "body", StepType: "command",
		Attempt: 1, Iteration: 2, LoopItem: "beta", LoopTotal: 3,
		State: StepRunning,
	}
	if err := s.CreateStepRun(ctx, in); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	got, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.LoopTotal != 3 {
		t.Errorf("GetStepRun LoopTotal = %d, want 3", got.LoopTotal)
	}
	if got.LoopItem != "beta" {
		t.Errorf("GetStepRun LoopItem = %q, want beta", got.LoopItem)
	}

	// Every read path selects the same column list, so a column missing from
	// one of them is a column missing from all of them — assert through the
	// list readers too rather than trusting the single-row scan for them.
	runs, err := s.ListStepRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].LoopTotal != 3 {
		t.Errorf("ListStepRuns LoopTotal = %+v, want one row carrying 3", runs)
	}
	at, err := s.ListStepRunsAt(ctx, task.ID, 1)
	if err != nil {
		t.Fatalf("ListStepRunsAt: %v", err)
	}
	if len(at) != 1 || at[0].LoopTotal != 3 {
		t.Errorf("ListStepRunsAt LoopTotal = %+v, want one row carrying 3", at)
	}

	// The extent is not a mutable field: UpdateStepRun writes the row's
	// mutable columns and leaves this one exactly as the insert wrote it,
	// the same way it leaves `loop_item`.
	finished := time.Now()
	got.State, got.FinishedAt, got.LoopTotal, got.LoopItem = StepSucceeded, &finished, 99, "zeta"
	if err := s.UpdateStepRun(ctx, got); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	after, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun after update: %v", err)
	}
	if after.LoopTotal != 3 || after.LoopItem != "beta" {
		t.Errorf("after update LoopTotal/LoopItem = %d/%q, want 3/beta — both are insert-only",
			after.LoopTotal, after.LoopItem)
	}
}

// A row written outside a loop records no extent, and 0 is what says so. It
// is the same value a pre-0026 row reads, which is what lets a reader treat
// "no extent" as one case rather than two.
func TestStepRunLoopTotalZeroOutsideALoop(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	in := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "implement", StepType: "agent",
		Attempt: 1, State: StepRunning,
	}
	if err := s.CreateStepRun(ctx, in); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	got, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.LoopTotal != 0 {
		t.Errorf("LoopTotal = %d, want 0 — a row outside a loop records no extent", got.LoopTotal)
	}
}

// A row written before migration 0026 reads back the same 0, so a loop
// already in flight over the upgrade renders exactly as it did before rather
// than claiming an extent nobody recorded.
func TestMigrateLeavesLoopTotalZeroOnPreexistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 25)

	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0025: %v", err)
		}
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatalf("disable foreign keys: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO step_runs
			(task_id, step_index, step_id, step_type, attempt, iteration, loop_item, state, started_at)
			VALUES (1, 0, 'body', 'command', 1, 2, 'beta', 'succeeded', ?)`,
			formatTime(time.Now())); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('step_runs') WHERE name = 'loop_total'`).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info: %v", err)
		}
		if n != 0 {
			t.Fatalf("loop_total exists at schema 25; the fixture is not a pre-0026 database")
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 25 -> %d): %v", latestSchemaVersion, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	runs, err := s.ListStepRuns(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want the legacy row", len(runs))
	}
	if runs[0].LoopTotal != 0 {
		t.Errorf("legacy LoopTotal = %d, want 0 — no extent was recorded for it", runs[0].LoopTotal)
	}
	if runs[0].LoopItem != "beta" {
		t.Errorf("legacy LoopItem = %q, want beta — the added column must not shift the scan",
			runs[0].LoopItem)
	}
}
