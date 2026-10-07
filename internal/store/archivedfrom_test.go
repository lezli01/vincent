package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

// seedArchivedFromFixture writes, at schema 0041, the shapes migration 0042
// has to backfill: an archived task whose archiving events disagree (the
// newest must win), one with no event at all, one whose only event names a
// state the CHECK would refuse, and a live task that must stay NULL.
func seedArchivedFromFixture(t *testing.T, path string) {
	t.Helper()
	migrateTo(t, path, 41)
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open at 0041: %v", err)
	}
	defer func() { _ = db.Close() }()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
		VALUES (1, 'p1', '/p1', 'main', ?, ?)`, backfillAt(0), backfillAt(0))
	task := func(id int, state string, archived any) {
		t.Helper()
		exec(`INSERT INTO tasks (id, project_id, title, workflow_name, workflow_snapshot, base_branch,
			branch_name, state, created_at, updated_at, archived_at)
			VALUES (?, 1, 't', 'adhoc', 'steps: []', 'main', ?, ?, ?, ?, ?)`,
			id, "b"+strconv.Itoa(id), state, backfillAt(id), backfillAt(id+10), archived)
	}
	event := func(task int, at int, payload string) {
		t.Helper()
		exec(`INSERT INTO events (ts, type, task_id, project_id, payload_json)
			VALUES (?, 'task.state_changed', ?, 1, ?)`, backfillAt(at), task, payload)
	}
	task(1, "archived", backfillAt(30))
	event(1, 20, `{"from":"running","to":"done"}`)
	event(1, 21, `{"from":"done","to":"archived"}`)
	event(1, 22, `{"from":"aborted","to":"archived"}`) // newest wins
	task(2, "archived", backfillAt(30))                // no event survives
	task(3, "archived", backfillAt(30))
	event(3, 23, `{"from":"running","to":"archived"}`) // not a settled state
	task(4, "done", nil)
	event(4, 24, `{"from":"done","to":"archived"}`) // stray: the row is not archived
}

// TestMigration0042BackfillsArchivedFrom: opening a 0041 database records
// each archived task's earlier state from its newest archiving event, and
// `done` when no usable event survives (task 134.3).
func TestMigration0042BackfillsArchivedFrom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	seedArchivedFromFixture(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 41 -> 42): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for id, want := range map[int64]TaskState{1: TaskAborted, 2: TaskDone, 3: TaskDone, 4: ""} {
		got, err := s.GetTask(t.Context(), id)
		if err != nil {
			t.Fatalf("GetTask(%d): %v", id, err)
		}
		if got.ArchivedFrom != want {
			t.Errorf("task %d ArchivedFrom = %q, want %q", id, got.ArchivedFrom, want)
		}
	}
}

// TestArchiveRecordsTheStateItLeft: the transition to archived writes the
// settled state it came from, and no other task carries one.
func TestArchiveRecordsTheStateItLeft(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	ctx := t.Context()

	for _, from := range []TaskState{TaskDone, TaskAborted} {
		task := newTask(p.ID, "from-"+string(from), from)
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		before, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if before.ArchivedFrom != "" {
			t.Errorf("%s task ArchivedFrom = %q before archive, want empty", from, before.ArchivedFrom)
		}
		updated, _, err := s.TransitionTask(ctx, task.ID, from, TaskArchived, TaskChange{})
		if err != nil {
			t.Fatalf("archive %s task: %v", from, err)
		}
		if updated.ArchivedFrom != from {
			t.Errorf("returned task ArchivedFrom = %q, want %q", updated.ArchivedFrom, from)
		}
		got, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.ArchivedFrom != from {
			t.Errorf("stored ArchivedFrom = %q, want %q", got.ArchivedFrom, from)
		}
	}
}

// TestRestoreFromABackupBeforeArchivedFrom: a backup taken at schema 0041 is
// migrated when it is opened, so the task it restores carries the state its
// own events recorded — the backup path needs no code of its own.
func TestRestoreFromABackupBeforeArchivedFrom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.db")
	seedArchivedFromFixture(t, path)
	src, err := Open(path)
	if err != nil {
		t.Fatalf("Open backup: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	exp, err := src.ExportTask(t.Context(), 1)
	if err != nil {
		t.Fatalf("ExportTask: %v", err)
	}

	live := openTest(t)
	testProject(t, live, "p1")
	if _, err := live.ImportTask(t.Context(), exp, ImportOptions{}); err != nil {
		t.Fatalf("ImportTask: %v", err)
	}
	got, err := live.GetTask(t.Context(), 1)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ArchivedFrom != TaskAborted {
		t.Errorf("restored ArchivedFrom = %q, want %q", got.ArchivedFrom, TaskAborted)
	}
}
