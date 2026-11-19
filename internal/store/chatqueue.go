package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// ChatQueuedMessage is a follow-up message sent while a chat's turn was live
// (task 156, spec §5.5). It is not a turn: it becomes one when the runner
// pops it, in the transaction that deletes it, so a message is either a queued
// row or a turn and never both.
type ChatQueuedMessage struct {
	ID        int64
	ChatID    int64
	Position  int
	Prompt    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrQueuedMessageGone is an edit or delete of a queued message that is no
// longer queued: it started (it is a turn now), or it was deleted or cleared.
// The API answers it 409, because the message did exist and the caller's view
// of the queue is stale.
var ErrQueuedMessageGone = errors.New("the message is no longer queued")

// Queue pause reasons that are not a turn failure reason (task 156 decision
// 3). A failed turn pauses the queue with its own fail_reason.
const (
	QueuePauseInterrupted = "interrupted"
	QueuePauseCanceled    = "canceled"
)

const chatQueueColumns = `id, chat_id, position, prompt, created_at, updated_at`

// EnqueueChatMessage appends prompt to chat's queue. It is legal only while
// the chat holds a live turn (`running` or `awaiting_input`): a queued
// message exists only behind one (task 156 decision 1). An idle chat is
// ErrInvalidChatAction, which is the caller's cue to start a turn instead;
// the check and the insert are one transaction, so a turn finishing in the
// same instant either sees this row and drains it or ran first and left the
// chat idle.
func (s *Store) EnqueueChatMessage(ctx context.Context, chatID int64, prompt string) (*ChatQueuedMessage, error) {
	var out *ChatQueuedMessage
	ev, err := s.chatQueueTx(ctx, chatID, func(tx *sql.Tx, c *Chat) error {
		if !chatstate.HoldsProcess(c.State) || !chatstate.AllowedFor(c.Kind(), c.State, chatstate.Send) {
			return fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		var pos int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(position), 0) + 1 FROM chat_queued_messages WHERE chat_id = ?`,
			chatID).Scan(&pos); err != nil {
			return fmt.Errorf("next queue position: %w", err)
		}
		now := time.Now()
		m := &ChatQueuedMessage{ChatID: chatID, Position: pos, Prompt: prompt, CreatedAt: now, UpdatedAt: now}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chat_queued_messages (chat_id, position, prompt, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, chatID, pos, prompt, formatTime(now), formatTime(now))
		if err != nil {
			return fmt.Errorf("queue chat message: %w", err)
		}
		if m.ID, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("queue chat message: %w", err)
		}
		out = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return out, nil
}

// ListChatQueue returns a chat's queued messages, oldest first — the order
// they will run in.
func (s *Store) ListChatQueue(ctx context.Context, chatID int64) ([]ChatQueuedMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatQueueColumns+`
		FROM chat_queued_messages WHERE chat_id = ? ORDER BY position`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list chat queue: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ChatQueuedMessage
	for rows.Next() {
		m, err := scanChatQueued(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat queue: %w", err)
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat queue: %w", err)
	}
	return out, nil
}

// EditChatQueued replaces a queued message's text. A message that already
// started — or was deleted — is ErrQueuedMessageGone.
func (s *Store) EditChatQueued(ctx context.Context, chatID, id int64, prompt string) (*ChatQueuedMessage, error) {
	var out *ChatQueuedMessage
	ev, err := s.chatQueueTx(ctx, chatID, func(tx *sql.Tx, _ *Chat) error {
		now := time.Now()
		res, err := tx.ExecContext(ctx,
			`UPDATE chat_queued_messages SET prompt = ?, updated_at = ? WHERE id = ? AND chat_id = ?`,
			prompt, formatTime(now), id, chatID)
		if err != nil {
			return fmt.Errorf("edit queued message %d: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("queued message %d: %w", id, ErrQueuedMessageGone)
		}
		out, err = scanChatQueued(tx.QueryRowContext(ctx,
			`SELECT `+chatQueueColumns+` FROM chat_queued_messages WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return out, nil
}

// DeleteChatQueued removes one queued message. A message that already started
// is ErrQueuedMessageGone: it is a turn now, and a turn is history.
func (s *Store) DeleteChatQueued(ctx context.Context, chatID, id int64) error {
	ev, err := s.chatQueueTx(ctx, chatID, func(tx *sql.Tx, c *Chat) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM chat_queued_messages WHERE id = ? AND chat_id = ?`, id, chatID)
		if err != nil {
			return fmt.Errorf("delete queued message %d: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("queued message %d: %w", id, ErrQueuedMessageGone)
		}
		return unpauseIfEmptyTx(ctx, tx, c)
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// ClearChatQueue deletes every queued message and lifts the pause (task 156
// decision 3).
func (s *Store) ClearChatQueue(ctx context.Context, chatID int64) error {
	ev, err := s.chatQueueTx(ctx, chatID, func(tx *sql.Tx, c *Chat) error {
		if err := deleteChatQueueTx(ctx, tx, chatID); err != nil {
			return err
		}
		return setQueuePauseTx(ctx, tx, c, false, "")
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// ResumeChatQueue lifts a paused queue. When start is set and the chat is
// idle with messages queued, the oldest is popped into a turn in the same
// transaction and returned — the `idle → running` a send would make, so the
// caller has already checked `max_parallel_chats` exactly as a send does. On
// a live chat the queue simply drains when that turn ends `done`.
func (s *Store) ResumeChatQueue(ctx context.Context, chatID int64, start bool) (*ChatTurn, error) {
	var turn *ChatTurn
	var turnEv *Event
	ev, err := s.chatQueueTx(ctx, chatID, func(tx *sql.Tx, c *Chat) error {
		if chatstate.Terminal(c.State) {
			return fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		if err := setQueuePauseTx(ctx, tx, c, false, ""); err != nil {
			return err
		}
		if !start || c.State != chatstate.Idle {
			return nil
		}
		t, err := popChatQueueTx(ctx, tx, c)
		if err != nil || t == nil {
			return err
		}
		c.State, c.PendingInput, c.UpdatedAt = chatstate.Running, nil, time.Now()
		if _, err := tx.ExecContext(ctx,
			`UPDATE chats SET state = ?, pending_input = NULL, updated_at = ? WHERE id = ?`,
			string(c.State), formatTime(c.UpdatedAt), c.ID); err != nil {
			return fmt.Errorf("update chat %d: %w", chatID, err)
		}
		turn = t
		if turnEv, err = chatEvent(EventChatTurn, c, t); err != nil {
			return err
		}
		return appendEventTx(ctx, tx, turnEv)
	})
	if err != nil {
		return nil, err
	}
	s.notify(turnEv, ev)
	return turn, nil
}

// PauseChatQueue pauses a chat's queue with reason when it holds any message.
// It is recovery's half of task 156 decision 3: a turn finalized
// `interrupted` leaves the rest of the queue paused, so a restart drains
// nothing on its own.
func (s *Store) PauseChatQueue(ctx context.Context, chatID int64, reason string) error {
	ev, err := s.chatQueueTx(ctx, chatID, func(tx *sql.Tx, c *Chat) error {
		if c.QueuedCount == 0 || c.QueuePaused {
			return errNoQueueChange
		}
		return setQueuePauseTx(ctx, tx, c, true, reason)
	})
	if errors.Is(err, errNoQueueChange) {
		return nil
	}
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// errNoQueueChange rolls back a queue transaction that had nothing to do, so
// no event is written for it.
var errNoQueueChange = errors.New("no queue change")

// FinishChatTurn records a turn's ending and decides, in the same
// transaction, what the chat does next (task 156 decisions 1 and 3):
//
//   - a `done` turn with messages queued and the queue not paused pops the
//     oldest into a new turn, returned, and the chat stays `running` — it
//     never leaves its `max_parallel_chats` slot, so no other chat can take
//     it in between (`running → running` is §5.5's TurnStarted);
//   - any other ending returns the chat to `idle`, and pauses a non-empty
//     queue with pauseReason.
//
// It also stores the turn's session id on the chat, as the next turn resumes
// it. A chat already terminal (a close raced the turn's ending) stays
// terminal and drains nothing.
func (s *Store) FinishChatTurn(ctx context.Context, t *ChatTurn, pauseReason string) (*ChatTurn, error) {
	var (
		next   *ChatTurn
		events []*Event
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := updateChatTurnTx(ctx, tx, t); err != nil {
			return err
		}
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, t.ChatID))
		if err != nil {
			return fmt.Errorf("get chat %d: %w", t.ChatID, err)
		}
		ev, err := chatEvent(EventChatTurn, c, t)
		if err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, ev); err != nil {
			return err
		}
		events = append(events, ev)
		if t.SessionID != "" && t.SessionID != c.SessionID {
			c.SessionID = t.SessionID
			if _, err := tx.ExecContext(ctx, `UPDATE chats SET session_id = ? WHERE id = ?`,
				t.SessionID, c.ID); err != nil {
				return fmt.Errorf("store chat %d session: %w", c.ID, err)
			}
		}
		if chatstate.Terminal(c.State) {
			return nil
		}
		if t.State == chatstate.TurnDone && !c.QueuePaused && c.QueuedCount > 0 {
			nt, err := popChatQueueTx(ctx, tx, c)
			if err != nil {
				return err
			}
			c.State, c.PendingInput, c.UpdatedAt = chatstate.Running, nil, time.Now()
			if _, err := tx.ExecContext(ctx,
				`UPDATE chats SET state = ?, pending_input = NULL, updated_at = ? WHERE id = ?`,
				string(c.State), formatTime(c.UpdatedAt), c.ID); err != nil {
				return fmt.Errorf("update chat %d: %w", c.ID, err)
			}
			next = nt
			for _, mk := range []func() (*Event, error){
				func() (*Event, error) { return chatEvent(EventChatTurn, c, nt) },
				func() (*Event, error) { return chatQueueEvent(c) },
			} {
				ev, err := mk()
				if err != nil {
					return err
				}
				if err := appendEventTx(ctx, tx, ev); err != nil {
					return err
				}
				events = append(events, ev)
			}
			return nil
		}
		c.State, c.PendingInput, c.UpdatedAt = chatstate.Idle, nil, time.Now()
		if _, err := tx.ExecContext(ctx,
			`UPDATE chats SET state = ?, pending_input = NULL, updated_at = ? WHERE id = ?`,
			string(c.State), formatTime(c.UpdatedAt), c.ID); err != nil {
			return fmt.Errorf("update chat %d: %w", c.ID, err)
		}
		if ev, err = chatEvent(EventChatState, c, nil); err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, ev); err != nil {
			return err
		}
		events = append(events, ev)
		if t.State != chatstate.TurnDone && c.QueuedCount > 0 && !c.QueuePaused {
			if err := setQueuePauseTx(ctx, tx, c, true, pauseReason); err != nil {
				return err
			}
			if ev, err = chatQueueEvent(c); err != nil {
				return err
			}
			if err := appendEventTx(ctx, tx, ev); err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notify(events...)
	return next, nil
}

// popChatQueueTx turns c's oldest queued message into a running turn, deleting
// the row in the same transaction. It returns nil when the queue is empty.
// The caller moves the chat's state.
func popChatQueueTx(ctx context.Context, tx *sql.Tx, c *Chat) (*ChatTurn, error) {
	m, err := scanChatQueued(tx.QueryRowContext(ctx, `SELECT `+chatQueueColumns+`
		FROM chat_queued_messages WHERE chat_id = ? ORDER BY position LIMIT 1`, c.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // an empty queue is not an error
	}
	if err != nil {
		return nil, fmt.Errorf("pop chat queue: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_queued_messages WHERE id = ?`, m.ID); err != nil {
		return nil, fmt.Errorf("pop chat queue: %w", err)
	}
	c.QueuedCount--
	return insertChatTurnTx(ctx, tx, c, m.Prompt)
}

// deleteChatQueueTx deletes every queued message of chatID. Archive, close
// and hand-off call it in the transaction that makes the chat terminal (task
// 156 decision 6).
func deleteChatQueueTx(ctx context.Context, tx *sql.Tx, chatID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_queued_messages WHERE chat_id = ?`, chatID); err != nil {
		return fmt.Errorf("clear chat %d queue: %w", chatID, err)
	}
	return nil
}

func setQueuePauseTx(ctx context.Context, tx *sql.Tx, c *Chat, paused bool, reason string) error {
	if !paused {
		reason = ""
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE chats SET queue_paused = ?, queue_pause_reason = ? WHERE id = ?`,
		paused, nullString(reason), c.ID); err != nil {
		return fmt.Errorf("set chat %d queue pause: %w", c.ID, err)
	}
	c.QueuePaused, c.QueuePauseReason = paused, reason
	return nil
}

// unpauseIfEmptyTx lifts the pause once the last queued message is gone: a
// paused empty queue would be a card offering to resume nothing.
func unpauseIfEmptyTx(ctx context.Context, tx *sql.Tx, c *Chat) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chat_queued_messages WHERE chat_id = ?`, c.ID).Scan(&n); err != nil {
		return fmt.Errorf("count chat queue: %w", err)
	}
	c.QueuedCount = n
	if n == 0 && c.QueuePaused {
		return setQueuePauseTx(ctx, tx, c, false, "")
	}
	return nil
}

// chatQueueTx runs fn over the chat's row inside one transaction and appends a
// chat.queue_changed event read back after fn, which it returns for the
// caller to publish after the commit.
func (s *Store) chatQueueTx(ctx context.Context, chatID int64, fn func(*sql.Tx, *Chat) error) (*Event, error) {
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		if err := fn(tx, c); err != nil {
			return err
		}
		if c, err = scanChat(tx.QueryRowContext(ctx,
			`SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID)); err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		if ev, err = chatQueueEvent(c); err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	return ev, err
}

// chatQueueEvent is chat.queue_changed: chatEvent plus the queue's count and
// pause, so a client re-renders the badge and the paused card without a
// fetch (§13.3).
func chatQueueEvent(c *Chat) (*Event, error) {
	ev, err := chatEvent(EventChatQueue, c, nil)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatQueue, err)
	}
	body["queued_count"] = c.QueuedCount
	body["queue_paused"] = c.QueuePaused
	if c.QueuePauseReason != "" {
		body["queue_pause_reason"] = c.QueuePauseReason
	}
	if ev.Payload, err = json.Marshal(body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatQueue, err)
	}
	return ev, nil
}

func scanChatQueued(r rowScanner) (*ChatQueuedMessage, error) {
	var m ChatQueuedMessage
	var createdAt, updatedAt string
	if err := r.Scan(&m.ID, &m.ChatID, &m.Position, &m.Prompt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	var err error
	if m.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if m.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}
