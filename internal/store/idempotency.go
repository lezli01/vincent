package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrIdempotencyKeyExists reports that the `(method, path, key)` row a create
// tried to write is already there — the concurrent-duplicate case, where two
// requests carrying the same key raced and this one lost. The store's single
// WAL connection serializes the two transactions, so the loser sees the
// primary-key violation, rolls its task insert back, and the API re-reads by
// key and replays the winner's task (task 040).
//
// It is a sentinel rather than a wrapped driver error because that is the only
// thing the caller can act on: every other insert failure is a 500, and this
// one is a 201.
var ErrIdempotencyKeyExists = errors.New("idempotency key already exists")

// IdempotencyKey is one recorded replay-protected request (§13.1, task 040).
// It stores a *reference* to what the request produced, not the response body
// it produced: TaskID names the created task, and a replay re-reads that task
// to render the response.
type IdempotencyKey struct {
	Method     string
	Path       string
	Key        string
	RequestSHA string
	TaskID     int64
	CreatedAt  time.Time
}

// GetIdempotencyKey returns the recorded key for one route, or ErrNotFound
// when the caller has not used this key on this route inside the retention
// window.
func (s *Store) GetIdempotencyKey(ctx context.Context, method, path, key string) (*IdempotencyKey, error) {
	k := IdempotencyKey{Method: method, Path: path, Key: key}
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT request_sha, task_id, created_at FROM idempotency_keys
		WHERE method = ? AND path = ? AND key = ?`,
		method, path, key).Scan(&k.RequestSHA, &k.TaskID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get idempotency key: %w", err)
	}
	k.CreatedAt, err = parseTime(created)
	if err != nil {
		return nil, fmt.Errorf("parse idempotency key created_at: %w", err)
	}
	return &k, nil
}

// insertIdempotencyKeyTx writes the key row inside the caller's transaction —
// the same transaction as the task insert, so the row and the key commit
// together or not at all. That atomicity is the whole point: a key committed
// without its task would replay a task that does not exist, and a task
// committed without its key would be duplicated by the retry the key exists to
// absorb.
func insertIdempotencyKeyTx(ctx context.Context, tx *sql.Tx, k *IdempotencyKey, taskID int64, now time.Time) error {
	if k.CreatedAt.IsZero() {
		k.CreatedAt = now
	}
	k.TaskID = taskID
	_, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_keys (method, path, key, request_sha, task_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		k.Method, k.Path, k.Key, k.RequestSHA, k.TaskID, formatTime(k.CreatedAt))
	if err != nil {
		// The driver reports the primary-key violation as a constraint failure
		// naming the table; matched on the message the way the projects.name
		// clash already is, because modernc's error carries no typed column
		// identity to branch on.
		if strings.Contains(err.Error(), "constraint failed") &&
			strings.Contains(err.Error(), "idempotency_keys") {
			return ErrIdempotencyKeyExists
		}
		return fmt.Errorf("insert idempotency key: %w", err)
	}
	return nil
}

// PruneIdempotencyKeys deletes keys recorded before cutoff, returning how many
// went. The window is fixed at 24 hours by the caller (§17, task 040) rather
// than configured: a key exists to cover a transport retry, which happens in
// seconds, so a day is three orders of magnitude of headroom and a knob here
// would be config surface to document and defend forever for a number nobody
// would tune.
//
// cutoff is a parameter so tests can age rows without sleeping.
func (s *Store) PruneIdempotencyKeys(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < ?`, formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("prune idempotency keys: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune idempotency keys: %w", err)
	}
	return n, nil
}
