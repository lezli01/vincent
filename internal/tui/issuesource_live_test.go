package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 142.8 against the real API handlers: a provider other than GitHub
// needs no client change. A Jira issue's badge is the key the daemon
// rendered, its state-change note names Jira, and a list mixing it with a
// GitHub issue never collapses a row to gh#N.
func TestJiraIssueSourceAgainstTheRealAPI(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	ctx := t.Context()
	upsert := func(r store.RemoteIssue) {
		t.Helper()
		r.ProjectID, r.State = h.projectID, issuestate.Open
		if _, _, err := h.st.UpsertRemoteIssue(ctx, r, issuestate.Sync); err != nil {
			t.Fatalf("UpsertRemoteIssue: %v", err)
		}
	}
	upsert(store.RemoteIssue{
		Provider: "jira", RemoteKey: "10012", URL: "https://example.atlassian.net/browse/PROJ-12",
		RemoteJSON: `{"key":"PROJ-12"}`, Title: "From Jira",
	})
	list, err := h.m.client.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: h.projectID})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListIssues = %v, %v", list, err)
	}
	jira := list[0]
	if !issuesShareRepo(list) {
		t.Error("a Jira-only list reads as mixed")
	}
	for _, width := range []int{160, 78} {
		if line := ansi.Strip(issueLine(jira, width, false, true)); !strings.Contains(line, "PROJ-12") || strings.Contains(line, "gh#") {
			t.Errorf("%d columns: the Jira row is not badged PROJ-12:\n%s", width, line)
		}
	}
	a := &issueAction{issue: jira, action: issueActClose}
	if note := strings.Join(a.confirmText(), "\n"); !strings.Contains(note, "written to Jira too") || strings.Contains(note, "GitHub") {
		t.Errorf("the close note does not name Jira:\n%s", note)
	}

	upsert(store.RemoteIssue{
		Provider: "github", RemoteKey: "I_41", Repo: "octo/web", Number: 41,
		URL: "https://github.com/octo/web/issues/41", RemoteJSON: `{"state":"open"}`, Title: "From GitHub",
	})
	list, err = h.m.client.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: h.projectID})
	if err != nil || len(list) != 2 {
		t.Fatalf("ListIssues = %v, %v", list, err)
	}
	if issuesShareRepo(list) {
		t.Error("a GitHub and a Jira issue read as one source")
	}
	for _, iss := range list {
		// Narrow enough that a shared list would shorten the badge.
		line := ansi.Strip(issueLine(iss, 78, false, issuesShareRepo(list)))
		if strings.Contains(line, "gh#") {
			t.Errorf("a mixed list collapsed a row to gh#N:\n%s", line)
		}
		if iss.Source.Provider == "github" && !strings.Contains(line, "octo/web#41") {
			t.Errorf("the GitHub row lost its full reference:\n%s", line)
		}
	}
}
