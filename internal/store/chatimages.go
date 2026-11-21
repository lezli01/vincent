package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// ChatImage is one image a client uploaded to a chat (task 170, §5.5). Number
// counts per chat from 1 and is what the composer's `[image N]` chip names;
// Path is the stored file.
type ChatImage struct {
	ChatID    int64
	Number    int
	MediaType string
	Bytes     int64
	Name      string
	Path      string
	CreatedAt time.Time
}

// AddChatImage records an image on a live chat under the next free number.
// pathFor names the file for that number; the caller writes it, and the row
// is committed first so two uploads can never be given one number. A terminal
// chat answers ErrInvalidChatAction.
func (s *Store) AddChatImage(
	ctx context.Context, chatID int64, mediaType, name string, size int64, pathFor func(number int) string,
) (*ChatImage, error) {
	var out *ChatImage
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		if chatstate.Terminal(c.State) {
			return fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(number), 0) + 1 FROM chat_images WHERE chat_id = ?`, chatID).Scan(&n); err != nil {
			return fmt.Errorf("chat %d images: %w", chatID, err)
		}
		now := time.Now()
		img := &ChatImage{
			ChatID: chatID, Number: n, MediaType: mediaType, Bytes: size, Name: name,
			Path: pathFor(n), CreatedAt: now.UTC(),
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_images
			(chat_id, number, media_type, bytes, name, path, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chatID, n, mediaType, size, name, img.Path, formatTime(now)); err != nil {
			return fmt.Errorf("add chat %d image: %w", chatID, err)
		}
		out = img
		return nil
	})
	return out, err
}

// RemoveChatImage drops an image's row: the undo of an AddChatImage whose
// file could not be written.
func (s *Store) RemoveChatImage(ctx context.Context, chatID int64, number int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chat_images WHERE chat_id = ? AND number = ?`, chatID, number)
	if err != nil {
		return fmt.Errorf("remove chat %d image %d: %w", chatID, number, err)
	}
	return nil
}

// ListChatImages returns a chat's images in number order.
func (s *Store) ListChatImages(ctx context.Context, chatID int64) ([]ChatImage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id, number, media_type, bytes, name, path, created_at
		FROM chat_images WHERE chat_id = ? ORDER BY number`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list chat %d images: %w", chatID, err)
	}
	defer func() { _ = rows.Close() }()
	out := []ChatImage{}
	for rows.Next() {
		var (
			img     ChatImage
			created string
		)
		if err := rows.Scan(&img.ChatID, &img.Number, &img.MediaType, &img.Bytes, &img.Name, &img.Path, &created); err != nil {
			return nil, fmt.Errorf("scan chat image: %w", err)
		}
		if img.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}
