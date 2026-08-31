package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent github` (spec §12.1, task 035, task 069). It exists so issues can
// be browsed without opening the TUI — the same reason every other data view
// has a subcommand.
//
// It was read-only until task 069 and now has exactly one write, `pr create`,
// which is the same amendment decision record row 27 took: one write path, for
// pull-request creation, on a human's say-so. `issues`, `prs` and `status`
// still write nothing.
func newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Read GitHub issues and pull requests, and open one for a task",
	}
	cmd.AddCommand(newGitHubIssuesCmd(), newGitHubPullsCmd(), newGitHubPRCmd(), newGitHubStatusCmd())
	return cmd
}

func newGitHubIssuesCmd() *cobra.Command {
	var (
		projectID int64
		state     string
		limit     int
	)
	cmd := &cobra.Command{
		Use:   "issues",
		Short: "List a project's GitHub issues, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				issues, err := c.ListGitHubIssues(ctx, projectID, apiclient.GitHubIssuesOptions{
					State: state, Limit: limit,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					if issues == nil {
						issues = []apiclient.GitHubIssue{}
					}
					return emitJSON(cmd.OutOrStdout(), issues)
				}
				rows := make([][]string, 0, len(issues))
				for _, i := range issues {
					rows = append(rows, []string{
						"#" + strconv.Itoa(i.Number), i.State, i.Title,
						dash(i.LabelList()), dash(i.Assignee),
					})
				}
				return table(cmd.OutOrStdout(),
					[]string{"ISSUE", "STATE", "TITLE", "LABELS", "ASSIGNEE"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	cmd.Flags().StringVar(&state, "state", "open", "Issue state: open, closed or all")
	cmd.Flags().IntVar(&limit, "limit", 0, "How many issues to list (default: the daemon's)")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

// `vincent github prs` is the listing without the TUI (task 052), so it is
// scriptable and a gate script can assert it over curl and jq without driving
// a terminal. Open by default; `--state` reaches the closed and merged ones a
// task can now be created from (task 064 decision 9).
func newGitHubPullsCmd() *cobra.Command {
	var (
		projectID int64
		state     string
		limit     int
	)
	cmd := &cobra.Command{
		Use:   "prs",
		Short: "List a project's GitHub pull requests, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				pulls, err := c.ListGitHubPulls(ctx, projectID,
					apiclient.GitHubPullsOptions{State: state, Limit: limit})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					if pulls == nil {
						pulls = []apiclient.GitHubPullRequest{}
					}
					return emitJSON(cmd.OutOrStdout(), pulls)
				}
				rows := make([][]string, 0, len(pulls))
				for _, p := range pulls {
					task := "-"
					if p.TaskID != nil {
						task = "#" + strconv.FormatInt(*p.TaskID, 10)
					}
					rows = append(rows, []string{
						"#" + strconv.Itoa(p.Number), p.Status(), p.Title,
						dash(p.HeadBranch), task,
					})
				}
				return table(cmd.OutOrStdout(),
					[]string{"PR", "STATE", "TITLE", "BRANCH", "TASK"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	cmd.Flags().StringVar(&state, "state", "open", "Pull request state: open, closed or all")
	cmd.Flags().IntVar(&limit, "limit", 0, "How many pull requests to list (default: the daemon's)")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

// `vincent github status` is the per-project half of the `vincent doctor`
// row: doctor answers "can this machine read GitHub at all", this answers
// "and is *this* project one it would read".
func newGitHubStatusCmd() *cobra.Command {
	var projectID int64
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether a project's issues can be read",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				status, err := c.ProjectGitHub(ctx, projectID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), status)
				}
				rows := [][]string{
					{"enabled", boolWord(status.Enabled)},
					{"repo", dash(status.Repo)},
				}
				if status.Available {
					rows = append(rows, []string{"issues", "readable via " + status.Via})
				} else {
					rows = append(rows, []string{"issues", "unavailable: " + status.Unavailable()})
				}
				return table(cmd.OutOrStdout(), []string{"CHECK", "VALUE"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

// githubIssueSummary is the one-line confirmation `task add --github-issue`
// prints, so a human sees which issue the daemon actually resolved.
func githubIssueSummary(issue *apiclient.GitHubIssue) string {
	if issue == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("from %s#%d: %s", issue.Repo, issue.Number, issue.Title))
}

// githubPullSummary is `vincent task add --github-pull`'s confirmation line
// (task 064). It names the pull request the daemon resolved and the branch
// consequence, because a task whose branch is somebody else's head branch is
// not something to discover from `git status` later.
func githubPullSummary(t apiclient.TaskDetail) string {
	if t.GitHubPull == nil || !t.GitHubPull.Branch {
		return ""
	}
	out := fmt.Sprintf("from %s#%d, running on its head branch %s",
		t.GitHubPull.Repo, t.GitHubPull.Number, t.BranchName)
	if t.GitHubPull.Fork {
		out += " (a fork: the branch has no upstream, so nothing can be pushed back)"
	}
	return out
}

// `vincent github pr create` is the write path without the TUI (task 069).
//
// It exists for the reason every other subcommand does — the TUI holds no
// state and no action the daemon does not — and because a gate script has to
// be able to drive the one route that writes to a forge without driving a
// terminal.
//
// It is the only thing under `vincent github` that writes, and it writes only
// when a human runs it. `--draft` is the popup's toggle; the title and body
// are the prefill a human edits, and `--body` is optional because a pull
// request with no description is a legal one.
func newGitHubPRCreateCmd() *cobra.Command {
	var (
		taskID int64
		title  string
		body   string
		draft  bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Push a task's branch and open its pull request",
		Long: "Push a task's branch to origin and create its pull request.\n\n" +
			"Only committed work is pushed: anything uncommitted in the task's\n" +
			"worktree is not in the pull request. The push never forces — a\n" +
			"diverged or rejected push creates nothing and changes nothing on\n" +
			"the remote.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				out, err := c.CreateGitHubPull(ctx, taskID, apiclient.GitHubPullCreateRequest{
					Title: title, Body: body, Draft: draft,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), out)
				}
				// The fallback is not a failure and does not exit non-zero: the
				// branch is on the remote, and the URL is the page that opens
				// GitHub's own form for it.
				if !out.Created {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(),
						"Pushed %s to %s.\nvincent could not create the pull request (%s).\nOpen this instead:\n%s\n",
						out.Branch, out.Remote, dash(out.Reason), out.CompareURL)
					return nil
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Pushed %s to %s.\nCreated %s#%d (%s)\n%s\n",
					out.Branch, out.Remote, out.Pull.Repo, out.Pull.Number,
					out.Pull.Status(), out.Pull.URL)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	cmd.Flags().StringVar(&title, "title", "", "Pull request title (required)")
	cmd.Flags().StringVar(&body, "body", "", "Pull request description")
	cmd.Flags().BoolVar(&draft, "draft", false, "Open the pull request as a draft")
	_ = cmd.MarkFlagRequired("task")
	_ = cmd.MarkFlagRequired("title")
	jsonFlag(cmd)
	return cmd
}

// newGitHubPRCmd groups the write path under its own noun, so `prs` stays the
// listing and nothing that writes hides inside it.
func newGitHubPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Act on one task's pull request",
	}
	cmd.AddCommand(newGitHubPRCreateCmd())
	return cmd
}
