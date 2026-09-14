package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/trigger"
)

// triggerDirName is {config_dir}/triggers (§12.2, task 096 decision 8).
const triggerDirName = "triggers"

// triggerListLimit bounds one GitHub trigger listing. A seed lists the most
// recently updated items and a catch-up lists those updated since the
// watermark; a hundred is more than a tick's worth of change on any
// repository a laptop daemon watches, and the diff tolerates an item it has
// never seen by taking it as a baseline.
const triggerListLimit = 100

// Reasons a GitHub trigger's poll status reads failing (decision 31D: never
// quiet).
var (
	errTriggerGitHubOff      = errors.New("github.enabled is off in config.yaml")
	errTriggerGitHubNoPoll   = errors.New("github.poll_interval is 0 in config.yaml, so GitHub is never polled")
	errTriggerGitHubNotRepo  = errors.New("the project's origin remote is not a github.com repository")
	errTriggerGitHubNoClient = errors.New("no GitHub client is wired")
)

// newTriggerGitHubLister is the fetch GitHub triggers are judged from: the
// daemon owns the network, internal/trigger receives listings.
func newTriggerGitHubLister(
	st *store.Store, cfg func() config.Config, git *gitx.Git, client *github.Client,
) trigger.GitHubLister {
	return func(ctx context.Context, projectID int64, want trigger.GitHubWant) trigger.GitHubListing {
		if !cfg().GitHub.Enabled {
			return trigger.GitHubListing{Err: errTriggerGitHubOff}
		}
		if client == nil || git == nil {
			return trigger.GitHubListing{Err: errTriggerGitHubNoClient}
		}
		project, err := st.GetProject(ctx, projectID)
		if err != nil {
			return trigger.GitHubListing{Err: fmt.Errorf("project %d: %w", projectID, err)}
		}
		repo, ok := githubRepoFor(ctx, git, *project)
		if !ok {
			return trigger.GitHubListing{Err: errTriggerGitHubNotRepo}
		}
		return listForTriggers(ctx, client, repo, want)
	}
}

// listForTriggers makes at most one issues and one pulls listing.
func listForTriggers(ctx context.Context, client *github.Client, repo github.Repo, want trigger.GitHubWant) trigger.GitHubListing {
	var out trigger.GitHubListing
	opts := github.ListOptions{State: github.StateAll, Limit: triggerListLimit, Since: want.Since}
	if want.Issues {
		issues, err := client.List(ctx, repo, opts)
		if err != nil {
			return trigger.GitHubListing{Err: fmt.Errorf("listing issues of %s: %s", repo, github.ReasonOf(err))}
		}
		for i := range issues {
			is := &issues[i]
			out.Issues = append(out.Issues, trigger.GitHubIssue{
				Number: is.Number, Title: is.Title, Body: is.Body, URL: is.URL, Author: is.Author,
				State: is.State, Labels: is.Labels, Assignees: issueAssignees(is),
				CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt,
			})
		}
	}
	if want.Pulls {
		pulls, err := client.ListPulls(ctx, repo, opts)
		if err != nil {
			return trigger.GitHubListing{Err: fmt.Errorf("listing pull requests of %s: %s", repo, github.ReasonOf(err))}
		}
		for i := range pulls {
			p := &pulls[i]
			out.Pulls = append(out.Pulls, trigger.GitHubPull{
				Number: p.Number, Title: p.Title, Body: p.Body, URL: p.URL, Author: p.Author,
				State: p.State, HeadRef: p.HeadBranch, BaseRef: p.BaseBranch, Draft: p.Draft, Merged: p.Merged,
				Labels: p.Labels, RequestedReviewers: p.RequestedReviewers,
				CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
			})
		}
	}
	return out
}

func issueAssignees(is *github.Issue) []string {
	if len(is.Assignees) > 0 {
		return is.Assignees
	}
	if is.Assignee != "" {
		return []string{is.Assignee}
	}
	return nil
}

// githubRepoFor derives a project's GitHub identity from its `origin` remote
// at the point of use — the same derivation the API does, and the same known
// narrowness: an SSH alias, or a GitHub remote not named `origin`, is simply
// not GitHub-based (task 035 decision 5, unreversed by task 052).
func githubRepoFor(ctx context.Context, git *gitx.Git, project store.Project) (github.Repo, bool) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	remote, err := git.Run(ctx, project.Path, "remote", "get-url", "origin")
	if err != nil {
		return github.Repo{}, false
	}
	return github.ParseRemote(remote)
}
