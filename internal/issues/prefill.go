package issues

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/store"
)

// The declared field names a vincent issue can fill, matched **exactly**
// (task 035 decision 7, retargeted by task 130 decision 7). No aliases, no
// fuzzy matching, no case folding.
//
// FieldIssue receives the vincent issue id and FieldGitHubIssue the GitHub
// number of an imported issue (task 130 decision 8): a workflow that hands a
// number to `gh` reads `github_issue`, so a vincent id never reaches GitHub.
const (
	FieldIssue       = "issue"
	FieldGitHubIssue = "github_issue"
	FieldLabels      = "labels"
	FieldAssignee    = "assignee"
	FieldMilestone   = "milestone"
	FieldKind        = "kind"
)

// The §8.1.2 field-type vocabulary, restated so this package imports only
// internal/store and internal/issuestate. internal/workflow owns the
// definition.
const (
	TypeString  = "string"
	TypeInteger = "integer"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
)

// providerGitHub is issue_remotes.provider for an imported GitHub issue.
const providerGitHub = "github"

// FieldDecl is one workflow-declared field reduced to what the mapping
// reads. The caller keeps the full declaration and is the one that
// validates a candidate against it (its `pattern`, `enum`, bounds): this
// package owns no second copy of §8.1.2's validation.
type FieldDecl struct {
	Name string
	Type string
}

// Prefill is what creating a task from an issue would fill in: a title, a
// description, and the candidate values for the declared fields the issue
// can fill. Fields holds only candidates; the caller drops any its own
// declaration rejects and lets an explicit request value win over the rest.
type Prefill struct {
	Title       string
	Description string
	Fields      map[string]string
}

// PrefillFrom computes the prefill for a task created from snap with decls
// declared (task 130 decision 7). It is pure: it reads the snapshot the task
// will carry, so what the form prefills and what `.Issue` later renders are
// the same issue, and it makes no provider call — a GitHub-imported issue is
// prefilled from what sync already stored.
//
// A local issue's title is its bare title and its description its body; an
// imported GitHub issue keeps the `#N title` and trailing link line the
// removed `github_issue` create path produced (task 035), so a board row
// still reads as the GitHub issue it came from.
func PrefillFrom(snap *store.IssueSnapshot, decls []FieldDecl) Prefill {
	if snap == nil {
		return Prefill{}
	}
	out := Prefill{Title: prefillTitle(snap), Description: prefillDescription(snap)}
	for _, decl := range decls {
		value, ok := candidate(snap, decl)
		if !ok {
			continue
		}
		if out.Fields == nil {
			out.Fields = map[string]string{}
		}
		out.Fields[decl.Name] = value
	}
	return out
}

// gitHubRemote is snap's GitHub reference, nil for a local issue.
func gitHubRemote(snap *store.IssueSnapshot) *store.IssueSnapshotRemote {
	if snap.Remote == nil || snap.Remote.Provider != providerGitHub || snap.Remote.Number == 0 {
		return nil
	}
	return snap.Remote
}

// prefillTitle is the bare title for a local issue — a `#N` prefix would
// read as a GitHub number (decision 2) — and `#<GitHub number> title` for an
// imported one, never doubling a prefix the title already carries.
func prefillTitle(snap *store.IssueSnapshot) string {
	title := strings.TrimSpace(snap.Title)
	rem := gitHubRemote(snap)
	if rem == nil {
		return title
	}
	prefix := "#" + strconv.Itoa(rem.Number)
	switch {
	case title == "":
		return prefix
	case title == prefix, strings.HasPrefix(title, prefix+" "):
		return title
	default:
		return prefix + " " + title
	}
}

// prefillDescription is the body with CRLF normalized. An imported issue
// gets the `GitHub issue #N: <url>` link line as its own trailing block; a
// local one gets none (decision 3). Nothing is truncated here: an oversized
// body fails the API's bound check the way a pasted one would (decision 4).
func prefillDescription(snap *store.IssueSnapshot) string {
	body := strings.ReplaceAll(snap.Body, "\r\n", "\n")
	body = strings.TrimRight(body, "\n")
	rem := gitHubRemote(snap)
	if rem == nil {
		return body
	}
	link := fmt.Sprintf("GitHub issue #%d: %s", rem.Number, rem.URL)
	if strings.TrimSpace(body) == "" {
		return link
	}
	return body + "\n\n" + link
}

// candidate is the value snap offers for decl, and false when it offers
// none — an undeclared name, a value the declared type cannot hold, or a
// value the issue does not have.
func candidate(snap *store.IssueSnapshot, decl FieldDecl) (string, bool) {
	kind := decl.Type
	if kind == "" {
		kind = TypeString
	}
	numeric := kind == TypeString || kind == TypeInteger || kind == TypeNumber
	rem := gitHubRemote(snap)
	switch decl.Name {
	case FieldIssue:
		if !numeric || snap.ID == 0 {
			return "", false
		}
		return strconv.FormatInt(snap.ID, 10), true
	case FieldGitHubIssue:
		if !numeric || rem == nil {
			return "", false
		}
		return strconv.Itoa(rem.Number), true
	case FieldLabels:
		if kind != TypeString || len(snap.Labels) == 0 {
			return "", false
		}
		return strings.Join(snap.Labels, ", "), true
	case FieldAssignee:
		if kind != TypeString || rem == nil || len(rem.Assignees) == 0 {
			return "", false
		}
		return rem.Assignees[0], true
	case FieldMilestone:
		if rem == nil {
			return "", false
		}
		switch {
		case kind == TypeString && rem.Milestone != "":
			return rem.Milestone, true
		case (kind == TypeInteger || kind == TypeNumber) && rem.MilestoneNumber != 0:
			return strconv.Itoa(rem.MilestoneNumber), true
		default:
			return "", false
		}
	case FieldKind:
		if kind != TypeString || snap.Kind == "" {
			return "", false
		}
		return snap.Kind, true
	default:
		// Undeclared names are never invented: issue metadata reaches
		// templates through `.Issue`.
		return "", false
	}
}
