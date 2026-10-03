package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// The issue state write-back outbox (task 130.10, spec §12.4, §14). A close
// or reopen of an issue with a live GitHub remote leaves one pending
// set_state row, written in the transaction that changes the state; the
// daemon's drain sends it and settles it. Nothing here calls GitHub.

// Outbox row statuses.
const (
	OutboxPending    = "pending"
	OutboxDone       = "done"
	OutboxFailed     = "failed"
	OutboxConflict   = "conflict"
	OutboxSuperseded = "superseded"
)

// OutboxOpSetState is the one operation the outbox carries in v1: title,
// body, labels and comments are a read-only mirror and are never written
// back (task 130 decision 9).
const OutboxOpSetState = "set_state"

// outboxProvider is the provider whose remotes write back. It is the
// importer's provider column value.
const outboxProvider = "github"

// IssueStateValue is an issue's state in GitHub's terms: State is "open" or
// "closed", Reason GitHub's state_reason for a closed one, and DuplicateOf
// the number of the issue a duplicate names in the same repository, 0 for
// none.
type IssueStateValue struct {
	State       string `json:"state"`
	Reason      string `json:"state_reason,omitempty"`
	DuplicateOf int    `json:"duplicate_of,omitempty"`
}

// Same compares two values the way echo suppression and the drain's
// preflight do (task 130.10): by state, and by reason when closed — never
// by a timestamp, and never by DuplicateOf, which GitHub does not report
// back.
func (v IssueStateValue) Same(o IssueStateValue) bool {
	if v.State != o.State {
		return false
	}
	return v.State != string(issuestate.Closed) || v.Reason == o.Reason
}

// GitHubStateOf maps a local state and close reason onto GitHub's value.
// issuestate's reasons are GitHub's vocabulary already.
func GitHubStateOf(state issuestate.State, reason issuestate.Reason) IssueStateValue {
	if issuestate.Normalize(state) != issuestate.Closed {
		return IssueStateValue{State: string(issuestate.Open)}
	}
	if reason == "" {
		reason = issuestate.Completed
	}
	return IssueStateValue{State: string(issuestate.Closed), Reason: string(reason)}
}

// IssueOutbox is one row of issue_sync_outbox. ProjectID is the issue's.
type IssueOutbox struct {
	ID, IssueID, ProjectID int64
	Op                     string
	Desired, Base          IssueStateValue
	Status                 string
	Attempts               int
	NextAttemptAt          time.Time
	LastReason             string
	Origin                 issuestate.Actor
	CreatedAt, UpdatedAt   time.Time
}

// IssueOutboxCounts is a project's write-back health: issues whose newest
// write is pending, failed or ended in a conflict.
type IssueOutboxCounts struct {
	Pending, Failed, Conflict int
}

// OnIssueOutboxEnqueued registers the one callback a state change that
// enqueued a write runs after its commit — the daemon's drain wakes from
// it. Like OnIssueSyncRequested it runs on the writing goroutine and must
// not block.
func (s *Store) OnIssueOutboxEnqueued(fn func()) { s.outboxEnqueued.Store(&fn) }

func (s *Store) kickOutbox() {
	if fn := s.outboxEnqueued.Load(); fn != nil && *fn != nil {
		(*fn)()
	}
}

// writesBack reports whether a state change of an issue with remote rem is
// written back: a live GitHub remote only. A moved or missing one keeps the
// change local (task 130.10), and a local issue has nowhere to write.
func writesBack(rem *IssueRemote) bool {
	return rem != nil && rem.IssueID != nil && rem.Provider == outboxProvider && rem.Status == RemoteStatusLive
}

// enqueueStateWriteTx records the write-back of cur's move to (to, reason)
// in tx, the transaction making that move. It writes nothing for sync's own
// transitions, for an issue that does not write back, or when the mapped
// GitHub value is the one GitHub already has. A pending row for the issue
// is superseded, and its base carried over: GitHub has not been written
// since, so close→reopen with base open sends nothing at all. Reports
// whether a row was inserted.
func enqueueStateWriteTx(ctx context.Context, tx *sql.Tx, cur *Issue, to issuestate.State, reason issuestate.Reason, duplicateOf *int64, by issuestate.Actor, now time.Time) (bool, error) {
	if by == issuestate.Sync || !writesBack(cur.Remote) {
		return false, nil
	}
	desired := GitHubStateOf(to, reason)
	if duplicateOf != nil {
		var (
			repo   string
			number sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, `
			SELECT repo, number FROM issue_remotes WHERE issue_id = ? AND provider = ? AND remote_status = ''`,
			*duplicateOf, outboxProvider).Scan(&repo, &number)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return false, fmt.Errorf("read duplicate's remote: %w", err)
		case number.Valid && strings.EqualFold(repo, cur.Remote.Repo):
			desired.DuplicateOf = int(number.Int64)
		}
	}
	base := GitHubStateOf(cur.State, cur.CloseReason)
	var baseJSON string
	err := tx.QueryRowContext(ctx, `
		SELECT base_json FROM issue_sync_outbox WHERE issue_id = ? AND status = ? ORDER BY id DESC LIMIT 1`,
		cur.ID, OutboxPending).Scan(&baseJSON)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("read pending write of issue %d: %w", cur.ID, err)
	default:
		if err := json.Unmarshal([]byte(baseJSON), &base); err != nil {
			return false, fmt.Errorf("decode pending write of issue %d: %w", cur.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_sync_outbox SET status = ?, updated_at = ? WHERE issue_id = ? AND status = ?`,
			OutboxSuperseded, formatTime(now), cur.ID, OutboxPending); err != nil {
			return false, fmt.Errorf("supersede pending write of issue %d: %w", cur.ID, err)
		}
	}
	if desired.Same(base) {
		return false, nil
	}
	dj, err := json.Marshal(desired)
	if err != nil {
		return false, fmt.Errorf("encode desired state: %w", err)
	}
	bj, err := json.Marshal(base)
	if err != nil {
		return false, fmt.Errorf("encode base state: %w", err)
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_sync_outbox (issue_id, op, desired_json, base_json, status, next_attempt_at,
			origin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cur.ID, OutboxOpSetState, string(dj), string(bj), OutboxPending, stamp, string(by), stamp, stamp); err != nil {
		return false, fmt.Errorf("enqueue write of issue %d: %w", cur.ID, err)
	}
	return true, nil
}

// hasPendingWriteTx reports whether the issue has a write still to send.
// While it does, an import refresh keeps the local state (task 130.10): the
// drain's preflight, not the importer, decides between the two.
func hasPendingWriteTx(ctx context.Context, tx *sql.Tx, issueID int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM issue_sync_outbox WHERE issue_id = ? AND status = ? LIMIT 1`,
		issueID, OutboxPending).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read pending write of issue %d: %w", issueID, err)
	}
	return true, nil
}

const outboxSelect = `SELECT o.id, o.issue_id, i.project_id, o.op, o.desired_json, o.base_json, o.status,
	o.attempts, o.next_attempt_at, o.last_reason, o.origin, o.created_at, o.updated_at
	FROM issue_sync_outbox o JOIN issues i ON i.id = o.issue_id`

func scanOutbox(r rowScanner) (*IssueOutbox, error) {
	var (
		o                     IssueOutbox
		desired, base, origin string
		next, created, upd    string
	)
	if err := r.Scan(&o.ID, &o.IssueID, &o.ProjectID, &o.Op, &desired, &base, &o.Status, &o.Attempts,
		&next, &o.LastReason, &origin, &created, &upd); err != nil {
		return nil, err
	}
	o.Origin = issuestate.Actor(origin)
	if err := json.Unmarshal([]byte(desired), &o.Desired); err != nil {
		return nil, fmt.Errorf("decode outbox %d desired: %w", o.ID, err)
	}
	if err := json.Unmarshal([]byte(base), &o.Base); err != nil {
		return nil, fmt.Errorf("decode outbox %d base: %w", o.ID, err)
	}
	var err error
	if o.NextAttemptAt, err = parseTime(next); err != nil {
		return nil, err
	}
	if o.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if o.UpdatedAt, err = parseTime(upd); err != nil {
		return nil, err
	}
	return &o, nil
}

func queryOutbox(ctx context.Context, q issueQuerier, query string, args ...any) (out []*IssueOutbox, err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list issue outbox: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("list issue outbox: %w", cerr)
		}
	}()
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			return nil, fmt.Errorf("scan issue outbox: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list issue outbox: %w", err)
	}
	return out, nil
}

// PendingIssueWrites lists the pending writes, oldest due first. dueBy, when
// non-zero, keeps only the rows due by then.
func (s *Store) PendingIssueWrites(ctx context.Context, dueBy time.Time) ([]*IssueOutbox, error) {
	q := outboxSelect + ` WHERE o.status = ?`
	args := []any{OutboxPending}
	if !dueBy.IsZero() {
		q += ` AND o.next_attempt_at <= ?`
		args = append(args, formatTime(dueBy))
	}
	return queryOutbox(ctx, s.db, q+` ORDER BY o.next_attempt_at, o.id`, args...)
}

// IssueWrites lists every outbox row of the issue, oldest first.
func (s *Store) IssueWrites(ctx context.Context, issueID int64) ([]*IssueOutbox, error) {
	return queryOutbox(ctx, s.db, outboxSelect+` WHERE o.issue_id = ? ORDER BY o.id`, issueID)
}

// loadIssueSync fills each issue's Sync with its newest write that was not
// superseded. Local issues are not asked about: they never have one.
func loadIssueSync(ctx context.Context, q issueQuerier, issues []*Issue) error {
	byID := map[int64]*Issue{}
	var ids []any
	for _, iss := range issues {
		if iss.Remote != nil {
			byID[iss.ID] = iss
			ids = append(ids, iss.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := queryOutbox(ctx, q, outboxSelect+` WHERE o.status != ? AND o.issue_id IN `+placeholders(len(ids))+
		` ORDER BY o.id`, append([]any{OutboxSuperseded}, ids...)...)
	if err != nil {
		return err
	}
	for _, o := range rows {
		byID[o.IssueID].Sync = o
	}
	return nil
}

// DeferIssueWrite keeps a pending write pending: reason says why it was not
// sent, next when it is due again, and attempted whether a call was made
// (and so counts as an attempt). A reason that changed announces
// issue.updated {changed: ["sync"]}; the same reason again, tick after tick,
// announces nothing. A row no longer pending is left alone.
func (s *Store) DeferIssueWrite(ctx context.Context, id int64, reason string, next time.Time, attempted bool) error {
	inc := 0
	if attempted {
		inc = 1
	}
	return s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		o, err := getOutboxTx(ctx, tx, id)
		if err != nil || o.Status != OutboxPending {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_sync_outbox SET last_reason = ?, next_attempt_at = ?, attempts = attempts + ?, updated_at = ?
			WHERE id = ?`, reason, formatTime(next), inc, formatTime(time.Now()), id); err != nil {
			return nil, fmt.Errorf("defer issue write %d: %w", id, err)
		}
		if o.LastReason == reason {
			return nil, nil
		}
		return syncChangedEvent(o)
	})
}

// SettleIssueWrite ends a pending write as done, failed or conflict, with
// reason, and announces issue.updated {changed: ["sync"]}. written, for a
// done write, is the value GitHub now holds: a pending successor — the
// human changed their mind while this one was in flight — takes it as its
// base, so its own preflight does not mistake this write for a conflict.
// When there is no successor because the change was undone (close then
// reopen while the close was in flight), a write back to the local state is
// enqueued from the value that landed. A row no longer pending is not
// re-settled; settled reports whether this row changed.
func (s *Store) SettleIssueWrite(ctx context.Context, id int64, status, reason string, written *IssueStateValue) (settled bool, err error) {
	switch status {
	case OutboxDone, OutboxFailed, OutboxConflict:
	default:
		return false, fmt.Errorf("cannot settle an issue write as %q", status)
	}
	enqueued := false
	err = s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		o, err := getOutboxTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if written != nil {
			if enqueued, err = rebaseSuccessorTx(ctx, tx, o, *written); err != nil {
				return nil, err
			}
		}
		if o.Status != OutboxPending {
			return nil, nil
		}
		now := formatTime(time.Now())
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_sync_outbox SET status = ?, last_reason = ?, attempts = attempts + 1, updated_at = ?
			WHERE id = ?`, status, reason, now, id); err != nil {
			return nil, fmt.Errorf("settle issue write %d: %w", id, err)
		}
		settled = true
		return syncChangedEvent(o)
	})
	if err == nil && enqueued {
		s.kickOutbox()
	}
	return settled, err
}

// rebaseSuccessorTx makes written — the value GitHub now holds after o —
// the base of the issue's pending write, or, when o was superseded and left
// none, enqueues the write that brings GitHub back to the local state.
func rebaseSuccessorTx(ctx context.Context, tx *sql.Tx, o *IssueOutbox, written IssueStateValue) (bool, error) {
	bj, err := json.Marshal(written)
	if err != nil {
		return false, fmt.Errorf("encode written state: %w", err)
	}
	now := time.Now()
	res, err := tx.ExecContext(ctx, `
		UPDATE issue_sync_outbox SET base_json = ?, updated_at = ? WHERE issue_id = ? AND status = ? AND id > ?`,
		string(bj), formatTime(now), o.IssueID, OutboxPending, o.ID)
	if err != nil {
		return false, fmt.Errorf("rebase pending write of issue %d: %w", o.IssueID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 || o.Status != OutboxSuperseded {
		return false, err
	}
	cur, err := getIssue(ctx, tx, o.IssueID)
	if err != nil {
		return false, err
	}
	desired := GitHubStateOf(cur.State, cur.CloseReason)
	if !writesBack(cur.Remote) || desired.Same(written) {
		return false, nil
	}
	dj, err := json.Marshal(desired)
	if err != nil {
		return false, fmt.Errorf("encode desired state: %w", err)
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_sync_outbox (issue_id, op, desired_json, base_json, status, next_attempt_at,
			origin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.IssueID, OutboxOpSetState, string(dj), string(bj), OutboxPending, stamp, string(o.Origin), stamp, stamp); err != nil {
		return false, fmt.Errorf("enqueue write of issue %d: %w", o.IssueID, err)
	}
	return true, nil
}

func getOutboxTx(ctx context.Context, tx *sql.Tx, id int64) (*IssueOutbox, error) {
	o, err := scanOutbox(tx.QueryRowContext(ctx, outboxSelect+` WHERE o.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("issue write %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read issue write %d: %w", id, err)
	}
	return o, nil
}

func syncChangedEvent(o *IssueOutbox) (*Event, error) {
	return issueEvent(EventIssueUpdated, o.ProjectID, o.IssueID, issuestate.Sync,
		map[string]any{"changed": []string{"sync"}})
}

// CountIssueWrites reads a project's write-back health: how many issues'
// newest write is pending, failed, or ended in a conflict. A write followed
// by a newer one is history, not health.
func (s *Store) CountIssueWrites(ctx context.Context, projectID int64) (c IssueOutboxCounts, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.status, COUNT(*) FROM issue_sync_outbox o JOIN issues i ON i.id = o.issue_id
		WHERE i.project_id = ? AND o.status IN (?, ?, ?)
			AND NOT EXISTS (SELECT 1 FROM issue_sync_outbox n
				WHERE n.issue_id = o.issue_id AND n.id > o.id AND n.status != ?)
		GROUP BY o.status`,
		projectID, OutboxPending, OutboxFailed, OutboxConflict, OutboxSuperseded)
	if err != nil {
		return c, fmt.Errorf("count issue writes: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("count issue writes: %w", cerr)
		}
	}()
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return c, fmt.Errorf("count issue writes: %w", err)
		}
		switch status {
		case OutboxPending:
			c.Pending = n
		case OutboxFailed:
			c.Failed = n
		case OutboxConflict:
			c.Conflict = n
		}
	}
	if err := rows.Err(); err != nil {
		return c, fmt.Errorf("count issue writes: %w", err)
	}
	return c, nil
}
