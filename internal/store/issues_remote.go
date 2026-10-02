package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// RemoteIssue is an issue as a provider reports it, which UpsertRemoteIssue
// writes into the project's issue set (task 130 decision 9).
type RemoteIssue struct {
	ProjectID                 int64
	Provider, RemoteKey, Repo string
	Number                    int
	URL, RemoteJSON           string
	RemoteUpdatedAt           *time.Time
	Title, Body, Author       string
	// Kind seeds a newly imported issue's kind and is ignored on a refresh:
	// kind and priority are vincent's, never the remote's (task 130.8).
	Kind        string
	State       issuestate.State
	CloseReason issuestate.Reason
	Labels      []string // upserted with source = Provider
}

// UpsertRemoteIssue imports or refreshes the issue keyed (project, provider,
// remote key). The key is per project (decision 2), so the same remote issue
// imported into two projects is two issues.
//
// A new key creates the issue and its remote row and announces issue.created.
// A live key takes the remote's title, body, state, close reason and labels
// — the remote is the authority for those on an imported issue — stamps
// synced_at, and announces one issue.updated naming what moved, "state"
// included; a refresh that moved nothing announces nothing and keeps the
// version. Kind and priority are local, vincent-owned fields a refresh
// never writes (task 130.8): in.Kind is used only when creating. A refresh
// also resets remote_status to live — matching the node id proves the
// issue is reachable again, after a repo rename say — and names "remote"
// among what moved when that clears a moved or missing mark. A tombstoned key — its issue was deleted here — is never
// resurrected: the result is (nil, false, nil) and nothing is written.
func (s *Store) UpsertRemoteIssue(ctx context.Context, in RemoteIssue, by issuestate.Actor) (iss *Issue, created bool, err error) {
	if err := checkActor(by); err != nil {
		return nil, false, err
	}
	if in.Provider == "" || in.RemoteKey == "" {
		return nil, false, errors.New("remote issue needs a provider and a remote key")
	}
	state := issuestate.Normalize(in.State)
	var reason issuestate.Reason
	if state == issuestate.Closed {
		if reason, err = issuestate.ResolveReason(issuestate.RemoteClosed, in.CloseReason); err != nil {
			return nil, false, err
		}
	}

	var (
		id        int64
		tombstone bool
	)
	err = s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		if err := projectExistsTx(ctx, tx, in.ProjectID); err != nil {
			return nil, err
		}
		var (
			remoteID int64
			issueID  sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, `
			SELECT id, issue_id FROM issue_remotes WHERE project_id = ? AND provider = ? AND remote_key = ?`,
			in.ProjectID, in.Provider, in.RemoteKey).Scan(&remoteID, &issueID)
		now := time.Now()
		switch {
		case errors.Is(err, sql.ErrNoRows):
			created = true
			return insertRemoteIssueTx(ctx, tx, in, state, reason, now, by, &id)
		case err != nil:
			return nil, fmt.Errorf("read issue remote: %w", err)
		case !issueID.Valid:
			tombstone = true
			return nil, nil
		}
		id = issueID.Int64
		return refreshRemoteIssueTx(ctx, tx, in, remoteID, id, state, reason, now, by)
	})
	if err != nil || tombstone {
		return nil, false, err
	}
	iss, err = s.GetIssue(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return iss, created, nil
}

func insertRemoteIssueTx(ctx context.Context, tx *sql.Tx, in RemoteIssue, state issuestate.State, reason issuestate.Reason, now time.Time, by issuestate.Actor, id *int64) (*Event, error) {
	var err error
	*id, err = insertIssueTx(ctx, tx, NewIssue{
		ProjectID: in.ProjectID, Title: in.Title, Body: in.Body, Kind: in.Kind, Author: in.Author,
	}, state, reason, now)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_remotes (issue_id, project_id, provider, remote_key, repo, number, url,
			remote_json, remote_updated_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		*id, in.ProjectID, in.Provider, in.RemoteKey, in.Repo, remoteNumber(in.Number), in.URL,
		nullString(in.RemoteJSON), formatTimePtr(in.RemoteUpdatedAt), formatTime(now)); err != nil {
		return nil, fmt.Errorf("insert issue remote: %w", err)
	}
	if err := replaceIssueLabelsTx(ctx, tx, in.ProjectID, *id, in.Labels, in.Provider); err != nil {
		return nil, err
	}
	return issueEvent(EventIssueCreated, in.ProjectID, *id, by, nil)
}

func refreshRemoteIssueTx(ctx context.Context, tx *sql.Tx, in RemoteIssue, remoteID, id int64, state issuestate.State, reason issuestate.Reason, now time.Time, by issuestate.Actor) (*Event, error) {
	cur, err := getIssue(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	var changed []string
	if in.Title != cur.Title {
		changed = append(changed, "title")
	}
	if in.Body != cur.Body {
		changed = append(changed, "body")
	}
	from := issuestate.Normalize(cur.State)
	if state != from || cur.State != from {
		changed = append(changed, "state")
	}
	if reason != cur.CloseReason {
		changed = append(changed, "close_reason")
	}
	labelsMoved := !sameLabelSet(cur.Labels, in.Labels)
	if labelsMoved {
		changed = append(changed, "labels")
	}
	if r := cur.Remote; r != nil && (r.Repo != in.Repo || r.Number != in.Number || r.URL != in.URL ||
		r.Status != RemoteStatusLive) {
		changed = append(changed, "remote")
	}

	// The remote row is refreshed on every sync — synced_at is the record
	// that the poll happened, whether or not anything moved.
	if _, err := tx.ExecContext(ctx, `
		UPDATE issue_remotes SET repo = ?, number = ?, url = ?, remote_json = ?, remote_updated_at = ?, synced_at = ?,
			remote_status = ''
		WHERE id = ?`,
		in.Repo, remoteNumber(in.Number), in.URL, nullString(in.RemoteJSON), formatTimePtr(in.RemoteUpdatedAt),
		formatTime(now), remoteID); err != nil {
		return nil, fmt.Errorf("update issue remote: %w", err)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if labelsMoved {
		if err := replaceIssueLabelsTx(ctx, tx, cur.ProjectID, id, in.Labels, in.Provider); err != nil {
			return nil, err
		}
	}
	stamp := formatTime(now)
	switch {
	case state == issuestate.Open:
		_, err = tx.ExecContext(ctx, `
			UPDATE issues SET title = ?, body = ?, state = ?, close_reason = NULL, closed_at = NULL,
				duplicate_of_issue_id = NULL, version = version + 1, updated_at = ?
			WHERE id = ?`, in.Title, in.Body, string(state), stamp, id)
	case from == issuestate.Closed:
		// Still closed: closed_at keeps the moment it was first seen closed.
		_, err = tx.ExecContext(ctx, `
			UPDATE issues SET title = ?, body = ?, state = ?, close_reason = ?,
				version = version + 1, updated_at = ?
			WHERE id = ?`, in.Title, in.Body, string(state), string(reason), stamp, id)
	default:
		_, err = tx.ExecContext(ctx, `
			UPDATE issues SET title = ?, body = ?, state = ?, close_reason = ?, closed_at = ?,
				version = version + 1, updated_at = ?
			WHERE id = ?`, in.Title, in.Body, string(state), string(reason), stamp, stamp, id)
	}
	if err != nil {
		return nil, fmt.Errorf("refresh issue %d: %w", id, err)
	}
	slices.Sort(changed)
	return issueEvent(EventIssueUpdated, cur.ProjectID, id, by, map[string]any{"changed": changed})
}

// remoteNumber stores a zero number as NULL: a provider without numbers has
// none, which is not issue #0.
func remoteNumber(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
