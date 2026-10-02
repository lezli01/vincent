package issues

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// This package imports no provider: PrefillFrom reads a snapshot, so a
// prefill from an imported GitHub issue makes no GitHub call by construction.

func localSnap() *store.IssueSnapshot {
	return &store.IssueSnapshot{
		ID: 12, Title: "  Lock file leaks ", Body: "Seen twice.\r\nOn macOS.\r\n",
		State: "open", Kind: "bug", Labels: []string{"bug", "daemon"},
	}
}

func importedSnap() *store.IssueSnapshot {
	return &store.IssueSnapshot{
		ID: 13, Title: "Select an issue", Body: "Body.", State: "open", Kind: "feature",
		Labels: []string{"enhancement"},
		Remote: &store.IssueSnapshotRemote{
			Provider: "github", Repo: "o/r", Number: 200, URL: "https://github.com/o/r/issues/200",
			Assignees: []string{"hubot", "octo"}, Milestone: "v0.2.0", MilestoneNumber: 4,
		},
	}
}

func allDecls(kind string) []FieldDecl {
	var out []FieldDecl
	for _, name := range []string{FieldIssue, FieldGitHubIssue, FieldLabels, FieldAssignee, FieldMilestone, FieldKind} {
		out = append(out, FieldDecl{Name: name, Type: kind})
	}
	return out
}

func TestPrefillFromALocalIssue(t *testing.T) {
	got := PrefillFrom(localSnap(), allDecls(TypeString))
	want := Prefill{
		// Bare title (decision 2), body without a link line (decision 3).
		Title:       "Lock file leaks",
		Description: "Seen twice.\nOn macOS.",
		Fields:      map[string]string{"issue": "12", "labels": "bug, daemon", "kind": "bug"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prefill =\n %+v\nwant\n %+v", got, want)
	}
}

func TestPrefillFromAnImportedIssue(t *testing.T) {
	got := PrefillFrom(importedSnap(), allDecls(TypeString))
	want := Prefill{
		Title:       "#200 Select an issue",
		Description: "Body.\n\nGitHub issue #200: https://github.com/o/r/issues/200",
		Fields: map[string]string{
			"issue": "13", "github_issue": "200", "labels": "enhancement",
			"assignee": "hubot", "milestone": "v0.2.0", "kind": "feature",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prefill =\n %+v\nwant\n %+v", got, want)
	}

	// Numeric declarations take numbers and nothing else.
	got = PrefillFrom(importedSnap(), allDecls(TypeInteger))
	if want := map[string]string{"issue": "13", "github_issue": "200", "milestone": "4"}; !reflect.DeepEqual(got.Fields, want) {
		t.Errorf("integer fields = %v, want %v", got.Fields, want)
	}
	if got := PrefillFrom(importedSnap(), allDecls(TypeBoolean)); got.Fields != nil {
		t.Errorf("boolean fields = %v, want none", got.Fields)
	}
}

func TestPrefillTitleNeverDoublesThePrefix(t *testing.T) {
	snap := importedSnap()
	snap.Title = "#200 already numbered"
	if got := PrefillFrom(snap, nil).Title; got != "#200 already numbered" {
		t.Errorf("title = %q", got)
	}
	snap.Title, snap.Body = "", ""
	got := PrefillFrom(snap, nil)
	if got.Title != "#200" || got.Description != "GitHub issue #200: https://github.com/o/r/issues/200" {
		t.Errorf("empty issue = %+v", got)
	}
}

// TestPrefillNeverInventsAField: an undeclared name is never filled, and a
// local issue offers no GitHub-only value.
func TestPrefillNeverInventsAField(t *testing.T) {
	got := PrefillFrom(importedSnap(), []FieldDecl{{Name: "Issue"}, {Name: "issue_number"}, {Name: "title"}})
	if got.Fields != nil {
		t.Errorf("fields = %v, want none", got.Fields)
	}
	local := PrefillFrom(localSnap(), []FieldDecl{{Name: FieldGitHubIssue}, {Name: FieldAssignee}, {Name: FieldMilestone}})
	if local.Fields != nil {
		t.Errorf("local fields = %v, want none", local.Fields)
	}
	if got := PrefillFrom(nil, allDecls(TypeString)); !reflect.DeepEqual(got, Prefill{}) {
		t.Errorf("nil snapshot = %+v", got)
	}
}

// TestPrefillDoesNotTruncate (decision 4): the bound lives in the API.
func TestPrefillDoesNotTruncate(t *testing.T) {
	snap := localSnap()
	snap.Body = strings.Repeat("x", 1<<20)
	if got := PrefillFrom(snap, nil).Description; len(got) != 1<<20 {
		t.Errorf("description is %d bytes, want the whole body", len(got))
	}
}
