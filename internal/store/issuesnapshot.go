package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// IssueSnapshot is the issue a task was created from, frozen at creation
// (spec §5.6, task 130 decision 5). It is the store's own type, deliberately
// not an import of the issue entity: the snapshot is what a run renders, so
// it must not change shape because the live issue did, and it outlives the
// issue — deleting one sets tasks.issue_id NULL and leaves this as it was
// (decision 6).
//
// Every field past the first seven was added by 130.4 and is omitempty, so
// rows written before it decode unchanged and the column needed no migration.
type IssueSnapshot struct {
	ID          int64    `json:"id"`
	Title       string   `json:"title"`
	Body        string   `json:"body,omitempty"`
	State       string   `json:"state"`
	CloseReason string   `json:"close_reason,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Author      string   `json:"author,omitempty"`
	// URL is the remote's URL for an imported issue, "" for a local one —
	// the same value Remote.URL carries, kept because #660 shipped it.
	URL string `json:"url,omitempty"`
	// Remote is the provider reference of an imported issue, nil for a local
	// one. `.Issue.Source` is rendered from it (task 130 decision 8).
	Remote *IssueSnapshotRemote `json:"remote,omitempty"`
	// CreatedAt is when the issue was opened: the remote's creation time for
	// an imported issue whose record carries one, else the vincent row's.
	// Added by 130.14 for $VINCENT_ISSUE_FILE's gh-shaped `createdAt`; a
	// snapshot written before it decodes zero and the file says null.
	CreatedAt  time.Time `json:"created_at,omitzero"`
	CapturedAt time.Time `json:"captured_at,omitzero"`
	// Comments is the issue's discussion thread at task creation, oldest
	// first and untruncated (task 130.16, decision 24.5), read from
	// issue_comments in the create transaction and never from the network.
	// A later comment never reaches an existing task's snapshot. Omitted
	// when the thread is empty, so older rows decode with nil.
	Comments []IssueSnapshotComment `json:"comments,omitempty"`
}

// IssueSnapshotComment is one comment of a snapshot's thread: who wrote it,
// what it said and when, local and mirrored alike.
type IssueSnapshotComment struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// IssueSnapshotRemote is an imported issue's provider reference as it stood
// at task creation.
type IssueSnapshotRemote struct {
	Provider        string   `json:"provider"`
	Repo            string   `json:"repo,omitempty"`
	Number          int      `json:"number,omitempty"`
	URL             string   `json:"url,omitempty"`
	State           string   `json:"state,omitempty"`
	Assignees       []string `json:"assignees,omitempty"`
	Milestone       string   `json:"milestone,omitempty"`
	MilestoneNumber int      `json:"milestone_number,omitempty"`
}

// Clone returns a deep copy, nil for nil. A fan-out lane inherits its
// parent's snapshot (task 035 decision 9, kept by task 130 decision 5), and
// must share neither the parent's slices nor its Remote.
func (s *IssueSnapshot) Clone() *IssueSnapshot {
	if s == nil {
		return nil
	}
	c := *s
	c.Labels = slices.Clone(s.Labels)
	c.Comments = slices.Clone(s.Comments)
	if s.Remote != nil {
		r := *s.Remote
		r.Assignees = slices.Clone(s.Remote.Assignees)
		c.Remote = &r
	}
	return &c
}

// remoteDetail is the part of issue_remotes.remote_json a snapshot reads.
// The column holds the provider's own record, and for GitHub that is
// internal/github's Issue encoding; these tags are its spelling, restated
// because the store imports no provider package. A field the record lacks
// stays zero, and a record that does not parse contributes nothing — the
// snapshot is built from the columns first and this only enriches it.
type remoteDetail struct {
	State           string    `json:"state"`
	Assignee        string    `json:"assignee"`
	Assignees       []string  `json:"assignees"`
	Milestone       string    `json:"milestone"`
	MilestoneNumber int       `json:"milestone_number"`
	CreatedAt       time.Time `json:"created_at"`
}

// NewIssueSnapshot freezes iss for a task created from it at now (task 130
// decision 5). Remote is filled from the issue's live issue_remotes row and
// is nil for a local issue and for a tombstoned row. It reads only what the
// caller already loaded: building a snapshot makes no provider call.
func NewIssueSnapshot(iss *Issue, now time.Time) *IssueSnapshot {
	if iss == nil {
		return nil
	}
	snap := &IssueSnapshot{
		ID:          iss.ID,
		Title:       iss.Title,
		Body:        iss.Body,
		State:       string(iss.State),
		CloseReason: string(iss.CloseReason),
		Kind:        iss.Kind,
		Priority:    iss.Priority,
		Labels:      slices.Clone(iss.Labels),
		Author:      iss.Author,
		CreatedAt:   iss.CreatedAt.UTC(),
		CapturedAt:  now.UTC(),
	}
	if rem := iss.Remote; rem != nil && rem.IssueID != nil {
		var detail remoteDetail
		if rem.RemoteJSON != "" {
			// A malformed record is ignored rather than failing the create:
			// the columns already carry the reference a template needs.
			_ = json.Unmarshal([]byte(rem.RemoteJSON), &detail)
		}
		assignees := slices.Clone(detail.Assignees)
		if len(assignees) == 0 && detail.Assignee != "" {
			assignees = []string{detail.Assignee}
		}
		state := detail.State
		if state == "" {
			state = string(iss.State)
		}
		if !detail.CreatedAt.IsZero() {
			snap.CreatedAt = detail.CreatedAt.UTC()
		}
		snap.URL = rem.URL
		snap.Remote = &IssueSnapshotRemote{
			Provider:        rem.Provider,
			Repo:            rem.Repo,
			Number:          rem.Number,
			URL:             rem.URL,
			State:           state,
			Assignees:       assignees,
			Milestone:       detail.Milestone,
			MilestoneNumber: detail.MilestoneNumber,
		}
	}
	return snap
}

// issueSnapshotCommentsTx reads issueID's thread for a snapshot inside the
// task create transaction, in ListIssueComments' order, so the snapshot and
// the task row commit together (task 130.16, decision 24.5). Nil when the
// issue has no comments, which leaves the key out of issue_json.
func issueSnapshotCommentsTx(ctx context.Context, tx *sql.Tx, issueID int64) ([]IssueSnapshotComment, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT author, body, created_at FROM issue_comments
		WHERE issue_id = ? ORDER BY created_at ASC, id ASC`, issueID)
	if err != nil {
		return nil, fmt.Errorf("read issue %d comments: %w", issueID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []IssueSnapshotComment
	for rows.Next() {
		var (
			c       IssueSnapshotComment
			created string
		)
		if err := rows.Scan(&c.Author, &c.Body, &created); err != nil {
			return nil, fmt.Errorf("scan issue %d comment: %w", issueID, err)
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read issue %d comments: %w", issueID, err)
	}
	return out, nil
}

// marshalIssueSnapshot renders a task's issue snapshot for storage; no
// snapshot is SQL NULL, the same shape marshalGitHubIssue uses for its column.
func marshalIssueSnapshot(snap *IssueSnapshot) (any, error) {
	if snap == nil {
		return nil, nil
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("marshal issue snapshot: %w", err)
	}
	return string(b), nil
}

// unmarshalIssueSnapshot is marshalIssueSnapshot's inverse: SQL NULL (or an
// empty string) is nil.
func unmarshalIssueSnapshot(raw string, valid bool) (*IssueSnapshot, error) {
	if !valid || raw == "" {
		return nil, nil
	}
	var snap IssueSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return nil, fmt.Errorf("issue_json: %w", err)
	}
	return &snap, nil
}
