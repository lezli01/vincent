package store

import (
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// jiraRemoteIn seeds a Jira row the way task 142 decision 9.4's dated note
// (issue #905) fixes the convention #908's importer must write: the Jira
// project key in repo, the issue number in number, and remote_key the stable
// Jira issue id. No Jira importer exists yet, so the row is written directly.
func jiraRemoteIn(projectID int64, id, project string, number int) RemoteIssue {
	return RemoteIssue{
		ProjectID: projectID, Provider: "jira", RemoteKey: id, Repo: project, Number: number,
		URL: "https://example.atlassian.net/browse/" + project, RemoteJSON: `{"n":1}`, Title: "jira " + id,
		State: issuestate.Open,
	}
}

// TestIssueIDsByRemoteRef: a GitHub reference matches any repo, a Jira one
// its project key case-insensitively; both are scoped by project and
// provider, and a tombstone never answers.
func TestIssueIDsByRemoteRef(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	other := testProject(t, s, "p2")
	gh := mustUpsertRemote(t, s, remoteIn(p.ID, "I_142", 142))
	jira := mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "10042", "PROJ", 12))
	mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "10043", "OTHER", 12))
	mustUpsertRemote(t, s, jiraRemoteIn(other.ID, "20042", "PROJ", 12))

	for _, q := range []struct {
		provider, repo string
		number         int
		want           []int64
	}{
		{"github", "", 142, []int64{gh.ID}},
		{"github", "o/r", 142, []int64{gh.ID}},
		{"github", "o/x", 142, []int64{}},
		{"jira", "PROJ", 12, []int64{jira.ID}},
		{"jira", "proj", 12, []int64{jira.ID}},
		{"jira", "PROJ", 13, []int64{}},
		{"jira", "", 142, []int64{}},
		{"github", "", 12, []int64{}},
	} {
		got, err := s.IssueIDsByRemoteRef(ctx, p.ID, q.provider, q.repo, q.number)
		if err != nil || !slices.Equal(got, q.want) {
			t.Errorf("%s %q #%d = %v, %v; want %v", q.provider, q.repo, q.number, got, err, q.want)
		}
	}
	if got, err := s.IssueIDsByRemoteNumber(ctx, p.ID, "github", 142); err != nil || !slices.Equal(got, []int64{gh.ID}) {
		t.Errorf("IssueIDsByRemoteNumber = %v, %v; want [%d]", got, err, gh.ID)
	}

	if err := s.DeleteIssue(ctx, jira.ID, issuestate.Human); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	if got, err := s.IssueIDsByRemoteRef(ctx, p.ID, "jira", "PROJ", 12); err != nil || len(got) != 0 {
		t.Errorf("tombstone answered: %v, %v", got, err)
	}
}
