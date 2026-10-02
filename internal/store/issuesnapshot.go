package store

import (
	"encoding/json"
	"fmt"
	"slices"
)

// IssueSnapshot is the issue a task was created from, frozen at creation
// (spec §5.6, task 130 decision 5). It is the store's own type, deliberately
// not an import of the issue entity: the snapshot is what a run renders, so
// it must not change shape because the live issue did, and it outlives the
// issue — deleting one sets tasks.issue_id NULL and leaves this as it was
// (decision 6).
type IssueSnapshot struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	Kind     string   `json:"kind,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	URL      string   `json:"url,omitempty"`
}

// Clone returns a deep copy, nil for nil. A fan-out lane inherits its
// parent's snapshot (task 035 decision 9, kept by task 130 decision 5), and
// must not share the parent's Labels backing array.
func (s *IssueSnapshot) Clone() *IssueSnapshot {
	if s == nil {
		return nil
	}
	c := *s
	c.Labels = slices.Clone(s.Labels)
	return &c
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
