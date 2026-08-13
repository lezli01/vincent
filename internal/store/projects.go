package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const projectColumns = `id, name, path, default_branch, default_workflow, max_parallel_tasks, branch_template, created_at, updated_at`

// CreateProject inserts p and assigns its ID and timestamps, writing the
// durable project.created event in the same transaction (spec §13.3). A
// caller-set CreatedAt is kept (tests rely on this); zero means now.
func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO projects (name, path, default_branch, default_workflow, max_parallel_tasks, branch_template, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Name, p.Path, p.DefaultBranch, nullString(p.DefaultWorkflow), p.MaxParallelTasks,
			nullString(p.BranchTemplate), formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
		if err != nil {
			return fmt.Errorf("insert project: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("insert project: %w", err)
		}
		p.ID = id
		ev, err = projectEvent(EventProjectCreated, p.ID, p.Name)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// projectEvent builds a project.* event carrying the name for display;
// clients re-fetch the full object as needed (§13.3).
func projectEvent(evType string, id int64, name string) (*Event, error) {
	payload, err := json.Marshal(map[string]any{"name": name})
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", evType, err)
	}
	return &Event{Type: evType, ProjectID: &id, Payload: payload}, nil
}

// GetProject returns the project with the given id, or ErrNotFound.
func (s *Store) GetProject(ctx context.Context, id int64) (*Project, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("project %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get project %d: %w", id, err)
	}
	return p, nil
}

// ListProjects returns all projects ordered by id.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return out, nil
}

// UpdateProject writes every mutable field of p (matched by ID) and bumps
// UpdatedAt, writing the durable project.updated event in the same
// transaction (spec §13.3). Returns ErrNotFound when the row does not exist.
func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	p.UpdatedAt = time.Now()
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE projects SET name = ?, path = ?, default_branch = ?, default_workflow = ?,
				max_parallel_tasks = ?, branch_template = ?, updated_at = ?
			WHERE id = ?`,
			p.Name, p.Path, p.DefaultBranch, nullString(p.DefaultWorkflow), p.MaxParallelTasks,
			nullString(p.BranchTemplate), formatTime(p.UpdatedAt), p.ID)
		if err != nil {
			return fmt.Errorf("update project %d: %w", p.ID, err)
		}
		if err := oneRowAffected(res, fmt.Sprintf("project %d", p.ID)); err != nil {
			return err
		}
		ev, err = projectEvent(EventProjectUpdated, p.ID, p.Name)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// DeleteProject removes the project row. Returns ErrNotFound when the row
// does not exist. Deleting a project that still has tasks fails on the
// foreign-key constraint; the API layer guards with CountNonArchivedTasks
// before cascading (spec §13.2).
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	return oneRowAffected(res, fmt.Sprintf("project %d", id))
}

// DeleteProjectCascade hard-deletes the project and every row that hangs off
// it (events, step_runs, tasks) in one transaction, keeping the schema's
// foreign keys strict (T1.5 decision). Returns ErrNotFound when the project
// does not exist; nothing is deleted then.
//
// A durable project.deleted event is appended in the same transaction, after
// the cascade: the events table has no foreign keys, so the event survives
// the project whose end it records (PR D decision).
func (s *Store) DeleteProjectCascade(ctx context.Context, id int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, id).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	for _, q := range []string{
		`DELETE FROM events WHERE project_id = ?1 OR task_id IN (SELECT id FROM tasks WHERE project_id = ?1)`,
		`DELETE FROM step_runs WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?1)`,
		`DELETE FROM tasks WHERE project_id = ?1`,
	} {
		if _, err = tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("delete project %d cascade: %w", id, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	if err = oneRowAffected(res, fmt.Sprintf("project %d", id)); err != nil {
		return err
	}
	ev, err := projectEvent(EventProjectDeleted, id, name)
	if err != nil {
		return err
	}
	if err = appendEventTx(ctx, tx, ev); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	s.notify(ev)
	return nil
}

func scanProject(r rowScanner) (*Project, error) {
	var (
		p                Project
		workflow         sql.NullString
		branchTemplate   sql.NullString
		maxPar           sql.NullInt64
		created, updated string
	)
	if err := r.Scan(&p.ID, &p.Name, &p.Path, &p.DefaultBranch, &workflow, &maxPar,
		&branchTemplate, &created, &updated); err != nil {
		return nil, err
	}
	p.DefaultWorkflow = workflow.String
	p.BranchTemplate = branchTemplate.String
	if maxPar.Valid {
		v := int(maxPar.Int64)
		p.MaxParallelTasks = &v
	}
	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if p.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	return &p, nil
}

// oneRowAffected maps a zero-row UPDATE/DELETE result to ErrNotFound.
func oneRowAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}
