package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// ProjectStats is one project's counts for GET /v1/projects?stats=true
// (§13.2, task 132.1): what a project picker needs to rank and badge a row
// without listing every task, issue and chat it owns.
//
// The task figures cover every non-archived task row, fan-out lanes
// included, so they line up with slots_used (§11's all-rows figure). That is
// why TasksActive is deliberately not Issue.Active's definition, which
// counts root tasks only: an issue is worked by its root task, a project by
// all of them (task 132 decision 21).
type ProjectStats struct {
	// TasksByState counts non-archived tasks per state; a state with no task
	// is absent, and `archived` never appears.
	TasksByState map[taskstate.State]int
	// TasksActive is the tasks not yet settled (taskstate.Settled), lanes
	// included.
	TasksActive int
	// TasksAttention is the tasks waiting on a person (taskstate.NeedsHuman),
	// lanes included. An awaiting_children parent is not one: its lane is.
	TasksAttention int
	// IssuesOpen is the open issues; IssuesOpenImported those of them with a
	// remote link; IssuesActive those with an unsettled root task — the
	// issue store's own Active definition.
	IssuesOpen, IssuesOpenImported, IssuesActive int
	// ChatsLive is the chats not in a terminal state; ChatsAwaitingInput the
	// ones parked on a question, the chat-attention figure kept apart from
	// TasksAttention (spec decision row 29).
	ChatsLive, ChatsAwaitingInput int
	// IssueSync is the stored sync row, nil when the project has none.
	IssueSync *IssueSyncState
	// LastActivityAt is the newest updated_at over the project's tasks,
	// issues and chats, nil when it has none of them.
	LastActivityAt *time.Time
}

// projectStatsPass is one statement of ProjectStats, folding its rows into
// the per-project map.
type projectStatsPass func(ctx context.Context, s *Store, out map[int64]*ProjectStats) error

// projectStatsPasses is the whole of ProjectStats, one statement each. None
// takes a project id: each is a GROUP BY over its table, so the number of
// statements a stats request costs is len(projectStatsPasses) however many
// projects are registered (task 132.1). A pass that loops over projects
// would break that, and is what this shape exists to rule out.
var projectStatsPasses = []projectStatsPass{
	projectTaskStats,
	projectIssueStats,
	projectChatStats,
	projectSyncStats,
	projectLastActivity,
}

// ProjectStats returns every project's counts, keyed by project id. A
// project with nothing to count is absent; a caller reads a missing key as
// the zero ProjectStats, which is the same answer.
func (s *Store) ProjectStats(ctx context.Context) (map[int64]ProjectStats, error) {
	acc := make(map[int64]*ProjectStats)
	for _, pass := range projectStatsPasses {
		if err := pass(ctx, s, acc); err != nil {
			return nil, fmt.Errorf("project stats: %w", err)
		}
	}
	out := make(map[int64]ProjectStats, len(acc))
	for id, st := range acc {
		out[id] = *st
	}
	return out, nil
}

func statsFor(out map[int64]*ProjectStats, id int64) *ProjectStats {
	st, ok := out[id]
	if !ok {
		st = &ProjectStats{}
		out[id] = st
	}
	return st
}

// projectTaskStats counts tasks per project and state. active and attention
// are folded from the same rows in Go, through taskstate, so neither state
// set is spelled out in SQL.
func projectTaskStats(ctx context.Context, s *Store, out map[int64]*ProjectStats) error {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, state, COUNT(*) FROM tasks
		WHERE state != ? GROUP BY project_id, state`, string(taskstate.Archived))
	if err != nil {
		return fmt.Errorf("count tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id    int64
			state string
			n     int
		)
		if err := rows.Scan(&id, &state, &n); err != nil {
			return fmt.Errorf("count tasks: %w", err)
		}
		st := statsFor(out, id)
		if st.TasksByState == nil {
			st.TasksByState = make(map[taskstate.State]int)
		}
		ts := taskstate.State(state)
		st.TasksByState[ts] += n
		if !taskstate.Settled(ts) {
			st.TasksActive += n
		}
		if taskstate.NeedsHuman(ts) {
			st.TasksAttention += n
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("count tasks: %w", err)
	}
	return nil
}

// projectIssueStats counts open issues, the imported ones (a joined remote
// row; a tombstone has no issue_id and never joins), and the active ones by
// issueSelect's definition — an unsettled root task.
func projectIssueStats(ctx context.Context, s *Store, out map[int64]*ProjectStats) error {
	settled := settledTaskStates()
	//nolint:gosec // G202: placeholders() emits bind markers only; every value binds
	q := `SELECT i.project_id, COUNT(*), COUNT(r.id),
			COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM tasks t WHERE t.issue_id = i.id
				AND t.parent_task_id IS NULL AND t.state NOT IN ` + placeholders(len(settled)) + `)
				THEN 1 ELSE 0 END), 0)
		FROM issues i LEFT JOIN issue_remotes r ON r.issue_id = i.id
		WHERE i.state = ? GROUP BY i.project_id`
	rows, err := s.db.QueryContext(ctx, q, append(settled, string(issuestate.Open))...)
	if err != nil {
		return fmt.Errorf("count issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var open, imported, active int
		if err := rows.Scan(&id, &open, &imported, &active); err != nil {
			return fmt.Errorf("count issues: %w", err)
		}
		st := statsFor(out, id)
		st.IssuesOpen, st.IssuesOpenImported, st.IssuesActive = open, imported, active
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("count issues: %w", err)
	}
	return nil
}

// projectChatStats counts chats per project and state; which states are
// live comes from chatstate.
func projectChatStats(ctx context.Context, s *Store, out map[int64]*ProjectStats) error {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, state, COUNT(*) FROM chats GROUP BY project_id, state`)
	if err != nil {
		return fmt.Errorf("count chats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id    int64
			state string
			n     int
		)
		if err := rows.Scan(&id, &state, &n); err != nil {
			return fmt.Errorf("count chats: %w", err)
		}
		cs := chatstate.State(state)
		if chatstate.Terminal(cs) {
			continue
		}
		st := statsFor(out, id)
		st.ChatsLive += n
		if cs == chatstate.AwaitingInput {
			st.ChatsAwaitingInput += n
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("count chats: %w", err)
	}
	return nil
}

// projectSyncStats attaches each project's stored sync row.
func projectSyncStats(ctx context.Context, s *Store, out map[int64]*ProjectStats) error {
	states, err := s.ListIssueSyncStates(ctx)
	if err != nil {
		return err
	}
	for i := range states {
		statsFor(out, states[i].ProjectID).IssueSync = &states[i]
	}
	return nil
}

// projectLastActivity is the newest updated_at over a project's tasks,
// issues and chats. MAX over the TEXT column is chronological because
// TimeFormat is fixed-width. The events table is not consulted: it has no
// project index.
func projectLastActivity(ctx context.Context, s *Store, out map[int64]*ProjectStats) error {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, MAX(at) FROM (
			SELECT project_id, MAX(updated_at) AS at FROM tasks GROUP BY project_id
			UNION ALL SELECT project_id, MAX(updated_at) FROM issues GROUP BY project_id
			UNION ALL SELECT project_id, MAX(updated_at) FROM chats GROUP BY project_id
		) GROUP BY project_id`)
	if err != nil {
		return fmt.Errorf("last activity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var at sql.NullString
		if err := rows.Scan(&id, &at); err != nil {
			return fmt.Errorf("last activity: %w", err)
		}
		t, err := parseTimePtr(at)
		if err != nil {
			return fmt.Errorf("last activity: %w", err)
		}
		if t != nil {
			statsFor(out, id).LastActivityAt = t
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("last activity: %w", err)
	}
	return nil
}
