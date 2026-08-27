package taskrun

import (
	"slices"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/workflow"
)

// issueContext maps a task's stored GitHub issue snapshot onto `.Issue`
// (§8.4, task 035).
//
// A task with no linked issue renders the zero value, which is the whole
// point: `{{ if .Issue.Number }}` is what a template shared between linked
// and unlinked tasks tests, exactly as `.Loop.Index` is (decision 8).
//
// This is the only path from a stored issue to a template, and it reads the
// task row — never the network. That is what keeps §8.4's promise that a step
// render cannot fail for an external reason, and it is why an issue edited on
// GitHub after task creation does not change what a later step renders.
func issueContext(issue *github.Issue) workflow.IssueContext {
	if issue == nil {
		return workflow.IssueContext{}
	}
	return workflow.IssueContext{
		Number:          issue.Number,
		Repo:            issue.Repo,
		Title:           issue.Title,
		Body:            issue.Body,
		URL:             issue.URL,
		State:           issue.State,
		Labels:          slices.Clone(issue.Labels),
		Author:          issue.Author,
		Assignee:        issue.Assignee,
		Milestone:       issue.Milestone,
		MilestoneNumber: issue.MilestoneNumber,
	}
}

// cloneIssue copies a snapshot for a task that inherits it — a `fan_out` lane
// (§7.6, task 035 decision 9). Lanes already inherit `Fields`, and a lane
// prompt that could read `.Task.Fields` but not `.Issue` would be an
// arbitrary hole. The copy is deep because the two rows are independent
// tasks: nothing today mutates a snapshot, and sharing a slice between rows
// is not a property worth relying on.
func cloneIssue(issue *github.Issue) *github.Issue {
	if issue == nil {
		return nil
	}
	copied := *issue
	copied.Labels = slices.Clone(issue.Labels)
	return &copied
}
