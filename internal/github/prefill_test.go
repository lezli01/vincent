package github

import (
	"strings"
	"testing"
)

var sample = Issue{
	Repo:            "octo/repo",
	Number:          200,
	Title:           "Select a GitHub issue when creating a task",
	Body:            "### Problem\n\nSomething is wrong.",
	URL:             "https://github.com/octo/repo/issues/200",
	State:           StateOpen,
	Labels:          []string{"enhancement", "area/api"},
	Author:          "octocat",
	Assignee:        "hubot",
	Milestone:       "v0.2.0",
	MilestoneNumber: 4,
}

// TestDescriptionAppendsTheLinkLine: the link is its own trailing block — a
// blank line, then `GitHub issue #N: <url>` — so a task read on its own still
// points back (decision 7).
func TestDescriptionAppendsTheLinkLine(t *testing.T) {
	got := Description(sample)
	want := "### Problem\n\nSomething is wrong.\n\nGitHub issue #200: https://github.com/octo/repo/issues/200"
	if got != want {
		t.Errorf("Description =\n%q\nwant\n%q", got, want)
	}
}

// TestDescriptionOfAnEmptyBody: the link alone, with no leading blank line.
// An issue filed as a title and nothing else is common, and a description
// that opened with whitespace would be visible in every prompt it reaches.
func TestDescriptionOfAnEmptyBody(t *testing.T) {
	issue := sample
	issue.Body = ""
	if got := Description(issue); got != LinkLine(issue) {
		t.Errorf("Description = %q, want just the link line %q", got, LinkLine(issue))
	}
	issue.Body = "   \n\n  "
	if got := Description(issue); got != LinkLine(issue) {
		t.Errorf("a whitespace-only body produced %q, want just the link line", got)
	}
}

// TestDescriptionNormalizesCRLF: GitHub's web editor writes CRLF, and a
// description carrying them would put stray carriage returns into every agent
// prompt that interpolates it.
func TestDescriptionNormalizesCRLF(t *testing.T) {
	issue := sample
	issue.Body = "line one\r\nline two\r\n"
	if got := Description(issue); strings.Contains(got, "\r") {
		t.Errorf("Description kept carriage returns: %q", got)
	}
}

// TestCandidateMapsOnlyTheFourNames, by exact match and by type. A guess
// that has to be reviewed is cheap; a guess that is hard to predict is not,
// which is why there are no aliases and no case folding (decision 7).
func TestCandidateMapsOnlyTheFourNames(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		want       string
		offered    bool
	}{
		{FieldIssue, TypeInteger, "200", true},
		{FieldIssue, TypeNumber, "200", true},
		// A `string` issue field gets the bare decimal spelling, not the "#"
		// the prefilled title carries.
		{FieldIssue, TypeString, "200", true},
		{FieldIssue, TypeBoolean, "", false},
		{FieldLabels, TypeString, "enhancement, area/api", true},
		{FieldAssignee, TypeString, "hubot", true},
		{FieldMilestone, TypeString, "v0.2.0", true},
		{FieldMilestone, TypeInteger, "4", true},
		{FieldMilestone, TypeNumber, "4", true},
		// An omitted `type:` is normalized to string by the workflow loader;
		// the mapping treats it the same rather than refusing.
		{FieldLabels, "", "enhancement, area/api", true},
		// Type mismatches: nothing is offered rather than something the
		// create call would then 400 on.
		{FieldLabels, TypeInteger, "", false},
		{FieldLabels, TypeBoolean, "", false},
		{FieldAssignee, TypeInteger, "", false},
		{FieldMilestone, TypeBoolean, "", false},
		// Names that are close but not exact. Undeclared names are never
		// invented, and near-misses are never guessed at.
		{"Labels", TypeString, "", false},
		{"LABELS", TypeString, "", false},
		{"label", TypeString, "", false},
		{"github_labels", TypeString, "", false},
		{"ticket", TypeString, "", false},
	} {
		got, ok := Candidate(sample, FieldDecl{Name: tc.name, Type: tc.kind})
		if ok != tc.offered {
			t.Errorf("Candidate(%q, %q) offered = %v, want %v", tc.name, tc.kind, ok, tc.offered)
			continue
		}
		if got != tc.want {
			t.Errorf("Candidate(%q, %q) = %q, want %q", tc.name, tc.kind, got, tc.want)
		}
	}
}

// TestCandidateOffersNothingForMissingMetadata: an issue with no labels, no
// assignee and no milestone leaves every declared field empty rather than
// filling it with a blank.
func TestCandidateOffersNothingForMissingMetadata(t *testing.T) {
	bare := Issue{Number: 41, Title: "bare", URL: "u", State: StateOpen}
	for _, name := range []string{FieldLabels, FieldAssignee, FieldMilestone} {
		for _, kind := range []string{TypeString, TypeInteger} {
			if value, ok := Candidate(bare, FieldDecl{Name: name, Type: kind}); ok {
				t.Errorf("Candidate(%q, %q) offered %q for an issue with no metadata", name, kind, value)
			}
		}
	}
}

// TestTitleCarriesTheIssueNumber: trimmed, and prefixed with `#N` so a board
// row and a branch slug say which issue the task came from. Any bound on its
// length is the API's, applied to a typed title and a prefilled one alike.
func TestTitleCarriesTheIssueNumber(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"  padded  ", "#200 padded"},
		{"Select a GitHub issue", "#200 Select a GitHub issue"},
		// Already prefixed: left alone rather than doubled into "#200 #200 …".
		{"#200 already numbered", "#200 already numbered"},
		{"#200", "#200"},
		// A near-miss is a different issue's number and stays where it is.
		{"#2000 a different issue", "#200 #2000 a different issue"},
		// No title at all yields the prefix alone, not a trailing space.
		{"   ", "#200"},
	} {
		issue := sample
		issue.Title = tc.title
		if got := Title(issue); got != tc.want {
			t.Errorf("Title(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

// TestTitleWithoutANumber: the zero Issue is what an unlinked task renders,
// and prefixing "#0" onto anything would be a lie about a real issue.
func TestTitleWithoutANumber(t *testing.T) {
	if got := Title(Issue{Title: "  no issue  "}); got != "no issue" {
		t.Errorf("Title = %q, want %q", got, "no issue")
	}
}

// TestCandidateOffersNoIssueNumberWithoutOne: same reason — a declared
// `issue` field is left empty rather than filled with 0.
func TestCandidateOffersNoIssueNumberWithoutOne(t *testing.T) {
	for _, kind := range []string{TypeString, TypeInteger, TypeNumber} {
		if value, ok := Candidate(Issue{Title: "unlinked"}, FieldDecl{Name: FieldIssue, Type: kind}); ok {
			t.Errorf("Candidate(issue, %q) offered %q for an issue with no number", kind, value)
		}
	}
}
