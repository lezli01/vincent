package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The task workspace's Issue section (task 130.13): a task started from a
// vincent issue renders the issue, and a legacy row created through
// `github_issue` still renders the GitHub issue it captured.

func TestIssueSectionRendersALegacyGitHubIssue(t *testing.T) {
	d := taskDetailFixture(t)
	d.task.GitHubIssue = &apiclient.GitHubIssue{
		Repo: "octo/repo", Number: 200, Title: "Crash on start", State: "open",
		URL: "https://github.com/octo/repo/issues/200",
	}
	plain := ansi.Strip(strings.Join(newTaskView(d).detailLines(120), "\n"))
	if !hasDetailSection(plain, "Issue") {
		t.Fatalf("a legacy row has no Issue section:\n%s", plain)
	}
	if !strings.Contains(plain, "octo/repo#200 · Crash on start") {
		t.Errorf("the captured issue is not rendered:\n%s", plain)
	}
}

func TestIssueSectionRendersTheLinkedIssue(t *testing.T) {
	d := taskDetailFixture(t)
	d.task.Issue = &apiclient.TaskIssue{
		ID: 7, Title: "Crash on start", State: "closed",
		Source: &apiclient.IssueSource{Provider: "github", Repo: "octo/repo", Number: 200},
	}
	// The derived github_issue an imported issue's task also carries is not
	// a second section.
	d.task.GitHubIssue = &apiclient.GitHubIssue{Repo: "octo/repo", Number: 200, Title: "Crash on start"}
	plain := ansi.Strip(strings.Join(newTaskView(d).detailLines(120), "\n"))
	if !hasDetailSection(plain, "Issue") || hasDetailSection(plain, "GitHub issue") {
		t.Errorf("want one Issue section:\n%s", plain)
	}
	for _, want := range []string{"#7 · Crash on start", "closed", "octo/repo#200"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Issue section lacks %q:\n%s", want, plain)
		}
	}
	tv := newTaskView(d)
	tv.detail.loaded = true
	if extras := tv.paletteExtras(); len(extras) != 1 || !strings.Contains(extras[0].label, "#7") {
		t.Errorf("palette extras = %+v, want the open-this-issue row", extras)
	}
}

func TestNoIssueNoPaletteRow(t *testing.T) {
	d := taskDetailFixture(t)
	tv := newTaskView(d)
	tv.detail.loaded = true
	if extras := tv.paletteExtras(); len(extras) != 0 {
		t.Errorf("a task without an issue offers %+v", extras)
	}
}

func hasDetailSection(plain, title string) bool {
	for _, s := range splitTaskDetailDocument(strings.Split(plain, "\n")).sections {
		if s.title == title {
			return true
		}
	}
	return false
}
