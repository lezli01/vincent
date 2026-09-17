package store

// Chats linked to a task (task 115, spec §5.5, §6, §10).
//
// A linked chat works in its task's worktree and on its branch while the task
// is stopped, and while it is open the task is locked: every §6 action but
// `cancel` is refused. The lock is one fact — a non-terminal chat whose
// `linked_task_id` names the task — and it is checked inside the transaction
// that performs the §6 compare-and-swap, so SQLite's single writer makes the
// check and the write one step. Nothing about it is cached anywhere else.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// EventChatClosed is a linked chat reaching `closed` (task 115, §13.3). It
// carries the linked task's id so a client can re-fetch the task, whose lock
// just lifted, without a second fetch to find which task that is.
const EventChatClosed = "chat.closed"

// TaskLockedError is a §6 action refused because a chat linked to the task is
// open (task 115). The API answers 409 `task_locked_by_chat` with the chat's
// id, which is what the operator closes to lift it.
type TaskLockedError struct {
	TaskID int64
	ChatID int64
}

func (e *TaskLockedError) Error() string {
	return fmt.Sprintf("task %d is locked by chat %d, which works in its worktree; close the chat first",
		e.TaskID, e.ChatID)
}

// AsTaskLocked extracts a *TaskLockedError from err, if that is what it is.
func AsTaskLocked(err error) (*TaskLockedError, bool) {
	var e *TaskLockedError
	ok := errors.As(err, &e)
	return e, ok
}

// ErrTaskHasNoWorktree is a chat refused because the task never got a
// worktree — blocked on `branch_exists`, say, or aborted before admission.
// The daemon does not create one on the chat's behalf: the engine owns
// worktree preparation (task 115).
var ErrTaskHasNoWorktree = errors.New("task has no worktree")

// openLinkedChatSQL finds the chat locking a task. The three terminal states
// are spelled out so the (linked_task_id, state) index answers it.
const openLinkedChatSQL = `SELECT id FROM chats
	WHERE linked_task_id = ? AND state NOT IN (?, ?, ?) ORDER BY id LIMIT 1`

func terminalChatStateArgs() []any {
	return []any{string(chatstate.Archived), string(chatstate.HandedOff), string(chatstate.Closed)}
}

// openLinkedChatTx returns the id of the open chat locking taskID, or 0.
func openLinkedChatTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID int64,
) (int64, error) {
	var id int64
	args := append([]any{taskID}, terminalChatStateArgs()...)
	err := q.QueryRowContext(ctx, openLinkedChatSQL, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open chat of task %d: %w", taskID, err)
	}
	return id, nil
}

// OpenLinkedChatID returns the id of the open chat locking task taskID, or 0
// when the task is not locked.
func (s *Store) OpenLinkedChatID(ctx context.Context, taskID int64) (int64, error) {
	return openLinkedChatTx(ctx, s.db, taskID)
}

// OpenLinkedChatIDs returns task id → open chat id for every locked task: the
// task list's `open_chat_id`, read once per list and turned into a map, the
// way SourceChatIDs is (task 074 decision 2).
func (s *Store) OpenLinkedChatIDs(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT linked_task_id, id FROM chats
		WHERE linked_task_id IS NOT NULL AND state NOT IN (?, ?, ?)`, terminalChatStateArgs()...)
	if err != nil {
		return nil, fmt.Errorf("list open linked chats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]int64)
	for rows.Next() {
		var taskID, chatID int64
		if err := rows.Scan(&taskID, &chatID); err != nil {
			return nil, fmt.Errorf("scan open linked chat: %w", err)
		}
		out[taskID] = chatID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open linked chats: %w", err)
	}
	return out, nil
}

// refuseLockedTx is the lock check every §6 compare-and-swap runs inside its
// own transaction. Only the states a chat can be opened from can be locked,
// so every other transition — admission, the engine's own events — pays
// nothing for it.
func refuseLockedTx(ctx context.Context, tx *sql.Tx, taskID int64, from TaskState) error {
	if !taskstate.Lockable(from) {
		return nil
	}
	chatID, err := openLinkedChatTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if chatID != 0 {
		return &TaskLockedError{TaskID: taskID, ChatID: chatID}
	}
	return nil
}

// RefuseLocked returns a *TaskLockedError when task taskID is locked by an
// open chat. It is the pre-check for an action that has side effects ahead
// of its compare-and-swap — skip's decision row, archive's worktree removal,
// retry's branch rename — which must be refused before they happen. The
// swap's own check is still what makes the refusal race-free.
func (s *Store) RefuseLocked(ctx context.Context, taskID int64) error {
	chatID, err := s.OpenLinkedChatID(ctx, taskID)
	if err != nil {
		return err
	}
	if chatID != 0 {
		return &TaskLockedError{TaskID: taskID, ChatID: chatID}
	}
	return nil
}

// OpenLinkedChat inserts c as a chat linked to task taskID, in one
// transaction that first proves the task is still in state want, has a
// worktree, and has no open chat already (task 115). A task that moved is a
// *StateConflictError; a second chat is a *TaskLockedError.
//
// The chat copies the task's branch, base branch and base SHA as display
// history, the way a handed-off chat keeps them, and stores no worktree path:
// the task keeps the §10 claim.
func (s *Store) OpenLinkedChat(ctx context.Context, taskID int64, want TaskState, c *Chat) error {
	now := time.Now()
	c.CreatedAt, c.UpdatedAt = now, now
	c.State = chatstate.Idle
	c.LinkedTaskID = &taskID
	c.WorktreePath = ""
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, taskID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("task %d: %w", taskID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get task %d: %w", taskID, err)
		}
		if t.State != want {
			return &StateConflictError{TaskID: taskID, Want: want, Got: t.State}
		}
		if chatID, err := openLinkedChatTx(ctx, tx, taskID); err != nil {
			return err
		} else if chatID != 0 {
			return &TaskLockedError{TaskID: taskID, ChatID: chatID}
		}
		if t.WorktreePath == "" {
			return fmt.Errorf("task %d: %w", taskID, ErrTaskHasNoWorktree)
		}
		c.ProjectID = t.ProjectID
		c.Branch, c.BaseBranch, c.BaseSHA = t.BranchName, t.BaseBranch, t.BaseSHA
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chats (project_id, title, state, agent, model, effort, permission_mode,
				branch, base_branch, base_sha, linked_task_id, opening_context, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ProjectID, c.Title, string(c.State), c.Agent, nullString(c.Model), nullString(c.Effort),
			c.PermissionMode, c.Branch, c.BaseBranch, nullString(c.BaseSHA), taskID,
			nullString(c.OpeningContext), formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
		if err != nil {
			return fmt.Errorf("insert linked chat: %w", err)
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("insert linked chat: %w", err)
		}
		ev, err = chatEvent(EventChatCreated, c, nil)
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

// CloseChat moves a linked chat from `idle` to `closed` under the linked
// table and publishes chat.closed (task 115). A chat in any other state, or a
// free chat, is ErrInvalidChatAction: the caller cancels a live turn first.
func (s *Store) CloseChat(ctx context.Context, id int64) (*Chat, error) {
	var (
		out *Chat
		ev  *Event
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", id, err)
		}
		if !chatstate.AllowedFor(c.Linked(), c.State, chatstate.Close) {
			return fmt.Errorf("chat %d: %w", id, ErrInvalidChatAction)
		}
		out, ev, err = closeChatTx(ctx, tx, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return out, nil
}

// closeChatTx writes c as closed and appends its event. It does not consult
// the FSM: CloseChat does, and cancel's close of a locked task closes
// whatever state recovery left the row in, because the turn is already dead.
func closeChatTx(ctx context.Context, tx *sql.Tx, c *Chat) (*Chat, *Event, error) {
	c.State = chatstate.Closed
	c.PendingInput = nil
	c.UpdatedAt = time.Now()
	if _, err := tx.ExecContext(ctx,
		`UPDATE chats SET state = ?, pending_input = NULL, updated_at = ? WHERE id = ?`,
		string(c.State), formatTime(c.UpdatedAt), c.ID); err != nil {
		return nil, nil, fmt.Errorf("close chat %d: %w", c.ID, err)
	}
	ev, err := chatEvent(EventChatClosed, c, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := appendEventTx(ctx, tx, ev); err != nil {
		return nil, nil, err
	}
	return c, ev, nil
}

// closeLinkedChatsTx closes every open chat linked to taskID, returning the
// events to publish after commit. It is `cancel` on a locked task (task 115):
// the chat's close and the task's abort commit together.
func closeLinkedChatsTx(ctx context.Context, tx *sql.Tx, taskID int64) ([]*Event, error) {
	args := append([]any{taskID}, terminalChatStateArgs()...)
	rows, err := tx.QueryContext(ctx, `SELECT `+chatColumns+` FROM chats
		WHERE linked_task_id = ? AND state NOT IN (?, ?, ?) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list open chats of task %d: %w", taskID, err)
	}
	var open []*Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		open = append(open, c)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list open chats of task %d: %w", taskID, err)
	}
	evs := make([]*Event, 0, len(open))
	for _, c := range open {
		_, ev, err := closeChatTx(ctx, tx, c)
		if err != nil {
			return nil, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// linkedChatEventPayload adds linked_task_id to a chat event's payload.
func linkedChatEventPayload(ev *Event, c *Chat) error {
	if c.LinkedTaskID == nil {
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		return fmt.Errorf("marshal %s event: %w", ev.Type, err)
	}
	body["linked_task_id"] = *c.LinkedTaskID
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", ev.Type, err)
	}
	ev.Payload = payload
	return nil
}
