package jiramap

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// Bounds repeated from internal/issues, which this package cannot import (it
// imports the store). Keep them equal; internal/issues' tests assert it.
const (
	// MaxKindBytes is issues.MaxKindBytes.
	MaxKindBytes = 32
	// MaxLabelBytes is issues.MaxLabelBytes.
	MaxLabelBytes = 64
)

// kindPattern is issues' kindPattern, repeated for the same reason.
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Default resolution-name sets. Jira's stock resolutions are Done, Won't do
// and Duplicate, plus Cannot Reproduce in Jira Software; the rest are common
// additions.
var (
	DefaultDuplicateResolutions  = []string{"Duplicate"}
	DefaultNotPlannedResolutions = []string{"Won't do", "Won't Fix", "Cannot Reproduce", "Incomplete", "Declined"}
)

// cancelledNames are status or transition names that read as not planned
// beside the not-planned set.
var cancelledNames = []string{"Cancelled", "Canceled"}

// Config is a binding's mapping configuration, a plain value; persisting it
// is the caller's concern. The zero value is the defaults.
type Config struct {
	// Overrides name, per action and reason, the transition to use — by
	// transition name or by target status name, case-folded.
	Overrides Overrides
	// DuplicateResolutions and NotPlannedResolutions replace, when
	// non-empty, the corresponding default set rather than adding to it.
	DuplicateResolutions  []string
	NotPlannedResolutions []string
}

// Overrides are the optional per-key transition overrides. An empty field
// leaves that key to the heuristic.
type Overrides struct {
	CloseCompleted  string
	CloseNotPlanned string
	CloseDuplicate  string
	Reopen          string
}

func (c Config) duplicateSet() []string {
	if len(c.DuplicateResolutions) > 0 {
		return c.DuplicateResolutions
	}
	return DefaultDuplicateResolutions
}

func (c Config) notPlannedSet() []string {
	if len(c.NotPlannedResolutions) > 0 {
		return c.NotPlannedResolutions
	}
	return DefaultNotPlannedResolutions
}

// fold is the one name comparison: trimmed and case-folded.
func fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// inSet reports whether name is in set, compared folded. An empty name is
// in no set.
func inSet(name string, set []string) bool {
	n := fold(name)
	if n == "" {
		return false
	}
	for _, s := range set {
		if fold(s) == n {
			return true
		}
	}
	return false
}

// readsNotPlanned reports whether a status or transition name means "not
// planned": a not-planned resolution name, or Cancelled/Canceled.
func (c Config) readsNotPlanned(name string) bool {
	return inSet(name, c.notPlannedSet()) || inSet(name, cancelledNames)
}

// FromJira maps an issue's fields onto §5.6's state and close reason. It is
// closed iff the status category is "done"; the reason follows the
// resolution, with the null-resolution status-name fallback documented in
// doc.go. duplicateOfKey is the key of an outward "Duplicate" link's target
// when the reason is duplicate, else empty; the caller resolves it against
// same-project imports (task 130 decision 17.4).
func FromJira(f Fields, c Config) (state issuestate.State, reason issuestate.Reason, duplicateOfKey string) {
	if f.Status == nil || f.Status.StatusCategory.Key != CategoryDone {
		return issuestate.Open, "", ""
	}
	res := ""
	if f.Resolution != nil {
		res = f.Resolution.Name
	}
	switch {
	case fold(res) == "":
		if c.readsNotPlanned(f.Status.Name) {
			return issuestate.Closed, issuestate.NotPlanned, ""
		}
		return issuestate.Closed, issuestate.Completed, ""
	case inSet(res, c.duplicateSet()):
		return issuestate.Closed, issuestate.Duplicate, duplicateTarget(f.IssueLinks)
	case inSet(res, c.notPlannedSet()):
		return issuestate.Closed, issuestate.NotPlanned, ""
	default:
		return issuestate.Closed, issuestate.Completed, ""
	}
}

// duplicateTarget is the first outward "Duplicate" link's target key.
func duplicateTarget(links []IssueLink) string {
	for _, l := range links {
		if fold(l.Type.Name) == "duplicate" && l.OutwardIssue != nil && l.OutwardIssue.Key != "" {
			return l.OutwardIssue.Key
		}
	}
	return ""
}

// KindSeed normalizes an issue type name into a kind for a newly imported
// issue (task 130 decision 15.4, narrowed: seeded on first import only):
// lower-cased, trimmed, runs of spaces to "-". It is "" unless the result is
// a valid kind that fits MaxKindBytes.
func KindSeed(issuetypeName string) string {
	k := strings.Join(strings.Fields(strings.ToLower(issuetypeName)), "-")
	if len(k) > MaxKindBytes || !kindPattern.MatchString(k) {
		return ""
	}
	return k
}

// Labels keeps the labels that fit MaxLabelBytes, in order, and counts the
// ones it dropped. Jira allows 255 characters; vincent does not.
func Labels(in []string) (kept []string, dropped int) {
	for _, l := range in {
		if len(l) > MaxLabelBytes {
			dropped++
			continue
		}
		kept = append(kept, l)
	}
	return kept, dropped
}

// remoteRecord is the remote_json an imported Jira issue stores. Its first
// keys are GitHub's spelling where they mean the same thing, so the store's
// snapshot decoder (internal/store/issuesnapshot.go) reads them unchanged:
// `state` ("open"/"closed"), `assignee` and `assignees` (display names),
// `created_at` (RFC 3339). The rest are Jira's own fields. Struct order makes
// the encoding deterministic.
type remoteRecord struct {
	State     string    `json:"state"`
	Assignee  string    `json:"assignee,omitempty"`
	Assignees []string  `json:"assignees,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`

	Status            string       `json:"status,omitempty"`
	StatusCategory    string       `json:"status_category,omitempty"`
	Resolution        string       `json:"resolution,omitempty"`
	IssueType         string       `json:"issue_type,omitempty"`
	Priority          string       `json:"priority,omitempty"`
	AssigneeAccountID string       `json:"assignee_account_id,omitempty"`
	Reporter          string       `json:"reporter,omitempty"`
	ReporterAccountID string       `json:"reporter_account_id,omitempty"`
	Parent            string       `json:"parent,omitempty"`
	IssueLinks        []remoteLink `json:"issue_links,omitempty"`
	Components        []string     `json:"components,omitempty"`
	Created           string       `json:"created,omitempty"`
	ResolutionDate    string       `json:"resolution_date,omitempty"`
}

// remoteLink is one issue link: its type, which side the other issue is on,
// and that issue's key.
type remoteLink struct {
	Type      string `json:"type"`
	Direction string `json:"direction"`
	Key       string `json:"key"`
}

// jiraTimeLayouts are the forms Jira writes a timestamp in: its own
// millisecond-and-numeric-offset form, then RFC 3339 for safety.
var jiraTimeLayouts = []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700", time.RFC3339Nano}

func parseJiraTime(s string) time.Time {
	for _, layout := range jiraTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// RemoteJSON encodes the issue's remote_json record. Only the state, which
// is category-only, is derived; everything else is Jira's value as read.
func RemoteJSON(f Fields) json.RawMessage {
	r := remoteRecord{State: string(issuestate.Open)}
	if f.Status != nil {
		if f.Status.StatusCategory.Key == CategoryDone {
			r.State = string(issuestate.Closed)
		}
		r.Status = f.Status.Name
		r.StatusCategory = f.Status.StatusCategory.Key
	}
	if f.Resolution != nil {
		r.Resolution = f.Resolution.Name
	}
	if f.IssueType != nil {
		r.IssueType = f.IssueType.Name
	}
	if f.Priority != nil {
		r.Priority = f.Priority.Name
	}
	if f.Assignee != nil {
		r.Assignee = f.Assignee.DisplayName
		r.AssigneeAccountID = f.Assignee.AccountID
		if r.Assignee != "" {
			r.Assignees = []string{r.Assignee}
		}
	}
	if f.Reporter != nil {
		r.Reporter = f.Reporter.DisplayName
		r.ReporterAccountID = f.Reporter.AccountID
	}
	if f.Parent != nil {
		r.Parent = f.Parent.Key
	}
	for _, l := range f.IssueLinks {
		switch {
		case l.OutwardIssue != nil:
			r.IssueLinks = append(r.IssueLinks, remoteLink{Type: l.Type.Name, Direction: "outward", Key: l.OutwardIssue.Key})
		case l.InwardIssue != nil:
			r.IssueLinks = append(r.IssueLinks, remoteLink{Type: l.Type.Name, Direction: "inward", Key: l.InwardIssue.Key})
		}
	}
	for _, c := range f.Components {
		r.Components = append(r.Components, c.Name)
	}
	r.Created = f.Created
	r.CreatedAt = parseJiraTime(f.Created)
	r.ResolutionDate = f.ResolutionDate
	// remoteRecord holds strings, slices and a time.Time: it cannot fail to
	// encode.
	b, err := json.Marshal(r)
	if err != nil {
		panic("jiramap: encoding remote record: " + err.Error())
	}
	return b
}
