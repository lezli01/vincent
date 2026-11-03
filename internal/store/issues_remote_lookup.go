package store

import (
	"context"
	"fmt"
)

// IssueIDsByRemoteNumber returns the ids of the issues in projectID whose
// live remote row is provider's issue number, lowest first, and an empty
// slice when none is. It backs GET /v1/issues?remote_number= (task 130.11,
// decision 22.4): the lookup that replaced the removed `github_issue` create
// field, so a script holding a GitHub number can find the vincent issue to
// create a task from.
//
// The number is scoped by project because it means nothing across projects,
// and a tombstone (issue_id NULL) never answers: a deleted issue is not one a
// task can be created from. It is not scoped by repo — a project whose origin
// was re-pointed can hold the same number from two repos, and both are
// returned rather than one chosen silently.
func (s *Store) IssueIDsByRemoteNumber(ctx context.Context, projectID int64, provider string, number int) ([]int64, error) {
	return s.IssueIDsByRemoteRef(ctx, projectID, provider, "", number)
}

// IssueIDsByRemoteRef is IssueIDsByRemoteNumber narrowed to one repo when
// repo is not empty, compared case-insensitively. It backs GET
// /v1/issues?remote_key= (task 142.9, issue #905): a Jira row stores the
// Jira project key in repo and the issue number in number (task 142
// decision 9.4's dated note), so `PROJ-12` is provider jira, repo PROJ,
// number 12 — matched on the display key, never on remote_key, which stays
// the stable Jira issue id. A GitHub reference passes repo "" and keeps
// IssueIDsByRemoteNumber's any-repo semantics.
//
// The API parameter's name is the display key's, not the column's: a caller
// holding a provider's stable id — the remote_key column — wants
// IssueIDByRemoteKey instead.
func (s *Store) IssueIDsByRemoteRef(ctx context.Context, projectID int64, provider, repo string, number int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT issue_id FROM issue_remotes
		WHERE project_id = ? AND provider = ? AND number = ? AND issue_id IS NOT NULL
		AND (? = '' OR repo = ? COLLATE NOCASE)
		ORDER BY issue_id`, projectID, provider, number, repo, repo)
	if err != nil {
		return nil, fmt.Errorf("look up %s issue %s#%d: %w", provider, repo, number, err)
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan remote issue id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("look up %s issue %s#%d: %w", provider, repo, number, err)
	}
	return out, nil
}
