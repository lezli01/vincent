package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newIssueCmd is `vincent issue` (spec §5.6, §12.1, task 130). Task 130.8
// gave it its first leaf, `sync`; task 130.6 grew the rest of the tree around
// it, one leaf per issue route. Starting a task from an issue is not here: it
// is `vincent task add --issue`, beside every other way a task is created.
func newIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Work with a project's issues",
	}
	cmd.AddCommand(
		newIssueLsCmd(),
		newIssueShowCmd(),
		newIssueAddCmd(),
		newIssueEditCmd(),
		newIssueCloseCmd(),
		newIssueReopenCmd(),
		newIssueCommentCmd(),
		newIssueDeleteCmd(),
		newIssueLabelsCmd(),
		newIssueSyncCmd(),
	)
	return cmd
}

// parseIssueID reads an issue id argument.
func parseIssueID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("issue id must be a number: %q", arg)
	}
	return id, nil
}

// issueFail prints the daemon's refusal in its own words and exits 1, the
// way every data command reports one.
func issueFail(cmd *cobra.Command, err error) error {
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
	return exitError{code: 1}
}

// issueState renders an issue's state with its close reason, so a closed
// issue says how it was closed without a second column.
func issueState(iss *apiclient.Issue) string {
	if iss.State == "closed" && iss.CloseReason != "" {
		return "closed (" + iss.CloseReason + ")"
	}
	return iss.State
}

// issueLane renders an issue's board lane as the API spells it, marked with
// " !" when a root task is waiting on a human (task 134 decision 3). The
// marker rides any lane, done included: a closed issue can still have a
// stray live task that needs one.
func issueLane(iss *apiclient.Issue) string {
	if iss.Attention {
		return iss.Lane + " !"
	}
	return iss.Lane
}

// issuePriority renders a priority on Linear's scale; 0 is "none".
func issuePriority(p int) string {
	if p == 0 {
		return "-"
	}
	return strconv.Itoa(p)
}

func newIssueLsCmd() *cobra.Command {
	var opts apiclient.IssueListOptions
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List issues",
		Long: "Lists issues, most recently updated first. Without --project the list\n" +
			"spans every project and gains a PROJECT column. --state, --lane and --label\n" +
			"repeat; every --label given must match, and filters combine by AND.\n\n" +
			"LANE is the issue's board lane: open, in_progress, hand_off or done. A\n" +
			"trailing \" !\" marks an issue a root task is waiting on a human for.\n" +
			"--lane done is every closed issue, whatever its close reason, so\n" +
			"--state open --lane done lists nothing.\n\n" +
			"--github N finds the issue imported from GitHub issue #N and needs\n" +
			"--project: a GitHub number means nothing across projects. Its ID is what\n" +
			"`vincent task add --issue` takes. An issue not yet imported lists nothing;\n" +
			"`vincent issue sync --project P` imports it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The lookup that replaced `task add --github-issue` (task
			// 130.11, decision 22.4). Refused here as well as by the
			// daemon, so the message names the flags rather than the
			// query parameters.
			if cmd.Flags().Changed("github") {
				if opts.ProjectID == 0 {
					return errors.New("--github needs --project: a GitHub issue number is only meaningful within one project")
				}
				if opts.RemoteNumber < 1 {
					return fmt.Errorf("--github must be a positive issue number, got %d", opts.RemoteNumber)
				}
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				list, err := c.ListIssues(ctx, opts)
				if err != nil {
					return issueFail(cmd, err)
				}
				if list == nil {
					list = []apiclient.Issue{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), list)
				}
				spans := opts.ProjectID == 0
				var names map[int64]string
				if spans {
					names = projectNames(ctx, c)
				}
				header := []string{"ID", "STATE", "LANE", "KIND", "PRIORITY", "LABELS", "TASKS", "TITLE"}
				if spans {
					header = append([]string{"ID", "PROJECT"}, header[1:]...)
				}
				rows := make([][]string, 0, len(list))
				for i := range list {
					iss := &list[i]
					row := []string{
						strconv.FormatInt(iss.ID, 10), issueState(iss), issueLane(iss), dash(iss.Kind),
						issuePriority(iss.Priority), dash(strings.Join(iss.Labels, ",")),
						strconv.Itoa(iss.TaskCount), iss.Title,
					}
					if spans {
						project := names[iss.ProjectID]
						if project == "" {
							project = strconv.FormatInt(iss.ProjectID, 10)
						}
						row = append([]string{row[0], project}, row[1:]...)
					}
					rows = append(rows, row)
				}
				return table(cmd.OutOrStdout(), header, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&opts.ProjectID, "project", 0, "Only issues in this project (default: every project)")
	cmd.Flags().StringSliceVar(&opts.States, "state", nil, "Only issues in this state: open or closed (repeatable)")
	cmd.Flags().StringSliceVar(&opts.Lanes, "lane", nil,
		"Only issues in this lane: open, in_progress, hand_off or done (repeatable)")
	cmd.Flags().StringArrayVar(&opts.Labels, "label", nil, "Only issues carrying this label (repeatable; all must match)")
	cmd.Flags().StringVar(&opts.Kind, "kind", "", "Only issues of this kind")
	cmd.Flags().StringVar(&opts.Query, "search", "", "Only issues whose title or body contains this text")
	cmd.Flags().StringVar(&opts.Source, "source", "", "Only local or github issues")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "Maximum rows")
	cmd.Flags().IntVar(&opts.RemoteNumber, "github", 0,
		"Only the issue imported from this GitHub issue number (requires --project)")
	jsonFlag(cmd)
	return cmd
}

// projectNames maps project ids to names for the PROJECT column. A failure
// here costs only the names — the column falls back to ids — so it is not
// worth failing a list the daemon already answered.
func projectNames(ctx context.Context, c *apiclient.Client) map[int64]string {
	projects, err := c.ListProjects(ctx)
	if err != nil {
		return nil
	}
	names := make(map[int64]string, len(projects))
	for i := range projects {
		names[projects[i].ID] = projects[i].Name
	}
	return names
}

func newIssueShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one issue in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIssueID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				iss, err := c.GetIssue(ctx, id, "")
				if err != nil {
					return issueFail(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), iss)
				}
				thread, err := c.ListIssueComments(ctx, id)
				if err != nil {
					return issueFail(cmd, err)
				}
				if err := printIssue(cmd.OutOrStdout(), &iss); err != nil {
					return err
				}
				return printIssueThread(cmd.OutOrStdout(), thread)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// printIssue writes an issue for a human: its fields, then its body.
func printIssue(out io.Writer, iss *apiclient.Issue) error {
	state := issueState(iss)
	if iss.DuplicateOf != nil {
		state += fmt.Sprintf(", duplicate of #%d", *iss.DuplicateOf)
	}
	fields := [][2]string{
		{"id", strconv.FormatInt(iss.ID, 10)},
		{"title", iss.Title},
		{"state", state},
		{"lane", issueLane(iss)},
		{"project", strconv.FormatInt(iss.ProjectID, 10)},
		{"kind", dash(iss.Kind)},
		{"priority", issuePriority(iss.Priority)},
		{"labels", dash(strings.Join(iss.Labels, ", "))},
		{"author", dash(iss.Author)},
	}
	if src := iss.Source; src != nil {
		ref := src.Repo
		if src.Number != 0 {
			ref = fmt.Sprintf("%s#%d", src.Repo, src.Number)
		}
		fields = append(fields, [2]string{"source", ref})
		if src.URL != "" {
			fields = append(fields, [2]string{"url", src.URL})
		}
		synced := "synced " + doctorTime(src.LastSyncedAt, "never")
		if src.Status != "" {
			synced += " (" + src.Status + ")"
		}
		fields = append(fields, [2]string{"sync", synced})
	}
	tasks := strconv.Itoa(iss.Tasks.Count)
	if len(iss.Tasks.ActiveIDs) > 0 {
		ids := make([]string, 0, len(iss.Tasks.ActiveIDs))
		for _, t := range iss.Tasks.ActiveIDs {
			ids = append(ids, strconv.FormatInt(t, 10))
		}
		tasks += " (active: " + strings.Join(ids, ", ") + ")"
	}
	fields = append(fields, [2]string{"tasks", tasks})
	if len(iss.AvailableActions) > 0 {
		fields = append(fields, [2]string{"actions", strings.Join(iss.AvailableActions, ", ")})
	}
	for _, f := range fields {
		if _, err := fmt.Fprintf(out, "%-9s %s\n", f[0], f[1]); err != nil {
			return err
		}
	}
	if iss.Body != "" {
		if _, err := fmt.Fprintf(out, "\n%s\n", strings.TrimRight(iss.Body, "\n")); err != nil {
			return err
		}
	}
	return nil
}

// printIssueThread writes an issue's comments after its body, oldest first:
// a header line per comment with its author and time, a mirrored one marked
// as GitHub's (task 130.16), then its text.
func printIssueThread(out io.Writer, thread []apiclient.IssueComment) error {
	for i := range thread {
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
		if err := printIssueComment(out, &thread[i]); err != nil {
			return err
		}
	}
	return nil
}

// printIssueComment writes one comment: its attribution line, then its text.
func printIssueComment(out io.Writer, c *apiclient.IssueComment) error {
	h := fmt.Sprintf("--- %s, %s", dash(c.Author), doctorTime(&c.CreatedAt, "-"))
	if c.Remote {
		h += " (github)"
	}
	_, err := fmt.Fprintf(out, "%s\n%s\n", h, strings.TrimRight(c.Body, "\n"))
	return err
}

// newIssueCommentCmd is `vincent issue comment`: a local comment, its author
// derived by the daemon (task 130 decision 24.6). One on an issue imported
// from GitHub is refused, as an edit of its title is: nothing is ever posted
// there, and its thread is mirrored read-only.
func newIssueCommentCmd() *cobra.Command {
	var body, bodyFile string
	cmd := &cobra.Command{
		Use:   "comment <id>",
		Short: "Comment on a local issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIssueID(args[0])
			if err != nil {
				return err
			}
			if err := readIssueBody(cmd, &body, bodyFile); err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				comment, err := c.AddIssueComment(ctx, id, body)
				if err != nil {
					return issueFail(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), comment)
				}
				return printIssueComment(cmd.OutOrStdout(), &comment)
			})
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "The comment (Markdown)")
	cmd.Flags().StringVar(&bodyFile, "body-file", "",
		"Read the comment from this file (- for stdin), byte for byte")
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
	cmd.MarkFlagsOneRequired("body", "body-file")
	jsonFlag(cmd)
	return cmd
}

// issueBodyFlags adds --body and --body-file, which are one value spelled two
// ways: the file form keeps a body out of argv, where a shell may rewrite it.
func issueBodyFlags(cmd *cobra.Command, body, bodyFile *string) {
	cmd.Flags().StringVar(body, "body", "", "The issue's body (Markdown)")
	cmd.Flags().StringVar(bodyFile, "body-file", "",
		"Read the body from this file (- for stdin), byte for byte")
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
}

// readIssueBody resolves --body-file into body when it was given. It reads
// before any request, so a missing file never leaves a half-done write.
func readIssueBody(cmd *cobra.Command, body *string, bodyFile string) error {
	if !cmd.Flags().Changed("body-file") {
		return nil
	}
	data, err := readInputFile("--body-file", bodyFile, cmd.InOrStdin())
	if err != nil {
		return err
	}
	*body = string(data)
	return nil
}

// printIssueLine is a write's one-line human answer.
func printIssueLine(cmd *cobra.Command, verb string, iss *apiclient.Issue) error {
	if wantJSON(cmd) {
		return emitJSON(cmd.OutOrStdout(), iss)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "issue %d %s: %s [%s]\n", iss.ID, verb, iss.Title, issueState(iss))
	return err
}

func newIssueAddCmd() *cobra.Command {
	var (
		req            apiclient.CreateIssueRequest
		bodyFile       string
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "File a local issue",
		Long: "Files an issue in a project. The daemon validates every field, including\n" +
			"the priority's 0–4 range. --idempotency-key makes the command safe to re-run\n" +
			"after a lost response: the same key returns the issue the first run created\n" +
			"instead of filing a second. Without it nothing is deduplicated — the CLI never\n" +
			"retries, so it never invents a key.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := readIssueBody(cmd, &req.Body, bodyFile); err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				iss, err := c.CreateIssue(ctx, req, idempotencyKey)
				if err != nil {
					return issueFail(cmd, err)
				}
				return printIssueLine(cmd, "filed", &iss)
			})
		},
	}
	cmd.Flags().Int64Var(&req.ProjectID, "project", 0, "Project id (required)")
	cmd.Flags().StringVar(&req.Title, "title", "", "The issue's title (required)")
	issueBodyFlags(cmd, &req.Body, &bodyFile)
	cmd.Flags().StringArrayVar(&req.Labels, "label", nil, "A label to file it with (repeatable)")
	cmd.Flags().StringVar(&req.Kind, "kind", "", "The issue's kind, such as bug or feature")
	cmd.Flags().IntVar(&req.Priority, "priority", 0, "Priority: 0 none, 1 urgent … 4 low")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "",
		"Send this Idempotency-Key, so a re-run returns the first run's issue")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("title")
	jsonFlag(cmd)
	return cmd
}

// issueEditFlags are the flags of `issue edit` that change something, in the
// order the help lists them.
var issueEditFlags = []string{"title", "body", "body-file", "add-label", "remove-label", "kind", "priority"}

// beforeIssuePatch runs between edit's read and its write. Tests set it to
// change the issue underneath, which is the race the version guards against.
var beforeIssuePatch = func() {}

func newIssueEditCmd() *cobra.Command {
	var (
		title, body, bodyFile, kind string
		addLabels, removeLabels     []string
		priority                    int
	)
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Change an issue's fields",
		Long: "Changes only the fields whose flags are given. --kind \"\" and --priority 0\n" +
			"clear them. Labels change by delta only: --add-label and --remove-label touch\n" +
			"the named labels and leave every other one alone.\n\n" +
			"The edit is sent at the version just read. If the issue changed in between,\n" +
			"the daemon refuses it and nothing is retried: re-run the command to apply it\n" +
			"on top of the current issue. An imported issue's title, body and labels\n" +
			"mirror GitHub and are refused; its kind and priority are vincent's own.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIssueID(args[0])
			if err != nil {
				return err
			}
			changed := false
			for _, f := range issueEditFlags {
				changed = changed || cmd.Flags().Changed(f)
			}
			if !changed {
				return errors.New("nothing to change: pass at least one of --" + strings.Join(issueEditFlags, ", --"))
			}
			if err := readIssueBody(cmd, &body, bodyFile); err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				cur, err := c.GetIssue(ctx, id, "")
				if err != nil {
					return issueFail(cmd, err)
				}
				p := apiclient.IssuePatch{Version: cur.Version, AddLabels: addLabels, RemoveLabels: removeLabels}
				if cmd.Flags().Changed("title") {
					p.Title = &title
				}
				if cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file") {
					p.Body = &body
				}
				if cmd.Flags().Changed("kind") {
					p.Kind = &kind
				}
				if cmd.Flags().Changed("priority") {
					p.Priority = &priority
				}
				beforeIssuePatch()
				iss, err := c.PatchIssue(ctx, id, p)
				if err != nil {
					return issueEditFailed(cmd, err, &cur)
				}
				return printIssueLine(cmd, "updated", &iss)
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "New title")
	issueBodyFlags(cmd, &body, &bodyFile)
	cmd.Flags().StringArrayVar(&addLabels, "add-label", nil, "Add this label (repeatable)")
	cmd.Flags().StringArrayVar(&removeLabels, "remove-label", nil, "Remove this label (repeatable)")
	cmd.Flags().StringVar(&kind, "kind", "", "New kind; \"\" clears it")
	cmd.Flags().IntVar(&priority, "priority", 0, "New priority: 0 none, 1 urgent … 4 low")
	jsonFlag(cmd)
	return cmd
}

// issueEditFailed reports a refused edit. The two issue 409s get a line that
// says what to do next; anything else is the daemon's own wording.
func issueEditFailed(cmd *cobra.Command, err error, read *apiclient.Issue) error {
	reason, _, ok := apiclient.IssueConflict(err)
	switch {
	case ok && reason == apiclient.IssueReasonChanged:
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"Error: issue %d changed since it was read; re-run the command to apply the edit on top of the current version\n",
			read.ID)
		return exitError{code: 1}
	case ok && reason == apiclient.IssueReasonMirrored:
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "editable on this issue: %s\n", dash(strings.Join(read.Editable, ", ")))
		return exitError{code: 1}
	}
	return issueFail(cmd, err)
}

func newIssueCloseCmd() *cobra.Command {
	var (
		req         apiclient.CloseIssueRequest
		duplicateOf int64
	)
	cmd := &cobra.Command{
		Use:   "close <id>",
		Short: "Close an open issue",
		Long: "Closes an issue with a reason: completed (the default), not_planned or\n" +
			"duplicate. --duplicate-of names the issue this one duplicates; it needs\n" +
			"--reason duplicate and an issue in the same project.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIssueID(args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("duplicate-of") {
				req.DuplicateOf = &duplicateOf
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				iss, err := c.CloseIssue(ctx, id, req)
				if err != nil {
					return issueFail(cmd, err)
				}
				return printIssueLine(cmd, "closed", &iss)
			})
		},
	}
	cmd.Flags().StringVar(&req.Reason, "reason", "", "completed, not_planned or duplicate (default completed)")
	cmd.Flags().Int64Var(&duplicateOf, "duplicate-of", 0, "The issue this one duplicates; needs --reason duplicate")
	jsonFlag(cmd)
	return cmd
}

func newIssueReopenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reopen <id>",
		Short: "Reopen a closed issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIssueID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				iss, err := c.ReopenIssue(ctx, id)
				if err != nil {
					return issueFail(cmd, err)
				}
				return printIssueLine(cmd, "reopened", &iss)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// errIssueDeleteNeedsForce is `issue delete` without --force. An issue can be
// deleted in any state (task 130 decision 6), so there is no daemon refusal
// to stand in for confirmation the way there is for a task or a chat, and the
// command never prompts: --force is the confirmation.
var errIssueDeleteNeedsForce = errors.New("deleting an issue is permanent; pass --force to delete it")

func newIssueDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <id>...",
		Aliases: []string{"rm"},
		Short:   "Permanently delete issues",
		Long: "Deletes an issue for good, in any state, and requires --force. It never\n" +
			"touches GitHub: an imported issue stays open upstream, and the delete leaves\n" +
			"a tombstone so the next sync does not import it again.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids := make([]int64, 0, len(args))
			for _, a := range args {
				id, err := parseIssueID(a)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			if !force {
				return errIssueDeleteNeedsForce
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				out := cmd.OutOrStdout()
				reports := make([]deleteReport, 0, len(ids))
				bad := false
				for _, id := range ids {
					if err := c.DeleteIssue(ctx, id); err != nil {
						bad = true
						reports = append(reports, deleteReport{ID: id, Error: apiMessage(err)})
						if !wantJSON(cmd) {
							_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: issue %d: %s\n", id, apiMessage(err))
						}
						continue
					}
					reports = append(reports, deleteReport{ID: id, Deleted: true})
					if !wantJSON(cmd) {
						_, _ = fmt.Fprintf(out, "issue %d deleted\n", id)
					}
				}
				if wantJSON(cmd) {
					if err := emitJSON(out, reports); err != nil {
						return err
					}
				}
				if bad {
					return exitError{code: 1}
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Confirm the permanent delete (required)")
	jsonFlag(cmd)
	return cmd
}

func newIssueLabelsCmd() *cobra.Command {
	var projectID int64
	cmd := &cobra.Command{
		Use:   "labels",
		Short: "List a project's label catalogue",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				labels, err := c.ListIssueLabels(ctx, projectID)
				if err != nil {
					return issueFail(cmd, err)
				}
				if labels == nil {
					labels = []apiclient.IssueLabel{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), labels)
				}
				rows := make([][]string, 0, len(labels))
				for _, l := range labels {
					rows = append(rows, []string{l.Name, l.Source, strconv.Itoa(l.IssueCount), dash(l.Description)})
				}
				return table(cmd.OutOrStdout(), []string{"NAME", "SOURCE", "ISSUES", "DESCRIPTION"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

// `vincent issue sync` asks the daemon to import and refresh a project's
// GitHub issues now, then reports how the import stands. The daemon does the
// sync on its own reconciler goroutine and answers at once, so what is
// printed is the status as of the request, not the result of this sync.
// `--status` only reads it.
func newIssueSyncCmd() *cobra.Command {
	var (
		projectID  int64
		statusOnly bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync a project's GitHub issues now, or report the import status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				var (
					status apiclient.IssueSyncStatus
					err    error
				)
				if statusOnly {
					status, err = c.IssueSyncStatus(ctx, projectID)
				} else {
					status, err = c.SyncIssues(ctx, projectID)
				}
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), status)
				}
				return table(cmd.OutOrStdout(), []string{"CHECK", "VALUE"}, issueSyncRows(status, !statusOnly))
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	cmd.Flags().BoolVar(&statusOnly, "status", false, "Only report the import status; do not ask for a sync")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

// issueSyncRows renders an import status for a human. requested says the
// command just asked for a sync, so the row says one is on its way — unless
// a switch is off, in which case nothing will happen until it is turned on.
func issueSyncRows(s apiclient.IssueSyncStatus, requested bool) [][]string {
	reason := "-"
	if !s.OK && s.Reason != "" {
		reason = s.Reason
	}
	rows := [][]string{
		{"enabled", boolWord(s.Enabled)},
		{"repo", dash(s.Repo)},
		{"last synced", doctorTime(s.LastSyncedAt, "never")},
		{"ok", boolWord(s.OK)},
		{"reason", reason},
		{"import complete", boolWord(s.ImportComplete)},
		{"state writes", writesValue(s.WritesPending, s.WritesFailed, s.WritesConflict)},
	}
	if s.RateLimitedUntil != nil {
		rows = append(rows, []string{"rate limited until", doctorTime(s.RateLimitedUntil, "-")})
	}
	if requested {
		if s.Enabled {
			rows = append(rows, []string{"sync", "requested"})
		} else {
			rows = append(rows, []string{"sync", "requested, but import is off (" + s.Reason + ")"})
		}
	}
	return rows
}

// writesValue is a project's state write-back health (task 130.10): the
// imported issues whose newest write is pending, failed or in conflict.
func writesValue(pending, failed, conflict int) string {
	return fmt.Sprintf("%d pending, %d failed, %d conflict", pending, failed, conflict)
}
