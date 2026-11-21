package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// The accepted-hunk marks of a chat's diff, and the reject that leaves a note
// in the chat's draft (task 169, #1018, spec §5.5).

// ChatHunkReview is one accepted hunk of a chat's diff: the file's path as
// the patch spells it (always /-separated) and a hash of the hunk's content.
// Nothing invalidates a mark (task 169 decision 2): a hunk whose content
// changed has another hash, and the old mark just stops matching.
type ChatHunkReview struct {
	ChatID     int64
	Path       string
	HunkHash   string
	AcceptedAt time.Time
}

// AcceptChatHunk records the mark. Accepting twice is a no-op that keeps the
// first accepted_at, and an unknown chat is ErrNotFound. A chat in any state
// takes a mark: it is a reader's note about a diff, not something the chat
// does, and the diff of an archived chat can still be read.
func (s *Store) AcceptChatHunk(ctx context.Context, chatID int64, path, hunkHash string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		// Asked first rather than left to the foreign key, whose violation
		// is not an error a caller can tell from any other constraint.
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM chats WHERE id = ?`, chatID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chat_hunk_reviews (chat_id, path, hunk_hash, accepted_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT (chat_id, path, hunk_hash) DO NOTHING`,
			chatID, path, hunkHash, formatTime(time.Now())); err != nil {
			return fmt.Errorf("accept chat %d hunk: %w", chatID, err)
		}
		return nil
	})
}

// UnacceptChatHunk removes the mark. Removing one that is not there — the
// chat's own row included — is not an error.
func (s *Store) UnacceptChatHunk(ctx context.Context, chatID int64, path, hunkHash string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM chat_hunk_reviews WHERE chat_id = ? AND path = ? AND hunk_hash = ?`,
		chatID, path, hunkHash); err != nil {
		return fmt.Errorf("unaccept chat %d hunk: %w", chatID, err)
	}
	return nil
}

// ListChatHunkReviews lists a chat's marks ordered by path, then hunk hash.
// A chat with none, or no such chat, is an empty list.
func (s *Store) ListChatHunkReviews(ctx context.Context, chatID int64) ([]ChatHunkReview, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, hunk_hash, accepted_at FROM chat_hunk_reviews
		 WHERE chat_id = ? ORDER BY path, hunk_hash`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list chat %d hunk reviews: %w", chatID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []ChatHunkReview
	for rows.Next() {
		r := ChatHunkReview{ChatID: chatID}
		var at string
		if err := rows.Scan(&r.Path, &r.HunkHash, &at); err != nil {
			return nil, fmt.Errorf("scan chat hunk review: %w", err)
		}
		if r.AcceptedAt, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list chat %d hunk reviews: %w", chatID, err)
	}
	return out, nil
}

// RecordChatHunkReject rejects a hunk in one transaction: the mark for
// (chatID, path, hunkHash) is deleted if there is one, and edit is handed the
// chat's draft and returns the draft with the reject's note in it, which is
// written exactly as UpdateChatDraft would write it — the same columns and the
// same chat.draft_changed, published after the commit. The note's wording is
// the caller's; the store supplies the transaction, so no client ever sees
// the mark gone without the note or the note without the mark gone.
//
// A reject is never refused for the note's sake (task 169 decision 8). When
// edit returns !ok, when its result is over MaxChatDraftBytes, when the chat
// is terminal (UpdateChatDraft refuses a draft there: nothing could ever send
// it) or when the result is the draft as it already reads, the draft is left
// alone, noteAdded is false and err is nil; the mark is deleted all the same.
// An unknown chat is ErrNotFound. Returns the chat as it now reads.
//
// edit runs inside the transaction, on the store's one connection: it must
// not call back into the store.
func (s *Store) RecordChatHunkReject(
	ctx context.Context, chatID int64, path, hunkHash string, edit func(draft string) (next string, ok bool),
) (chat *Chat, noteAdded bool, err error) {
	var ev *Event
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		chat = c
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM chat_hunk_reviews WHERE chat_id = ? AND path = ? AND hunk_hash = ?`,
			chatID, path, hunkHash); err != nil {
			return fmt.Errorf("reject chat %d hunk: %w", chatID, err)
		}
		if chatstate.Terminal(c.State) {
			return nil
		}
		next, ok := edit(c.Draft)
		if !ok || len(next) > MaxChatDraftBytes || next == c.Draft {
			return nil
		}
		c.Draft = next
		if ev, err = writeChatDraftTx(ctx, tx, c); err != nil {
			return err
		}
		noteAdded = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if ev != nil {
		s.notify(ev)
	}
	return chat, noteAdded, nil
}
