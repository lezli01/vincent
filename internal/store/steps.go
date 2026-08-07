package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const stepRunColumns = `id, task_id, step_index, step_id, step_type, attempt, state, agent, model, effort, pid,
	proc_started_at, exit_code, check_exit_code, failure_reason, result_summary,
	prompt_override, run_override, transcript_path,
	input_tokens, output_tokens, cost_usd, started_at, finished_at`

// TerminalizeOpenStepRuns closes every still-running step run of a task,
// recording state and reason. It exists for the human actions that end a
// task with no live actor to close its own rows — a cancel from
// `awaiting_gate`, whose manual row the actor wrote before exiting (§6).
// Returns how many rows were closed.
func (s *Store) TerminalizeOpenStepRuns(
	ctx context.Context, taskID int64, state StepRunState, reason string,
) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE step_runs SET state = ?, failure_reason = ?, pid = NULL, finished_at = ?
		WHERE task_id = ? AND state = ?`,
		string(state), nullString(reason), formatTime(time.Now()), taskID, string(StepRunning))
	if err != nil {
		return 0, fmt.Errorf("terminalize step runs of task %d: %w", taskID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("terminalize step runs of task %d: %w", taskID, err)
	}
	return n, nil
}

// CreateStepRun inserts r and assigns its ID. A caller-set StartedAt is
// kept; zero means now. Every attempt is a fresh row — history is
// append-only (spec §5.4).
func (s *Store) CreateStepRun(ctx context.Context, r *StepRun) error {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO step_runs (task_id, step_index, step_id, step_type, attempt, state, agent, model, effort, pid,
			proc_started_at, exit_code, check_exit_code, failure_reason, result_summary,
			prompt_override, run_override, transcript_path,
			input_tokens, output_tokens, cost_usd, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.StepIndex, r.StepID, r.StepType, r.Attempt, string(r.State),
		nullString(r.Agent), nullString(r.Model), nullString(r.Effort),
		r.PID, formatTimePtr(r.ProcStartedAt), r.ExitCode, r.CheckExitCode,
		nullString(r.FailureReason), nullString(r.ResultSummary),
		nullString(r.PromptOverride), nullString(r.RunOverride), nullString(r.TranscriptPath),
		r.InputTokens, r.OutputTokens, r.CostUSD, formatTime(r.StartedAt), formatTimePtr(r.FinishedAt))
	if err != nil {
		return fmt.Errorf("insert step run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("insert step run: %w", err)
	}
	r.ID = id
	return nil
}

// UpdateStepRun writes every mutable field of r (matched by ID). Returns
// ErrNotFound when the row does not exist.
func (s *Store) UpdateStepRun(ctx context.Context, r *StepRun) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE step_runs SET state = ?, agent = ?, model = ?, effort = ?, pid = ?, proc_started_at = ?,
			exit_code = ?, check_exit_code = ?, failure_reason = ?, result_summary = ?,
			prompt_override = ?, run_override = ?, transcript_path = ?,
			input_tokens = ?, output_tokens = ?, cost_usd = ?, finished_at = ?
		WHERE id = ?`,
		string(r.State), nullString(r.Agent), nullString(r.Model), nullString(r.Effort),
		r.PID, formatTimePtr(r.ProcStartedAt),
		r.ExitCode, r.CheckExitCode, nullString(r.FailureReason), nullString(r.ResultSummary),
		nullString(r.PromptOverride), nullString(r.RunOverride), nullString(r.TranscriptPath),
		r.InputTokens, r.OutputTokens, r.CostUSD, formatTimePtr(r.FinishedAt),
		r.ID)
	if err != nil {
		return fmt.Errorf("update step run %d: %w", r.ID, err)
	}
	return oneRowAffected(res, fmt.Sprintf("step run %d", r.ID))
}

// GetStepRun returns the step run with the given id, or ErrNotFound.
func (s *Store) GetStepRun(ctx context.Context, id int64) (*StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs WHERE id = ?`, id)
	r, err := scanStepRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("step run %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get step run %d: %w", id, err)
	}
	return r, nil
}

// ListStepRuns returns every attempt of every step of the task, ordered by
// step index, then attempt.
func (s *Store) ListStepRuns(ctx context.Context, taskID int64) ([]StepRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE task_id = ? ORDER BY step_index ASC, attempt ASC, id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list step runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StepRun
	for rows.Next() {
		r, err := scanStepRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan step run: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list step runs: %w", err)
	}
	return out, nil
}

func scanStepRun(row rowScanner) (*StepRun, error) {
	var (
		r                                       StepRun
		agent, model, effort                    sql.NullString
		failure, summary, transcript            sql.NullString
		promptOv, runOv                         sql.NullString
		pid, exitCode, checkExit, inTok, outTok sql.NullInt64
		cost                                    sql.NullFloat64
		procStarted, finished                   sql.NullString
		started                                 string
	)
	if err := row.Scan(&r.ID, &r.TaskID, &r.StepIndex, &r.StepID, &r.StepType, &r.Attempt,
		(*string)(&r.State), &agent, &model, &effort, &pid, &procStarted, &exitCode, &checkExit,
		&failure, &summary, &promptOv, &runOv, &transcript,
		&inTok, &outTok, &cost, &started, &finished); err != nil {
		return nil, err
	}
	r.Agent = agent.String
	r.Model = model.String
	r.Effort = effort.String
	r.FailureReason = failure.String
	r.ResultSummary = summary.String
	r.PromptOverride = promptOv.String
	r.RunOverride = runOv.String
	r.TranscriptPath = transcript.String
	if pid.Valid {
		v := int(pid.Int64)
		r.PID = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		r.ExitCode = &v
	}
	if checkExit.Valid {
		v := int(checkExit.Int64)
		r.CheckExitCode = &v
	}
	if inTok.Valid {
		r.InputTokens = &inTok.Int64
	}
	if outTok.Valid {
		r.OutputTokens = &outTok.Int64
	}
	if cost.Valid {
		r.CostUSD = &cost.Float64
	}
	var err error
	if r.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if r.ProcStartedAt, err = parseTimePtr(procStarted); err != nil {
		return nil, err
	}
	if r.FinishedAt, err = parseTimePtr(finished); err != nil {
		return nil, err
	}
	return &r, nil
}
