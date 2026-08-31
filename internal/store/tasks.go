package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/taskstate"
)

const taskColumns = `id, project_id, title, description, fields_json, workflow_name, workflow_snapshot,
	base_branch, branch_name, worktree_path, base_sha, priority, agent_override, model_override, effort_override,
	state, current_step, block_reason, pause_requested, retry_cursor_at, pending_override_json,
	pending_repair_json, pending_follow_up_json, pending_input_json, admit_not_before, queued_reason,
	parent_task_id, parent_step_index, lane_id, lane_order, github_issue_json, github_pull_json,
	workflow_origin_json, created_by_task_id,
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
	return args, placeholders(len(args))
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
//
// It is the one-task spelling of CreateTasks, which is where the transaction
// lives.
func (s *Store) CreateTask(ctx context.Context, t *Task, resolveBranch func(id int64) (string, error)) error {
	return s.createTasks(ctx, []*Task{t}, resolveBranch, nil)
}

// CreateTaskWithKey is CreateTask with an idempotency key written in the same
// transaction as the row (task 040, issue #146). Either the task and its key
// both commit or neither does, which is what makes a replayed create find the
// key exactly when the task it names exists.
//
// A key already recorded for this `(method, path, key)` returns
// ErrIdempotencyKeyExists with the whole insert rolled back — the concurrent
// duplicate — and the caller replays the winner's task instead.
//
// k is the only caller-supplied part; the task id and, when unset, the
// timestamp are filled in from the insert.
func (s *Store) CreateTaskWithKey(
	ctx context.Context, t *Task, resolveBranch func(id int64) (string, error), k *IdempotencyKey,
) error {
	return s.createTasks(ctx, []*Task{t}, resolveBranch, k)
}

// CreateTasks inserts several tasks in **one** transaction, resolving each
// one's branch name the way CreateTask does. Either every row is committed or
// none is.
//
// It exists for a fan_out step's lanes (spec §7.6). Spawning them one
// CreateTask at a time made a *partial* spawn reachable — some lanes
// committed, the rest not — and there is no honest way to clean that up
// afterwards: cancelling the lanes that made it leaves them settled-aborted
// and still attached to the step, so the parent's next admission takes the
// join path and blocks `lane_failed` forever, while deleting them would need
// the first destructive task primitive in a system whose posture is that
// nothing destroys work. One transaction removes the state instead of
// cleaning up after it.
//
// The branch claim (claimBranchTx) runs per task inside the shared
// transaction, so two lanes resolving to the same name collide here exactly
// as a lane colliding with an existing task does.
func (s *Store) CreateTasks(ctx context.Context, tasks []*Task, resolveBranch func(id int64) (string, error)) error {
	return s.createTasks(ctx, tasks, resolveBranch, nil)
}

// createTasks is the shared body. k, when non-nil, is the idempotency key
// recorded for the *first* task in the batch — which in practice means the
// single task CreateTaskWithKey passes, since the fan-out path never carries
// one (task 040): a lane spawn is the engine's own work, not a request a
// client could replay.
func (s *Store) createTasks(
	ctx context.Context, tasks []*Task,
	resolveBranch func(id int64) (string, error), k *IdempotencyKey,
) error {
	if len(tasks) == 0 {
		return nil
	}
	now := time.Now()
	events := make([]*Event, 0, len(tasks))
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		events = events[:0]
		for i, t := range tasks {
			ev, err := insertTaskTx(ctx, tx, t, now, resolveBranch)
			if err != nil {
				return err
			}
			if i == 0 && k != nil {
				if err := insertIdempotencyKeyTx(ctx, tx, k, t.ID, now); err != nil {
					return err
				}
			}
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// After the commit, in insertion order: a subscriber must never see a
	// task.created for a row a rollback took away.
	for _, ev := range events {
		s.notify(ev)
	}
	return nil
}

// insertTaskTx is one task's insert, branch-name resolution, branch claim and
// task.created event, inside the caller's transaction. The event is returned
// rather than published: publishing belongs after the commit.
func insertTaskTx(
	ctx context.Context, tx *sql.Tx, t *Task, now time.Time,
	resolveBranch func(id int64) (string, error),
) (*Event, error) {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	fields, err := marshalFields(t.Fields)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	issueJSON, err := marshalGitHubIssue(t.GitHubIssue)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	pullJSON, err := marshalGitHubPull(t.GitHubPull)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	originJSON, err := marshalWorkflowOrigin(t.WorkflowOrigin)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (project_id, title, description, fields_json, workflow_name, workflow_snapshot,
			base_branch, branch_name, worktree_path, base_sha, priority, agent_override, model_override, effort_override,
			state, current_step, block_reason, admit_not_before, queued_reason,
			parent_task_id, parent_step_index, lane_id, lane_order, github_issue_json,
			github_pull_json, workflow_origin_json, created_by_task_id,
			created_at, updated_at, started_at, finished_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ProjectID, t.Title, t.Description, fields, t.WorkflowName, t.WorkflowSnapshot,
		t.BaseBranch, t.BranchName, nullString(t.WorktreePath), nullString(t.BaseSHA), t.Priority,
		nullString(t.AgentOverride), nullString(t.ModelOverride), nullString(t.EffortOverride),
		string(t.State), t.CurrentStep, nullString(t.BlockReason),
		// The §11 hold rides along with the row it describes. UpdateTask has
		// always written these two, so an insert that dropped them made
		// "created already held" a two-statement affair with a window in the
		// middle — long enough for the scheduler's tick to admit the task
		// between them (task 003).
		formatTimePtr(t.AdmitNotBefore), nullString(t.QueuedReason),
		t.ParentTaskID, t.ParentStepIndex, nullString(t.LaneID), t.LaneOrder, issueJSON,
		pullJSON, originJSON, t.CreatedByTaskID,
		formatTime(t.CreatedAt), formatTime(t.UpdatedAt),
		formatTimePtr(t.StartedAt), formatTimePtr(t.FinishedAt), formatTimePtr(t.ArchivedAt))
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	t.ID = id
	// The id exists now, so a name that needed it can be produced and written
	// before this transaction commits.
	if resolveBranch != nil {
		branch, err := resolveBranch(id)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET branch_name = ? WHERE id = ?`, branch, id); err != nil {
			return nil, fmt.Errorf("assign branch name: %w", err)
		}
		t.BranchName = branch
	}
	if err := claimBranchTx(ctx, tx, t.ProjectID, t.BranchName, id); err != nil {
		return nil, err
	}
	// `workflow_origin` rides beside `workflow` (task 043): the name alone
	// cannot tell a shadowed `adhoc` from the built-in, and an event consumer
	// that never fetches the task should not have to.
	created := map[string]any{
		"state": string(t.State), "title": t.Title, "workflow": t.WorkflowName,
	}
	if t.WorkflowOrigin != nil {
		created["workflow_origin"] = t.WorkflowOrigin
	}
	payload, err := json.Marshal(created)
	if err != nil {
		return nil, fmt.Errorf("marshal task.created event: %w", err)
	}
	// Copied, not aliased: the event goes to the broker, and t belongs to the
	// caller.
	taskID, projectID := t.ID, t.ProjectID
	ev := &Event{
		TS: now, Type: EventTaskCreated,
		TaskID: &taskID, ProjectID: &projectID, Payload: payload,
	}
	// Appended inside the caller's transaction, so a rolled-back insert takes
	// its event with it; the caller publishes it to the broker after the
	// commit.
	if err := appendEventTx(ctx, tx, ev); err != nil {
		return nil, err
	}
	return ev, nil
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
	// Children decides whether fan-out lanes appear (task 014 decision 13).
	// The zero value is ChildrenExclude: a list is the work someone asked
	// for, and a 64-task tree would bury it.
	Children ChildrenFilter
	// ParentID lists exactly one parent's lanes, in merge order. It implies
	// nothing about Children — naming a parent *is* asking for children.
	ParentID int64
}

// ChildrenFilter decides whether fan-out lanes are listed (task 014).
type ChildrenFilter int

const (
	// ChildrenExclude returns root tasks only — parent_task_id IS NULL.
	ChildrenExclude ChildrenFilter = iota
	// ChildrenInclude returns the flat everything, roots and lanes alike.
	ChildrenInclude
)

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
	switch {
	case f.ParentID != 0:
		// Asking for one parent's lanes is asking for children, so the
		// Children filter does not also apply here.
		where = append(where, "parent_task_id = ?")
		args = append(args, f.ParentID)
	case f.Children == ChildrenExclude:
		where = append(where, "parent_task_id IS NULL")
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
	// Lanes of one parent read in merge order — the order the join will
	// merge them, which is what someone drilling into a fan-out is looking
	// at. Everything else is newest first.
	if f.ParentID != 0 {
		q += " ORDER BY lane_order ASC, id ASC"
	} else {
		q += " ORDER BY id DESC"
	}
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
			workflow_snapshot = ?, base_branch = ?, branch_name = ?, worktree_path = ?, base_sha = ?,
			priority = ?, agent_override = ?, model_override = ?, effort_override = ?,
			state = ?, current_step = ?, block_reason = ?,
			admit_not_before = ?, queued_reason = ?,
			updated_at = ?, started_at = ?, finished_at = ?, archived_at = ?
		WHERE id = ?`,
		t.Title, t.Description, fields, t.WorkflowName,
		t.WorkflowSnapshot, t.BaseBranch, t.BranchName, nullString(t.WorktreePath), nullString(t.BaseSHA),
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
// current slot count and cap, plus how many step runs it still has open.
//
// Ordering and both caps come from SQL, but the walk itself is the caller's:
// admitting a task changes the tallies, and a single statement cannot see
// its own in-flight admissions.
func (s *Store) ListAdmissible(ctx context.Context) ([]Candidate, error) {
	// G202: both interpolations are package-internal and value-free —
	// prefixed() alias-qualifies the taskColumns constant, slotPlaceholders is
	// placeholders(). The states themselves bind as arguments.
	//nolint:gosec // G202: see above; no caller value reaches the query text
	q := `SELECT ` + prefixed("t", taskColumns) + `,
			(SELECT COUNT(*) FROM tasks o
			  WHERE o.project_id = t.project_id AND o.state IN ` + slotPlaceholders + `),
			p.max_parallel_tasks,
			(SELECT COUNT(*) FROM step_runs r WHERE r.task_id = t.id AND r.state = ?)
		FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.state = ?
		ORDER BY t.priority DESC, t.created_at ASC, t.id ASC`
	args := append(append([]any{}, slotStates...), string(StepRunning), string(TaskQueued))
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
		t, err := scanTask(scannerWithTail(rows, &c.ProjectSlots, &limit, &c.OpenStepRuns))
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

// unreconcilableStates is the set of task states in which no step run can be
// live, so a `running` one is a contradiction rather than a race (§12.4).
//
// It is deliberately narrow. `queued` is the shape issue #142 produced —
// recovery re-queueing a task whose previous attempt it could not finalize —
// and the three settled states are the same contradiction after the fact.
// The waiting states are left out because a `running` row is *correct* in
// them: `awaiting_input` has a live process waiting for an answer (§7.4), and
// an `awaiting_gate` task's manual row is written open by an actor that then
// exits (§6). Reporting those would be reporting normal operation.
var unreconcilableStates, unreconcilablePlaceholders = func() ([]any, string) {
	args := []any{
		string(TaskQueued), string(TaskDone), string(TaskAborted), string(TaskArchived),
	}
	return args, placeholders(len(args))
}()

// UnreconciledTasks returns every task whose state and step runs contradict
// each other, ordered by id. It is `GET /v1/doctor`'s view of the §12.4
// invariant (§17): the combination is impossible, so any row here means a
// task was never reconciled and the scheduler is refusing to admit it.
func (s *Store) UnreconciledTasks(ctx context.Context) ([]Unreconciled, error) {
	args := append([]any{string(StepRunning)}, unreconcilableStates...)
	//nolint:gosec // G202: unreconcilablePlaceholders is placeholders(); states bind
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.state, COUNT(r.id)
		FROM tasks t JOIN step_runs r ON r.task_id = t.id AND r.state = ?
		WHERE t.state IN `+unreconcilablePlaceholders+`
		GROUP BY t.id, t.state
		ORDER BY t.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list unreconciled tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Unreconciled
	for rows.Next() {
		var (
			u     Unreconciled
			state string
		)
		if err := rows.Scan(&u.TaskID, &state, &u.OpenStepRuns); err != nil {
			return nil, fmt.Errorf("scan unreconciled task: %w", err)
		}
		u.State = TaskState(state)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unreconciled tasks: %w", err)
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

// CountTasksByState returns how many tasks sit in each lifecycle state,
// archived included — the §17 tally doctor prints so "12 blocked" is visible
// without opening the board (task 006).
//
// States with no rows are absent from the map rather than zero: the caller
// renders the §6 state list, so the query stays a plain GROUP BY and the
// vocabulary lives in one place (internal/taskstate).
func (s *Store) CountTasksByState(ctx context.Context) (map[TaskState]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM tasks GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("count tasks by state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	counts := map[TaskState]int{}
	for rows.Next() {
		var (
			state string
			n     int
		)
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("scan task state count: %w", err)
		}
		counts[TaskState(state)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task state counts: %w", err)
	}
	return counts, nil
}

// LiveTaskIDs returns the ids of every non-archived task — the set that tells
// a worktree directory apart from an orphan (task 006 decision 3: an orphan
// is a directory under {data_dir}/worktrees/ whose {task_id} matches no
// non-archived task).
func (s *Store) LiveTaskIDs(ctx context.Context) (map[int64]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks WHERE state != ?`, string(TaskArchived))
	if err != nil {
		return nil, fmt.Errorf("list live tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	live := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan live task id: %w", err)
		}
		live[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate live tasks: %w", err)
	}
	return live, nil
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
		t                              Task
		fields                         string
		worktree, baseSHA, blockReason sql.NullString
		agentOv, modelOv, effortOv     sql.NullString
		retryCursor, pendingOv         sql.NullString
		pendingRepair, pendingFollowUp sql.NullString
		pendingInput                   sql.NullString
		admitNotBefore, queuedWhy      sql.NullString
		parentID, parentStep           sql.NullInt64
		laneID                         sql.NullString
		laneOrder                      sql.NullInt64
		githubIssue, githubPull        sql.NullString
		workflowOrigin                 sql.NullString
		createdBy                      sql.NullInt64
		created, updated               string
		started, finished, archived    sql.NullString
	)
	if err := r.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &fields, &t.WorkflowName,
		&t.WorkflowSnapshot, &t.BaseBranch, &t.BranchName, &worktree, &baseSHA, &t.Priority,
		&agentOv, &modelOv, &effortOv,
		(*string)(&t.State), &t.CurrentStep, &blockReason,
		&t.PauseRequested, &retryCursor, &pendingOv,
		&pendingRepair, &pendingFollowUp, &pendingInput, &admitNotBefore, &queuedWhy,
		&parentID, &parentStep, &laneID, &laneOrder, &githubIssue, &githubPull, &workflowOrigin,
		&createdBy,
		&created, &updated, &started, &finished, &archived); err != nil {
		return nil, err
	}
	if parentID.Valid {
		id := parentID.Int64
		t.ParentTaskID = &id
	}
	if parentStep.Valid {
		idx := int(parentStep.Int64)
		t.ParentStepIndex = &idx
	}
	if createdBy.Valid {
		id := createdBy.Int64
		t.CreatedByTaskID = &id
	}
	t.LaneID = laneID.String
	t.LaneOrder = int(laneOrder.Int64)
	t.WorktreePath = worktree.String
	t.BaseSHA = baseSHA.String
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
	if pendingRepair.Valid && pendingRepair.String != "" {
		var req RepairRequest
		if err := json.Unmarshal([]byte(pendingRepair.String), &req); err != nil {
			return nil, fmt.Errorf("pending_repair_json: %w", err)
		}
		t.PendingRepair = &req
	}
	if pendingFollowUp.Valid && pendingFollowUp.String != "" {
		var req FollowUpRequest
		if err := json.Unmarshal([]byte(pendingFollowUp.String), &req); err != nil {
			return nil, fmt.Errorf("pending_follow_up_json: %w", err)
		}
		t.PendingFollowUp = &req
	}
	if githubIssue.Valid && githubIssue.String != "" {
		var issue github.Issue
		if err := json.Unmarshal([]byte(githubIssue.String), &issue); err != nil {
			return nil, fmt.Errorf("github_issue_json: %w", err)
		}
		t.GitHubIssue = &issue
	}
	if githubPull.Valid && githubPull.String != "" {
		var link github.PullLink
		if err := json.Unmarshal([]byte(githubPull.String), &link); err != nil {
			return nil, fmt.Errorf("github_pull_json: %w", err)
		}
		t.GitHubPull = &link
	}
	// A NULL column stays nil rather than becoming a zero-valued origin: "not
	// recorded" and "recorded as an empty scope" are different claims, and only
	// the first one is true of a pre-0017 row (task 043 decision 4).
	if workflowOrigin.Valid && workflowOrigin.String != "" {
		var origin WorkflowOrigin
		if err := json.Unmarshal([]byte(workflowOrigin.String), &origin); err != nil {
			return nil, fmt.Errorf("workflow_origin_json: %w", err)
		}
		t.WorkflowOrigin = &origin
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

// marshalGitHubIssue renders a task's linked issue for storage; no issue is
// SQL NULL, the same shape marshalFollowUp uses for its column (task 035
// decision 3).
func marshalGitHubIssue(issue *github.Issue) (any, error) {
	if issue == nil || issue.Zero() {
		return nil, nil
	}
	b, err := json.Marshal(issue)
	if err != nil {
		return nil, fmt.Errorf("marshal github issue: %w", err)
	}
	return string(b), nil
}

// marshalGitHubPull renders a task's pull-request link for storage. A nil
// link is SQL NULL; a suppressed one is **not**, because a human's refusal is
// exactly what has to survive the next reconciler tick (task 052).
func marshalGitHubPull(link *github.PullLink) (any, error) {
	if link == nil || (link.Number == 0 && !link.Suppressed) {
		return nil, nil
	}
	b, err := json.Marshal(link)
	if err != nil {
		return nil, fmt.Errorf("marshal github pull link: %w", err)
	}
	return string(b), nil
}

// marshalWorkflowOrigin renders a task's workflow provenance for storage; no
// origin is SQL NULL, the same shape marshalGitHubIssue uses for its column
// (task 043 decision 4).
func marshalWorkflowOrigin(origin *WorkflowOrigin) (any, error) {
	if origin == nil || origin.Scope == "" {
		return nil, nil
	}
	b, err := json.Marshal(origin)
	if err != nil {
		return nil, fmt.Errorf("marshal workflow origin: %w", err)
	}
	return string(b), nil
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
