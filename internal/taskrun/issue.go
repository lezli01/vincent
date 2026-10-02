package taskrun

import (
	"slices"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// issueContext maps a task's stored issue snapshot onto `.Issue` (§8.4, task
// 035, task 130 decision 8). The order is fixed:
//
//   - the vincent snapshot (Task.Issue): Number is the vincent id and Source
//     the remote reference, zero for a local issue;
//   - else the legacy GitHub snapshot (Task.GitHubIssue, written by the
//     `github_issue` create path until 130.11 removes it): Number is the
//     GitHub number and Source that same reference, so a template written
//     before the reshape renders byte-identically;
//   - else the zero value.
//
// A task with no linked issue renders the zero value, which is the whole
// point: `{{ if .Issue.Number }}` is what a template shared between linked
// and unlinked tasks tests, exactly as `.Loop.Index` is (decision 8).
//
// This is the only path from a stored issue to a template, and it reads the
// task row — never the network. That is what keeps §8.4's promise that a step
// render cannot fail for an external reason, and it is why an issue edited
// after task creation does not change what a later step renders.
func issueContext(task *store.Task) workflow.IssueContext {
	if task == nil {
		return workflow.IssueContext{}
	}
	if snap := task.Issue; snap != nil {
		ctx := workflow.IssueContext{
			Number:   int(snap.ID),
			Title:    snap.Title,
			Body:     snap.Body,
			State:    snap.State,
			Labels:   slices.Clone(snap.Labels),
			Kind:     snap.Kind,
			Priority: snap.Priority,
			Author:   snap.Author,
		}
		if rem := snap.Remote; rem != nil {
			ctx.Source = workflow.IssueSource{
				Provider: rem.Provider,
				Repo:     rem.Repo,
				Number:   rem.Number,
				URL:      rem.URL,
				State:    rem.State,
			}
			ctx.Repo, ctx.URL = rem.Repo, rem.URL
			if len(rem.Assignees) > 0 {
				ctx.Assignee = rem.Assignees[0]
			}
			ctx.Milestone, ctx.MilestoneNumber = rem.Milestone, rem.MilestoneNumber
		}
		return ctx
	}
	if issue := task.GitHubIssue; issue != nil {
		return workflow.IssueContext{
			Number:          issue.Number,
			Title:           issue.Title,
			Body:            issue.Body,
			State:           issue.State,
			Labels:          slices.Clone(issue.Labels),
			Author:          issue.Author,
			Assignee:        issue.Assignee,
			Milestone:       issue.Milestone,
			MilestoneNumber: issue.MilestoneNumber,
			Source: workflow.IssueSource{
				Provider: providerGitHub,
				Repo:     issue.Repo,
				Number:   issue.Number,
				URL:      issue.URL,
				State:    issue.State,
			},
			Repo: issue.Repo,
			URL:  issue.URL,
		}
	}
	return workflow.IssueContext{}
}

// providerGitHub is the Source.Provider of a legacy GitHub snapshot — the
// same spelling issue_remotes.provider uses for an imported GitHub issue.
const providerGitHub = "github"

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

// cloneInt64 copies a nullable id, so a lane's IssueID is its own pointer
// rather than an alias of the parent row's field.
func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
