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

// TestCandidateMapsOnlyTheThreeNames, by exact match and by type. A guess
// that has to be reviewed is cheap; a guess that is hard to predict is not,
// which is why there are no aliases and no case folding (decision 7).
func TestCandidateMapsOnlyTheThreeNames(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		want       string
		offered    bool
	}{
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

// TestTitleIsTheIssueTitle: verbatim, trimmed. Any bound on its length is the
// API's, applied to a typed title and a prefilled one alike.
func TestTitleIsTheIssueTitle(t *testing.T) {
	issue := sample
	issue.Title = "  padded  "
	if got := Title(issue); got != "padded" {
		t.Errorf("Title = %q, want %q", got, "padded")
	}
}
