package store

import (
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
	Remote     *IssueSnapshotRemote `json:"remote,omitempty"`
	CapturedAt time.Time            `json:"captured_at,omitzero"`
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
	State           string   `json:"state"`
	Assignee        string   `json:"assignee"`
	Assignees       []string `json:"assignees"`
	Milestone       string   `json:"milestone"`
	MilestoneNumber int      `json:"milestone_number"`
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
