package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent github` (spec §12.1, task 035). It exists so issues can be browsed
// without opening the TUI — the same reason every other data view has a
// subcommand — and it is read-only: nothing under this command writes to
// GitHub, which is decision 10's boundary held at the CLI as well as in the
// daemon.
func newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Read GitHub issues and pull requests for a project",
	}
	cmd.AddCommand(newGitHubIssuesCmd(), newGitHubPullsCmd(), newGitHubStatusCmd())
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
// a terminal. Open pull requests only: the daemon's listing is open-only, and
// a merged one is answered from the task that links it.
func newGitHubPullsCmd() *cobra.Command {
	var (
		projectID int64
		limit     int
	)
	cmd := &cobra.Command{
		Use:   "prs",
		Short: "List a project's open GitHub pull requests, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				pulls, err := c.ListGitHubPulls(ctx, projectID, limit)
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
