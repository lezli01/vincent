package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// UpsertLabel returns the project's label named name, creating it if the
// catalogue has none. The match is case-insensitive (decision 4): adding
// `Bug` where `bug` exists returns the `bug` row, keeping its spelling. A
// non-empty color or description refreshes the existing row's; source is
// kept, since the catalogue entry is the same label whoever names it next.
func (s *Store) UpsertLabel(ctx context.Context, projectID int64, name, color, description, source string) (*Label, error) {
	var l *Label
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := projectExistsTx(ctx, tx, projectID); err != nil {
			return err
		}
		var err error
		l, err = upsertLabelTx(ctx, tx, projectID, name, color, description, source)
		return err
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

func upsertLabelTx(ctx context.Context, tx *sql.Tx, projectID int64, name, color, description, source string) (*Label, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("label name is empty")
	}
	if source == "" {
		source = labelSourceLocal
	}
	l := &Label{ProjectID: projectID}
	// labels.name is COLLATE NOCASE, so `=` matches `Bug` against `bug`.
	err := tx.QueryRowContext(ctx, `
		SELECT id, name, color, description, source FROM labels WHERE project_id = ? AND name = ?`,
		projectID, name).Scan(&l.ID, &l.Name, &l.Color, &l.Description, &l.Source)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `
			INSERT INTO labels (project_id, name, color, description, source) VALUES (?, ?, ?, ?, ?)`,
			projectID, name, color, description, source)
		if err != nil {
			return nil, fmt.Errorf("insert label %q: %w", name, err)
		}
		if l.ID, err = res.LastInsertId(); err != nil {
			return nil, fmt.Errorf("insert label %q: %w", name, err)
		}
		l.Name, l.Color, l.Description, l.Source = name, color, description, source
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("read label %q: %w", name, err)
	}
	if (color != "" && color != l.Color) || (description != "" && description != l.Description) {
		if color != "" {
			l.Color = color
		}
		if description != "" {
			l.Description = description
		}
		if _, err := tx.ExecContext(ctx, `UPDATE labels SET color = ?, description = ? WHERE id = ?`,
			l.Color, l.Description, l.ID); err != nil {
			return nil, fmt.Errorf("update label %q: %w", l.Name, err)
		}
	}
	return l, nil
}

// ListLabels returns a project's label catalogue, sorted case-insensitively,
// each with how many issues carry it.
func (s *Store) ListLabels(ctx context.Context, projectID int64) ([]*Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.project_id, l.name, l.color, l.description, l.source,
			(SELECT COUNT(*) FROM issue_labels il WHERE il.label_id = l.id)
		FROM labels l
		WHERE l.project_id = ? ORDER BY l.name COLLATE NOCASE ASC, l.name ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Name, &l.Color, &l.Description, &l.Source, &l.IssueCount); err != nil {
			return nil, fmt.Errorf("scan label: %w", err)
		}
		out = append(out, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	return out, nil
}

// replaceIssueLabelsTx upserts each name into the project's catalogue with
// source and makes it the issue's whole label set.
func replaceIssueLabelsTx(ctx context.Context, tx *sql.Tx, projectID, issueID int64, names []string, source string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_labels WHERE issue_id = ?`, issueID); err != nil {
		return fmt.Errorf("clear issue %d labels: %w", issueID, err)
	}
	for _, n := range normalizeLabelNames(names) {
		l, err := upsertLabelTx(ctx, tx, projectID, n, "", "", source)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_labels (issue_id, label_id) VALUES (?, ?)`,
			issueID, l.ID); err != nil {
			return fmt.Errorf("label issue %d: %w", issueID, err)
		}
	}
	return nil
}

// sameLabelSet reports whether two name lists name the same labels,
// case-insensitively — the test for a label write that changes nothing.
func sameLabelSet(a, b []string) bool {
	key := func(names []string) []string {
		out := make([]string, 0, len(names))
		for _, n := range normalizeLabelNames(names) {
			out = append(out, strings.ToLower(n))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(key(a), key(b))
}

// SetIssueLabels replaces an issue's labels, upserting each name into the
// project's catalogue. A human or an agent creates `local` labels; sync
// creates them with the issue's provider. The same set, in any order or
// case, writes nothing.
func (s *Store) SetIssueLabels(ctx context.Context, id int64, names []string, by issuestate.Actor) (*Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	err := s.writeIssue(ctx, func(tx *sql.Tx) (*Event, error) {
		cur, err := getIssue(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if sameLabelSet(cur.Labels, names) {
			return nil, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET version = version + 1, updated_at = ? WHERE id = ?`,
			formatTime(time.Now()), id); err != nil {
			return nil, fmt.Errorf("update issue %d: %w", id, err)
		}
		return replaceLabelsAndAnnounceTx(ctx, tx, cur, names, by)
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// replaceLabelsAndAnnounceTx makes names cur's whole label set and returns
// the issue.labels_changed event naming the set it ends with. The caller
// bumps the version. A human or an agent creates `local` labels; sync
// creates them with the issue's provider.
func replaceLabelsAndAnnounceTx(ctx context.Context, tx *sql.Tx, cur *Issue, names []string, by issuestate.Actor) (*Event, error) {
	source := labelSourceLocal
	if by == issuestate.Sync && cur.Remote != nil {
		source = cur.Remote.Provider
	}
	if err := replaceIssueLabelsTx(ctx, tx, cur.ProjectID, cur.ID, names, source); err != nil {
		return nil, err
	}
	after, err := getIssue(ctx, tx, cur.ID)
	if err != nil {
		return nil, err
	}
	labels := after.Labels
	if labels == nil {
		labels = []string{}
	}
	added, removed := labelDelta(cur.Labels, after.Labels)
	return issueEvent(EventIssueLabelsChanged, cur.ProjectID, cur.ID, by, map[string]any{
		"labels": labels, "labels_added": added, "labels_removed": removed,
	})
}

// labelDelta is what after has that before lacks, and the reverse, compared
// case-insensitively as sameLabelSet is. It is what lets the `type: issues`
// trigger source map one event to `labeled`/`unlabeled` without a snapshot
// of its own (task 130.15 decision 2). Both lists are non-nil and sorted, so
// the payload is the same on every run.
func labelDelta(before, after []string) (added, removed []string) {
	has := func(names []string) map[string]bool {
		out := make(map[string]bool, len(names))
		for _, n := range names {
			out[strings.ToLower(strings.TrimSpace(n))] = true
		}
		return out
	}
	was, is := has(before), has(after)
	added, removed = []string{}, []string{}
	for _, n := range normalizeLabelNames(after) {
		if !was[strings.ToLower(n)] {
			added = append(added, n)
		}
	}
	for _, n := range normalizeLabelNames(before) {
		if !is[strings.ToLower(n)] {
			removed = append(removed, n)
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	return added, removed
}
