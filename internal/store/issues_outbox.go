package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issueprovider"
	"github.com/lezli01/vincent/internal/issuestate"
)

// The issue state write-back outbox (task 130.10, spec §12.4, §14). A close
// or reopen of an issue with a live remote of a provider that writes back
// (internal/issueprovider) leaves one pending set_state row, written in the
// transaction that changes the state; the daemon's drain sends it and
// settles it. Nothing here calls a provider.

// Outbox row statuses.
const (
	OutboxPending    = "pending"
	OutboxDone       = "done"
	OutboxFailed     = "failed"
	OutboxConflict   = "conflict"
	OutboxSuperseded = "superseded"
)

// Outbox operations. OutboxOpSetState is a close or reopen; title, body
// and labels are a read-only mirror and are never written back (task 130
// decision 9). OutboxOpAddComment is a granted handoff's comment (task 137
// decision 6, 137.7): the one comment write-back there is — a human's or an
// ungranted agent's comment on a live-remote thread is still refused
// (decision 24.3).
const (
	OutboxOpSetState   = "set_state"
	OutboxOpAddComment = "add_comment"
)

// IssueStateValue is an issue's state as the outbox carries it to a
// provider: State is "open" or "closed", Reason the close reason of a closed
// one in §5.6's vocabulary (which is GitHub's state_reason, hence the JSON
// tag), and DuplicateOf the number of the issue a duplicate names in the
// same provider and repository, 0 for none. Each provider's writer maps it
// onto its own terms.
type IssueStateValue struct {
	State       string `json:"state"`
	Reason      string `json:"state_reason,omitempty"`
	DuplicateOf int    `json:"duplicate_of,omitempty"`
}

// Same compares two values the way echo suppression and the drain's
// preflight do (task 130.10): by state, and by reason when closed — never
// by a timestamp, and never by DuplicateOf, which no provider reports
// back.
func (v IssueStateValue) Same(o IssueStateValue) bool {
	if v.State != o.State {
		return false
	}
	return v.State != string(issuestate.Closed) || v.Reason == o.Reason
}

// RemoteStateOf maps a local state and close reason onto the value the
// outbox carries. issuestate's reasons are the outbox's vocabulary already.
func RemoteStateOf(state issuestate.State, reason issuestate.Reason) IssueStateValue {
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
	// CommentID is the local comment an add_comment row posts; nil on a
	// set_state row, whose Desired and Base are the only payload.
	CommentID            *int64
	CreatedAt, UpdatedAt time.Time
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
// written back: a live remote of a provider that writes back
// (issueprovider.WritesBack, task 142.5). A moved or missing one keeps the
// change local (task 130.10), as does an unknown provider, and a local issue
// has nowhere to write.
func writesBack(rem *IssueRemote) bool {
	return rem != nil && rem.IssueID != nil && issueprovider.WritesBack(rem.Provider) && rem.Status == RemoteStatusLive
}

// enqueueStateWriteTx records the write-back of cur's move to (to, reason)
// in tx, the transaction making that move. It writes nothing for sync's own
// transitions, for an issue that does not write back, or when the mapped
// value is the one the provider already has. A pending row for the issue
// is superseded, and its base carried over: the provider has not been written
// since, so close→reopen with base open sends nothing at all. Reports
// whether a row was inserted.
func enqueueStateWriteTx(ctx context.Context, tx *sql.Tx, cur *Issue, to issuestate.State, reason issuestate.Reason, duplicateOf *int64, by issuestate.Actor, now time.Time) (bool, error) {
	if by == issuestate.Sync || !writesBack(cur.Remote) {
		return false, nil
	}
	desired := RemoteStateOf(to, reason)
	if duplicateOf != nil {
		// The target counts only when it is a live remote of the same
		// provider and repository: a number means nothing anywhere else.
		var (
			repo   string
			number sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, `
			SELECT repo, number FROM issue_remotes WHERE issue_id = ? AND provider = ? AND remote_status = ''`,
			*duplicateOf, cur.Remote.Provider).Scan(&repo, &number)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return false, fmt.Errorf("read duplicate's remote: %w", err)
		case number.Valid && strings.EqualFold(repo, cur.Remote.Repo):
			desired.DuplicateOf = int(number.Int64)
		}
	}
	base := RemoteStateOf(cur.State, cur.CloseReason)
	var baseJSON string
	err := tx.QueryRowContext(ctx, `
		SELECT base_json FROM issue_sync_outbox WHERE issue_id = ? AND op = ? AND status = ? ORDER BY id DESC LIMIT 1`,
		cur.ID, OutboxOpSetState, OutboxPending).Scan(&baseJSON)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("read pending write of issue %d: %w", cur.ID, err)
	default:
		if err := json.Unmarshal([]byte(baseJSON), &base); err != nil {
			return false, fmt.Errorf("decode pending write of issue %d: %w", cur.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_sync_outbox SET status = ?, updated_at = ? WHERE issue_id = ? AND op = ? AND status = ?`,
			OutboxSuperseded, formatTime(now), cur.ID, OutboxOpSetState, OutboxPending); err != nil {
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

// holdsLocalStateTx reports whether an import refresh that finds GitHub at
// remote keeps the issue's local state rather than adopting GitHub's, by the
// issue's newest write (task 130.10). A pending one holds it: the drain's
// preflight, not the importer, decides between the two. A failed one —
// `no_write_scope`, `not_found` and the rest — holds it while GitHub still
// shows that write's base, because §12.4 ends such a write keeping the local
// state, and adopting the very value the write failed to replace would undo
// it on the next tick. Once GitHub moves off the base someone else changed
// it, and GitHub is the authority again.
//
// A done one holds it only when unconfirmed: the value comes from a listing
// that may trail the source's own writes (issuesource.LaggingLister, Jira's
// search) and was not re-read by id, and it differs from what the write
// sent (task 142.13 decision 1). The importer decides which issues to
// re-read before this transaction opens, so a write the drain settles in
// between reaches here done with only the stale listing to adopt; holding
// it leaves the decision to the next pass, whose read by id is consistent
// (review F1 on #945).
func holdsLocalStateTx(ctx context.Context, tx *sql.Tx, issueID int64, remote IssueStateValue, unconfirmed bool) (bool, error) {
	var status, desiredJSON, baseJSON string
	err := tx.QueryRowContext(ctx, `SELECT status, desired_json, base_json FROM issue_sync_outbox WHERE issue_id = ? AND op = ?
		ORDER BY id DESC LIMIT 1`, issueID, OutboxOpSetState).Scan(&status, &desiredJSON, &baseJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read newest write of issue %d: %w", issueID, err)
	}
	switch status {
	case OutboxPending:
		return true, nil
	case OutboxFailed:
		var base IssueStateValue
		if err := json.Unmarshal([]byte(baseJSON), &base); err != nil {
			return false, fmt.Errorf("decode newest write of issue %d: %w", issueID, err)
		}
		return remote.Same(base), nil
	case OutboxDone:
		if !unconfirmed {
			return false, nil
		}
		var desired IssueStateValue
		if err := json.Unmarshal([]byte(desiredJSON), &desired); err != nil {
			return false, fmt.Errorf("decode newest write of issue %d: %w", issueID, err)
		}
		return !remote.Same(desired), nil
	}
	return false, nil
}

const outboxSelect = `SELECT o.id, o.issue_id, i.project_id, o.op, o.desired_json, o.base_json, o.status,
	o.attempts, o.next_attempt_at, o.last_reason, o.origin, o.comment_id, o.created_at, o.updated_at
	FROM issue_sync_outbox o JOIN issues i ON i.id = o.issue_id`

func scanOutbox(r rowScanner) (*IssueOutbox, error) {
	var (
		o                     IssueOutbox
		desired, base, origin string
		next, created, upd    string
		commentID             sql.NullInt64
	)
	if err := r.Scan(&o.ID, &o.IssueID, &o.ProjectID, &o.Op, &desired, &base, &o.Status, &o.Attempts,
		&next, &o.LastReason, &origin, &commentID, &created, &upd); err != nil {
		return nil, err
	}
	if commentID.Valid {
		o.CommentID = &commentID.Int64
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

// loadIssueSync fills each issue's Sync with its newest state write that
// was not superseded. Local issues are not asked about: they never have
// one. A comment write is not the issue's sync state: it is about one
// comment, and the project's counts carry its failure.
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
	rows, err := queryOutbox(ctx, q, outboxSelect+` WHERE o.op = ? AND o.status != ? AND o.issue_id IN `+
		placeholders(len(ids))+` ORDER BY o.id`, append([]any{OutboxOpSetState, OutboxSuperseded}, ids...)...)
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
		UPDATE issue_sync_outbox SET base_json = ?, updated_at = ? WHERE issue_id = ? AND op = ? AND status = ? AND id > ?`,
		string(bj), formatTime(now), o.IssueID, OutboxOpSetState, OutboxPending, o.ID)
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
	desired := RemoteStateOf(cur.State, cur.CloseReason)
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
// by a newer one of the same operation is history, not health; a state
// write and a comment write are counted apart.
func (s *Store) CountIssueWrites(ctx context.Context, projectID int64) (c IssueOutboxCounts, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.status, COUNT(*) FROM issue_sync_outbox o JOIN issues i ON i.id = o.issue_id
		WHERE i.project_id = ? AND o.status IN (?, ?, ?)
			AND NOT EXISTS (SELECT 1 FROM issue_sync_outbox n
				WHERE n.issue_id = o.issue_id AND n.op = o.op AND n.id > o.id AND n.status != ?)
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

// enqueueCommentWriteTx records the write-back of a granted handoff's
// comment commentID on cur, in tx, the transaction inserting it (task
// 137.7). Only the handoff actor writes a comment back, and only to an
// issue that writes back; reports whether a row was inserted. The value
// columns carry an empty object: a comment is appended, never compared.
func enqueueCommentWriteTx(ctx context.Context, tx *sql.Tx, cur *Issue, commentID int64, by issuestate.Actor, now time.Time) (bool, error) {
	if by != issuestate.Handoff || !writesBack(cur.Remote) {
		return false, nil
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_sync_outbox (issue_id, op, desired_json, base_json, status, next_attempt_at,
			origin, comment_id, created_at, updated_at)
		VALUES (?, ?, '{}', '{}', ?, ?, ?, ?, ?, ?)`,
		cur.ID, OutboxOpAddComment, OutboxPending, stamp, string(by), commentID, stamp, stamp); err != nil {
		return false, fmt.Errorf("enqueue comment write of issue %d: %w", cur.ID, err)
	}
	return true, nil
}

// SettleIssueCommentWrite ends a pending add_comment row done and records
// remoteKey — GitHub's id of the comment it posted — on the local comment,
// in one transaction, so the comment sync's next pass finds the row by its
// key and updates it in place. A copy that pass already imported under the
// same key, between the post and this settle, is dropped in favour of the
// local row: the thread shows the comment once either way. A row no longer
// pending is not re-settled.
func (s *Store) SettleIssueCommentWrite(ctx context.Context, id int64, remoteKey string) (settled bool, err error) {
	if remoteKey == "" {
		return false, fmt.Errorf("settle comment write %d: empty remote key", id)
	}
	err = s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		o, err := getOutboxTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if o.Status != OutboxPending || o.Op != OutboxOpAddComment || o.CommentID == nil {
			return nil, nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM issue_comments WHERE issue_id = ? AND remote_key = ? AND id != ?`,
			o.IssueID, remoteKey, *o.CommentID); err != nil {
			return nil, fmt.Errorf("drop imported copy of comment %d: %w", *o.CommentID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE issue_comments SET remote_key = ? WHERE id = ?`,
			remoteKey, *o.CommentID); err != nil {
			return nil, fmt.Errorf("record remote key of comment %d: %w", *o.CommentID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_sync_outbox SET status = ?, last_reason = '', attempts = attempts + 1, updated_at = ?
			WHERE id = ?`, OutboxDone, formatTime(time.Now()), id); err != nil {
			return nil, fmt.Errorf("settle comment write %d: %w", id, err)
		}
		settled = true
		return syncChangedEvent(o)
	})
	return settled, err
}
