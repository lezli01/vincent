package store

// The base-refresh record: what fetching the base branch and fast-forwarding
// the local one did when a worktree was created (§10, task 099).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// BaseRefresh is what the base-branch fetch and the local fast-forward did when the worktree was created (§10, task 099).
//
// The store does not validate any of its strings: the worktree package decides
// the vocabulary, and the record is persisted verbatim so a restart serves
// exactly what happened.
type BaseRefresh struct {
	Fetch       BaseFetch       `json:"fetch"`
	FastForward BaseFastForward `json:"fast_forward"`
}

// BaseFetch is the fetch half of a BaseRefresh.
//
// Its fields mirror worktree.FetchOutcome — same names, same types, same
// order — so a caller converts with a plain `store.BaseFetch(c.Fetch)`. The
// store cannot import internal/worktree, so that conversion is the whole
// coupling; reordering or retyping a field on either side breaks it at
// compile time, which is the point.
type BaseFetch struct {
	Remote string `json:"remote,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Result string `json:"result"` // fetched | no_upstream | error | disabled
	Error  string `json:"error,omitempty"`
}

// BaseFastForward is the local fast-forward half of a BaseRefresh.
//
// Its fields mirror worktree.FastForwardOutcome in the same order, for the
// same reason BaseFetch's mirror worktree.FetchOutcome: callers convert with
// `store.BaseFastForward(c.FastForward)`.
type BaseFastForward struct {
	Result   string `json:"result"`           // advanced | up_to_date | skipped | not_attempted
	Reason   string `json:"reason,omitempty"` // diverged | local_ahead | checkout_dirty | checkout_busy | error
	Worktree string `json:"worktree,omitempty"`
	Error    string `json:"error,omitempty"`
}

// marshalBaseRefresh encodes r for the `base_refresh` column; nil is NULL.
func marshalBaseRefresh(r *BaseRefresh) (any, error) {
	if r == nil {
		return nil, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal base refresh: %w", err)
	}
	return string(b), nil
}

// unmarshalBaseRefresh decodes the `base_refresh` column. NULL or empty is
// nil, and so is a value that is not valid JSON.
//
// Swallowing the malformed case is deliberate, and unlike the task's other
// JSON columns: those drive the engine, so a bad one must stop the read. This
// one is display-only. Failing the scan over it would make the whole task or
// chat unreadable — unlistable on the board, unrecoverable at startup — to
// protect a line of history nothing acts on.
func unmarshalBaseRefresh(v sql.NullString) *BaseRefresh {
	if !v.Valid || v.String == "" {
		return nil
	}
	var r BaseRefresh
	if err := json.Unmarshal([]byte(v.String), &r); err != nil {
		return nil
	}
	return &r
}

// ClaimTaskWorktree records the worktree a task's first admission created:
// its path, the commit its branch was cut from, and what refreshing the base
// did on the way (§10, task 099). One statement, so the three can never be
// seen apart, and no event — like SetTaskProgress's worktree-only write, it is
// bookkeeping no stream client renders.
//
// It is called only from the claim callback that creates the worktree, which
// is the only place base_sha is written: a task whose worktree already
// existed is never re-recorded. baseSHA "" and refresh nil each write NULL.
// Returns an ErrNotFound-wrapped error when the task does not exist.
func (s *Store) ClaimTaskWorktree(
	ctx context.Context, id int64, path, baseSHA string, refresh *BaseRefresh,
) error {
	refreshJSON, err := marshalBaseRefresh(refresh)
	if err != nil {
		return fmt.Errorf("claim task %d worktree: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET worktree_path = ?, base_sha = ?, base_refresh = ?, updated_at = ?
		WHERE id = ?`,
		nullString(path), nullString(baseSHA), refreshJSON, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("claim task %d worktree: %w", id, err)
	}
	return oneRowAffected(res, fmt.Sprintf("task %d", id))
}

// ClaimChatWorktree is ClaimTaskWorktree for a chat (§10, task 099). It is
// SetChatWorktree plus the refresh record: baseSHA is kept when empty, as
// there, and a nil refresh likewise leaves the stored record alone.
func (s *Store) ClaimChatWorktree(
	ctx context.Context, id int64, path, baseSHA string, refresh *BaseRefresh,
) (*Chat, error) {
	return s.updateChat(ctx, id, "", func(c *Chat) {
		c.WorktreePath = path
		if baseSHA != "" {
			c.BaseSHA = baseSHA
		}
		if refresh != nil {
			c.BaseRefresh = refresh
		}
	})
}
