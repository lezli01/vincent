package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const taskColumns = `id, project_id, title, description, fields_json, workflow_name, workflow_snapshot,
	base_branch, branch_name, worktree_path, priority, agent_override, model_override, effort_override,
	state, current_step, block_reason,
	created_at, updated_at, started_at, finished_at, archived_at`

// CreateTask inserts t and assigns its ID and timestamps. A caller-set
// CreatedAt is kept (tests rely on this); zero means now.
func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	fields, err := marshalFields(t.Fields)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
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
	return nil
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

// TaskFilter narrows ListTasks. Zero values mean "no filter".
type TaskFilter struct {
	ProjectID int64     // 0 = all projects
	State     TaskState // "" = all states
	Limit     int       // 0 = unlimited
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

// UpdateTask writes every mutable field of t (matched by ID) and bumps
// UpdatedAt. Returns ErrNotFound when the row does not exist.
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
			updated_at = ?, started_at = ?, finished_at = ?, archived_at = ?
		WHERE id = ?`,
		t.Title, t.Description, fields, t.WorkflowName,
		t.WorkflowSnapshot, t.BaseBranch, t.BranchName, nullString(t.WorktreePath),
		t.Priority, nullString(t.AgentOverride), nullString(t.ModelOverride), nullString(t.EffortOverride),
		string(t.State), t.CurrentStep, nullString(t.BlockReason),
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

// CountRunning returns the number of tasks in state running — the quantity
// both concurrency caps count (spec §11).
func (s *Store) CountRunning(ctx context.Context) (int, error) {
	return s.countTasks(ctx, `SELECT COUNT(*) FROM tasks WHERE state = ?`, string(TaskRunning))
}

// CountRunningByProject returns the number of the project's tasks in state
// running (per-project cap, spec §11).
func (s *Store) CountRunningByProject(ctx context.Context, projectID int64) (int, error) {
	return s.countTasks(ctx, `SELECT COUNT(*) FROM tasks WHERE state = ? AND project_id = ?`,
		string(TaskRunning), projectID)
}

// CountNonArchivedTasks returns how many of the project's tasks are not
// archived — the guard for project deletion (spec §13.2).
func (s *Store) CountNonArchivedTasks(ctx context.Context, projectID int64) (int, error) {
	return s.countTasks(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND state != ?`,
		projectID, string(TaskArchived))
}

// SweepInterrupted marks every running step run `interrupted` and every
// running task `blocked` (block_reason "interrupted") — the M1 interim for
// daemon stop/crash leftovers until T2.8 lands real re-queue semantics
// (T1.7–T1.9 decision). Returns how many tasks were swept.
func (s *Store) SweepInterrupted(ctx context.Context) (int64, error) {
	now := formatTime(time.Now())
	if _, err := s.db.ExecContext(ctx, `UPDATE step_runs SET state = ?, finished_at = ?
		WHERE state = ?`, string(StepInterrupted), now, string(StepRunning)); err != nil {
		return 0, fmt.Errorf("sweep step runs: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET state = ?, block_reason = ?, updated_at = ?
		WHERE state = ?`, string(TaskBlocked), "interrupted", now, string(TaskRunning))
	if err != nil {
		return 0, fmt.Errorf("sweep tasks: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep tasks: %w", err)
	}
	return n, nil
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
		created, updated            string
		started, finished, archived sql.NullString
	)
	if err := r.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &fields, &t.WorkflowName,
		&t.WorkflowSnapshot, &t.BaseBranch, &t.BranchName, &worktree, &t.Priority,
		&agentOv, &modelOv, &effortOv,
		(*string)(&t.State), &t.CurrentStep, &blockReason,
		&created, &updated, &started, &finished, &archived); err != nil {
		return nil, err
	}
	t.WorktreePath = worktree.String
	t.BlockReason = blockReason.String
	t.AgentOverride = agentOv.String
	t.ModelOverride = modelOv.String
	t.EffortOverride = effortOv.String
	if err := json.Unmarshal([]byte(fields), &t.Fields); err != nil {
		return nil, fmt.Errorf("fields_json: %w", err)
	}
	if len(t.Fields) == 0 {
		t.Fields = nil
	}
	var err error
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
