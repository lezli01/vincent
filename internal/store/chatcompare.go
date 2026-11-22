package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ChatCompareSide is one side of a compare (task 171): its chat — nil once
// that chat was permanently deleted — the agent it runs on, and the
// permission mode it actually runs under, with ModeNote saying why when that
// is not restricted (decision 7).
type ChatCompareSide struct {
	ChatID   *int64
	Agent    string
	Mode     string
	ModeNote string
}

// ChatCompare pairs the two forks one compare made (task 171 decisions 6-9):
// both cut from SourceChatID before turn Seq, both sent Prompt. KeptChatID
// is the side a human kept, nil until one is.
type ChatCompare struct {
	ID           int64
	ProjectID    int64
	SourceChatID int64
	Seq          int
	Prompt       string
	Left, Right  ChatCompareSide
	KeptChatID   *int64
	CreatedAt    time.Time
}

// Side names which side chatID is, "" when it is neither.
func (c *ChatCompare) Side(chatID int64) string {
	switch {
	case c.Left.ChatID != nil && *c.Left.ChatID == chatID:
		return "left"
	case c.Right.ChatID != nil && *c.Right.ChatID == chatID:
		return "right"
	}
	return ""
}

// CreateChatCompare records a compare's pairing and sets its ID and
// CreatedAt. It publishes nothing: each side announced itself as
// chat.created when it was forked.
func (s *Store) CreateChatCompare(ctx context.Context, c *ChatCompare) error {
	c.CreatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO chat_compares
		(project_id, source_chat_id, seq, prompt,
		 left_chat_id, left_agent, left_mode, left_mode_note,
		 right_chat_id, right_agent, right_mode, right_mode_note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ProjectID, c.SourceChatID, c.Seq, c.Prompt,
		c.Left.ChatID, c.Left.Agent, c.Left.Mode, c.Left.ModeNote,
		c.Right.ChatID, c.Right.Agent, c.Right.Mode, c.Right.ModeNote, formatTime(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("create chat compare: %w", err)
	}
	if c.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("create chat compare: %w", err)
	}
	return nil
}

const chatCompareColumns = `id, project_id, source_chat_id, seq, prompt,
	left_chat_id, left_agent, left_mode, left_mode_note,
	right_chat_id, right_agent, right_mode, right_mode_note, kept_chat_id, created_at`

// GetChatCompare reads one compare, ErrNotFound when there is none.
func (s *Store) GetChatCompare(ctx context.Context, id int64) (*ChatCompare, error) {
	return scanChatCompare(s.db.QueryRowContext(ctx,
		`SELECT `+chatCompareColumns+` FROM chat_compares WHERE id = ?`, id))
}

// GetChatCompareForChat reads the compare chatID is a side of, ErrNotFound
// when it is none. A fork is made by one compare at most, so there is one.
func (s *Store) GetChatCompareForChat(ctx context.Context, chatID int64) (*ChatCompare, error) {
	return scanChatCompare(s.db.QueryRowContext(ctx,
		`SELECT `+chatCompareColumns+` FROM chat_compares
		 WHERE left_chat_id = ? OR right_chat_id = ? ORDER BY id DESC LIMIT 1`, chatID, chatID))
}

func scanChatCompare(row *sql.Row) (*ChatCompare, error) {
	var (
		c                 ChatCompare
		left, right, kept sql.NullInt64
		createdAt         string
	)
	err := row.Scan(&c.ID, &c.ProjectID, &c.SourceChatID, &c.Seq, &c.Prompt,
		&left, &c.Left.Agent, &c.Left.Mode, &c.Left.ModeNote,
		&right, &c.Right.Agent, &c.Right.Mode, &c.Right.ModeNote, &kept, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read chat compare: %w", err)
	}
	c.Left.ChatID, c.Right.ChatID, c.KeptChatID = nullID(left), nullID(right), nullID(kept)
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func nullID(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// SetChatCompareKept records which side a human kept (task 171 decision 8).
// keptChatID must be one of the compare's sides.
func (s *Store) SetChatCompareKept(ctx context.Context, id, keptChatID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE chat_compares SET kept_chat_id = ?
		WHERE id = ? AND (left_chat_id = ? OR right_chat_id = ?)`, keptChatID, id, keptChatID, keptChatID)
	if err != nil {
		return fmt.Errorf("keep chat compare side: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
