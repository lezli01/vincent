package github

import (
	"sort"
	"strings"
	"time"
)

// Issue states, normalized. `gh` reports OPEN/CLOSED and the REST API reports
// open/closed; one spelling reaches the rest of vincent.
const (
	StateOpen   = "open"
	StateClosed = "closed"
)

// Issue is the normalized GitHub issue — the *only* shape either leg
// produces, and the shape persisted verbatim on the task as
// `github_issue_json` (task 035 decision 3, spec §5.3, §14).
//
// It is snapshotted at creation and never re-fetched: an issue edited on
// GitHub afterwards is deliberately not reflected, which is what keeps a run
// reproducible and keeps every network failure out of the render path (§8.4).
//
// Labels is a real list rather than a joined string. §8.1.2 task field values
// are strings everywhere, so the prefill joins them for a declared `labels`
// field — but a template reading `.Issue.Labels` gets the structure back.
type Issue struct {
	// Repo is `owner/name`, recorded so the snapshot identifies itself
	// without re-parsing a remote that may since have been re-pointed.
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	URL    string `json:"url"`
	State  string `json:"state"`

	Labels   []string `json:"labels,omitempty"`
	Author   string   `json:"author,omitempty"`
	Assignee string   `json:"assignee,omitempty"`
	// Milestone and MilestoneNumber are the two halves a declared field may
	// ask for: a `string` milestone field gets the title, an `integer` one
	// gets the number (decision 7).
	Milestone       string `json:"milestone,omitempty"`
	MilestoneNumber int    `json:"milestone_number,omitempty"`

	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// FetchedAt is when this snapshot was taken. It is the one honest answer
	// to "how old is this?", and the detail view renders it rather than
	// implying the issue is current.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
}

// Zero reports that no issue is linked. It is the test `.Issue` templates
// spell as `{{ if .Issue.Number }}`, stated once here for Go callers.
func (i Issue) Zero() bool { return i.Number == 0 }

// LabelList is the comma-joined spelling a declared `labels` field receives.
func (i Issue) LabelList() string { return strings.Join(i.Labels, ", ") }

// sortIssues orders a listing newest first, which is what the picker shows
// and what both legs are asked for. It is applied after parsing rather than
// trusted from the wire: `gh` and the REST API agree today, and a listing
// whose order depends on which leg answered would be a difference a user can
// see (decision 1).
func sortIssues(issues []Issue) {
	sort.SliceStable(issues, func(a, b int) bool {
		ta, tb := issues[a].CreatedAt, issues[b].CreatedAt
		if ta.Equal(tb) {
			return issues[a].Number > issues[b].Number
		}
		return ta.After(tb)
	})
}

// normalizeState folds either leg's spelling onto the constants above.
// Anything else passes through lowercased rather than being dropped: an
// unknown state is information, and inventing "open" for it would be a lie.
func normalizeState(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
