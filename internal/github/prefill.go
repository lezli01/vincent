package github

import (
	"fmt"
	"strconv"
	"strings"
)

// The three declared field names an issue can fill, matched **exactly**
// (task 035 decision 7). No aliases, no fuzzy matching, no case folding: a
// guess that has to be reviewed is cheap, a guess that is hard to predict is
// not.
const (
	FieldLabels    = "labels"
	FieldAssignee  = "assignee"
	FieldMilestone = "milestone"
)

// The §8.1.2 field-type vocabulary, restated here so this package stays a
// leaf. internal/workflow owns the definition; these are the four spellings
// its `type:` accepts, and the mapping below reads nothing else.
const (
	TypeString  = "string"
	TypeInteger = "integer"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
)

// FieldDecl is one workflow-declared field reduced to what the mapping reads.
// The caller keeps the full declaration and is the one that validates the
// candidate against it — this package deliberately owns no second copy of
// §8.1.2's validation (decision 7).
type FieldDecl struct {
	Name string
	Type string
}

// Candidate is the value this issue offers for a declared field, and false
// when it offers none.
//
// Only the three names above match, and only when the declared type can hold
// the value: a declared `integer` named `milestone` gets the milestone
// *number*, a `string` gets its title, and a declared `boolean` named
// `labels` gets nothing at all. The caller must still validate the result
// against the declaration's `pattern` before offering it — a candidate that
// would fail validation must be left empty rather than pre-filling a value
// the create call would 400 on.
func Candidate(issue Issue, decl FieldDecl) (string, bool) {
	kind := decl.Type
	if kind == "" {
		kind = TypeString
	}
	switch decl.Name {
	case FieldLabels:
		if kind != TypeString || len(issue.Labels) == 0 {
			return "", false
		}
		return issue.LabelList(), true
	case FieldAssignee:
		if kind != TypeString || issue.Assignee == "" {
			return "", false
		}
		return issue.Assignee, true
	case FieldMilestone:
		switch kind {
		case TypeString:
			if issue.Milestone == "" {
				return "", false
			}
			return issue.Milestone, true
		case TypeInteger, TypeNumber:
			if issue.MilestoneNumber == 0 {
				return "", false
			}
			return strconv.Itoa(issue.MilestoneNumber), true
		default:
			return "", false
		}
	default:
		// Undeclared names are never invented (decision 7): issue metadata
		// reaches templates through `.Issue`, which is what the "flatten
		// everything into Task.Fields" alternative was rejected for.
		return "", false
	}
}

// LinkLine is the trailing pointer appended to a prefilled description:
// `GitHub issue #N: <url>`. It is plain text in an editable row — a human may
// delete it — and it is what makes a task read on its own still point back.
func LinkLine(issue Issue) string {
	return fmt.Sprintf("GitHub issue #%d: %s", issue.Number, issue.URL)
}

// Description is the issue body followed by the link line as its own trailing
// block: a blank line, then the link (decision 7). An empty body yields the
// link alone rather than a leading blank line.
func Description(issue Issue) string {
	// GitHub bodies arrive with CRLF from the web editor; a description that
	// carried them would put stray carriage returns into every agent prompt
	// that interpolates it. Normalized *before* the trim, or the trim leaves
	// the final "\r" behind.
	body := strings.ReplaceAll(issue.Body, "\r\n", "\n")
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		return LinkLine(issue)
	}
	return body + "\n\n" + LinkLine(issue)
}

// Title is the issue title, verbatim. It is truncated only by the same rules
// any typed title gets, which live in the API's own bounds check — this
// returns the whole thing so those rules stay in one place.
func Title(issue Issue) string { return strings.TrimSpace(issue.Title) }
