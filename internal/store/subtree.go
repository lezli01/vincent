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
	"encoding/json"
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

// EventTaskChildrenChanged tells a fan-out ancestor that something in its
// subtree moved (§13.3, task 014 decision 14). Payload is
// {task_id, child_id, to_state}; a client re-fetches the rollup rather than
// reading state out of the event, which is how §13.3 hands out everything
// else.
const EventTaskChildrenChanged = "task.children_changed"

// EmitChildrenChanged appends one children_changed event per fan-out ancestor
// of childID.
//
// Emitting is what the per-task SSE stream needs: it filters on `task_id = ?`,
// so a root's stream never sees a depth-2 transition and its rollup could not
// update live. Widening that filter to a subtree-membership test was the
// alternative, and it fails because the subtree is not fixed at subscribe
// time — children appear as fan-outs fire — so the subscription would have to
// grow itself by watching task.created from inside the fan-out. The cost here
// is bounded and explicit: at most max_depth extra rows per transition.
//
// A task with no ancestors — every ordinary task — writes nothing.
func (s *Store) EmitChildrenChanged(ctx context.Context, childID int64, toState TaskState) error {
	ancestors, err := s.FanOutAncestors(ctx, childID)
	if err != nil {
		return err
	}
	for _, ancestorID := range ancestors {
		payload, err := json.Marshal(map[string]any{
			"task_id": ancestorID, "child_id": childID, "to_state": string(toState),
		})
		if err != nil {
			return fmt.Errorf("marshal children_changed: %w", err)
		}
		id := ancestorID
		if err := s.AppendEvent(ctx, &Event{
			Type: EventTaskChildrenChanged, TaskID: &id, Payload: payload,
		}); err != nil {
			return fmt.Errorf("append children_changed for %d: %w", ancestorID, err)
		}
	}
	return nil
}

// EventTaskCreatedByChanged does not exist, deliberately: MCP provenance is
// written once at insert and never changes, so there is nothing to emit.

// MCPAncestry returns the chain of tasks that created taskID over MCP, nearest
// creator first (§13.4, task 057 decision 7). The task itself is not in it.
//
// It walks `created_by_task_id` rather than `parent_task_id`, in the ancestor
// direction, which is the one idx_tasks_created_by exists for. The recursion
// is bounded by `mcp.max_depth` at creation, so a chain this walks is a chain
// something already refused to make longer; the LIMIT is the backstop for a
// database that arrived from somewhere else.
func (s *Store) MCPAncestry(ctx context.Context, taskID int64, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE chain(id, creator) AS (
			SELECT id, created_by_task_id FROM tasks WHERE id = ?
			UNION ALL
			SELECT t.id, t.created_by_task_id FROM tasks t JOIN chain ON t.id = chain.creator
		)
		SELECT id FROM chain WHERE id != ? LIMIT ?`, taskID, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("mcp ancestry of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan mcp ancestor: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mcp ancestry of task %d: %w", taskID, err)
	}
	return out, nil
}

// MCPChainSize counts every task in the MCP creation chain rooted at the
// ultimate creator of taskID — ancestors, the task itself, and everything they
// or their descendants created. It is what `mcp.max_tasks` bounds.
func (s *Store) MCPChainSize(ctx context.Context, rootID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		WITH RECURSIVE tree(id) AS (
			SELECT id FROM tasks WHERE id = ?
			UNION
			SELECT t.id FROM tasks t JOIN tree ON t.created_by_task_id = tree.id
		)
		SELECT COUNT(*) FROM tree`, rootID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mcp chain size of task %d: %w", rootID, err)
	}
	return n, nil
}
