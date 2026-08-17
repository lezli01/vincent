package store

// The fan-out subtree (task 014, spec §7.6/§13.2). A parent parked in
// `awaiting_children` needs two questions answered about work that is not on
// its own row: is everything settled, and what is a human being asked for.
//
// Both are answered by walking `parent_task_id` with one recursive CTE, and
// neither is stored. A denormalized counter would be a second truth that
// drifts from the rows it counts, and this daemon's whole shape is one truth
// (decision 13). The walk is bounded by `fan_out.max_depth`, enforced when
// the tree is created.

import (
	"context"
	"fmt"

	"github.com/lezli01/vincent/internal/taskstate"
)

// ChildrenRollup summarizes a task's descendants: how many are in each state,
// and the ids of the ones a human has to do something about.
//
// Total counts every descendant at any depth, not just direct lanes — a root
// whose lane fanned out again is still waiting on the whole subtree.
type ChildrenRollup struct {
	Total int
	// ByState counts descendants per state. States with no descendants are
	// absent rather than zero, so a client can range over what is there.
	ByState map[TaskState]int
	// Blocked and AwaitingGate are the descendants holding the join open on a
	// human. Ids, not objects: §13.3's convention is that a client re-fetches
	// what it decides it needs.
	Blocked      []int64
	AwaitingGate []int64
	// Settled is how many descendants have reached a state the join can
	// proceed from (§6 `Settled`). The parent may resume when it equals
	// Total.
	Settled int
}

// Done reports whether every descendant has settled — the condition the
// scheduler re-queues a parked parent on (decision 25).
//
// A subtree with no descendants at all is done: a fan_out step that spawned
// nothing has nothing to wait for.
func (r ChildrenRollup) Done() bool { return r.Settled == r.Total }

// ChildrenOf returns the rollup for one task's whole subtree.
//
// The CTE walks children rather than ancestors because that is the direction
// the index runs (idx_tasks_parent). Archived descendants are counted like
// any other: an archived lane has certainly settled, and excluding them would
// make Total disagree with the rows a client can list.
func (s *Store) ChildrenOf(ctx context.Context, taskID int64) (ChildrenRollup, error) {
	out := ChildrenRollup{ByState: map[TaskState]int{}}
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE subtree(id, state) AS (
			SELECT id, state FROM tasks WHERE parent_task_id = ?
			UNION ALL
			SELECT t.id, t.state FROM tasks t JOIN subtree ON t.parent_task_id = subtree.id
		)
		SELECT id, state FROM subtree`, taskID)
	if err != nil {
		return out, fmt.Errorf("children of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return out, fmt.Errorf("scan subtree row: %w", err)
		}
		st := TaskState(state)
		out.Total++
		out.ByState[st]++
		switch st {
		case TaskBlocked:
			out.Blocked = append(out.Blocked, id)
		case TaskAwaitingGate:
			out.AwaitingGate = append(out.AwaitingGate, id)
		}
		if taskstate.Settled(st) {
			out.Settled++
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("children of task %d: %w", taskID, err)
	}
	return out, nil
}

// FanOutAncestors returns the ids of every ancestor of a task, nearest first.
//
// It exists for `task.children_changed` (decision 14): a descendant's
// transition has to reach every fan-out ancestor's rollup, and the per-task
// SSE stream filters on task_id alone, so the event is emitted once per
// ancestor rather than the subscription growing a subtree test.
func (s *Store) FanOutAncestors(ctx context.Context, taskID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE ancestors(id, parent_task_id, depth) AS (
			SELECT id, parent_task_id, 0 FROM tasks WHERE id = ?
			UNION ALL
			SELECT t.id, t.parent_task_id, a.depth + 1
				FROM tasks t JOIN ancestors a ON t.id = a.parent_task_id
		)
		SELECT id FROM ancestors WHERE depth > 0 ORDER BY depth ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("ancestors of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan ancestor: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ancestors of task %d: %w", taskID, err)
	}
	return out, nil
}

// ListChildren returns one task's direct lanes in merge order. The join reads
// them this way, and so does a client drilling into a parent.
func (s *Store) ListChildren(ctx context.Context, parentID int64) ([]Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE parent_task_id = ? ORDER BY lane_order ASC, id ASC`, parentID)
}

// NonTerminalDescendants returns the ids of descendants that have not settled
// — what a cascading cancel walks and what archive refuses on (decision 11).
// Depth-first, deepest first, so a caller cancelling in order never cancels a
// parent before its own children.
func (s *Store) NonTerminalDescendants(ctx context.Context, taskID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE subtree(id, state, depth) AS (
			SELECT id, state, 1 FROM tasks WHERE parent_task_id = ?
			UNION ALL
			SELECT t.id, t.state, subtree.depth + 1
				FROM tasks t JOIN subtree ON t.parent_task_id = subtree.id
		)
		SELECT id, state FROM subtree ORDER BY depth DESC, id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("descendants of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, fmt.Errorf("scan descendant: %w", err)
		}
		if !taskstate.Settled(TaskState(state)) {
			out = append(out, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("descendants of task %d: %w", taskID, err)
	}
	return out, nil
}
