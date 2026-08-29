package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/github"
)

// The task↔pull-request link (migration 0018, task 052, spec §5.3/§14).
//
// The link is written in exactly two ways: the daemon's reconciler matches a
// pull request's head branch to a task's `branch_name`, or a human says so
// through the API. Neither writes anything else about the pull request —
// title, state, draft and merged status are always re-fetched.

// LinkCandidate is one row the reconciler considers: a task's identity and
// the branch a pull request would have to be from to match it.
//
// It is a narrow projection rather than a []Task because the reconciler wants
// one cheap read per project per tick and has no use for a workflow snapshot.
type LinkCandidate struct {
	TaskID     int64
	BranchName string
	// Pull is the link already stored, nil when there is none. The reconciler
	// reads it to leave a human link and a human's unlink alone.
	Pull *github.PullLink
}

// LinkCandidates returns a project's unarchived tasks with their branch names
// and current links, for the reconciler to match against a listing.
//
// Archived tasks are excluded: a link written onto a task nobody will look at
// again is work for no reader, and the branch of an archived task may since
// have been deleted.
func (s *Store) LinkCandidates(ctx context.Context, projectID int64) ([]LinkCandidate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, branch_name, github_pull_json FROM tasks
		 WHERE project_id = ? AND archived_at IS NULL AND branch_name <> ''
		 ORDER BY id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list link candidates for project %d: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []LinkCandidate
	for rows.Next() {
		var (
			c   LinkCandidate
			raw sql.NullString
		)
		if err := rows.Scan(&c.TaskID, &c.BranchName, &raw); err != nil {
			return nil, fmt.Errorf("scan link candidate: %w", err)
		}
		if raw.Valid && raw.String != "" {
			var link github.PullLink
			if err := json.Unmarshal([]byte(raw.String), &link); err != nil {
				return nil, fmt.Errorf("github_pull_json: %w", err)
			}
			c.Pull = &link
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list link candidates for project %d: %w", projectID, err)
	}
	return out, nil
}

// SetTaskGitHubPull writes a task's pull-request link and records the durable
// `task.github_pull_changed` event (§13.3), so a running TUI re-renders
// without polling. A nil link clears the column.
//
// `updated_at` is deliberately **not** bumped: the link is a fact about
// GitHub, not about the task's own progress, and a reconciler tick that
// touched every linked task's timestamp would reorder nothing but would make
// "when did this task last change" mean something else.
func (s *Store) SetTaskGitHubPull(ctx context.Context, id int64, link *github.PullLink) (*Task, error) {
	raw, err := marshalGitHubPull(link)
	if err != nil {
		return nil, err
	}
	var (
		out *Task
		ev  *Event
	)
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks SET github_pull_json = ? WHERE id = ?`, raw, id)
		if err != nil {
			return fmt.Errorf("set task %d github pull link: %w", id, err)
		}
		if err := oneRowAffected(res, fmt.Sprintf("task %d", id)); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
		if out, err = scanTask(row); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task %d: %w", id, ErrNotFound)
			}
			return fmt.Errorf("reload task %d: %w", id, err)
		}
		payload := map[string]any{}
		if link != nil {
			payload["repo"] = link.Repo
			payload["number"] = link.Number
			payload["source"] = link.Source
			payload["suppressed"] = link.Suppressed
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal task.github_pull_changed event: %w", err)
		}
		ev = &Event{Type: EventTaskGitHubPullChanged, TaskID: &id, Payload: body}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	// Post-commit, like every other fan-out in this package: a subscriber
	// must never observe an event the database has not durably recorded.
	s.notify(ev)
	return out, nil
}

// LinkPull is the human's link: it overwrites whatever the reconciler wrote
// and clears any earlier suppression, because a person naming a pull request
// is the strongest statement either side can make.
func LinkPull(repo string, number int, source string, now time.Time) *github.PullLink {
	return &github.PullLink{Repo: repo, Number: number, Source: source, LinkedAt: now}
}

// SuppressPull is the human's unlink. It keeps the repo and number rather
// than clearing the row, because "a human refused *this* pull request" is
// what the next reconciler tick has to read in order not to re-apply it.
func SuppressPull(existing *github.PullLink, now time.Time) *github.PullLink {
	out := github.PullLink{Suppressed: true, Source: github.SourceHuman, LinkedAt: now}
	if existing != nil {
		out.Repo, out.Number = existing.Repo, existing.Number
	}
	return &out
}
