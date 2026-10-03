package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// Remote statuses (task 130.8, spec §5.6): what the last sweep learned of a
// live remote. Neither moved nor missing deletes anything — the local issue
// is kept as it was, and a refresh that matches the key again resets the
// status to live.
const (
	RemoteStatusLive    = ""
	RemoteStatusMoved   = "moved"   // transferred away; remote_json carries "moved_to"
	RemoteStatusMissing = "missing" // gone, or no longer readable
)

// IssueSyncState is a project's issue import bookkeeping (task 130.8, spec
// §5.6, §14): one row per project the reconciler has polled or been asked
// to poll. A nil time is "never".
type IssueSyncState struct {
	ProjectID      int64
	Provider, Repo string
	// Watermark is the newest remote updated_at seen; Since is the bound
	// the next incremental poll asks for; ETag the last validator.
	Watermark, Since *time.Time
	ETag             string
	LastAttemptAt    *time.Time
	LastOKAt         *time.Time
	// OK and Reason are the last attempt's outcome; Reason is "" when OK.
	OK             bool
	Reason         string
	ImportComplete bool
	// LastFullScanAt paces the sweep for moved and missing remotes;
	// RateLimitedUntil defers the next attempt; RequestedAt is an
	// outstanding "sync now" (RequestIssueSync).
	LastFullScanAt, RateLimitedUntil, RequestedAt *time.Time
}

const issueSyncColumns = `project_id, provider, repo, watermark, etag, since, last_attempt_at,
	last_ok_at, ok, reason, import_complete, last_full_scan_at, rate_limited_until, requested_at`

func scanIssueSyncState(r rowScanner) (*IssueSyncState, error) {
	var (
		st                               IssueSyncState
		watermark, since, attempt, okAt  sql.NullString
		fullScan, rateLimited, requested sql.NullString
	)
	if err := r.Scan(&st.ProjectID, &st.Provider, &st.Repo, &watermark, &st.ETag, &since, &attempt,
		&okAt, &st.OK, &st.Reason, &st.ImportComplete, &fullScan, &rateLimited, &requested); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		dst **time.Time
		src sql.NullString
	}{
		{&st.Watermark, watermark},
		{&st.Since, since},
		{&st.LastAttemptAt, attempt},
		{&st.LastOKAt, okAt},
		{&st.LastFullScanAt, fullScan},
		{&st.RateLimitedUntil, rateLimited},
		{&st.RequestedAt, requested},
	} {
		t, err := parseTimePtr(f.src)
		if err != nil {
			return nil, err
		}
		*f.dst = t
	}
	return &st, nil
}

// GetIssueSyncState returns the project's sync row, or ErrNotFound when the
// project has none — it has never been polled nor asked to be.
func (s *Store) GetIssueSyncState(ctx context.Context, projectID int64) (*IssueSyncState, error) {
	st, err := scanIssueSyncState(s.db.QueryRowContext(ctx,
		`SELECT `+issueSyncColumns+` FROM issue_sync_state WHERE project_id = ?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("issue sync state of project %d: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read issue sync state of project %d: %w", projectID, err)
	}
	return st, nil
}

// ListIssueSyncStates returns every sync row, ordered by project id.
func (s *Store) ListIssueSyncStates(ctx context.Context) (out []IssueSyncState, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+issueSyncColumns+` FROM issue_sync_state ORDER BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("list issue sync states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		st, err := scanIssueSyncState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan issue sync state: %w", err)
		}
		out = append(out, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list issue sync states: %w", err)
	}
	return out, nil
}

// PutIssueSyncState writes the whole row in one transaction. It announces
// issue.sync_changed only when OK differs from the stored OK — a project
// with no row counts as ok, so a first failing write announces and a first
// ok write does not. Every attempt rewrites the row; only a transition is
// news, so a sync failing on each tick writes one event, not one per tick.
// A requested_at stored later than st.LastAttemptAt survives the write: it
// is a "sync now" that arrived while the attempt was running, after the
// caller read the row, and clearing it would drop the request on the floor.
// ErrNotFound for an unknown project.
func (s *Store) PutIssueSyncState(ctx context.Context, st IssueSyncState) error {
	return s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		if err := projectExistsTx(ctx, tx, st.ProjectID); err != nil {
			return nil, err
		}
		prevOK := true
		err := tx.QueryRowContext(ctx, `SELECT ok FROM issue_sync_state WHERE project_id = ?`, st.ProjectID).Scan(&prevOK)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("read issue sync state of project %d: %w", st.ProjectID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_sync_state (`+issueSyncColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id) DO UPDATE SET
				provider = excluded.provider, repo = excluded.repo, watermark = excluded.watermark,
				etag = excluded.etag, since = excluded.since, last_attempt_at = excluded.last_attempt_at,
				last_ok_at = excluded.last_ok_at, ok = excluded.ok, reason = excluded.reason,
				import_complete = excluded.import_complete, last_full_scan_at = excluded.last_full_scan_at,
				rate_limited_until = excluded.rate_limited_until,
				requested_at = CASE WHEN issue_sync_state.requested_at > excluded.last_attempt_at
					THEN issue_sync_state.requested_at ELSE excluded.requested_at END`,
			st.ProjectID, st.Provider, st.Repo, formatTimePtr(st.Watermark), st.ETag, formatTimePtr(st.Since),
			formatTimePtr(st.LastAttemptAt), formatTimePtr(st.LastOKAt), st.OK, st.Reason, st.ImportComplete,
			formatTimePtr(st.LastFullScanAt), formatTimePtr(st.RateLimitedUntil), formatTimePtr(st.RequestedAt),
		); err != nil {
			return nil, fmt.Errorf("write issue sync state of project %d: %w", st.ProjectID, err)
		}
		if st.OK == prevOK {
			return nil, nil
		}
		return issueSyncEvent(st)
	})
}

// issueSyncEvent builds issue.sync_changed: project-scoped like every issue
// event, reason omitted when ok.
func issueSyncEvent(st IssueSyncState) (*Event, error) {
	body := map[string]any{"project_id": st.ProjectID, "ok": st.OK}
	if !st.OK && st.Reason != "" {
		body["reason"] = st.Reason
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventIssueSyncChanged, err)
	}
	pid := st.ProjectID
	return &Event{Type: EventIssueSyncChanged, ProjectID: &pid, Payload: payload}, nil
}

// OnIssueSyncRequested registers the one callback RequestIssueSync runs
// after its commit — the daemon's reconciler wakes from it. Like
// SetEventHook it runs synchronously on the writing goroutine and must not
// block.
func (s *Store) OnIssueSyncRequested(fn func(projectID int64)) { s.syncRequested.Store(&fn) }

// RequestIssueSync records a "sync now" for the project (task 130.8):
// requested_at is stamped on its row, creating an ok github row when there
// is none — which is no transition, so no event — and after the commit the
// OnIssueSyncRequested callback is told. The store holds no lock while it
// runs. ErrNotFound for an unknown project.
func (s *Store) RequestIssueSync(ctx context.Context, projectID int64) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := projectExistsTx(ctx, tx, projectID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_sync_state (project_id, provider, ok, requested_at) VALUES (?, 'github', 1, ?)
			ON CONFLICT (project_id) DO UPDATE SET requested_at = excluded.requested_at`,
			projectID, formatTime(time.Now())); err != nil {
			return fmt.Errorf("request issue sync of project %d: %w", projectID, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if fn := s.syncRequested.Load(); fn != nil && *fn != nil {
		(*fn)(projectID)
	}
	return nil
}

// SetIssueRemoteStatus records what a sweep learned of a remote: live,
// moved (location, when given, is merged into remote_json as "moved_to") or
// missing. It never deletes and never touches a tombstone. A live remote
// whose status changed announces issue.updated {changed: ["remote_status"]}
// by sync; an unchanged one announces nothing. ErrNotFound for an unknown
// key.
func (s *Store) SetIssueRemoteStatus(ctx context.Context, projectID int64, provider, remoteKey, status, location string) error {
	switch status {
	case RemoteStatusLive, RemoteStatusMoved, RemoteStatusMissing:
	default:
		return fmt.Errorf("unknown remote status %q", status)
	}
	return s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		var (
			remoteID int64
			issueID  sql.NullInt64
			cur      string
			raw      sql.NullString
		)
		err := tx.QueryRowContext(ctx, `
			SELECT id, issue_id, remote_status, remote_json FROM issue_remotes
			WHERE project_id = ? AND provider = ? AND remote_key = ?`,
			projectID, provider, remoteKey).Scan(&remoteID, &issueID, &cur, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("issue remote %s %q: %w", provider, remoteKey, ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read issue remote: %w", err)
		}
		if !issueID.Valid {
			return nil, nil
		}
		remoteJSON := raw.String
		if status == RemoteStatusMoved && location != "" {
			if remoteJSON, err = mergeMovedTo(raw.String, location); err != nil {
				return nil, err
			}
		}
		if status == cur && remoteJSON == raw.String {
			return nil, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE issue_remotes SET remote_status = ?, remote_json = ? WHERE id = ?`,
			status, nullString(remoteJSON), remoteID); err != nil {
			return nil, fmt.Errorf("update issue remote status: %w", err)
		}
		if status == cur {
			return nil, nil
		}
		return issueEvent(EventIssueUpdated, projectID, issueID.Int64, issuestate.Sync,
			map[string]any{"changed": []string{"remote_status"}})
	})
}

// mergeMovedTo sets "moved_to" in a remote_json object, keeping the rest.
func mergeMovedTo(raw, location string) (string, error) {
	obj := map[string]json.RawMessage{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
			// Not an object: replaced, since moved_to is what a reader of a
			// moved remote needs.
			obj = map[string]json.RawMessage{}
		}
	}
	loc, err := json.Marshal(location)
	if err != nil {
		return "", fmt.Errorf("marshal moved_to: %w", err)
	}
	obj["moved_to"] = loc
	out, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("marshal remote_json: %w", err)
	}
	return string(out), nil
}

// ListOpenRemoteIssues returns the project's live remotes of the provider
// whose issue is open and whose status is live — the set a full sweep must
// still find on the remote (task 130.8). Ordered by remote row id.
func (s *Store) ListOpenRemoteIssues(ctx context.Context, projectID int64, provider string) (out []IssueRemote, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.issue_id, r.project_id, r.provider, r.remote_key, r.repo, r.number, r.url,
			r.remote_json, r.remote_updated_at, r.synced_at, r.suppressed, r.remote_status
		FROM issue_remotes r JOIN issues i ON i.id = r.issue_id
		WHERE r.project_id = ? AND r.provider = ? AND r.remote_status = '' AND i.state = ?
		ORDER BY r.id`, projectID, provider, string(issuestate.Open))
	if err != nil {
		return nil, fmt.Errorf("list open remote issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r                        IssueRemote
			issueID, number          sql.NullInt64
			raw, remoteUpd, syncedAt sql.NullString
		)
		if err := rows.Scan(&r.ID, &issueID, &r.ProjectID, &r.Provider, &r.RemoteKey, &r.Repo, &number, &r.URL,
			&raw, &remoteUpd, &syncedAt, &r.Suppressed, &r.Status); err != nil {
			return nil, fmt.Errorf("scan remote issue: %w", err)
		}
		if issueID.Valid {
			r.IssueID = &issueID.Int64
		}
		r.Number = int(number.Int64)
		r.RemoteJSON = raw.String
		if r.RemoteUpdatedAt, err = parseTimePtr(remoteUpd); err != nil {
			return nil, err
		}
		if r.SyncedAt, err = parseTimePtr(syncedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open remote issues: %w", err)
	}
	return out, nil
}

// RemoteIssueKnown reports whether the key has a remote row in the project,
// live or tombstoned — either way the importer must not create it again
// (decision 6).
func (s *Store) RemoteIssueKnown(ctx context.Context, projectID int64, provider, remoteKey string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM issue_remotes WHERE project_id = ? AND provider = ? AND remote_key = ?`,
		projectID, provider, remoteKey).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read issue remote: %w", err)
	}
	return true, nil
}
