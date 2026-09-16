package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
)

// `vincent github` (spec §12.1, task 035, task 069, task 102). It exists so
// issues can be browsed without opening the TUI — the same reason every other
// data view has a subcommand.
//
// It was read-only until task 069 gave it `pr create`, and task 068.4 added
// `pr merge`, `close`, `reopen`, `comment` and `rerun` — every write under the
// `pr` noun, each on a human's say-so, which is what decision record rows 11
// and 27 now say. Task 102's `pr link` and `pr unlink` write too, but only
// vincent's own link column — no request reaches GitHub from either. `issues`,
// `prs`, `status`, `pr show` and `pr checks` write nothing anywhere.
func newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Read GitHub issues and pull requests, link them to tasks, and act on a task's pull request",
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

// `vincent github pr link` is the human link without the TUI (task 102): the
// POST route the takeover's link action drives.
//
// The number is positional and the task is `--task`, so it reads "link PR 412
// to task 61" and matches `pr create` beside it (decision 1). It does not
// check the pull request exists, because the route does not either (task 052
// decision 5): a wrong number is `not_found` the next time it is read.
//
// The confirmation and `--json` come from reading the task back rather than
// from the POST's answer. `apiclient.Task` carries no `github_pull` — only
// TaskDetail does — and the read is vincent's own database, so it still makes
// no GitHub call.
func newGitHubPRLinkCmd() *cobra.Command {
	var taskID int64
	cmd := &cobra.Command{
		Use:   "link NUMBER",
		Short: "Link a pull request to a task by hand",
		Long: "Link pull request NUMBER to a task, the way the TUI's link action does.\n\n" +
			"Only vincent's own record is written: nothing is sent to GitHub, and the\n" +
			"number is not checked there. A link made by hand wins over the daemon's\n" +
			"head-branch matching and clears an earlier unlink.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil || number < 1 {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"Error: the pull request number must be a positive integer, got %q\n", args[0])
				return exitError{code: 1}
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if _, err := c.LinkGitHubPull(ctx, taskID, number); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				t, err := c.GetTask(ctx, taskID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				if t.GitHubPull == nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Linked task %d to pull request #%d.\n", t.ID, number)
					return nil
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Linked task %d to %s#%d.\n",
					t.ID, t.GitHubPull.Repo, t.GitHubPull.Number)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	_ = cmd.MarkFlagRequired("task")
	jsonFlag(cmd)
	return cmd
}

// `vincent github pr unlink` is the human unlink without the TUI (task 102).
//
// It refuses before sending anything when the task has no live link
// (decision 3). DELETE on a never-linked task is not a no-op: it writes a
// suppressed number-0 link, and the reconciler then skips that task for good,
// so it would never auto-link even once its branch had a pull request. The TUI
// never offers unlink in that state, and this must not be the first surface
// that does. The check is a fast client-side failure, not the authority — the
// route is unchanged.
func newGitHubPRUnlinkCmd() *cobra.Command {
	var taskID int64
	cmd := &cobra.Command{
		Use:   "unlink",
		Short: "Remove a task's pull-request link and keep it removed",
		Long: "Remove a task's pull-request link, the way the TUI's unlink action does.\n\n" +
			"The unlink is sticky: the daemon's head-branch matching will not link\n" +
			"that pull request again on its own. `vincent github pr link` restores it.\n" +
			"Nothing is sent to GitHub.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				before, err := c.GetTask(ctx, taskID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if !pullLinkLive(before.GitHubPull) {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"Error: task %d has no linked pull request, so there is nothing to unlink\n", taskID)
					return exitError{code: 1}
				}
				link := *before.GitHubPull
				if _, err := c.UnlinkGitHubPull(ctx, taskID); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				after, err := c.GetTask(ctx, taskID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), after)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(),
					"Unlinked %s#%d from task %d.\nvincent will not link it again on its own; `vincent github pr link` restores it.\n",
					link.Repo, link.Number, taskID)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	_ = cmd.MarkFlagRequired("task")
	jsonFlag(cmd)
	return cmd
}

// pullLinkLive is github.PullLink.Linked over the client's copy of the link:
// a number, and not a human's refusal.
func pullLinkLive(l *apiclient.GitHubPullLink) bool {
	return l != nil && l.Number > 0 && !l.Suppressed
}

// `vincent github pr show` is the CLI's read of a task's pull request, live
// (task 102 decision 4) — the row the TUI fetches on every workspace open.
// `task show --json` carries only the stored pointer; this is what the pull
// request says now.
//
// The route answers 200 whatever it found, so the exit code is the command's
// own (decision 5): 0 when the pull request was read, 1 when there is no live
// link or a named reason stopped the read. `--json` emits the body unchanged
// either way, so a script has the reason and the status both.
func newGitHubPRShowCmd() *cobra.Command {
	var taskID int64
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show a task's linked pull request, read live from GitHub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				pull, err := c.TaskGitHubPull(ctx, taskID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				read := pull.Reason == "" && pull.Linked && pull.Pull != nil
				if wantJSON(cmd) {
					if err := emitJSON(cmd.OutOrStdout(), pull); err != nil {
						return err
					}
					if !read {
						return exitError{code: 1}
					}
					return nil
				}
				stderr := cmd.ErrOrStderr()
				switch {
				case pull.Reason != "":
					_, _ = fmt.Fprintln(stderr, github.Message(pull.Reason))
					return exitError{code: 1}
				case !pull.Linked:
					_, _ = fmt.Fprintln(stderr, notLinkedLine(taskID, pull.Suppressed, pull.Repo, pull.Number))
					if pull.CompareURL != "" {
						_, _ = fmt.Fprintf(stderr, "Open one on GitHub:\n%s\n", pull.CompareURL)
					}
					return exitError{code: 1}
				case pull.Pull == nil:
					_, _ = fmt.Fprintf(stderr, "%s#%d could not be read\n", pull.Repo, pull.Number)
					return exitError{code: 1}
				}
				p := pull.Pull
				out := cmd.OutOrStdout()
				_, _ = fmt.Fprintf(out, "%s#%d: %s\n", p.Repo, p.Number, p.Title)
				for _, row := range [][2]string{
					{"state", p.Status()},
					{"branch", dash(p.HeadBranch) + " → " + dash(p.BaseBranch)},
					{"linked by", dash(pull.Source)},
					{"url", dash(p.URL)},
				} {
					_, _ = fmt.Fprintf(out, "%-10s %s\n", row[0], row[1])
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	_ = cmd.MarkFlagRequired("task")
	jsonFlag(cmd)
	return cmd
}

// `vincent github pr checks` is the Pull Request tab's check rows without the
// TUI (task 102). The rollup is fetched live on every call and never cached
// (task 068 decision 6); the route enforces that, and this adds nothing.
//
// Exit 0 means the rollup was read, **whatever CI concluded** (decision 5): a
// script reads the verdict from `--json`'s `.state`. An exit code that
// followed CI would need a code for "pending", and the 0/1/2 contract has no
// room for one.
func newGitHubPRChecksCmd() *cobra.Command {
	var taskID int64
	cmd := &cobra.Command{
		Use:   "checks",
		Short: "List the CI checks on a task's pull request, read live from GitHub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				checks, err := c.TaskGitHubChecks(ctx, taskID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				read := checks.Reason == "" && checks.Linked
				if wantJSON(cmd) {
					if err := emitJSON(cmd.OutOrStdout(), checks); err != nil {
						return err
					}
					if !read {
						return exitError{code: 1}
					}
					return nil
				}
				if checks.Reason != "" {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), github.Message(checks.Reason))
					return exitError{code: 1}
				}
				if !checks.Linked {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), notLinkedLine(taskID, false, checks.Repo, checks.Number))
					return exitError{code: 1}
				}
				out := cmd.OutOrStdout()
				if len(checks.Runs) == 0 {
					_, _ = fmt.Fprintf(out, "%s#%d: no checks reported on its head commit %s\n",
						checks.Repo, checks.Number, shortCommit(checks.Ref))
					return nil
				}
				_, _ = fmt.Fprintf(out, "%s#%d: %s on %s\n",
					checks.Repo, checks.Number, dash(checks.State), shortCommit(checks.Ref))
				rows := make([][]string, 0, len(checks.Runs))
				for _, run := range checks.Runs {
					// RUN is the Actions run a re-run would target, and `-` for a
					// row no Actions run backs (task 068 decision 3).
					id := "-"
					if run.Actions() {
						id = strconv.FormatInt(run.RunID, 10)
					}
					rows = append(rows, []string{run.Name, run.State, id, dash(run.URL)})
				}
				return table(out, []string{"CHECK", "STATE", "RUN", "URL"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id (required)")
	_ = cmd.MarkFlagRequired("task")
	jsonFlag(cmd)
	return cmd
}

// notLinkedLine is the one sentence `pr show` and `pr checks` print for a task
// with no live link. A suppressed link names what was removed, because "no
// pull request" alone reads as though vincent never found one.
func notLinkedLine(taskID int64, suppressed bool, repo string, number int) string {
	if suppressed && number > 0 {
		return fmt.Sprintf("task %d has no linked pull request (its link to %s#%d was removed)", taskID, repo, number)
	}
	return fmt.Sprintf("task %d has no linked pull request", taskID)
}

// shortCommit abbreviates a commit the way the TUI's Pull Request tab does.
// Display only: the full ref is in `--json`.
func shortCommit(ref string) string {
	switch {
	case ref == "":
		return "(unknown commit)"
	case len(ref) > 12:
		return ref[:12]
	default:
		return ref
	}
}

// newGitHubPRCmd groups what acts on one task's pull request under its own
// noun, so `prs` stays the listing and nothing that writes hides inside it.
func newGitHubPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Act on one task's pull request",
	}
	cmd.AddCommand(newGitHubPRCreateCmd(), newGitHubPRLinkCmd(), newGitHubPRUnlinkCmd(),
		newGitHubPRShowCmd(), newGitHubPRChecksCmd(), newGitHubPRMergeCmd(),
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
