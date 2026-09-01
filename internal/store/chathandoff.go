package store

// Handing a chat's worktree and branch to a task (task 074, spec §5.5, §10).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// HandoffChat creates t as the task that adopts chat chatID's workspace, in
// **one** transaction: the task row and its branch claim, the link, the chat's
// transition to `handed_off`, the release of the chat's §10 claim, and both
// durable events.
//
// One transaction is the whole point of the feature, not an implementation
// detail. Split in two it becomes reachable to have a task owning a worktree
// no chat released, or a terminal chat that never produced a task; the
// scheduler could admit the task before it owned a complete workspace, and gc
// could observe the directory claimed twice or not at all.
//
// The chat's state is re-read and required to be `idle` *inside* the
// transaction rather than checked by the caller: the API's read and this write
// are separate statements, and a `send` that lands between them must lose one
// of the two. It loses this one, with ErrInvalidChatAction — the same error,
// and so the same 409, an illegal action anywhere else produces.
//
// `worktree_path` is cleared because that is what transferring the claim means
// concretely: the reclaimer's rule is that the claim decides, and two rows
// naming one directory is exactly the ambiguity it must never see. `branch`,
// `base_branch` and `base_sha` stay on the chat row as history — nothing reads
// them for ownership once the chat is terminal.
//
// t is filled in as insertTaskTx fills it: id, timestamps and, when it needs
// one, its branch name. Its workspace fields are the caller's to have set from
// the chat; the store does not invent them.
func (s *Store) HandoffChat(ctx context.Context, chatID int64, t *Task) (*Chat, error) {
	now := time.Now()
	var chat *Chat
	var taskEv, chatEv *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID)
		c, err := scanChat(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		next, ok := chatstate.Next(c.State, chatstate.HandOff)
		if !ok {
			return fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		// No resolveBranch: a handed-off task's branch is the chat's,
		// verbatim, so there is no name that needs the id. claimBranchTx
		// still runs inside insertTaskTx, so a live task already holding
		// this branch collides here exactly as any other create does.
		if taskEv, err = insertTaskTx(ctx, tx, t, now, nil); err != nil {
			return err
		}
		c.State = next
		c.HandoffTaskID = &t.ID
		c.WorktreePath = ""
		c.PendingInput = nil
		c.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			UPDATE chats SET state = ?, handoff_task_id = ?, worktree_path = NULL,
				pending_input = NULL, updated_at = ?
			WHERE id = ? AND state = ?`,
			string(c.State), t.ID, formatTime(c.UpdatedAt), chatID, string(chatstate.Idle)); err != nil {
			return fmt.Errorf("hand off chat %d: %w", chatID, err)
		}
		if chatEv, err = chatHandoffEvent(c, t.ID); err != nil {
			return err
		}
		chat = c
		return appendEventTx(ctx, tx, chatEv)
	})
	if err != nil {
		return nil, err
	}
	// After the commit, task first: a subscriber that reacts to
	// chat.handed_off by fetching the task must find it.
	s.notify(taskEv)
	s.notify(chatEv)
	return chat, nil
}

// chatHandoffEvent is chatEvent plus the task id — the whole of what a client
// needs to follow the link without a fetch (§13.3).
func chatHandoffEvent(c *Chat, taskID int64) (*Event, error) {
	ev, err := chatEvent(EventChatHandedOff, c, nil)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatHandedOff, err)
	}
	body["handoff_task_id"] = taskID
	if ev.Payload, err = json.Marshal(body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatHandedOff, err)
	}
	return ev, nil
}

// SourceChatIDs returns task id → chat id for every handed-off chat: the
// reverse of the one authoritative foreign key (task 074 decision 2).
//
// It is one indexed query, read once per list and turned into a map, rather
// than a column on `tasks`: a second stored copy is a second thing that can
// disagree, and 063's posture is that no existing task query changes meaning
// when chats exist.
func (s *Store) SourceChatIDs(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT handoff_task_id, id FROM chats WHERE handoff_task_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("list chat handoffs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]int64)
	for rows.Next() {
		var taskID, chatID int64
		if err := rows.Scan(&taskID, &chatID); err != nil {
			return nil, fmt.Errorf("scan chat handoff: %w", err)
		}
		out[taskID] = chatID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat handoffs: %w", err)
	}
	return out, nil
}

// SourceChatID returns the id of the chat task taskID was handed off from, or
// 0 when it was created any other way.
func (s *Store) SourceChatID(ctx context.Context, taskID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM chats WHERE handoff_task_id = ?`, taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("source chat of task %d: %w", taskID, err)
	}
	return id, nil
}
