package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// RemoteIssueComment is one provider comment as the sync read it (task
// 130.16, decision 24): RemoteKey is GitHub's comment id in decimal, the
// times are the provider's own.
type RemoteIssueComment struct {
	RemoteKey            string // GitHub comment id, decimal
	Author, Body         string
	CreatedAt, UpdatedAt time.Time
}

// UpsertRemoteIssueComment mirrors one provider comment onto issueID, keyed
// by (issue_id, remote_key), always as the sync actor (task 130.16, decision
// 24.4). A new key inserts with the provider's times and announces
// issue.comment_added; a stored row whose author, body or updated_at differs
// is updated in place — id and created_at kept — and announces
// issue.comment_updated; an equal row writes nothing and announces nothing,
// so an unchanged re-list is silent. changed reports whether anything was
// written. A comment deleted on the provider is never seen here and its row
// stays (decision 24.1). ErrNotFound for an unknown issue.
func (s *Store) UpsertRemoteIssueComment(ctx context.Context, issueID int64, c RemoteIssueComment) (changed bool, err error) {
	if c.RemoteKey == "" {
		// '' is what a local comment means (IssueComment.RemoteKey), and the
		// unique index does not cover it: an empty key would append forever.
		return false, fmt.Errorf("remote comment on issue %d: empty remote key", issueID)
	}
	created, updated := formatTime(c.CreatedAt), formatTime(c.UpdatedAt)
	err = s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		var projectID int64
		err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id = ?`, issueID).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("issue %d: %w", issueID, ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read issue %d: %w", issueID, err)
		}
		var (
			id                      int64
			author, body, storedUpd string
		)
		err = tx.QueryRowContext(ctx, `
			SELECT id, author, body, updated_at FROM issue_comments
			WHERE issue_id = ? AND remote_key = ?`, issueID, c.RemoteKey).Scan(&id, &author, &body, &storedUpd)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx, `
				INSERT INTO issue_comments (issue_id, author, body, remote_key, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				issueID, c.Author, c.Body, c.RemoteKey, created, updated)
			if err != nil {
				return nil, fmt.Errorf("insert remote comment %s on issue %d: %w", c.RemoteKey, issueID, err)
			}
			if id, err = res.LastInsertId(); err != nil {
				return nil, fmt.Errorf("insert remote comment %s on issue %d: %w", c.RemoteKey, issueID, err)
			}
			changed = true
			return issueEvent(EventIssueCommentAdded, projectID, issueID, issuestate.Sync, map[string]any{"comment_id": id})
		case err != nil:
			return nil, fmt.Errorf("read remote comment %s on issue %d: %w", c.RemoteKey, issueID, err)
		}
		if author == c.Author && body == c.Body && storedUpd == updated {
			return nil, nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_comments SET author = ?, body = ?, updated_at = ? WHERE id = ?`,
			c.Author, c.Body, updated, id); err != nil {
			return nil, fmt.Errorf("update remote comment %s on issue %d: %w", c.RemoteKey, issueID, err)
		}
		changed = true
		return issueEvent(EventIssueCommentUpdated, projectID, issueID, issuestate.Sync, map[string]any{"comment_id": id})
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// IssueIDByRemoteNumber returns the issue whose live remote (issue_id NOT
// NULL) in projectID is provider's issue number in repo; ok is false when
// none is. It serves the comment sync (task 130.16, decision 24.4), which
// must attribute a listed comment to exactly one issue: unlike
// IssueIDsByRemoteNumber it is scoped by repo, so a project whose origin was
// re-pointed never files a comment under the other repo's same number, and
// a tombstone never answers — a comment on a deleted or un-imported issue is
// ignored. GitHub never reuses a number within a repo, so a second live
// row is not expected; should one exist the lowest issue id answers, the
// same deterministic order IssueIDsByRemoteNumber lists in.
func (s *Store) IssueIDByRemoteNumber(ctx context.Context, projectID int64, provider, repo string, number int) (id int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT issue_id FROM issue_remotes
		WHERE project_id = ? AND provider = ? AND repo = ? AND number = ? AND issue_id IS NOT NULL
		ORDER BY issue_id LIMIT 1`, projectID, provider, repo, number).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("look up %s %s#%d: %w", provider, repo, number, err)
	}
	return id, true, nil
}
