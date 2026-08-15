package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/taskstate"
)

const taskColumns = `id, project_id, title, description, fields_json, workflow_name, workflow_snapshot,
	base_branch, branch_name, worktree_path, priority, agent_override, model_override, effort_override,
	state, current_step, block_reason, pause_requested, retry_cursor_at, pending_override_json,
	pending_input_json, admit_not_before, queued_reason,
	created_at, updated_at, started_at, finished_at, archived_at`

// slotStates is the set of states that occupy a concurrency slot (spec §11),
// rendered as SQL placeholders. It is derived from taskstate rather than
// written out, so a new slot-holding state cannot be added to the FSM without
// the caps noticing.
var slotStates, slotPlaceholders = func() ([]any, string) {
	var args []any
	for _, s := range taskstate.All {
		if taskstate.HoldsSlot(s) {
			args = append(args, string(s))
		}
	}
	return args, "(?" + strings.Repeat(", ?", len(args)-1) + ")"
}()

// BranchClaimedError reports that another unarchived task in the same project
// already holds the branch name a new task resolved to (task 001).
//
// It is a distinct type because the API turns it into a 400 while every other
// insert failure is a 500: the name is the caller's input, and "task 41 already
// has that branch" is something they can act on.
type BranchClaimedError struct {
	Branch string
	TaskID int64
}

func (e *BranchClaimedError) Error() string {
	return fmt.Sprintf("branch %q is already claimed by task %d", e.Branch, e.TaskID)
}

// CreateTask inserts t and assigns its ID and timestamps, writing the
// durable task.created event in the same transaction (spec §13.3). A
// caller-set CreatedAt is kept (tests rely on this); zero means now.
//
// resolveBranch fills in a branch name that could not be known before the insert
// because it depends on the task id. When it is nil, t.BranchName is used as
// given. Either way the name is written inside the same transaction as the row
// and the task.created event, so no committed task ever carries an empty
// branch_name and there is no window for a crash to land in (task 001). It
// replaces SetTaskBranchName, whose second write was that window, and the
// recompute in taskrun that tried to paper over it — a recompute that silently
// produced the *default* name and so would have discarded a user's chosen one.
//
// Whichever path supplies the name, it is checked against other unarchived tasks
// in the same project before the commit; a clash returns *BranchClaimedError and
// rolls back. Archived tasks are excluded on purpose: they keep their
// branch_name, so counting them would forbid reusing a name even after the user
// deleted the branch by hand, which is the one case where reuse is legitimate.
func (s *Store) CreateTask(ctx context.Context, t *Task, resolveBranch func(id int64) (string, error)) error {
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	fields, err := marshalFields(t.Fields)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	var ev *Event
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tasks (project_id, title, description, fields_json, workflow_name, workflow_snapshot,
				base_branch, branch_name, worktree_path, priority, agent_override, model_override, effort_override,
				state, current_step, block_reason,
				created_at, updated_at, started_at, finished_at, archived_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ProjectID, t.Title, t.Description, fields, t.WorkflowName, t.WorkflowSnapshot,
			t.BaseBranch, t.BranchName, nullString(t.WorktreePath), t.Priority,
			nullString(t.AgentOverride), nullString(t.ModelOverride), nullString(t.EffortOverride),
			string(t.State), t.CurrentStep, nullString(t.BlockReason),
			formatTime(t.CreatedAt), formatTime(t.UpdatedAt),
			formatTimePtr(t.StartedAt), formatTimePtr(t.FinishedAt), formatTimePtr(t.ArchivedAt))
		if err != nil {
			return fmt.Errorf("insert task: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("insert task: %w", err)
		}
		t.ID = id
		// The id exists now, so a name that needed it can be produced and written
		// before this transaction commits.
		if resolveBranch != nil {
			branch, err := resolveBranch(id)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE tasks SET branch_name = ? WHERE id = ?`, branch, id); err != nil {
				return fmt.Errorf("assign branch name: %w", err)
			}
			t.BranchName = branch
		}
		if err := claimBranchTx(ctx, tx, t.ProjectID, t.BranchName, id); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"state": string(t.State), "title": t.Title, "workflow": t.WorkflowName,
		})
		if err != nil {
			return fmt.Errorf("marshal task.created event: %w", err)
		}
		// Copied, not aliased: the event goes to the broker, and t belongs to the
		// caller.
		taskID, projectID := t.ID, t.ProjectID
		ev = &Event{
			TS: now, Type: EventTaskCreated,
			TaskID: &taskID, ProjectID: &projectID, Payload: payload,
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// claimBranchTx fails when another unarchived task in the project already holds
// branch. It runs inside the caller's transaction because it is the half of the
// collision check that can: it is a plain query, whereas the git-side checks
// shell out and must never hold SQLite's single write lock across a subprocess
// that has a 30-second timeout.
//
// It runs for *every* path, including id-bearing templates, which are not
// self-protecting: `feat/{{.ID}}` on task 5 and a literal `feat/5` typed on
// task 9 resolve to the same name.
func claimBranchTx(ctx context.Context, tx *sql.Tx, projectID int64, branch string, selfID int64) error {
	if branch == "" {
		return fmt.Errorf("insert task: branch name is empty")
	}
	var other int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM tasks
		WHERE project_id = ? AND branch_name = ? AND id <> ? AND archived_at IS NULL
		LIMIT 1`, projectID, branch, selfID).Scan(&other)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check branch claim: %w", err)
	default:
		return &BranchClaimedError{Branch: branch, TaskID: other}
	}
}

// GetTask returns the task with the given id, or ErrNotFound.
func (s *Store) GetTask(ctx context.Context, id int64) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get task %d: %w", id, err)
	}
	return t, nil
}

// ArchivedFilter selects how ListTasks treats archived tasks. The zero value
// excludes them: a board that listed every archive ever would grow without
// bound, and the callers that want history ask for it (§13.2).
type ArchivedFilter int

const (
	// ArchivedExclude omits archived tasks. Default.
	ArchivedExclude ArchivedFilter = iota
	// ArchivedOnly returns archived tasks and nothing else.
	ArchivedOnly
	// ArchivedAll applies no archived-state filter at all.
	ArchivedAll
)

// TaskFilter narrows ListTasks. Zero values mean "no filter", except
// Archived, whose zero value excludes archived tasks.
type TaskFilter struct {
	ProjectID int64     // 0 = all projects
	State     TaskState // "" = all states
	Archived  ArchivedFilter
	Limit     int // 0 = unlimited
	Offset    int
}

// ListTasks returns tasks matching f, newest first.
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	q := `SELECT ` + taskColumns + ` FROM tasks`
	var where []string
	var args []any
	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, string(f.State))
	}
	// An explicit State always wins: asking for state=archived and getting
	// nothing back because the default excludes archives would be absurd.
	if f.State == "" {
		switch f.Archived {
		case ArchivedExclude:
			where = append(where, "state != ?")
			args = append(args, string(TaskArchived))
		case ArchivedOnly:
			where = append(where, "state = ?")
			args = append(args, string(TaskArchived))
		case ArchivedAll:
		}
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC"
	if f.Limit > 0 || f.Offset > 0 {
		limit := f.Limit
		if limit == 0 {
			limit = -1 // SQLite: negative LIMIT means unlimited
		}
		q += " LIMIT ? OFFSET ?"
		args = append(args, limit, f.Offset)
	}
	return s.queryTasks(ctx, q, args...)
}

// SetTaskBranchName renames a task's branch, and exists for exactly one caller:
// recovering a task blocked with `branch_exists` through
// `POST /v1/tasks/{id}/retry { branch_override }` (task 001). It is no longer
// part of task creation — CreateTask writes the name inside its own transaction,
// which is what closed the window this function used to open.
//
// It writes nothing but the name on purpose: the scheduler may admit the task
// between this write and the next, and a full-row update would overwrite the
// state its CAS just set (phase 2 decision: only TransitionTask writes state).
// The claim check runs in the same transaction, so a rename cannot take a name
// another live task already holds.
func (s *Store) SetTaskBranchName(ctx context.Context, id, projectID int64, branch string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := claimBranchTx(ctx, tx, projectID, branch, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks SET branch_name = ?, updated_at = ? WHERE id = ?`,
			branch, formatTime(time.Now()), id)
		if err != nil {
			return fmt.Errorf("set task %d branch name: %w", id, err)
		}
		return oneRowAffected(res, fmt.Sprintf("task %d", id))
	})
}

// UpdateTask writes every mutable field of t (matched by ID) and bumps
// UpdatedAt — including state, so it must never run against a task an actor
// or the scheduler may be touching; live code paths go through
// TransitionTask and the targeted setters instead. Test fixtures are its
// remaining callers. Returns ErrNotFound when the row does not exist.
func (s *Store) UpdateTask(ctx context.Context, t *Task) error {
	t.UpdatedAt = time.Now()
	fields, err := marshalFields(t.Fields)
	if err != nil {
		return fmt.Errorf("update task %d: %w", t.ID, err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET title = ?, description = ?, fields_json = ?, workflow_name = ?,
			workflow_snapshot = ?, base_branch = ?, branch_name = ?, worktree_path = ?,
			priority = ?, agent_override = ?, model_override = ?, effort_override = ?,
			state = ?, current_step = ?, block_reason = ?,
			admit_not_before = ?, queued_reason = ?,
			updated_at = ?, started_at = ?, finished_at = ?, archived_at = ?
		WHERE id = ?`,
		t.Title, t.Description, fields, t.WorkflowName,
		t.WorkflowSnapshot, t.BaseBranch, t.BranchName, nullString(t.WorktreePath),
		t.Priority, nullString(t.AgentOverride), nullString(t.ModelOverride), nullString(t.EffortOverride),
		string(t.State), t.CurrentStep, nullString(t.BlockReason),
		formatTimePtr(t.AdmitNotBefore), nullString(t.QueuedReason),
		formatTime(t.UpdatedAt), formatTimePtr(t.StartedAt), formatTimePtr(t.FinishedAt), formatTimePtr(t.ArchivedAt),
		t.ID)
	if err != nil {
		return fmt.Errorf("update task %d: %w", t.ID, err)
	}
	return oneRowAffected(res, fmt.Sprintf("task %d", t.ID))
}

// ListQueuedInOrder returns queued tasks in scheduler admission order:
// priority DESC, then created_at ASC, with id as the final tiebreaker
// (spec §11).
func (s *Store) ListQueuedInOrder(ctx context.Context) ([]Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskColumns+` FROM tasks WHERE state = ?
		ORDER BY priority DESC, created_at ASC, id ASC`, string(TaskQueued))
}

// CountSlotHolders returns how many tasks occupy a concurrency slot — what
// the global cap counts (spec §11).
func (s *Store) CountSlotHolders(ctx context.Context) (int, error) {
	return s.countTasks(ctx,
		`SELECT COUNT(*) FROM tasks WHERE state IN `+slotPlaceholders, slotStates...)
}

// CountSlotHoldersByProject returns how many of the project's tasks occupy a
// concurrency slot — what the per-project cap counts (spec §11).
func (s *Store) CountSlotHoldersByProject(ctx context.Context, projectID int64) (int, error) {
	args := append(append([]any{}, slotStates...), projectID)
	return s.countTasks(ctx,
		`SELECT COUNT(*) FROM tasks WHERE state IN `+slotPlaceholders+` AND project_id = ?`, args...)
}

// ListAdmissible returns every queued task in admission order — priority
// DESC, created_at ASC, id ASC (spec §11) — each carrying its project's
// current slot count and cap.
//
// Ordering and both caps come from SQL, but the walk itself is the caller's:
// admitting a task changes the tallies, and a single statement cannot see
// its own in-flight admissions.
func (s *Store) ListAdmissible(ctx context.Context) ([]Candidate, error) {
	q := `SELECT ` + prefixed("t", taskColumns) + `,
			(SELECT COUNT(*) FROM tasks o
			  WHERE o.project_id = t.project_id AND o.state IN ` + slotPlaceholders + `),
			p.max_parallel_tasks
		FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.state = ?
		ORDER BY t.priority DESC, t.created_at ASC, t.id ASC`
	args := append(append([]any{}, slotStates...), string(TaskQueued))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list admissible: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Candidate
	for rows.Next() {
		var (
			c     Candidate
			limit sql.NullInt64
		)
		t, err := scanTask(scannerWithTail(rows, &c.ProjectSlots, &limit))
		if err != nil {
			return nil, fmt.Errorf("scan admissible: %w", err)
		}
		c.Task = *t
		if limit.Valid {
			n := int(limit.Int64)
			c.ProjectCap = &n
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list admissible: %w", err)
	}
	return out, nil
}

// prefixed qualifies each comma-separated column with a table alias.
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// scannerWithTail lets scanTask consume the leading task columns of a wider
// row while extra trailing columns land in tail.
func scannerWithTail(r rowScanner, tail ...any) rowScanner {
	return tailScanner{row: r, tail: tail}
}

type tailScanner struct {
	row  rowScanner
	tail []any
}

func (t tailScanner) Scan(dest ...any) error { return t.row.Scan(append(dest, t.tail...)...) }

// CountNonArchivedTasks returns how many of the project's tasks are not
// archived — the guard for project deletion (spec §13.2).
func (s *Store) CountNonArchivedTasks(ctx context.Context, projectID int64) (int, error) {
	return s.countTasks(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND state != ?`,
		projectID, string(TaskArchived))
}

// WorktreeClaim is one task's claim on a directory under the worktree root
// (task 005). An empty Path means the row claims nothing — the state a task
// is in before its first admission, and the state a crash between
// `git worktree add` and the claim write leaves behind.
type WorktreeClaim struct {
	TaskID int64
	Path   string
	State  TaskState
}

// ListWorktreeClaims returns every task's worktree claim, archived rows
// included. gc classifies a directory by *claim*, not by name, so this is the
// authoritative set: a directory nothing here names is an orphan, and a claim
// naming a directory that is gone is the reverse mismatch (§18).
//
// Archived rows are in deliberately. Their worktree is normally already gone
// and their claim already cleared, but an archive interrupted between the two
// leaves a live claim, and gc must not delete what it names.
func (s *Store) ListWorktreeClaims(ctx context.Context) ([]WorktreeClaim, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(worktree_path, ''), state FROM tasks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list worktree claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []WorktreeClaim
	for rows.Next() {
		var c WorktreeClaim
		if err := rows.Scan(&c.TaskID, &c.Path, (*string)(&c.State)); err != nil {
			return nil, fmt.Errorf("scan worktree claim: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate worktree claims: %w", err)
	}
	return out, nil
}

// ListTaskIDs returns every task id, archived rows included — what gc diffs
// the transcript root against (task 005). A transcript directory belongs to
// its task for as long as the row exists; §17 retention decides when an
// *archived* row's transcripts go, and only a row that no longer exists at
// all is gc's.
func (s *Store) ListTaskIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list task ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan task id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task ids: %w", err)
	}
	return ids, nil
}

// ArchivedTaskIDsBefore returns the ids of tasks archived before cutoff —
// the input to transcript pruning (§17 retention).
//
// It selects on `archived_at`, not on `state`, because retention is measured
// from when the task was archived rather than from when it finished: a task
// left running for a week and archived yesterday is a day old, not eight.
// Rows are never deleted, only their transcripts (§17: rows are small,
// history is valuable).
func (s *Store) ArchivedTaskIDsBefore(ctx context.Context, cutoff time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM tasks WHERE archived_at IS NOT NULL AND archived_at < ? ORDER BY id`,
		formatTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("list archived tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan archived task id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate archived tasks: %w", err)
	}
	return ids, nil
}

// RequestPause records a pause against a running task without changing its
// state: §6 lets the current step finish first, and the engine applies the
// transition at the step boundary. The write is conditional on the task
// still running, so a task that finished in the meantime reports a state
// conflict rather than acquiring a flag nothing will ever read.
func (s *Store) RequestPause(ctx context.Context, id int64) (*Task, error) {
	return s.conditionalTaskUpdate(ctx, id, TaskRunning,
		[]TaskState{TaskRunning},
		`UPDATE tasks SET pause_requested = 1, updated_at = ? WHERE id = ? AND state = ?`,
		formatTime(time.Now()), id, string(TaskRunning))
}

// SetTaskPriority reorders admission (spec §6: queued and paused only). It is
// not a transition — the state is unchanged — so the caller is responsible
// for the durable event and for waking the scheduler.
func (s *Store) SetTaskPriority(ctx context.Context, id int64, priority int) (*Task, error) {
	allowed := []TaskState{TaskQueued, TaskPaused}
	return s.conditionalTaskUpdate(ctx, id, TaskQueued, allowed,
		`UPDATE tasks SET priority = ?, updated_at = ? WHERE id = ? AND state IN (?, ?)`,
		priority, formatTime(time.Now()), id, string(TaskQueued), string(TaskPaused))
}

// TakePendingOverride reads and clears the edit+retry text a human left for
// the next attempt, in one transaction so an override is consumed exactly
// once. A task with none returns a zero Override.
func (s *Store) TakePendingOverride(ctx context.Context, id int64) (Override, error) {
	var ov Override
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT pending_override_json FROM tasks WHERE id = ?`, id).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task %d: %w", id, ErrNotFound)
			}
			return fmt.Errorf("read pending override: %w", err)
		}
		if !raw.Valid || raw.String == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(raw.String), &ov); err != nil {
			return fmt.Errorf("pending_override_json: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET pending_override_json = NULL WHERE id = ?`, id); err != nil {
			return fmt.Errorf("clear pending override: %w", err)
		}
		return nil
	})
	if err != nil {
		return Override{}, err
	}
	return ov, nil
}

// conditionalTaskUpdate runs an update guarded on the task's state and
// returns the refreshed row. Zero rows affected means the task moved, so the
// caller answers 409 with the state actually found (spec §13.1); want is the
// state named in that error.
func (s *Store) conditionalTaskUpdate(
	ctx context.Context, id int64, want TaskState, allowed []TaskState, q string, args ...any,
) (*Task, error) {
	var out *Task
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
		t, err := scanTask(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get task %d: %w", id, err)
		}
		if !containsState(allowed, t.State) {
			return &StateConflictError{TaskID: id, Want: want, Got: t.State}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("update task %d: %w", id, err)
		}
		row = tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
		if out, err = scanTask(row); err != nil {
			return fmt.Errorf("reload task %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func containsState(states []TaskState, s TaskState) bool {
	for _, v := range states {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Store) countTasks(ctx context.Context, q string, args ...any) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count tasks: %w", err)
	}
	return n, nil
}

func (s *Store) queryTasks(ctx context.Context, q string, args ...any) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	return out, nil
}

func scanTask(r rowScanner) (*Task, error) {
	var (
		t                           Task
		fields                      string
		worktree, blockReason       sql.NullString
		agentOv, modelOv, effortOv  sql.NullString
		retryCursor, pendingOv      sql.NullString
		pendingInput                sql.NullString
		admitNotBefore, queuedWhy   sql.NullString
		created, updated            string
		started, finished, archived sql.NullString
	)
	if err := r.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &fields, &t.WorkflowName,
		&t.WorkflowSnapshot, &t.BaseBranch, &t.BranchName, &worktree, &t.Priority,
		&agentOv, &modelOv, &effortOv,
		(*string)(&t.State), &t.CurrentStep, &blockReason,
		&t.PauseRequested, &retryCursor, &pendingOv,
		&pendingInput, &admitNotBefore, &queuedWhy,
		&created, &updated, &started, &finished, &archived); err != nil {
		return nil, err
	}
	t.WorktreePath = worktree.String
	t.BlockReason = blockReason.String
	t.PendingInputJSON = pendingInput.String
	t.QueuedReason = queuedWhy.String
	t.AgentOverride = agentOv.String
	t.ModelOverride = modelOv.String
	t.EffortOverride = effortOv.String
	if pendingOv.Valid && pendingOv.String != "" {
		var ov Override
		if err := json.Unmarshal([]byte(pendingOv.String), &ov); err != nil {
			return nil, fmt.Errorf("pending_override_json: %w", err)
		}
		t.PendingOverride = &ov
	}
	if err := json.Unmarshal([]byte(fields), &t.Fields); err != nil {
		return nil, fmt.Errorf("fields_json: %w", err)
	}
	if len(t.Fields) == 0 {
		t.Fields = nil
	}
	var err error
	if t.RetryCursorAt, err = parseTimePtr(retryCursor); err != nil {
		return nil, err
	}
	if t.AdmitNotBefore, err = parseTimePtr(admitNotBefore); err != nil {
		return nil, err
	}
	if t.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	if t.StartedAt, err = parseTimePtr(started); err != nil {
		return nil, err
	}
	if t.FinishedAt, err = parseTimePtr(finished); err != nil {
		return nil, err
	}
	if t.ArchivedAt, err = parseTimePtr(archived); err != nil {
		return nil, err
	}
	return &t, nil
}

// marshalFields serializes the free-form key/value fields; nil or empty maps
// become the schema default "{}".
func marshalFields(fields map[string]string) (string, error) {
	if len(fields) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("fields: %w", err)
	}
	return string(b), nil
}
