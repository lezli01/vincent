package store

import (
	"context"
	"fmt"
)

// GetIssueArchiveCursor returns the id of the last event the auto-archive
// worker processed (task 143.3, migration 0053). Migration 0053 seeds the
// row at the newest event the upgrade found, so it always exists.
func (s *Store) GetIssueArchiveCursor(ctx context.Context) (int64, error) {
	var c int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT cursor FROM issue_archive_cursor WHERE id = 1`).Scan(&c); err != nil {
		return 0, fmt.Errorf("get issue archive cursor: %w", err)
	}
	return c, nil
}

// PutIssueArchiveCursor records that the worker processed every event up to
// and including id. The worker is the only writer, one goroutine, so a
// whole-row upsert cannot lose a concurrent update.
func (s *Store) PutIssueArchiveCursor(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO issue_archive_cursor (id, cursor) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET cursor = excluded.cursor`, id); err != nil {
		return fmt.Errorf("put issue archive cursor: %w", err)
	}
	return nil
}

// IssueHandoffClaimed reports whether a handoff item on issueID still claims
// it — its outcome is unresolved or waiting (HandoffOutcome.Claims), the
// condition idx_handoff_items_claim indexes. The auto-archive worker holds a
// claimed issue's tasks (task 143 decision 10): a handoff `return` after a
// reopen follows up the issue's latest main task, which archiving would
// refuse.
func (s *Store) IssueHandoffClaimed(ctx context.Context, issueID int64) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM handoff_items hi WHERE hi.issue_id = ? AND `+claimCond+`)`,
		issueID).Scan(&n); err != nil {
		return false, fmt.Errorf("read issue %d handoff claim: %w", issueID, err)
	}
	return n != 0, nil
}
