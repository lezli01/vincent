package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// tableColumns reads a table's columns and declared types from the schema
// itself, so the round-trip below covers a column a later migration adds
// without anyone editing this file.
func tableColumns(t *testing.T, s *Store, table string) map[string]string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), `SELECT name, type FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		out[name] = typ
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	return out
}

// fillEveryColumn gives every column of one row a distinctive non-NULL value,
// except the ones named in keep: ids, foreign keys and state, which have to
// stay meaningful. A column left NULL would round-trip as NULL whether or not
// import copied it.
func fillEveryColumn(t *testing.T, s *Store, table string, id int64, keep ...string) {
	t.Helper()
	for col, typ := range tableColumns(t, s, table) {
		if slices.Contains(keep, col) {
			continue
		}
		var v any
		switch typ {
		case "INTEGER":
			v = int64(len(col)) + 700
		case "REAL":
			v = float64(len(col)) + 0.25
		default:
			v = "value-of-" + col
		}
		q := fmt.Sprintf(`UPDATE %s SET "%s" = ? WHERE id = ?`, table, col)
		if _, err := s.db.ExecContext(t.Context(), q, v, id); err != nil {
			t.Fatalf("fill %s.%s: %v", table, col, err)
		}
	}
}

// importSource is a store standing in for a backup's staged database: one
// project, a creator task (id 1), and an archived task (id 2) with two step
// runs whose every column is filled.
func importSource(t *testing.T) (*Store, *TaskExport) {
	t.Helper()
	src := openTest(t)
	p := testProject(t, src, "repo")
	creator := newArchivedTask(t, src, p.ID, "creator")
	task := newArchivedTask(t, src, p.ID, "restore me")
	for i := range 2 {
		r := &StepRun{TaskID: task.ID, StepIndex: i, StepID: "s", StepType: "agent", Attempt: 1, State: StepSucceeded}
		if err := src.CreateStepRun(t.Context(), r); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
		fillEveryColumn(t, src, "step_runs", r.ID, "id", "task_id")
	}
	fillEveryColumn(t, src, "tasks", task.ID,
		"id", "project_id", "state", "parent_task_id", "created_by_task_id")
	if _, err := src.db.ExecContext(t.Context(),
		`UPDATE tasks SET created_by_task_id = ? WHERE id = ?`, creator.ID, task.ID); err != nil {
		t.Fatalf("set creator: %v", err)
	}
	exp, err := src.ExportTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ExportTask: %v", err)
	}
	return src, exp
}

// snapshot is the live database's bytes, taken through VACUUM INTO so the
// comparison sees committed content rather than whatever the WAL holds.
func snapshot(t *testing.T, s *Store) []byte {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "snap.db")
	if err := s.BackupTo(t.Context(), dst); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	return b
}

func TestImportTaskRoundTripsEveryColumn(t *testing.T) {
	src, exp := importSource(t)
	live := openTest(t)
	p := testProject(t, live, "repo")
	// The creator's id is live here, so the provenance link survives.
	newArchivedTask(t, live, p.ID, "some other task")
	// And the id being imported is free: the live sequence has only reached 1.
	var events []*Event
	live.SetEventHook(func(e *Event) {
		// Post-commit: the row the event announces is already readable.
		// Read raw: the filler values are not JSON, so GetTask cannot parse them.
		var n int
		if err := live.db.QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM tasks WHERE id = 2`).Scan(&n); err != nil || n != 1 {
			t.Errorf("event %s published before its row was readable: %d, %v", e.Type, n, err)
		}
		events = append(events, e)
	})

	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	res, err := live.ImportTask(t.Context(), exp, ImportOptions{Now: now})
	if err != nil {
		t.Fatalf("ImportTask: %v", err)
	}
	if res.TaskID != 2 || res.ProjectID != p.ID || res.StepRuns != 2 || res.StepRunsRenumbered {
		t.Errorf("result = %+v, want task 2 in project %d with 2 kept step runs", res, p.ID)
	}

	for _, tc := range []struct {
		table, where string
		overridden   map[string]any
	}{
		{"tasks", "id = 2", map[string]any{
			"archived_at": formatTime(now), "worktree_path": nil,
		}},
		{"step_runs", "task_id = 2", nil},
	} {
		want, err := src.queryRows(t.Context(), `SELECT * FROM `+tc.table+` WHERE `+tc.where+` ORDER BY id`)
		if err != nil {
			t.Fatalf("read source %s: %v", tc.table, err)
		}
		got, err := live.queryRows(t.Context(), `SELECT * FROM `+tc.table+` WHERE `+tc.where+` ORDER BY id`)
		if err != nil {
			t.Fatalf("read live %s: %v", tc.table, err)
		}
		if len(got) != len(want) || len(got) == 0 {
			t.Fatalf("%s: got %d rows, want %d", tc.table, len(got), len(want))
		}
		cols := tableColumns(t, live, tc.table)
		for i := range got {
			if len(got[i].Columns) != len(cols) {
				t.Fatalf("%s row has %d columns, the schema %d", tc.table, len(got[i].Columns), len(cols))
			}
			for col := range cols {
				w := want[i].Get(col)
				if o, ok := tc.overridden[col]; ok {
					w = o
				} else if w == nil && col != "parent_task_id" {
					t.Errorf("%s.%s is NULL in the source; the test must fill it", tc.table, col)
				}
				if g := got[i].Get(col); !reflect.DeepEqual(g, w) {
					t.Errorf("%s.%s = %#v, want %#v", tc.table, col, g, w)
				}
			}
		}
	}

	if len(events) != 1 || events[0].Type != EventTaskRestored ||
		events[0].TaskID == nil || *events[0].TaskID != 2 || events[0].ProjectID == nil {
		t.Fatalf("events = %+v, want one task.restored for task 2", events)
	}
}

func TestImportTaskNullsAnAbsentCreatorAndRenumbersTakenStepRuns(t *testing.T) {
	_, exp := importSource(t)
	live := openTest(t)
	p := testProject(t, live, "repo")
	// Step run id 1 is live and task id 1 is not: every backed-up step run
	// must be renumbered and the creator link nulled. Nothing in the store
	// picks an id, so a real task and its attempt are moved to task id 9.
	holder := newArchivedTask(t, live, p.ID, "holder")
	held := &StepRun{TaskID: holder.ID, StepIndex: 0, StepID: "s", StepType: "agent", Attempt: 1, State: StepSucceeded}
	if err := live.CreateStepRun(t.Context(), held); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	for _, q := range []string{
		`PRAGMA foreign_keys = OFF`,
		`UPDATE tasks SET id = 9 WHERE id = 1`,
		`UPDATE step_runs SET task_id = 9 WHERE task_id = 1`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := live.db.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if held.ID != 1 {
		t.Fatalf("the held step run is %d, want 1", held.ID)
	}

	res, err := live.ImportTask(t.Context(), exp, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportTask: %v", err)
	}
	if !res.StepRunsRenumbered {
		t.Error("StepRunsRenumbered = false with id 1 taken")
	}
	got, err := live.queryRows(t.Context(), `SELECT * FROM step_runs WHERE task_id = 2 ORDER BY id`)
	if err != nil {
		t.Fatalf("read step runs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d step runs, want 2", len(got))
	}
	for i, r := range got {
		id, _ := r.Int64("id")
		if old, _ := exp.StepRuns[i].Int64("id"); id == old {
			t.Errorf("step run %d kept id %d; all must be renumbered", i, id)
		}
		// The original order survives the renumber.
		if r.Get("step_index") != exp.StepRuns[i].Get("step_index") {
			t.Errorf("step run %d is step_index %v, want %v", i, r.Get("step_index"), exp.StepRuns[i].Get("step_index"))
		}
	}
	task, err := live.queryRows(t.Context(), `SELECT created_by_task_id FROM tasks WHERE id = 2`)
	if err != nil || len(task) != 1 {
		t.Fatalf("read task: %v", err)
	}
	if v := task[0].Get("created_by_task_id"); v != nil {
		t.Errorf("created_by_task_id = %v, want NULL: task 1 is not live", v)
	}
}

func TestImportTaskRefusalsChangeNothing(t *testing.T) {
	type setup func(t *testing.T, live *Store, exp *TaskExport) ImportOptions
	for _, tc := range []struct {
		name   string
		reason string
		setup  setup
		// notFound: the refusal is ErrNotFound rather than *ImportRefusedError.
		notFound bool
	}{
		{"not archived", ImportRefusedNotArchived, func(_ *testing.T, _ *Store, exp *TaskExport) ImportOptions {
			exp.Task.Set("state", string(TaskDone))
			return ImportOptions{}
		}, false},
		{"task exists", ImportRefusedTaskExists, func(t *testing.T, live *Store, _ *TaskExport) ImportOptions {
			p := testProject(t, live, "other")
			newArchivedTask(t, live, p.ID, "one")
			newArchivedTask(t, live, p.ID, "two")
			return ImportOptions{ProjectID: p.ID}
		}, false},
		{"project with another name", ImportRefusedProjectMismatch, func(t *testing.T, live *Store, _ *TaskExport) ImportOptions {
			testProject(t, live, "not-repo")
			return ImportOptions{}
		}, false},
		{"no project at all", ImportRefusedProjectMismatch, func(_ *testing.T, _ *Store, _ *TaskExport) ImportOptions {
			return ImportOptions{}
		}, false},
		{"named project missing", "", func(t *testing.T, live *Store, _ *TaskExport) ImportOptions {
			testProject(t, live, "repo")
			return ImportOptions{ProjectID: 99}
		}, true},
		{"lane without its parent", ImportRefusedParentMissing, func(t *testing.T, live *Store, exp *TaskExport) ImportOptions {
			testProject(t, live, "repo")
			exp.Task.Set("parent_task_id", int64(1))
			return ImportOptions{}
		}, false},
		{"placement refused", ImportRefusedTranscriptsPresent, func(t *testing.T, live *Store, _ *TaskExport) ImportOptions {
			testProject(t, live, "repo")
			return ImportOptions{Place: func() (func(), error) {
				return nil, &ImportRefusedError{Reason: ImportRefusedTranscriptsPresent, Message: "stray"}
			}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, exp := importSource(t)
			live := openTest(t)
			opts := tc.setup(t, live, exp)
			var published int
			live.SetEventHook(func(*Event) { published++ })
			before := snapshot(t, live)

			_, err := live.ImportTask(t.Context(), exp, opts)
			if tc.notFound {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("ImportTask = %v, want ErrNotFound", err)
				}
			} else {
				var refused *ImportRefusedError
				if !errors.As(err, &refused) || refused.Reason != tc.reason {
					t.Fatalf("ImportTask = %v, want a %s refusal", err, tc.reason)
				}
				if tc.reason == ImportRefusedNotArchived && refused.State != string(TaskDone) {
					t.Errorf("State = %q, want the backed-up state", refused.State)
				}
			}
			if !bytes.Equal(snapshot(t, live), before) {
				t.Error("a refused import changed the database")
			}
			if published != 0 {
				t.Errorf("a refused import published %d event(s)", published)
			}
		})
	}
}

// TestImportTaskUndoesPlacementWhenTheInsertFails: the transcript directory is
// moved in before the rows, so a failure after that point has to take it
// back out.
func TestImportTaskUndoesPlacementWhenTheInsertFails(t *testing.T) {
	_, exp := importSource(t)
	live := openTest(t)
	testProject(t, live, "repo")
	if _, err := live.db.ExecContext(t.Context(), `CREATE TRIGGER no_runs BEFORE INSERT ON step_runs
		BEGIN SELECT RAISE(ABORT, 'refused by test'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	var undone, published bool
	live.SetEventHook(func(*Event) { published = true })
	_, err := live.ImportTask(t.Context(), exp, ImportOptions{Place: func() (func(), error) {
		return func() { undone = true }, nil
	}})
	if err == nil {
		t.Fatal("ImportTask succeeded through an aborting trigger")
	}
	if !undone {
		t.Error("the placement was not undone")
	}
	if published {
		t.Error("a failed import published an event")
	}
	if _, err := live.GetTask(t.Context(), 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTask after a failed import = %v, want ErrNotFound", err)
	}
}

func TestExportTaskNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.ExportTask(t.Context(), 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ExportTask(42) = %v, want ErrNotFound", err)
	}
}
