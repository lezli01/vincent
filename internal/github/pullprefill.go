package github

import (
	"fmt"
	"strconv"
	"strings"
)

// This file is the pull-request half of the prefill (task 064 decision 9). It
// mirrors the issue half in prefill.go rather than sharing its body: an issue
// and a pull request offer different metadata, and a function that took both
// would have to invent an "either" type whose zero value is a third case.

// FieldPull is the one declared field name a pull request fills, matched
// **exactly**, like every other candidate (035 decision 7). It exists for the
// same reason FieldIssue does: a `run:` step receives §8.5's environment and
// not §8.4's template context, so the number has to be somewhere a step body
// can read it. There is no `.Pull` to read it from either — 052's "a pull
// request is a pointer, never a snapshot" stands (064 decision 11) — which
// makes this the *only* way a workflow finds the number.
const FieldPull = "pull"

// PullCandidate is the value this pull request offers for a declared field,
// and false when it offers none.
//
// Only FieldPull matches, and only for a declaration that can hold a number:
// a `string` gets the decimal spelling, bare, without the "#" the title
// carries. Labels, assignee and milestone are deliberately absent — `gh pr`'s
// field set here does not carry them, and offering a name this leg cannot
// fill would make the prefill's shape depend on which leg answered.
func PullCandidate(pull PullRequest, decl FieldDecl) (string, bool) {
	kind := decl.Type
	if kind == "" {
		kind = TypeString
	}
	if decl.Name != FieldPull {
		return "", false
	}
	switch kind {
	case TypeString, TypeInteger, TypeNumber:
		if pull.Number == 0 {
			return "", false
		}
		return strconv.Itoa(pull.Number), true
	default:
		return "", false
	}
}

// PullLinkLine is the trailing pointer appended to a prefilled description:
// `GitHub pull request #N: <url>`. Plain text in an editable row, exactly
// like an issue's.
func PullLinkLine(pull PullRequest) string {
	link := pull.URL
	if link == "" {
		// A stored link carries a repo and a number and no URL; the page is
		// built from this package's own template rather than from any string
		// GitHub returned, the way PullURL's callers already do.
		if owner, name, ok := strings.Cut(pull.Repo, "/"); ok && owner != "" && name != "" {
			link = PullURL(Repo{Owner: owner, Name: name}, pull.Number)
		}
	}
	return fmt.Sprintf("GitHub pull request #%d: %s", pull.Number, link)
}

// PullDescription is the pull request body followed by the link line as its
// own trailing block, the same shape Description gives an issue.
func PullDescription(pull PullRequest) string {
	// GitHub bodies arrive with CRLF from the web editor; normalized before
	// the trim, or the trim leaves the final "\r" behind.
	body := strings.ReplaceAll(pull.Body, "\r\n", "\n")
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		return PullLinkLine(pull)
	}
	return body + "\n\n" + PullLinkLine(pull)
}

// PullTitle is the pull request title prefixed with its own number, for the
// reason Title prefixes an issue's: it is for the humans reading a board row.
// It is deliberately *not* how a workflow finds the number — FieldPull is —
// and on a task created from a pull request it is not how the branch is named
// either, since that branch is the pull request's head (decision 1).
func PullTitle(pull PullRequest) string {
	title := strings.TrimSpace(pull.Title)
	if pull.Number == 0 {
		return title
	}
	prefix := "#" + strconv.Itoa(pull.Number)
	switch {
	case title == "":
		return prefix
	case title == prefix, strings.HasPrefix(title, prefix+" "):
		return title
	default:
		return prefix + " " + title
	}
}

// joinRepo assembles `owner/name` from the two halves `gh` reports
// separately, and returns "" unless it has both: a half-identity is not a
// repository, and letting one through would make Fork compare against `"/x"`.
func joinRepo(owner, name string) string {
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}
