package store

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func strPtr(v string) *string { return &v }

// The identity token is opaque to the store: whatever procx produced goes in
// and the identical bytes come back, because §12.4 recovery compares them
// byte-for-byte (issue #149).
func TestStepRunProcIdentityRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	const token = "linux1:6f0b1e2c-0000-4000-8000-abcdefabcdef:1234567"
	in := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "impl", StepType: "agent",
		Attempt: 1, State: StepRunning,
		PID: intPtr(4321), ProcIdentity: strPtr(token),
	}
	if err := s.CreateStepRun(ctx, in); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	got, err := s.GetStepRun(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.ProcIdentity == nil || *got.ProcIdentity != token {
		t.Fatalf("ProcIdentity = %v, want %q", got.ProcIdentity, token)
	}

	// The update path carries it too: the engine journals PID and identity in
	// one UpdateStepRun after spawn.
	const second = "linux1:6f0b1e2c-0000-4000-8000-abcdefabcdef:7654321"
	got.ProcIdentity = strPtr(second)
	if err := s.UpdateStepRun(ctx, got); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	if again, _ := s.GetStepRun(ctx, in.ID); again.ProcIdentity == nil || *again.ProcIdentity != second {
		t.Errorf("ProcIdentity after update = %v, want %q", again.ProcIdentity, second)
	}

	// And nil round-trips as NULL, which is what a spawn whose identity read
	// failed persists.
	got.ProcIdentity = nil
	if err := s.UpdateStepRun(ctx, got); err != nil {
		t.Fatalf("UpdateStepRun(nil identity): %v", err)
	}
	if again, _ := s.GetStepRun(ctx, in.ID); again.ProcIdentity != nil {
		t.Errorf("ProcIdentity after clearing = %v, want nil", *again.ProcIdentity)
	}
}

// A closed row must keep no pointer at a process it no longer owns — the
// identity is cleared beside the PID, or a later reader could believe a
// terminal row still names something live.
func TestTerminalizeOpenStepRunsClearsProcIdentity(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	task := testTask(t, s)

	open := &StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "impl", StepType: "agent",
		Attempt: 1, State: StepRunning,
		PID: intPtr(4321), ProcIdentity: strPtr("darwin1:1756200000.123456"),
	}
	if err := s.CreateStepRun(ctx, open); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	n, err := s.TerminalizeOpenStepRuns(ctx, task.ID, StepInterrupted, "interrupted")
	if err != nil {
		t.Fatalf("TerminalizeOpenStepRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("closed %d rows, want 1", n)
	}
	got, err := s.GetStepRun(ctx, open.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.State != StepInterrupted || got.PID != nil || got.ProcIdentity != nil {
		t.Errorf("terminalized row = %+v, want interrupted with pid and identity cleared", got)
	}
}

// migrateTo applies every embedded migration up to and including version, so a
// test can build a database as an older binary left it.
func migrateTo(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := migrationVersion(name)
		if err != nil {
			t.Fatalf("migrationVersion(%s): %v", name, err)
		}
		if v > version {
			continue
		}
		if err := applyMigration(db, name, v); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

// A database written before 0013 carries rows with no identity at all, and
// they must survive the upgrade readable — nil identity is the value recovery
// reads as "fall back to the ±5 s tolerance", not an error (issue #149).
func TestMigrateAddsProcIdentityToPreexistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 12)

	// Written the way the 0012-era binary wrote it: a PID and a wall-clock
	// spawn stamp, and no column to put an identity in.
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0012: %v", err)
		}
		defer func() { _ = db.Close() }()
		// One connection, foreign keys off: the fixture is a single
		// `step_runs` row, and what is under test reads it without joining
		// the task that would own it in a real database.
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatalf("disable foreign keys: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO step_runs
			(task_id, step_index, step_id, step_type, attempt, state, pid, proc_started_at, started_at)
			VALUES (1, 0, 'impl', 'agent', 1, 'running', 4321, ?, ?)`,
			formatTime(time.Now()), formatTime(time.Now())); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('step_runs') WHERE name = 'proc_identity'`).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info: %v", err)
		}
		if n != 0 {
			t.Fatalf("proc_identity exists at schema 12; the fixture is not a pre-0013 database")
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 12 -> %d): %v", latestSchemaVersion, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := schemaVersion(t, s); v != latestSchemaVersion {
		t.Errorf("schema version = %d, want %d", v, latestSchemaVersion)
	}

	runs, err := s.ListRunningStepRuns(t.Context())
	if err != nil {
		t.Fatalf("ListRunningStepRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("running step runs = %d, want the legacy row", len(runs))
	}
	if runs[0].ProcIdentity != nil {
		t.Errorf("legacy row read back with identity %q, want nil", *runs[0].ProcIdentity)
	}
	if runs[0].PID == nil || *runs[0].PID != 4321 || runs[0].ProcStartedAt == nil {
		t.Errorf("legacy row lost its PID guard fields: %+v", runs[0])
	}
}
