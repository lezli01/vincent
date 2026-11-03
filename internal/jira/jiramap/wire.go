package jiramap

// The decoded subset of Jira Cloud's REST v3 wire types that the mapping
// reads. The json tags are Jira's own spelling, so the HTTP client decodes
// an issue's `fields` and a `transitions?expand=transitions.fields` answer
// straight into them. Fields not listed here are ignored by encoding/json.

// Category keys of a Jira status (statusCategory.key). These are the only
// fixed part of a Jira workflow; numeric category ids differ between sources
// and are never read. (The OpenAPI examples' "in-flight"/"completed" are not
// real keys.)
const (
	CategoryNew           = "new"
	CategoryIndeterminate = "indeterminate"
	CategoryDone          = "done"
	CategoryUndefined     = "undefined"
)

// Fields is an issue's `fields` object.
type Fields struct {
	Status         *Status     `json:"status,omitempty"`
	Resolution     *Named      `json:"resolution,omitempty"`
	IssueType      *Named      `json:"issuetype,omitempty"`
	Priority       *Named      `json:"priority,omitempty"`
	Assignee       *User       `json:"assignee,omitempty"`
	Reporter       *User       `json:"reporter,omitempty"`
	Parent         *IssueRef   `json:"parent,omitempty"`
	IssueLinks     []IssueLink `json:"issuelinks,omitempty"`
	Components     []Named     `json:"components,omitempty"`
	Labels         []string    `json:"labels,omitempty"`
	Created        string      `json:"created,omitempty"`
	ResolutionDate string      `json:"resolutiondate,omitempty"`
}

// Status is a workflow status and its fixed category.
type Status struct {
	Name           string         `json:"name"`
	StatusCategory StatusCategory `json:"statusCategory"`
}

// StatusCategory carries the category key (CategoryNew, …).
type StatusCategory struct {
	Key string `json:"key"`
}

// Named is any Jira object read for its name alone: a resolution, an issue
// type, a priority, a component, an allowed field value.
type Named struct {
	Name string `json:"name"`
}

// User is an assignee or reporter.
type User struct {
	DisplayName string `json:"displayName"`
	AccountID   string `json:"accountId"`
}

// IssueRef is another issue, by key.
type IssueRef struct {
	Key string `json:"key"`
}

// IssueLink is one entry of `issuelinks`. Exactly one of InwardIssue and
// OutwardIssue is set: an outward "Duplicate" link on A targeting B means A
// duplicates B.
type IssueLink struct {
	Type         LinkType  `json:"type"`
	InwardIssue  *IssueRef `json:"inwardIssue,omitempty"`
	OutwardIssue *IssueRef `json:"outwardIssue,omitempty"`
}

// LinkType names a link type and its two descriptions.
type LinkType struct {
	Name    string `json:"name"`
	Inward  string `json:"inward,omitempty"`
	Outward string `json:"outward,omitempty"`
}

// Transition is one transition available on an issue now, with its screen's
// fields when requested with expand=transitions.fields.
type Transition struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	To        Status               `json:"to"`
	HasScreen bool                 `json:"hasScreen"`
	Fields    map[string]FieldMeta `json:"fields,omitempty"`
}

// FieldMeta describes one field on a transition's screen.
type FieldMeta struct {
	Required        bool    `json:"required"`
	HasDefaultValue bool    `json:"hasDefaultValue"`
	AllowedValues   []Named `json:"allowedValues,omitempty"`
}

// resolutionField is the screen field a transition sets the resolution by.
const resolutionField = "resolution"
