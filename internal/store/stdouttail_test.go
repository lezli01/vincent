package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A row written before migration 0025 records no stdout tail. It must read
// back as nil rather than as an empty string: nil is what tells the engine to
// render `.Steps.<id>.Result` from `result_summary` the way it always did, so
// a task in flight over the upgrade is undisturbed (§8.4, issue #311).
func TestMigrateLeavesStdoutTailNullOnPreexistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 24)

	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0024: %v", err)
		}
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatalf("disable foreign keys: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO step_runs
			(task_id, step_index, step_id, step_type, attempt, state, result_summary, started_at)
			VALUES (1, 0, 'emit', 'command', 1, 'succeeded', 'alpha', ?)`,
			formatTime(time.Now())); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('step_runs') WHERE name = 'stdout_tail'`).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info: %v", err)
		}
		if n != 0 {
			t.Fatalf("stdout_tail exists at schema 24; the fixture is not a pre-0025 database")
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 24 -> %d): %v", latestSchemaVersion, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	runs, err := s.ListStepRuns(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want the legacy row", len(runs))
	}
	if runs[0].StdoutTail != nil {
		t.Errorf("legacy StdoutTail = %q, want nil so `.Result` falls back to result_summary",
			*runs[0].StdoutTail)
	}
	if runs[0].ResultSummary != "alpha" {
		t.Errorf("the migration disturbed an existing column: %q", runs[0].ResultSummary)
	}
}

// Empty and absent are different facts and must survive a round trip as
// different values: a command that printed nothing has an empty `.Result`,
// while a row that recorded no tail at all falls back.
func TestStepRunStdoutTailRoundTrip(t *testing.T) {
	s := openTest(t)
	taskID := testTask(t, s).ID
	empty, text := "", "alpha\nbeta"
	for _, tc := range []struct {
		name string
		want *string
	}{
		{"absent", nil},
		{"empty", &empty},
		{"text", &text},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &StepRun{
				TaskID: taskID, StepID: "emit", StepType: "command",
				Attempt: 1, State: StepRunning,
			}
			if err := s.CreateStepRun(t.Context(), run); err != nil {
				t.Fatalf("CreateStepRun: %v", err)
			}
			run.State, run.StdoutTail = StepSucceeded, tc.want
			if err := s.UpdateStepRun(t.Context(), run); err != nil {
				t.Fatalf("UpdateStepRun: %v", err)
			}
			got, err := s.GetStepRun(t.Context(), run.ID)
			if err != nil {
				t.Fatalf("GetStepRun: %v", err)
			}
			switch {
			case tc.want == nil && got.StdoutTail != nil:
				t.Errorf("StdoutTail = %q, want nil", *got.StdoutTail)
			case tc.want != nil && got.StdoutTail == nil:
				t.Errorf("StdoutTail = nil, want %q", *tc.want)
			case tc.want != nil && *got.StdoutTail != *tc.want:
				t.Errorf("StdoutTail = %q, want %q", *got.StdoutTail, *tc.want)
			}
		})
	}
}
