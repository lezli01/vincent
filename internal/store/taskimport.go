package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// EventTaskRestored announces a task imported from a backup (§13.3, task 117).
// It is task.deleted's mirror and, like it, not a §6 action: a restore enters
// no state — the row arrives `archived`, the state it was deleted in — so
// nothing that reacts to state entry (notify, the scheduler) reacts to it.
//
// Unlike task.deleted it carries a task_id: the row exists by the time the
// event commits, so the foreign key holds and the per-task stream sees it.
const EventTaskRestored = "task.restored"

// Reasons an import is refused (§13.2, task 117). One snake_case vocabulary
// with the delete refusals: `not_archived` means the same thing in both.
const (
	// ImportRefusedNotArchived: the task was not archived in the backup. A
	// task live at backup time carries `running` step runs with a pid and an
	// identity, which §12.4 recovery would treat as orphans (decision 4).
	ImportRefusedNotArchived = DeleteRefusedNotArchived
	// ImportRefusedTaskExists: a live task already holds the id (decision 2).
	ImportRefusedTaskExists = "task_exists"
	// ImportRefusedProjectMismatch: no live project has both the backed-up id
	// and the backed-up name, and no project was named (decision 7).
	ImportRefusedProjectMismatch = "project_mismatch"
	// ImportRefusedParentMissing: a fan-out lane whose parent is not live.
	// `parent_task_id` has no ON DELETE clause, so without the guard the
	// insert is a driver error rather than a refusal (decision 8).
	ImportRefusedParentMissing = "parent_missing"
	// ImportRefusedTranscriptsPresent: `{data_dir}/transcripts/{id}/` already
	// exists live. It is a stray, and §18 deletes and merges nothing.
	ImportRefusedTranscriptsPresent = "transcripts_present"
)

// ImportRefusedError is an import refused by one of the rules above. Message
// names what is in the way; State is the backed-up state, set only for
// ImportRefusedNotArchived.
type ImportRefusedError struct {
	ID      int64
	Reason  string
	Message string
	State   string
}

func (e *ImportRefusedError) Error() string { return e.Message }

// Row is one table row exactly as stored: its columns in table order beside
// their driver values. Import copies rows this way rather than through Task
// and StepRun because those types are views — they carry what the API shows,
// not every column — and a column added by a later migration must travel
// with the row without anyone remembering to add it here.
type Row struct {
	Columns []string
	Values  []any
}

// Get returns col's value, or nil when the row has no such column.
func (r Row) Get(col string) any {
	if i := slices.Index(r.Columns, col); i >= 0 {
		return r.Values[i]
	}
	return nil
}

// Set replaces col's value. It reports false when the row has no such column.
func (r Row) Set(col string, v any) bool {
	i := slices.Index(r.Columns, col)
	if i < 0 {
		return false
	}
	r.Values[i] = v
	return true
}

// Int64 returns col as an integer; ok is false for NULL or a missing column.
func (r Row) Int64(col string) (int64, bool) {
	v, ok := r.Get(col).(int64)
	return v, ok
}

// String returns col as text, "" for NULL or a missing column.
func (r Row) String(col string) string {
	switch v := r.Get(col).(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

func (r Row) clone() Row {
	return Row{Columns: slices.Clone(r.Columns), Values: slices.Clone(r.Values)}
}

func (r Row) without(col string) Row {
	i := slices.Index(r.Columns, col)
	if i < 0 {
		return r
	}
	return Row{
		Columns: slices.Delete(slices.Clone(r.Columns), i, i+1),
		Values:  slices.Delete(slices.Clone(r.Values), i, i+1),
	}
}

// TaskExport is one task as a backup holds it: the row, its step runs in id
// order, and the name of the project it belonged to.
type TaskExport struct {
	Task        Row
	StepRuns    []Row
	ProjectName string
}

// ExportTask reads task id's rows for an import (task 117). It is called on a
// store opened over the *staged copy* of a backup's database — never the live
// one — so Open has already migrated it to this binary's schema and the column
// sets match the live tables. Returns ErrNotFound when the task is not there.
func (s *Store) ExportTask(ctx context.Context, id int64) (*TaskExport, error) {
	tasks, err := s.queryRows(ctx, `SELECT * FROM tasks WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("export task %d: %w", id, err)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	runs, err := s.queryRows(ctx, `SELECT * FROM step_runs WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("export task %d step runs: %w", id, err)
	}
	exp := &TaskExport{Task: tasks[0], StepRuns: runs}
	if pid, ok := exp.Task.Int64("project_id"); ok {
		err := s.db.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, pid).Scan(&exp.ProjectName)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("export task %d project: %w", id, err)
		}
	}
	return exp, nil
}

// ImportOptions shapes one ImportTask call.
type ImportOptions struct {
	// ProjectID re-homes the task into this live project. Zero keeps the
	// backed-up project, which must then be live under the same id *and*
	// name: an id alone could name somebody else's project entirely.
	ProjectID int64
	// Place runs inside the transaction once every database refusal has
	// passed and before any row is written — it is where the caller moves
	// the task's transcripts into place. A non-nil error (an
	// *ImportRefusedError included) aborts the import. The undo it returns
	// runs if the import then fails to commit; it is safe to remove what
	// Place created, because this import is what created it.
	Place func() (undo func(), err error)
	// Now stamps archived_at; zero means time.Now. Tests pin it.
	Now time.Time
}

// ImportResult is what ImportTask wrote.
type ImportResult struct {
	TaskID             int64
	ProjectID          int64
	Title              string
	StepRuns           int
	StepRunsRenumbered bool
	ArchivedAt         time.Time
}

// ImportTask inserts an exported task and its step runs into this store in one
// transaction, appending one durable task.restored event (§13.2, §14, task
// 117). It is the undo for DeleteTaskCascade.
//
// The task keeps its id, and a live task holding it is a refusal (decision
// 2): AUTOINCREMENT never reissues a deleted id, so undoing a delete always
// gets it back. Step run ids are all kept when all are free and all
// renumbered, in their original order, when any is taken (decision 3) —
// nothing references `step_runs.id`, and transcript files are named by
// step index and attempt, so a renumber rewrites nothing else.
//
// Every other column is copied as it is, with four exceptions: `archived_at`
// is stamped now, so §17 retention restarts rather than pruning what was just
// restored (decision 5); `worktree_path` is NULL, since backups carry no
// worktrees; `project_id` follows opts.ProjectID; and `created_by_task_id` is
// NULL when that task is not live — its own ON DELETE SET NULL outcome.
//
// `events` rows are not copied (decision 6). A delete keeps them, and their id
// is the SSE cursor, so copies could only arrive as years-old history replayed
// under new ids.
//
// Returns *ImportRefusedError for each refusal, and ErrNotFound when
// opts.ProjectID names no project. Nothing is written in either case.
func (s *Store) ImportTask(ctx context.Context, exp *TaskExport, opts ImportOptions) (res *ImportResult, err error) {
	task := exp.Task.clone()
	id, ok := task.Int64("id")
	if !ok {
		return nil, errors.New("import task: the exported row has no id")
	}
	title := task.String("title")
	if state := task.String("state"); TaskState(state) != TaskArchived {
		return nil, &ImportRefusedError{
			ID: id, Reason: ImportRefusedNotArchived, State: state,
			Message: fmt.Sprintf("task %d was %s in the backup; only an archived task can be imported", id, state),
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("import task %d: %w", id, err)
	}
	var undo func()
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			if undo != nil {
				undo()
			}
		}
	}()

	var liveTitle string
	switch err = tx.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id = ?`, id).Scan(&liveTitle); {
	case err == nil:
		return nil, &ImportRefusedError{
			ID: id, Reason: ImportRefusedTaskExists,
			Message: fmt.Sprintf("task %d already exists here (%q); an import never replaces a live task", id, liveTitle),
		}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("import task %d: %w", id, err)
	}

	projectID := opts.ProjectID
	if projectID != 0 {
		var one int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, projectID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("project %d: %w", projectID, ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("import task %d: %w", id, err)
		}
	} else {
		projectID, _ = task.Int64("project_id")
		var liveName string
		err = tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, projectID).Scan(&liveName)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("import task %d: %w", id, err)
		}
		if errors.Is(err, sql.ErrNoRows) || liveName != exp.ProjectName {
			err = &ImportRefusedError{
				ID: id, Reason: ImportRefusedProjectMismatch,
				Message: fmt.Sprintf(
					"task %d belonged to project %d (%q), and no project here has that id and name; "+
						"name a project to import it into", id, projectID, exp.ProjectName),
			}
			return nil, err
		}
	}

	if parent, ok := task.Int64("parent_task_id"); ok {
		var live bool
		if live, err = taskExistsTx(ctx, tx, parent); err != nil {
			return nil, fmt.Errorf("import task %d: %w", id, err)
		}
		if !live {
			err = &ImportRefusedError{
				ID: id, Reason: ImportRefusedParentMissing,
				Message: fmt.Sprintf("task %d is a fan-out lane of task %d, which is not here; import task %d first",
					id, parent, parent),
			}
			return nil, err
		}
	}
	if creator, ok := task.Int64("created_by_task_id"); ok {
		var live bool
		if live, err = taskExistsTx(ctx, tx, creator); err != nil {
			return nil, fmt.Errorf("import task %d: %w", id, err)
		}
		if !live {
			task.Set("created_by_task_id", nil)
		}
	}

	renumber, err := stepRunIDsTaken(ctx, tx, exp.StepRuns)
	if err != nil {
		return nil, fmt.Errorf("import task %d: %w", id, err)
	}

	if opts.Place != nil {
		if undo, err = opts.Place(); err != nil {
			return nil, err
		}
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	task.Set("project_id", projectID)
	task.Set("archived_at", formatTime(now))
	task.Set("worktree_path", nil)
	if err = insertRow(ctx, tx, "tasks", task); err != nil {
		return nil, fmt.Errorf("import task %d: %w", id, err)
	}
	for _, run := range exp.StepRuns {
		run = run.clone()
		run.Set("task_id", id)
		if renumber {
			run = run.without("id")
		}
		if err = insertRow(ctx, tx, "step_runs", run); err != nil {
			return nil, fmt.Errorf("import task %d step runs: %w", id, err)
		}
	}

	payload, err := json.Marshal(map[string]any{"id": id, "title": title})
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventTaskRestored, err)
	}
	taskID, pid := id, projectID
	ev := &Event{Type: EventTaskRestored, TaskID: &taskID, ProjectID: &pid, Payload: payload}
	if err = appendEventTx(ctx, tx, ev); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("import task %d: %w", id, err)
	}
	s.notify(ev)
	return &ImportResult{
		TaskID: id, ProjectID: projectID, Title: title,
		StepRuns: len(exp.StepRuns), StepRunsRenumbered: renumber, ArchivedAt: now,
	}, nil
}

func taskExistsTx(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// stepRunIDsTaken reports whether any of runs' ids is already used live. One
// range query rather than an IN list: a long loop's attempts can outnumber
// SQLite's bind-variable limit.
func stepRunIDsTaken(ctx context.Context, tx *sql.Tx, runs []Row) (bool, error) {
	want := map[int64]bool{}
	var lo, hi int64
	for i, r := range runs {
		v, ok := r.Int64("id")
		if !ok {
			return false, errors.New("an exported step run has no id")
		}
		want[v] = true
		if i == 0 || v < lo {
			lo = v
		}
		if i == 0 || v > hi {
			hi = v
		}
	}
	if len(want) == 0 {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM step_runs WHERE id BETWEEN ? AND ?`, lo, hi)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return false, err
		}
		if want[v] {
			return true, nil
		}
	}
	return false, rows.Err()
}

// queryRows reads every column of every row q returns, as the driver hands
// them over: int64, float64, string, []byte or nil.
func (s *Store) queryRows(ctx context.Context, q string, args ...any) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, Row{Columns: slices.Clone(cols), Values: vals})
	}
	return out, rows.Err()
}

// insertRow inserts r into table with exactly r's columns. The table name is
// always a literal at the call site and the column names come from the schema
// itself (SELECT * over a store this binary migrated), and are quoted anyway;
// every value travels as a bind argument.
func insertRow(ctx context.Context, tx *sql.Tx, table string, r Row) error {
	cols := make([]string, len(r.Columns))
	for i, c := range r.Columns {
		cols[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES " + placeholders(len(cols)) //nolint:gosec // G202: see above
	_, err := tx.ExecContext(ctx, q, r.Values...)
	return err
}
