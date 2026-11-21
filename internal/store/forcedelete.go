package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Force delete (task 153): the escape hatch for a row that cannot reach
// `archived`, or that a permanent delete refuses, because something outside
// the database — usually git — will not cooperate. It is the one path that
// removes a task or a chat from a state other than archived, and it
// deliberately relaxes task 092 decision 2 ("archived rows only") for an
// explicit `force`. What it never relaxes is a live process: a task holding
// its slot (running, awaiting_input) or a chat mid-turn is refused, because
// the actor that owns it is still writing, and cancelling it is the way out.
//
// The rows go first, in one transaction; the worktree, container and
// transcripts are the caller's to clean afterwards, best-effort, from what
// this returns. That is the ordinary delete's order (a failed unlink must not
// resurrect a row reported gone), and here it is also the point: the cleanup
// is exactly what was failing.

// DeleteRefusedLive is a force delete refused because a row it would remove
// holds a live process: a task in a slot-holding state, or a chat with a turn
// in flight. Cancel it first.
const DeleteRefusedLive = "live_process"

// ForceDeleted is what a force delete removed, for the cleanup the store
// cannot do: each task's worktree and container, and every removed row's
// transcript directory.
type ForceDeleted struct {
	// Tasks are the removed task rows as they were read inside the
	// transaction, deepest lane first and the requested task last.
	Tasks []*Task
	// ChatIDs are the chats that went with them: those linked to a removed
	// task (the schema's cascade) and those handed off to one. Their
	// worktrees were the task's, so only their transcripts are left to clean.
	ChatIDs []int64
}

// ForceDeleteTaskTree permanently removes a task in any state but a
// slot-holding one, together with its whole fan-out subtree and the chats
// tied to it (task 153). It returns ErrNotFound for a missing row and a
// *DeleteRefusedError with DeleteRefusedLive when any task in the subtree, or
// any chat tied to one, holds a live process; nothing is removed then.
//
// The three refusals the ordinary delete owns are not refusals here:
// not_archived is what force is for; a fan-out parent's lanes are removed
// with it, deepest first; and a chat handed off to a removed task is removed
// too — the task owned its worktree and branch (task 074 decision 5), so the
// chat is the history of a workspace that no longer has a row.
func (s *Store) ForceDeleteTaskTree(ctx context.Context, id int64) (*ForceDeleted, error) {
	out := &ForceDeleted{}
	var evs []*Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		tasks, err := taskSubtreeTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			return fmt.Errorf("task %d: %w", id, ErrNotFound)
		}
		for _, t := range tasks {
			if taskstate.HoldsSlot(t.State) {
				return liveTaskRefusal(id, t)
			}
		}
		for _, t := range tasks {
			chats, err := tiedChatsTx(ctx, tx, t.ID)
			if err != nil {
				return err
			}
			for _, c := range chats {
				if chatHoldsProcess(c.state) {
					return liveChatRefusal("task", id, c.id, c.state)
				}
			}
		}
		for _, t := range tasks {
			chatEvs, chatIDs, err := forceDeleteTaskRowTx(ctx, tx, t)
			if err != nil {
				return err
			}
			evs = append(evs, chatEvs...)
			out.ChatIDs = append(out.ChatIDs, chatIDs...)
		}
		out.Tasks = tasks
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notify(evs...)
	return out, nil
}

// ForceDeleteChat permanently removes a chat in any state but one with a
// live turn (task 153), returning the row as it was so the caller can clean
// its worktree and transcripts. It returns ErrNotFound for a missing row and
// a *DeleteRefusedError with DeleteRefusedLive for a running or
// awaiting_input chat.
//
// The issue-chat carrier refusals of the ordinary delete do not apply: an
// issue left with no row naming its main branch cuts a new one for its next
// main task, which is the consequence a force accepts. The branch itself is
// never deleted here, so the work on it survives under its own name.
func (s *Store) ForceDeleteChat(ctx context.Context, id int64) (*Chat, error) {
	var (
		chat *Chat
		ev   *Event
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("force delete chat %d: %w", id, err)
		}
		if chatHoldsProcess(c.State) {
			return liveChatRefusal("chat", id, id, c.State)
		}
		ev, err = deleteChatRowTx(ctx, tx, id, c.ProjectID, c.Title, nullInt64From(c.IssueID))
		chat = c
		return err
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return chat, nil
}

// IssueDeleteHolders returns what holds an issue's ordinary delete (task
// 134.12, task 140 decision 10): its unsettled main- and side-role root
// tasks and its open issue chats, lowest id first. A force delete of the
// issue removes these first (task 153).
func (s *Store) IssueDeleteHolders(ctx context.Context, issueID int64) (taskIDs, chatIDs []int64, err error) {
	settled := settledTaskStates()
	taskIDs, err = s.queryIDs(ctx, `SELECT id FROM tasks
		WHERE issue_id = ? AND parent_task_id IS NULL AND issue_worktree IN ('main', 'side')
		  AND state NOT IN `+placeholders(len(settled))+` ORDER BY id`,
		append([]any{issueID}, settled...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("issue %d holders: %w", issueID, err)
	}
	terminal := terminalChatStateArgs()
	chatIDs, err = s.queryIDs(ctx, `SELECT id FROM chats
		WHERE issue_id = ? AND state NOT IN `+placeholders(len(terminal))+` ORDER BY id`,
		append([]any{issueID}, terminal...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("issue %d holders: %w", issueID, err)
	}
	return taskIDs, chatIDs, nil
}

// ForceDeleteBlocker reports the first of the named tasks (with their
// subtrees and tied chats) and chats that holds a live process, as the
// refusal a force delete of it would return, or nil. An issue's force delete
// asks it before removing anything, so a live holder refuses the whole call
// instead of leaving it half done.
func (s *Store) ForceDeleteBlocker(ctx context.Context, issueID int64, taskIDs, chatIDs []int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, id := range taskIDs {
			tasks, err := taskSubtreeTx(ctx, tx, id)
			if err != nil {
				return err
			}
			for _, t := range tasks {
				if taskstate.HoldsSlot(t.State) {
					return liveIssueRefusal(issueID, liveTaskRefusal(id, t))
				}
				chats, err := tiedChatsTx(ctx, tx, t.ID)
				if err != nil {
					return err
				}
				for _, c := range chats {
					if chatHoldsProcess(c.state) {
						return liveIssueRefusal(issueID, liveChatRefusal("task", id, c.id, c.state))
					}
				}
			}
		}
		for _, id := range chatIDs {
			var state string
			err := tx.QueryRowContext(ctx, `SELECT state FROM chats WHERE id = ?`, id).Scan(&state)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("read chat %d: %w", id, err)
			}
			if chatHoldsProcess(chatstate.State(state)) {
				return liveIssueRefusal(issueID, liveChatRefusal("chat", id, id, chatstate.State(state)))
			}
		}
		return nil
	})
}

// chatHoldsProcess is whether a chat in state has an agent process up.
func chatHoldsProcess(st chatstate.State) bool {
	return st == chatstate.Running || st == chatstate.AwaitingInput
}

func liveTaskRefusal(id int64, t *Task) *DeleteRefusedError {
	msg := fmt.Sprintf("task %d is %s and its agent is still live; cancel it first", t.ID, t.State)
	if t.ID != id {
		msg = fmt.Sprintf("lane task %d of task %d is %s and its agent is still live; cancel it first", t.ID, id, t.State)
	}
	return &DeleteRefusedError{Kind: "task", ID: id, Reason: DeleteRefusedLive, Message: msg}
}

func liveChatRefusal(kind string, id, chatID int64, st chatstate.State) *DeleteRefusedError {
	return &DeleteRefusedError{
		Kind: kind, ID: id, Reason: DeleteRefusedLive,
		Message: fmt.Sprintf("chat %d is %s and its agent is still live; cancel its turn first", chatID, st),
	}
}

// liveIssueRefusal re-addresses a holder's refusal to the issue, keeping the
// holder's words: they name the row to cancel.
func liveIssueRefusal(issueID int64, e *DeleteRefusedError) *DeleteRefusedError {
	return &DeleteRefusedError{Kind: "issue", ID: issueID, Reason: e.Reason, Message: e.Message}
}

// TaskTreeChatIDs lists the chats a delete of task id would take with it:
// those linked to, or handed off to, the task or any lane below it. A caller
// stops what those chats still run — a shell command (task 170) — before the
// rows and the directory it runs in go. A missing task is an empty list.
func (s *Store) TaskTreeChatIDs(ctx context.Context, id int64) ([]int64, error) {
	var out []int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		ids, err := taskSubtreeIDsTx(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, tid := range ids {
			chats, err := tiedChatsTx(ctx, tx, tid)
			if err != nil {
				return err
			}
			for _, c := range chats {
				out = append(out, c.id)
			}
		}
		return nil
	})
	return out, err
}

// taskSubtreeTx reads task id and every lane below it, deepest first, the
// requested task last. An empty result is a missing row.
func taskSubtreeTx(ctx context.Context, tx *sql.Tx, id int64) ([]*Task, error) {
	ids, err := taskSubtreeIDsTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	out := make([]*Task, 0, len(ids))
	for _, v := range ids {
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, v))
		if err != nil {
			return nil, fmt.Errorf("read task %d: %w", v, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// taskSubtreeIDsTx is taskSubtreeTx's ids, read and closed before any row is.
func taskSubtreeIDsTx(ctx context.Context, tx *sql.Tx, id int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE subtree(id, depth) AS (
			SELECT id, 0 FROM tasks WHERE id = ?
			UNION ALL
			SELECT t.id, subtree.depth + 1
				FROM tasks t JOIN subtree ON t.parent_task_id = subtree.id
		)
		SELECT id FROM subtree ORDER BY depth DESC, id DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("subtree of task %d: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan subtree: %w", err)
		}
		ids = append(ids, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("subtree of task %d: %w", id, err)
	}
	return ids, nil
}

// tiedChat is a chat a task's removal takes with it.
type tiedChat struct {
	id        int64
	state     chatstate.State
	handedOff bool
}

// tiedChatsTx lists the chats linked to taskID (task 119) and those handed
// off to it (task 074).
func tiedChatsTx(ctx context.Context, tx *sql.Tx, taskID int64) ([]tiedChat, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, state, linked_task_id IS NULL FROM chats
		WHERE linked_task_id = ? OR (handoff_task_id = ? AND state = ?) ORDER BY id`,
		taskID, taskID, string(chatstate.HandedOff))
	if err != nil {
		return nil, fmt.Errorf("chats of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []tiedChat
	for rows.Next() {
		var (
			c     tiedChat
			state string
		)
		if err := rows.Scan(&c.id, &state, &c.handedOff); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		c.state = chatstate.State(state)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chats of task %d: %w", taskID, err)
	}
	return out, nil
}

// forceDeleteTaskRowTx removes one task row of a force delete: the chats
// handed off to it, its step runs, the row (its linked chats go by the
// schema's cascade), and a task.deleted event chained with the lane event it
// caused. It returns every event appended, in order, and the ids of the chats
// that went.
func forceDeleteTaskRowTx(ctx context.Context, tx *sql.Tx, t *Task) ([]*Event, []int64, error) {
	chats, err := tiedChatsTx(ctx, tx, t.ID)
	if err != nil {
		return nil, nil, err
	}
	var (
		evs     []*Event
		chatIDs []int64
	)
	for _, c := range chats {
		chatIDs = append(chatIDs, c.id)
		if !c.handedOff {
			continue
		}
		var (
			projectID int64
			title     string
			issueID   sql.NullInt64
		)
		if err := tx.QueryRowContext(ctx, `SELECT project_id, title, issue_id FROM chats WHERE id = ?`, c.id).
			Scan(&projectID, &title, &issueID); err != nil {
			return nil, nil, fmt.Errorf("read chat %d: %w", c.id, err)
		}
		ev, err := deleteChatRowTx(ctx, tx, c.id, projectID, title, issueID)
		if err != nil {
			return nil, nil, err
		}
		evs = append(evs, ev)
	}
	watch, err := watchTaskLaneTx(ctx, tx, t.IssueID, t.ParentTaskID, t.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM step_runs WHERE task_id = ?`, t.ID); err != nil {
		return nil, nil, fmt.Errorf("force delete task %d cascade: %w", t.ID, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, t.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("force delete task %d: %w", t.ID, err)
	}
	if err := oneRowAffected(res, fmt.Sprintf("task %d", t.ID)); err != nil {
		return nil, nil, err
	}
	ev, err := deletedEvent(EventTaskDeleted, t.ID, t.ProjectID, t.Title, t.IssueID)
	if err != nil {
		return nil, nil, err
	}
	if err := appendEventTx(ctx, tx, ev); err != nil {
		return nil, nil, err
	}
	if err := watch.followTx(ctx, tx, ev, t.ID); err != nil {
		return nil, nil, err
	}
	return append(evs, ev), chatIDs, nil
}

func nullInt64From(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// queryIDs runs a query whose one column is an id.
func (s *Store) queryIDs(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
