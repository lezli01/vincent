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

const stepRunColumns = `id, task_id, step_index, step_id, step_type, attempt, state, agent, model, effort, pid,
	proc_started_at, exit_code, check_exit_code, failure_reason, result_summary,
	prompt_override, run_override, transcript_path,
	input_tokens, output_tokens, cost_usd, input_wait_ms, started_at, finished_at`

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
	return createStepRun(ctx, s.db, r)
}

// CreateStepRunTakingOverride inserts r with any pending edit+retry override
// drained onto it, clearing the override in the same transaction: the drain
// and the row it lands on commit together, so a crash cannot clear the
// human's override without recording it (phase 2 decision).
func (s *Store) CreateStepRunTakingOverride(ctx context.Context, r *StepRun) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var raw sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT pending_override_json FROM tasks WHERE id = ?`, r.TaskID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task %d: %w", r.TaskID, ErrNotFound)
			}
			return fmt.Errorf("read pending override: %w", err)
		}
		if raw.Valid && raw.String != "" {
			var ov Override
			if err := json.Unmarshal([]byte(raw.String), &ov); err != nil {
				return fmt.Errorf("pending_override_json: %w", err)
			}
			r.PromptOverride, r.RunOverride = ov.Prompt, ov.Run
			if _, err := tx.ExecContext(ctx,
				`UPDATE tasks SET pending_override_json = NULL WHERE id = ?`, r.TaskID); err != nil {
				return fmt.Errorf("clear pending override: %w", err)
			}
		}
		return createStepRun(ctx, tx, r)
	})
}

// execer is the subset of *sql.DB and *sql.Tx the insert needs.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func createStepRun(ctx context.Context, db execer, r *StepRun) error {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO step_runs (task_id, step_index, step_id, step_type, attempt, state, agent, model, effort, pid,
			proc_started_at, exit_code, check_exit_code, failure_reason, result_summary,
			prompt_override, run_override, transcript_path,
			input_tokens, output_tokens, cost_usd, input_wait_ms, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.StepIndex, r.StepID, r.StepType, r.Attempt, string(r.State),
		nullString(r.Agent), nullString(r.Model), nullString(r.Effort),
		r.PID, formatTimePtr(r.ProcStartedAt), r.ExitCode, r.CheckExitCode,
		nullString(r.FailureReason), nullString(r.ResultSummary),
		nullString(r.PromptOverride), nullString(r.RunOverride), nullString(r.TranscriptPath),
		r.InputTokens, r.OutputTokens, r.CostUSD, r.InputWaitMS,
		formatTime(r.StartedAt), formatTimePtr(r.FinishedAt))
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
			input_tokens = ?, output_tokens = ?, cost_usd = ?, input_wait_ms = ?, finished_at = ?
		WHERE id = ?`,
		string(r.State), nullString(r.Agent), nullString(r.Model), nullString(r.Effort),
		r.PID, formatTimePtr(r.ProcStartedAt),
		r.ExitCode, r.CheckExitCode, nullString(r.FailureReason), nullString(r.ResultSummary),
		nullString(r.PromptOverride), nullString(r.RunOverride), nullString(r.TranscriptPath),
		r.InputTokens, r.OutputTokens, r.CostUSD, r.InputWaitMS, formatTimePtr(r.FinishedAt),
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

// LastFailedStepRun returns the most recent failed attempt of one step, or
// nil when the step has none. It seeds `.LastFailure` when an admission
// starts on a step whose earlier attempts failed under a previous actor —
// the human-retry path, where the §8.4 failure block matters most.
//
// stepID narrows it to one member of a `parallel` group, for the reason
// CountStepAttempts gives (task 014 decision 16): the failure block a
// sub-step retries under must be its own, not a sibling's.
func (s *Store) LastFailedStepRun(ctx context.Context, taskID int64, stepIndex int, stepID string) (*StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE task_id = ? AND step_index = ? AND step_id = ? AND state = ?
		ORDER BY attempt DESC, id DESC LIMIT 1`,
		taskID, stepIndex, stepID, string(StepFailed))
	r, err := scanStepRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last failed step run: %w", err)
	}
	return r, nil
}

// LatestStepStates returns, for one step index, the state of the most recent
// attempt of each distinct step id. It exists for `parallel` groups (task
// 014): a group re-admitted after a block must re-run only the sub-steps that
// did not already succeed, and that fact is derived from these rows rather
// than stored on the group — which has no row of its own (decision 17).
//
// The map is keyed by step id. An index with no rows yields an empty map, not
// an error: a group running for the first time has no history.
func (s *Store) LatestStepStates(ctx context.Context, taskID int64, stepIndex int) (map[string]StepRunState, error) {
	// The join picks the highest attempt per step id — MAX(attempt) alone
	// would pair a maximum with an arbitrary row's state.
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.step_id, r.state FROM step_runs r
		JOIN (SELECT step_id, MAX(attempt) AS attempt FROM step_runs
			WHERE task_id = ? AND step_index = ? GROUP BY step_id) m
		ON r.step_id = m.step_id AND r.attempt = m.attempt
		WHERE r.task_id = ? AND r.step_index = ?`,
		taskID, stepIndex, taskID, stepIndex)
	if err != nil {
		return nil, fmt.Errorf("latest step states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]StepRunState)
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, fmt.Errorf("scan latest step state: %w", err)
		}
		out[id] = StepRunState(state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest step states: %w", err)
	}
	return out, nil
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

// ListRunningStepRuns returns every step run still marked running, across
// all tasks — the startup recovery's work list (spec §12.4): rows a previous
// daemon process left open, each carrying the PID it journaled.
func (s *Store) ListRunningStepRuns(ctx context.Context) ([]StepRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE state = ? ORDER BY id ASC`, string(StepRunning))
	if err != nil {
		return nil, fmt.Errorf("list running step runs: %w", err)
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
		return nil, fmt.Errorf("list running step runs: %w", err)
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
		&inTok, &outTok, &cost, &r.InputWaitMS, &started, &finished); err != nil {
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

// TaskRollup aggregates one task's step_run metrics across every attempt
// (§17). Retries are included on purpose: a step that failed twice before
// succeeding cost money three times, and a board reporting only the
// surviving attempt would under-report spend exactly on the tasks that
// burned it.
type TaskRollup struct {
	// CostUSD sums cost_usd over attempts that reported one. HasCost
	// distinguishes "every attempt reported nothing" from "genuinely free":
	// adapters that never report cost (§9) must not render as $0.00.
	CostUSD      float64
	HasCost      bool
	InputTokens  int64
	OutputTokens int64
}

// TaskRollups returns the rollup for each of ids that has any step runs.
// Tasks with no runs yet are absent from the map; callers treat a miss as a
// zero rollup. ids is expected to be one page of a task list, so the IN list
// is bounded by the caller's page size.
func (s *Store) TaskRollups(ctx context.Context, ids []int64) (map[int64]TaskRollup, error) {
	out := make(map[int64]TaskRollup, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(ids)-1) + "?"
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, SUM(cost_usd), SUM(input_tokens), SUM(output_tokens)
		FROM step_runs WHERE task_id IN (`+placeholders+`) GROUP BY task_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("roll up task metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			taskID        int64
			cost          sql.NullFloat64
			inTok, outTok sql.NullInt64
		)
		if err := rows.Scan(&taskID, &cost, &inTok, &outTok); err != nil {
			return nil, fmt.Errorf("scan task rollup: %w", err)
		}
		out[taskID] = TaskRollup{
			CostUSD:      cost.Float64,
			HasCost:      cost.Valid,
			InputTokens:  inTok.Int64,
			OutputTokens: outTok.Int64,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read task rollups: %w", err)
	}
	return out, nil
}
