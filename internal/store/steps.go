package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const stepRunColumns = `id, task_id, step_index, step_id, step_type, attempt, iteration, loop_item,
	loop_total,
	state, agent, model, effort, pid,
	proc_started_at, proc_identity, container_id, exit_code, check_exit_code, failure_reason, skip_reason,
	result_summary,
	stdout_tail,
	status_message,
	prompt_override, run_override, transcript_path,
	input_tokens, output_tokens, cost_usd, input_wait_ms, started_at, finished_at,
	rendered_prompt, rendered_run, rendered_check, rendered_if, rendered_for_each, input_truncated,
	agent_source, model_source, effort_source, permission_mode,
	timeout_ms, check_timeout_ms, shell, work_dir`

// TerminalizeOpenStepRuns closes every still-running step run of a task,
// recording state and reason. It exists for the human actions that end a
// task with no live actor to close its own rows — a cancel from
// `awaiting_gate`, whose manual row the actor wrote before exiting (§6).
// Returns how many rows were closed.
func (s *Store) TerminalizeOpenStepRuns(
	ctx context.Context, taskID int64, state StepRunState, reason string,
) (int64, error) {
	return terminalizeOpenStepRuns(ctx, s.db, taskID, state, reason)
}

// terminalizeOpenStepRuns is the statement behind it, taking an execer so the
// same write composes into a wider transaction: §12.4 recovery closes a task's
// open runs and moves the task itself in one commit (see InterruptTask).
func terminalizeOpenStepRuns(
	ctx context.Context, db execer, taskID int64, state StepRunState, reason string,
) (int64, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE step_runs SET state = ?, failure_reason = ?, pid = NULL, proc_identity = NULL,
			container_id = NULL, finished_at = ?
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
		if err := drainPendingOverride(ctx, tx, r); err != nil {
			return err
		}
		return createStepRun(ctx, tx, r)
	})
}

// OpenStepRun returns the still-`running` row at one position, or nil when
// that position has none.
//
// It exists for the `fan_out` step's two-admission shape (§7.6, issue #322):
// the park that spawns a round opens the round's row and the merge admission
// that ends the round finalizes that same row, so both halves have to be able
// to ask whether the round is already open. The park writes nothing when it
// is — a re-park must spend no retry budget (task 081) — and the merge adopts
// it rather than inserting a second one (task 080 decision 3).
//
// The position is the whole of ref, iteration included, because iteration is
// the round.
func (s *Store) OpenStepRun(ctx context.Context, ref StepRef) (*StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE task_id = ? AND step_index = ? AND step_id = ? AND iteration = ? AND state = ?
		ORDER BY attempt DESC, id DESC LIMIT 1`,
		ref.TaskID, ref.StepIndex, ref.StepID, ref.Iteration, string(StepRunning))
	r, err := scanStepRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open step run: %w", err)
	}
	return r, nil
}

// AdoptOpenStepRun hands the still-open row at r's position to the attempt
// starting now instead of inserting a second one, and reports whether there
// was such a row. False leaves r untouched, and the caller inserts as usual.
//
// One round of a `fan_out` step is one row (task 080 decision 3): the park
// opens it so the step is on the timeline while its lanes run, and the merge
// admission for that round adopts it here. The row keeps its id, its attempt
// number and its `started_at` — the step started when the fan-out began, not
// when its last lane settled, and the attempt number the park chose is the one
// §12.2 named that attempt's transcript after. It takes the columns the
// attempt owns: the transcript path and the agent selection.
//
// The pending edit+retry override is drained onto it in the same transaction
// as the write, for the reason CreateStepRunTakingOverride gives — a join is
// exactly a step a human retries after editing, and a crash must not clear
// their override without recording it (phase 2 decision).
//
// Nothing killable is written here. §12.4 recovery kills what a `running` row
// journaled, and there is no process behind a park.
func (s *Store) AdoptOpenStepRun(ctx context.Context, r *StepRun) (bool, error) {
	adopted := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
			WHERE task_id = ? AND step_index = ? AND step_id = ? AND iteration = ? AND state = ?
			ORDER BY attempt DESC, id DESC LIMIT 1`,
			r.TaskID, r.StepIndex, r.StepID, r.Iteration, string(StepRunning))
		open, err := scanStepRun(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("adopt step run: %w", err)
		}
		if err := drainPendingOverride(ctx, tx, r); err != nil {
			return err
		}
		r.ID, r.Attempt, r.StartedAt = open.ID, open.Attempt, open.StartedAt
		if _, err := tx.ExecContext(ctx, `
			UPDATE step_runs SET step_type = ?, agent = ?, model = ?, effort = ?,
				prompt_override = ?, run_override = ?, transcript_path = ?
			WHERE id = ?`,
			r.StepType, nullString(r.Agent), nullString(r.Model), nullString(r.Effort),
			nullString(r.PromptOverride), nullString(r.RunOverride), nullString(r.TranscriptPath),
			r.ID); err != nil {
			return fmt.Errorf("adopt step run %d: %w", r.ID, err)
		}
		adopted = true
		return nil
	})
	return adopted, err
}

// drainPendingOverride moves the task's pending edit+retry override onto r and
// clears it. It is called inside the transaction that writes r's row, so the
// drain and the row it lands on commit together.
func drainPendingOverride(ctx context.Context, tx *sql.Tx, r *StepRun) error {
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT pending_override_json FROM tasks WHERE id = ?`, r.TaskID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %d: %w", r.TaskID, ErrNotFound)
		}
		return fmt.Errorf("read pending override: %w", err)
	}
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var ov Override
	if err := json.Unmarshal([]byte(raw.String), &ov); err != nil {
		return fmt.Errorf("pending_override_json: %w", err)
	}
	r.PromptOverride, r.RunOverride = ov.Prompt, ov.Run
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET pending_override_json = NULL WHERE id = ?`, r.TaskID); err != nil {
		return fmt.Errorf("clear pending override: %w", err)
	}
	return nil
}

// execer is the subset of *sql.DB and *sql.Tx the insert needs.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func createStepRun(ctx context.Context, db execer, r *StepRun) error {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	// A decision row's rendered guard and a loop body row's resolved
	// `for_each` list are known *at insert* — the same rule loop_item and
	// loop_total already follow (what that admission planned) — so the insert
	// is the second write path for the input record, and takes the same cut.
	// The cut values go back onto r so the struct agrees with the row.
	r.RenderedPrompt = cutStepInput(r.RenderedPrompt, &r.InputTruncated)
	r.RenderedRun = cutStepInput(r.RenderedRun, &r.InputTruncated)
	r.RenderedCheck = cutStepInput(r.RenderedCheck, &r.InputTruncated)
	r.RenderedIf = cutStepInput(r.RenderedIf, &r.InputTruncated)
	r.RenderedForEach = cutStepInput(r.RenderedForEach, &r.InputTruncated)
	res, err := db.ExecContext(ctx, `
		INSERT INTO step_runs (task_id, step_index, step_id, step_type, attempt, iteration, loop_item,
			loop_total,
			state, agent, model, effort, pid,
			proc_started_at, proc_identity, container_id, exit_code, check_exit_code, failure_reason,
			skip_reason,
			result_summary, stdout_tail, status_message, prompt_override, run_override, transcript_path,
			input_tokens, output_tokens, cost_usd, input_wait_ms, started_at, finished_at,
			rendered_prompt, rendered_run, rendered_check, rendered_if, rendered_for_each,
			input_truncated,
			agent_source, model_source, effort_source, permission_mode,
			timeout_ms, check_timeout_ms, shell, work_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.StepIndex, r.StepID, r.StepType, r.Attempt, r.Iteration, nullString(r.LoopItem),
		r.LoopTotal,
		string(r.State),
		nullString(r.Agent), nullString(r.Model), nullString(r.Effort),
		r.PID, formatTimePtr(r.ProcStartedAt), r.ProcIdentity, r.ContainerID, r.ExitCode, r.CheckExitCode,
		nullString(r.FailureReason), nullString(r.SkipReason), nullString(r.ResultSummary),
		r.StdoutTail,
		nullString(r.StatusMessage),
		nullString(r.PromptOverride), nullString(r.RunOverride), nullString(r.TranscriptPath),
		r.InputTokens, r.OutputTokens, r.CostUSD, r.InputWaitMS,
		formatTime(r.StartedAt), formatTimePtr(r.FinishedAt),
		r.RenderedPrompt, r.RenderedRun, r.RenderedCheck, r.RenderedIf, r.RenderedForEach,
		r.InputTruncated,
		nullString(r.AgentSource), nullString(r.ModelSource), nullString(r.EffortSource),
		nullString(r.PermissionMode),
		r.TimeoutMS, r.CheckTimeoutMS, nullString(r.Shell), nullString(r.WorkDir))
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
//
// The migration-0027 input record is **not** in the SET list either, for the
// same reason and with more at stake (issue #323). The rendered input is
// written before the process starts, by RecordStepRunInput, and must already
// be on the row while the attempt is `running` and after §12.4 recovery
// finalizes it `interrupted` — which is precisely the attempt someone opens
// the details pane for. An UPDATE here carrying an actor's stale struct, whose
// new fields are zero because it read the row before the render, would erase
// the record and make the feature silently useless.
//
// `status_message` is deliberately **not** in the SET list (task 036). It is
// the one column the actor is not the sole writer of: the step's own process
// sets it through SetStepRunStatus while the actor is blocked in Wait, so an
// UPDATE carrying the actor's stale copy of the struct would erase whatever
// the step said. Leaving the column out is what makes the last live value
// survive onto the finished row for free.
func (s *Store) UpdateStepRun(ctx context.Context, r *StepRun) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE step_runs SET state = ?, agent = ?, model = ?, effort = ?, pid = ?, proc_started_at = ?,
			proc_identity = ?, container_id = ?,
			exit_code = ?, check_exit_code = ?, failure_reason = ?, skip_reason = ?, result_summary = ?,
			stdout_tail = ?,
			prompt_override = ?, run_override = ?, transcript_path = ?,
			input_tokens = ?, output_tokens = ?, cost_usd = ?, input_wait_ms = ?, finished_at = ?
		WHERE id = ?`,
		string(r.State), nullString(r.Agent), nullString(r.Model), nullString(r.Effort),
		r.PID, formatTimePtr(r.ProcStartedAt), r.ProcIdentity, r.ContainerID,
		r.ExitCode, r.CheckExitCode, nullString(r.FailureReason), nullString(r.SkipReason),
		nullString(r.ResultSummary), r.StdoutTail,
		nullString(r.PromptOverride), nullString(r.RunOverride), nullString(r.TranscriptPath),
		r.InputTokens, r.OutputTokens, r.CostUSD, r.InputWaitMS, formatTimePtr(r.FinishedAt),
		r.ID)
	if err != nil {
		return fmt.Errorf("update step run %d: %w", r.ID, err)
	}
	return oneRowAffected(res, fmt.Sprintf("step run %d", r.ID))
}

// StepInputLimit bounds each recorded input field (issue #323).
//
// 64 KiB, and the number is not arbitrary. On a retry the bytes an adapter
// receives are the §8.4 render *plus* the daemon's appended
// `<previous-attempt-failure>` block, whose output tail is bounded at 200
// lines **or 256 KiB** (taskrun's outputTailBytes) — a quarter-megabyte per
// retried attempt of bytes the transcript and result_summary already hold.
// Nothing prunes step_runs (taskrun's prune drops only archived-task
// transcripts and idempotency keys) and the database ships whole in `vincent
// daemon backup`, so an uncapped column is a permanent cost. The largest
// prompt vincent renders today is the `create-workflow` built-in's, about
// 10 KB, so this is roughly six times the biggest real case while still
// cutting the pathological one.
//
// resultSummaryLimit's 4096 is deliberately not reused: that bounds a summary
// a board renders, and this is a record whose value is being exact.
const StepInputLimit = 64 << 10

// cutStepInput bounds one recorded input field at StepInputLimit bytes,
// cutting on a rune boundary so a stored record is never invalid UTF-8, and
// sets *truncated when it removed anything. *truncated is only ever raised,
// never cleared: truncation is a fact about the row, and the several render
// sites of one attempt each write their own field.
//
// Both write paths — the insert and RecordStepRunInput — route every rendered
// field through here, which is what keeps a caller from storing an unbounded
// one.
func cutStepInput(v *string, truncated *bool) *string {
	if v == nil {
		return nil
	}
	s := *v
	if len(s) > StepInputLimit {
		// Back off to the start of the rune straddling the boundary: a cut
		// mid-rune would render as a replacement character forever after.
		cut := StepInputLimit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
		*truncated = true
	}
	return &s
}

// StepRunInput is the rendered input and run-time resolution recorded on a
// `step_runs` row (migration 0027). Only the members a call sets are written:
// a call carrying just Check cannot clear a Prompt an earlier call wrote.
//
// Guard is the rendered `if:` and ForEach the JSON array of resolved
// `for_each` items. Both are display only — a guard is re-evaluated every
// time it is reached and is never sticky (task 015 decision 10).
type StepRunInput struct {
	Prompt, Run, Check, Guard, ForEach     *string
	AgentSource, ModelSource, EffortSource string
	PermissionMode                         string
	TimeoutMS, CheckTimeoutMS              int64
	Shell, WorkDir                         string
}

// RecordStepRunInput records what one attempt was given. Additive: the SET
// list is built from the members that are non-nil (pointers) or non-zero
// (strings and ints), so the several render sites of one attempt each write
// their own field without clobbering the others — an agent step records its
// prompt at one moment, a command step its script at another and its check
// command later still, and all three are the same row. `input_truncated` is
// OR-ed, never cleared.
//
// A call whose in sets nothing is a no-op returning nil — an empty
// `UPDATE step_runs SET WHERE id = ?` is not a statement. Returns ErrNotFound
// when the row does not exist.
//
// It is narrow the way SetStepRunStatus is, rather than a widening of
// UpdateStepRun, and the reason is in UpdateStepRun's own comment: these
// columns are written once, before the process starts, and an actor's later
// UPDATE must not be able to carry them.
func (s *Store) RecordStepRunInput(ctx context.Context, runID int64, in StepRunInput) error {
	truncated := false
	sets := make([]string, 0, 14)
	args := make([]any, 0, 15)
	// Fixed order rather than a map walk: the SET list and the argument list
	// are two halves of one statement, so anything that reorders one silently
	// misaligns the other.
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	addRendered := func(col string, v *string) {
		if cut := cutStepInput(v, &truncated); cut != nil {
			add(col, *cut)
		}
	}
	addText := func(col, v string) {
		if v != "" {
			add(col, v)
		}
	}
	addRendered("rendered_prompt", in.Prompt)
	addRendered("rendered_run", in.Run)
	addRendered("rendered_check", in.Check)
	addRendered("rendered_if", in.Guard)
	addRendered("rendered_for_each", in.ForEach)
	addText("agent_source", in.AgentSource)
	addText("model_source", in.ModelSource)
	addText("effort_source", in.EffortSource)
	addText("permission_mode", in.PermissionMode)
	addText("shell", in.Shell)
	addText("work_dir", in.WorkDir)
	if in.TimeoutMS != 0 {
		add("timeout_ms", in.TimeoutMS)
	}
	if in.CheckTimeoutMS != 0 {
		add("check_timeout_ms", in.CheckTimeoutMS)
	}
	if truncated {
		add("input_truncated", true)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, runID)
	//nolint:gosec // G202: sets holds literal column names; every value binds as an argument
	res, err := s.db.ExecContext(ctx,
		`UPDATE step_runs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("record step run input %d: %w", runID, err)
	}
	return oneRowAffected(res, fmt.Sprintf("step run %d", runID))
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
// ref narrows it to one position for the reason CountStepAttempts gives: the
// failure block a step retries under must be its own — not a group sibling's
// (task 014 decision 16), and not the same body step's failure from a
// previous loop iteration (task 016 decision 6).
func (s *Store) LastFailedStepRun(ctx context.Context, ref StepRef) (*StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE task_id = ? AND step_index = ? AND step_id = ? AND iteration = ? AND state = ?
		ORDER BY attempt DESC, id DESC LIMIT 1`,
		ref.TaskID, ref.StepIndex, ref.StepID, ref.Iteration, string(StepFailed))
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

// LatestStepRun returns the newest attempt of one step, or nil when it has
// none.
//
// It exists for the fan-out join's re-entry rule (task 014 decision 9): a
// merge in progress means a crash or a human's resolution, and the two are
// told apart by how the last attempt ended. The task's live state cannot
// answer it — by the time the engine runs, the scheduler has already moved a
// retried task out of `blocked`.
func (s *Store) LatestStepRun(ctx context.Context, taskID int64, stepIndex int, stepID string) (*StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE task_id = ? AND step_index = ? AND step_id = ?
		ORDER BY attempt DESC, id DESC LIMIT 1`,
		taskID, stepIndex, stepID)
	r, err := scanStepRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest step run: %w", err)
	}
	return r, nil
}

// SucceededIterations counts the distinct iterations of one step that have a
// succeeded row — how many times this step has completed a repeat of itself.
//
// It exists for `schedule: eager` (§7.6, task 081 decision 2), where a
// fan_out's `iteration` can no longer be derived from the lane's wave in the
// graph: two eager admissions can merge two lanes of the same wave and would
// compute the same number, colliding on the retry budget and the §12.2
// transcript name that `stepEnv.ref()` scopes by it. Under eager the merge
// counter is this instead — how many merge rows the step already has.
//
// Succeeded rows only, deliberately: a merge that blocked on a conflict must
// re-enter at the *same* iteration when a human retries it, or the retry
// would start a fresh budget and write its transcript somewhere else.
func (s *Store) SucceededIterations(
	ctx context.Context, taskID int64, stepIndex int, stepID string,
) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT iteration) FROM step_runs
		WHERE task_id = ? AND step_index = ? AND step_id = ? AND state = ?`,
		taskID, stepIndex, stepID, string(StepSucceeded)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("succeeded iterations of step %q: %w", stepID, err)
	}
	return n, nil
}

// ListStepRunsAt returns every row at one step index, in position order:
// iteration, then attempt. It is a `loop` step's whole history in one read.
//
// A loop derives everything it needs from these rows rather than from a
// stored cursor (task 016 decision 7): which iteration it is in, which body
// steps of that iteration already succeeded, and — for `for_each` — which
// item each iteration ran on (decision 8). One query rather than three
// because the reduction is a walk in Go over a bounded set: a loop's rows are
// at most max_iterations × body size × attempts.
func (s *Store) ListStepRunsAt(ctx context.Context, taskID int64, stepIndex int) ([]StepRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE task_id = ? AND step_index = ?
		ORDER BY iteration ASC, attempt ASC, id ASC`, taskID, stepIndex)
	if err != nil {
		return nil, fmt.Errorf("list step runs at index %d: %w", stepIndex, err)
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
		return nil, fmt.Errorf("list step runs at index %d: %w", stepIndex, err)
	}
	return out, nil
}

// ListStepRuns returns every attempt of every step of the task in position
// order: step index, then iteration, then attempt.
//
// `iteration` sits between the two on purpose (task 016 decision 9). The §8.4
// assembly walks these rows in order and does `steps[run.StepID] = res`, so
// last row wins — which is exactly what makes `.Steps["suite"]` under
// repetition resolve to the *latest* iteration, for free, provided the
// ordering is this one. Ordering by attempt first would interleave the
// iterations and hand a guard an older pass's result.
func (s *Store) ListStepRuns(ctx context.Context, taskID int64) ([]StepRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+stepRunColumns+` FROM step_runs
		WHERE task_id = ? ORDER BY step_index ASC, iteration ASC, attempt ASC, id ASC`, taskID)
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
		failure, skip, summary, transcript      sql.NullString
		stdoutTail                              sql.NullString
		status                                  sql.NullString
		loopItem                                sql.NullString
		promptOv, runOv                         sql.NullString
		pid, exitCode, checkExit, inTok, outTok sql.NullInt64
		cost                                    sql.NullFloat64
		procStarted, procIdentity, finished     sql.NullString
		containerID                             sql.NullString
		started                                 string
		prompt, run, check, guard, forEach      sql.NullString
		agentSrc, modelSrc, effortSrc, permMode sql.NullString
		shell, workDir                          sql.NullString
	)
	if err := row.Scan(&r.ID, &r.TaskID, &r.StepIndex, &r.StepID, &r.StepType, &r.Attempt,
		&r.Iteration, &loopItem, &r.LoopTotal,
		(*string)(&r.State), &agent, &model, &effort, &pid, &procStarted, &procIdentity, &containerID,
		&exitCode, &checkExit,
		&failure, &skip, &summary, &stdoutTail, &status, &promptOv, &runOv, &transcript,
		&inTok, &outTok, &cost, &r.InputWaitMS, &started, &finished,
		&prompt, &run, &check, &guard, &forEach, &r.InputTruncated,
		&agentSrc, &modelSrc, &effortSrc, &permMode,
		&r.TimeoutMS, &r.CheckTimeoutMS, &shell, &workDir); err != nil {
		return nil, err
	}
	// NULL and empty are different facts for the rendered fields: nil is "no
	// input recorded" (a pre-0027 row, or a step type with no such input) and
	// a non-nil empty string is a render that produced nothing.
	r.RenderedPrompt = stringPtr(prompt)
	r.RenderedRun = stringPtr(run)
	r.RenderedCheck = stringPtr(check)
	r.RenderedIf = stringPtr(guard)
	r.RenderedForEach = stringPtr(forEach)
	r.AgentSource = agentSrc.String
	r.ModelSource = modelSrc.String
	r.EffortSource = effortSrc.String
	r.PermissionMode = permMode.String
	r.Shell = shell.String
	r.WorkDir = workDir.String
	r.Agent = agent.String
	r.Model = model.String
	r.Effort = effort.String
	r.LoopItem = loopItem.String
	r.FailureReason = failure.String
	r.SkipReason = skip.String
	r.ResultSummary = summary.String
	if stdoutTail.Valid {
		v := stdoutTail.String
		r.StdoutTail = &v
	}
	r.StatusMessage = status.String
	r.PromptOverride = promptOv.String
	r.RunOverride = runOv.String
	r.TranscriptPath = transcript.String
	if pid.Valid {
		v := int(pid.Int64)
		r.PID = &v
	}
	if procIdentity.Valid {
		v := procIdentity.String
		r.ProcIdentity = &v
	}
	if containerID.Valid {
		v := containerID.String
		r.ContainerID = &v
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
	inList := placeholders(len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	//nolint:gosec // G202: inList is placeholders(); the ids bind as arguments
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, SUM(cost_usd), SUM(input_tokens), SUM(output_tokens)
		FROM step_runs WHERE task_id IN `+inList+` GROUP BY task_id`, args...)
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
