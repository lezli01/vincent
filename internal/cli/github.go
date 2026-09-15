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
// It was read-only until task 069 gave it `pr create`, and task 068.4 added
// `pr merge`, `close`, `reopen`, `comment` and `rerun` — every write under the
// `pr` noun, each on a human's say-so, which is what decision record rows 11
// and 27 now say. `issues`, `prs` and `status` still write nothing.
func newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Read GitHub issues and pull requests, and act on a task's pull request",
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
// It writes only when a human runs it. `--draft` is the popup's toggle; the title and body
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

// newGitHubPRCmd groups the writes under their own noun, so `prs` stays the
// listing and nothing that writes hides inside it.
func newGitHubPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Act on one task's pull request",
	}
	cmd.AddCommand(newGitHubPRCreateCmd(), newGitHubPRMergeCmd(),
		newGitHubPRStateCmd("close", "Close a task's linked pull request without merging it", "Closed",
			(*apiclient.Client).CloseGitHubPull),
		newGitHubPRStateCmd("reopen", "Reopen a task's closed pull request", "Reopened",
			(*apiclient.Client).ReopenGitHubPull),
		newGitHubPRCommentCmd(), newGitHubPRRerunCmd())
	return cmd
}

// `vincent github pr merge` (task 068.4). The CLI has no confirmation popup,
// so the flags are where the human names exactly what is sent (task 068
// decision 4): `--method` has no default, and `--head-sha` is the commit the
// merge is for. The daemon refuses `head_changed`, and merges nothing, when
// the pull request's head has moved past it.
func newGitHubPRMergeCmd() *cobra.Command {
	var (
		taskID  int64
		method  string
		headSHA string
	)
	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Merge a task's linked pull request",
		Long: "Merge the pull request linked to a task.\n\n" +
			"There is no confirmation prompt, so both --method and --head-sha are\n" +
			"required: they are where you name exactly what is sent. The merge is\n" +
			"refused, and nothing is merged, when the pull request's head is no\n" +
			"longer --head-sha, when a check is still running, when the branch is\n" +
			"behind its base, or when GitHub would not merge it as it stands.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				pull, err := c.MergeGitHubPull(ctx, taskID, apiclient.GitHubPullMergeRequest{
					Method: method, HeadSHA: headSHA,
				})
				return printPullWrite(cmd, "Merged", pull, err)
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	cmd.Flags().StringVar(&method, "method", "", "Merge method: merge, squash or rebase (required)")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "The head commit being merged (required)")
	_ = cmd.MarkFlagRequired("task")
	_ = cmd.MarkFlagRequired("method")
	_ = cmd.MarkFlagRequired("head-sha")
	jsonFlag(cmd)
	return cmd
}

// newGitHubPRStateCmd is `pr close` and `pr reopen`: a task id, and nothing
// else to name.
func newGitHubPRStateCmd(use, short, done string,
	call func(*apiclient.Client, context.Context, int64) (apiclient.GitHubPullRequest, error),
) *cobra.Command {
	var taskID int64
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				pull, err := call(c, ctx, taskID)
				return printPullWrite(cmd, done, pull, err)
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	_ = cmd.MarkFlagRequired("task")
	jsonFlag(cmd)
	return cmd
}

// printPullWrite renders a write that answers with the pull request.
func printPullWrite(cmd *cobra.Command, done string, pull apiclient.GitHubPullRequest, err error) error {
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
		return exitError{code: 1}
	}
	if wantJSON(cmd) {
		return emitJSON(cmd.OutOrStdout(), pull)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s#%d (%s)\n%s\n",
		done, pull.Repo, pull.Number, pull.Status(), pull.URL)
	return nil
}

// `vincent github pr comment`. `--body-file -` reads stdin, so a long comment
// is something a pipe carries rather than something argv has to quote.
func newGitHubPRCommentCmd() *cobra.Command {
	var (
		taskID   int64
		body     string
		bodyFile string
	)
	cmd := &cobra.Command{
		Use:   "comment",
		Short: "Comment on a task's linked pull request",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			text, err := flagText(cmd, body, bodyFile)
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				out, err := c.CommentGitHubPull(ctx, taskID, text)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), out)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Commented on task %d's pull request\n%s\n", taskID, out.URL)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	cmd.Flags().StringVar(&body, "body", "", "Comment text")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read the comment from a file (- for stdin)")
	_ = cmd.MarkFlagRequired("task")
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
	cmd.MarkFlagsOneRequired("body", "body-file")
	jsonFlag(cmd)
	return cmd
}

// `vincent github pr rerun`. The run id is the `run_id` of a failed row in
// `vincent`'s check rollup; the daemon refuses any other before sending.
func newGitHubPRRerunCmd() *cobra.Command {
	var (
		taskID int64
		runID  int64
	)
	cmd := &cobra.Command{
		Use:   "rerun",
		Short: "Re-run the failed jobs of a GitHub Actions run on a task's pull request",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				out, err := c.RerunGitHubPullChecks(ctx, taskID, runID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), out)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Requested a re-run of the failed jobs in run %d\n", out.RunID)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	cmd.Flags().Int64Var(&runID, "run-id", 0, "GitHub Actions run id (required)")
	_ = cmd.MarkFlagRequired("task")
	_ = cmd.MarkFlagRequired("run-id")
	jsonFlag(cmd)
	return cmd
}
