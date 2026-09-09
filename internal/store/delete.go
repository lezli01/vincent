package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lezli01/vincent/internal/chatstate"
)

// Durable event types for a permanent delete (§13.3, task 092). PR D's ruling
// that an archive needs no type of its own — it is `task.state_changed` with
// `to: archived` — does not reach these: a delete has no state to change to,
// so without a type no other client ever learns the row is gone.
//
// The event outlives the row it records, exactly as `project.deleted` does:
// the events table has no foreign keys, and the historical rows behind the
// delete are deliberately left in place (task 092 decision, §17).
const (
	EventTaskDeleted = "task.deleted"
	EventChatDeleted = "chat.deleted"
)

// DeleteRefusedError is a permanent delete refused by a rule of its own rather
// than by the FSM (task 092 decision 2). Delete is not a §6 action — taskstate
// has no opinion on it and it never appears in `available_actions` — so the
// refusals are named here and mapped to a 409 by the API, the way
// `DELETE /v1/projects/{id}` already is.
//
// Reason is the snake_case vocabulary the API puts in `details`; Message is
// what a human reads. Each one names the row that is holding on, because
// "cannot delete" without the holder is not something a caller can act on.
type DeleteRefusedError struct {
	// Kind is "task" or "chat" — the thing that was not deleted.
	Kind string
	// ID is that thing's id.
	ID int64
	// Reason is one of the DeleteRefused* constants.
	Reason string
	// Message names the row that is holding on.
	Message string
}

func (e *DeleteRefusedError) Error() string { return e.Message }

// Reasons a permanent delete is refused (§13.2). They are snake_case for the
// reason every other reason vocabulary in the system is: a `details` value
// means the same thing wherever it originated.
const (
	// DeleteRefusedNotArchived: the row is still live. Delete applies to
	// archived rows only, and is never performed on anything else.
	DeleteRefusedNotArchived = "not_archived"
	// DeleteRefusedHasLanes: an archived fan-out parent whose lane rows still
	// exist. `tasks.parent_task_id` is a plain REFERENCES with no ON DELETE
	// clause (0007_fan_out.sql) and PRAGMA foreign_keys is on, so without this
	// guard the delete fails in the driver rather than refusing. The lanes go
	// first.
	DeleteRefusedHasLanes = "has_lanes"
	// DeleteRefusedHandedOff: a `handed_off` chat. The task named by
	// handoff_task_id owns the worktree and branch (task 074 decision 5), and
	// `handed_off` means "that task owns it" — a deleted row cannot say that.
	DeleteRefusedHandedOff = "handed_off"
	// DeleteRefusedHandoffTarget: the mirror of it — an archived task that a
	// `handed_off` chat points at. `chats.handoff_task_id` is ON DELETE SET
	// NULL (0023_chat_handoff.sql), so deleting the task would leave a
	// terminal chat pointing at nothing.
	DeleteRefusedHandoffTarget = "handoff_target"
)

// DeleteTaskCascade hard-deletes an archived task and its step_runs in one
// transaction, appending a durable `task.deleted` event after the delete
// (§13.2, §13.3, task 092). DeleteProjectCascade is the shape it copies.
//
// Returns ErrNotFound when the row does not exist and *DeleteRefusedError for
// each of the three refusals it owns; nothing is deleted in either case. The
// two remaining references clear themselves and need no refusal:
// `tasks.created_by_task_id` is ON DELETE SET NULL (MCP provenance, so
// `mcp.max_depth`'s walk simply stops one link early) and the idempotency rows
// are ON DELETE CASCADE.
//
// The `events` rows are deliberately *not* purged. A project delete purges
// them because the project's entire history is going and every row would
// afterwards reference nothing at all; deleting one archived task is not that,
// and `id` is the SSE Last-Event-ID cursor every subscriber is holding.
func (s *Store) DeleteTaskCascade(ctx context.Context, id int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var (
		state     string
		projectID int64
		title     string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT state, project_id, title FROM tasks WHERE id = ?`, id).Scan(&state, &projectID, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	if TaskState(state) != TaskArchived {
		return &DeleteRefusedError{
			Kind: "task", ID: id, Reason: DeleteRefusedNotArchived,
			Message: fmt.Sprintf("task %d is %s; only an archived task can be deleted", id, state),
		}
	}
	var lane int64
	if err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(id), 0) FROM tasks WHERE parent_task_id = ?`, id).Scan(&lane); err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	if lane != 0 {
		return &DeleteRefusedError{
			Kind: "task", ID: id, Reason: DeleteRefusedHasLanes,
			Message: fmt.Sprintf("task %d still has fan-out lanes (task %d); delete those first", id, lane),
		}
	}
	var chat int64
	if err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(id), 0) FROM chats WHERE handoff_task_id = ? AND state = ?`,
		id, string(chatstate.HandedOff)).Scan(&chat); err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	if chat != 0 {
		return &DeleteRefusedError{
			Kind: "task", ID: id, Reason: DeleteRefusedHandoffTarget,
			Message: fmt.Sprintf("chat %d was handed off to task %d and would be left pointing at nothing", chat, id),
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM step_runs WHERE task_id = ?`, id); err != nil {
		return fmt.Errorf("delete task %d cascade: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	if err = oneRowAffected(res, fmt.Sprintf("task %d", id)); err != nil {
		return err
	}
	ev, err := deletedEvent(EventTaskDeleted, id, projectID, title)
	if err != nil {
		return err
	}
	if err = appendEventTx(ctx, tx, ev); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	s.notify(ev)
	return nil
}

// DeleteChatCascade hard-deletes an archived chat, appending a durable
// `chat.deleted` event after the delete (§13.2, §13.3, task 092). The turn
// rows go with it through the schema's own cascade (0022_chats.sql), so the
// statement is the row alone.
//
// Returns ErrNotFound when the row does not exist, and *DeleteRefusedError for
// a chat that is not `archived` — which includes `handed_off`: that state
// means the task named by handoff_task_id owns the worktree and branch (task
// 074 decision 5), and a deleted row cannot say so.
//
// A chat's events carry no chat_id column at all — chatEvent puts the id in
// the payload — which is a second reason not to try to purge them.
func (s *Store) DeleteChatCascade(ctx context.Context, id int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete chat %d: %w", id, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var (
		state     string
		projectID int64
		title     string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT state, project_id, title FROM chats WHERE id = ?`, id).Scan(&state, &projectID, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("chat %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete chat %d: %w", id, err)
	}
	switch chatstate.State(state) {
	case chatstate.Archived:
	case chatstate.HandedOff:
		return &DeleteRefusedError{
			Kind: "chat", ID: id, Reason: DeleteRefusedHandedOff,
			Message: fmt.Sprintf(
				"chat %d was handed off to a task, which owns its worktree and branch; that task is what to delete", id),
		}
	default:
		return &DeleteRefusedError{
			Kind: "chat", ID: id, Reason: DeleteRefusedNotArchived,
			Message: fmt.Sprintf("chat %d is %s; only an archived chat can be deleted", id, state),
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete chat %d: %w", id, err)
	}
	if err = oneRowAffected(res, fmt.Sprintf("chat %d", id)); err != nil {
		return err
	}
	ev, err := deletedEvent(EventChatDeleted, id, projectID, title)
	if err != nil {
		return err
	}
	if err = appendEventTx(ctx, tx, ev); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("delete chat %d: %w", id, err)
	}
	s.notify(ev)
	return nil
}

// deletedEvent builds a task.deleted or chat.deleted event. It carries the id
// and the title and nothing more: a client's only use for it is to drop the
// row it is already holding, and the row it names no longer exists to be
// fetched.
//
// TaskID stays nil even for a task: the column is a foreign key, and the whole
// point of this event is that the task it names is gone.
func deletedEvent(evType string, id, projectID int64, title string) (*Event, error) {
	payload, err := json.Marshal(map[string]any{"id": id, "title": title})
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", evType, err)
	}
	pid := projectID
	return &Event{Type: evType, ProjectID: &pid, Payload: payload}, nil
}
