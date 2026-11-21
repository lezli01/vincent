package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// ConversationRewind is the conversation half of a chat rewind (task 167
// decision F): every turn from FromSeq on leaves the conversation under
// RewindID, and the chat's next turn is planned by SessionID, ResumeAt and
// Preamble. Draft is what the composer holds afterwards — turn FromSeq's
// prompt, given back to the human. The values are written verbatim: which
// session and anchor the next turn resumes at is the runner's decision, not
// the store's.
type ConversationRewind struct {
	FromSeq   int
	RewindID  string
	SessionID string
	ResumeAt  string
	Preamble  string
	Draft     string
}

// RewindChatConversation applies r to chat chatID in one transaction and
// publishes chat.updated. It changes refs and files nowhere — the worktree
// half of a rewind is the caller's, done before this — and it touches the
// turn rows only to mark them: a rewound turn keeps its prompt, result,
// tokens and cost, so every §17 total still counts what was spent, and the
// next turn's seq continues after it (insertChatTurnTx takes MAX(seq) over
// every turn). A turn an earlier rewind already marked keeps that rewind's
// id and time, so each turn names the rewind that took it out.
//
// The draft is written through writeChatDraftTx, so a composer that follows
// chat.draft_changed sees the prompt come back without reading chat.updated.
// A missing chat is ErrNotFound.
func (s *Store) RewindChatConversation(ctx context.Context, chatID int64, r ConversationRewind) (*Chat, error) {
	var out *Chat
	var events []*Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		now := time.Now()
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_turns SET rewound_at = ?, rewind_id = ?
			WHERE chat_id = ? AND seq >= ? AND rewound_at IS NULL`,
			formatTime(now), r.RewindID, chatID, r.FromSeq); err != nil {
			return fmt.Errorf("rewind chat %d turns: %w", chatID, err)
		}
		c.SessionID, c.ResumeAt, c.PendingPreamble = r.SessionID, r.ResumeAt, r.Preamble
		c.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			UPDATE chats SET session_id = ?, resume_at = ?, pending_preamble = ?, updated_at = ?
			WHERE id = ?`,
			nullString(c.SessionID), nullString(c.ResumeAt), nullString(c.PendingPreamble),
			formatTime(c.UpdatedAt), c.ID); err != nil {
			return fmt.Errorf("rewind chat %d: %w", chatID, err)
		}
		if c.Draft != r.Draft {
			c.Draft = r.Draft
			ev, err := writeChatDraftTx(ctx, tx, c)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		ev, err := chatEvent(EventChatUpdated, c, nil)
		if err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, ev); err != nil {
			return err
		}
		events = append(events, ev)
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.notify(ev)
	}
	return out, nil
}

// SetChatResumePlan writes the plan the chat's next turn runs on — the
// session, the anchor it resumes at, and the seeded preamble it starts a
// fresh session with — and publishes nothing, as SetChatSession does: the
// runner writes it when a turn consumes or replaces the plan, and the turn's
// own chat.turn_changed is what a client follows.
func (s *Store) SetChatResumePlan(ctx context.Context, chatID int64, sessionID, resumeAt, preamble string) (*Chat, error) {
	return s.updateChat(ctx, chatID, "", func(c *Chat) {
		c.SessionID, c.ResumeAt, c.PendingPreamble = sessionID, resumeAt, preamble
	})
}

// ExistingChatIDs reports which of ids still have a chats row. Archived,
// handed-off and closed chats exist; only a permanently deleted one (task
// 092) does not. It is how a fork's ForkedFromChatID, which carries no
// foreign key, is told apart from a dangling one. Every id asked about has
// an entry.
func (s *Store) ExistingChatIDs(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = false
	}
	for batch := range slices.Chunk(ids, chatTurnCountBatch) {
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		if err := s.markExistingChats(ctx, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// markExistingChats sets out[id] for each id in args that has a chats row.
func (s *Store) markExistingChats(ctx context.Context, args []any, out map[int64]bool) error {
	//nolint:gosec // G202: placeholders renders bind markers only, never values.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM chats WHERE id IN `+placeholders(len(args)), args...)
	if err != nil {
		return fmt.Errorf("existing chat ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan chat id: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate existing chat ids: %w", err)
	}
	return nil
}

// ErrPreambleNotPending refuses an edit of a chat's seeded context when there
// is none pending (task 171 decision 4): the chat was not left seeded by a
// rewind or fork, its next turn resumes natively, or a turn has already
// consumed the plan. The API answers it 409.
var ErrPreambleNotPending = errors.New("chat has no pending seeded context to edit")

// MaxPendingPreambleBytes bounds an edited seeded context (task 171
// decision 4). The builder's own output is held to 512 KiB of history plus
// the chat's opening context; an edit may grow it, but not without limit.
const MaxPendingPreambleBytes = 1 << 20

// EditChatPendingPreamble replaces the seeded context the chat's next turn
// will start its fresh session with (task 171 decision 4) and publishes
// chat.updated. It is legal only while the chat is seeded-pending — a
// preamble is pending and no resume anchor is — and its state holds no
// process; otherwise it returns ErrPreambleNotPending and writes nothing.
// Empty text clears the preamble: the next turn then starts a fresh session
// with no earlier context, which is the caller's explicit choice.
func (s *Store) EditChatPendingPreamble(ctx context.Context, chatID int64, text string) (*Chat, error) {
	if len(text) > MaxPendingPreambleBytes {
		return nil, fmt.Errorf("seeded context is %d bytes, over the %d-byte bound", len(text), MaxPendingPreambleBytes)
	}
	return s.updateChatOrRefuse(ctx, chatID, EventChatUpdated, func(c *Chat) error {
		if c.PendingPreamble == "" || c.ResumeAt != "" || chatstate.HoldsProcess(c.State) {
			return ErrPreambleNotPending
		}
		c.PendingPreamble = text
		return nil
	})
}
