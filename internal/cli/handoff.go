package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newHandoffCmd is `vincent handoff` (spec §5.7, task 137.5): a handoff
// claims hand_off issues for one integration task. It is not `vincent chat
// handoff`, which hands a chat's worktree to a task (task 137 decision 11
// keeps both names), so the help says "chat handoff" in full wherever the
// two could be confused.
func newHandoffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Start and inspect handoffs of hand-off issues",
		Long: "A handoff claims issues in the hand_off lane for one integration task,\n" +
			"which runs the workflow you name over them. This is not `vincent chat\n" +
			"handoff`, which turns a chat into a task.",
	}
	cmd.AddCommand(newHandoffStartCmd(), newHandoffLsCmd(), newHandoffShowCmd(),
		newHandoffVerbCmd(apiclient.HandoffDeliver), newHandoffVerbCmd(apiclient.HandoffReturn),
		newHandoffVerbCmd(apiclient.HandoffWait), newHandoffVerbCmd(apiclient.HandoffRelease))
	return cmd
}

func newHandoffStartCmd() *cobra.Command {
	var (
		req                    apiclient.CreateHandoffRequest
		sel                    apiclient.HandoffSelect
		priority               int
		fields                 []string
		fieldsFile             string
		maxCostUSD             float64
		description            string
		agent, model, effort   string
		grant                  []string
		paused, restricted     bool
		deliveries, kinds, lbl []string
	)
	cmd := &cobra.Command{
		Use:   "start [issue-id...]",
		Short: "Start a handoff over hand-off issues",
		Long: "Claims issues for a new integration task that runs --workflow, in one\n" +
			"step: the handoff, its task and every claim are created together.\n\n" +
			"With issue ids, exactly those are claimed, and the start is refused if any\n" +
			"is not eligible: not in hand_off, already claimed, or already delivered.\n" +
			"Without ids, the eligible issues are selected: --delivery, --kind and\n" +
			"--label narrow them (each repeats; every --label must match) and --max\n" +
			"caps them (at least 1). A selection takes the most urgent first —\n" +
			"priority 1, then 4, then issues with none — then the oldest delivery.\n\n" +
			"--grant gives the handoff's task capabilities for forge writes (issue.comment,\n" +
			"issue.close, pull.create, pull.comment, pull.merge, pull.close,\n" +
			"checks.rerun). Only a human may give one: a workflow step's call is refused.",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args {
				id, err := parseIssueID(a)
				if err != nil {
					return err
				}
				req.IssueIDs = append(req.IssueIDs, id)
			}
			selecting := cmd.Flags().Changed("delivery") || cmd.Flags().Changed("kind") ||
				cmd.Flags().Changed("label") || cmd.Flags().Changed("max")
			// sel.Max is omitempty, so a 0 would reach the daemon as no
			// cap at all: the widest claim, which the API refuses as
			// select.max below 1.
			if cmd.Flags().Changed("max") && sel.Max < 1 {
				return fmt.Errorf("--max must be at least 1")
			}
			if len(req.IssueIDs) > 0 && selecting {
				return fmt.Errorf("issue ids and --delivery, --kind, --label or --max cannot be combined: " +
					"name the issues, or select them")
			}
			if len(req.IssueIDs) == 0 {
				sel.Delivery, sel.Kind, sel.Labels = deliveries, kinds, lbl
				req.Select = &sel
			}
			flagFields, err := parseFieldFlags(fields)
			if err != nil {
				return err
			}
			var fileFields map[string]string
			if cmd.Flags().Changed("fields-file") {
				if fileFields, err = readFieldsFile(fieldsFile, cmd.InOrStdin()); err != nil {
					return err
				}
			}
			req.Fields = mergeTaskFields(fileFields, flagFields)
			for _, g := range grant {
				for _, part := range strings.Split(g, ",") {
					if part = strings.TrimSpace(part); part != "" {
						req.Grant = append(req.Grant, part)
					}
				}
			}
			req.Description, req.Agent, req.Model, req.Effort = description, agent, model, effort
			req.Paused, req.Restricted = paused, restricted
			if cmd.Flags().Changed("priority") {
				p := priority
				req.Priority = &p
			}
			if cmd.Flags().Changed("max-task-cost-usd") {
				req.MaxTaskCostUSD = &maxCostUSD
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				h, err := c.CreateHandoff(ctx, req, "")
				if err != nil {
					return issueFail(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), h)
				}
				out := cmd.OutOrStdout()
				ids := make([]string, 0, len(h.Items))
				for _, it := range h.Items {
					ids = append(ids, "#"+strconv.FormatInt(it.IssueID, 10))
				}
				if _, err := fmt.Fprintf(out, "handoff %d started: task %d claims %s\n",
					h.ID, h.TaskID, strings.Join(ids, ", ")); err != nil {
					return err
				}
				if h.Task != nil {
					for _, w := range h.Task.Warnings {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
					}
				}
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.Int64Var(&req.ProjectID, "project", 0, "Project id (required)")
	f.StringVar(&req.Workflow, "workflow", "", "Workflow the handoff's task runs (required)")
	f.StringVar(&req.Title, "title", "", "Task title (default: names how many issues it claims)")
	f.StringArrayVar(&deliveries, "delivery", nil, "Select by the delivery's kind: change, reply or none (repeatable)")
	f.StringArrayVar(&kinds, "kind", nil, "Select issues of this kind (repeatable)")
	f.StringArrayVar(&lbl, "label", nil, "Select issues carrying this label (repeatable; all must match)")
	f.IntVar(&sel.Max, "max", 0, "Select at most this many issues (default: every eligible one)")
	f.StringArrayVar(&grant, "grant", nil, "Capability to grant the handoff (repeatable or comma-separated)")
	f.BoolVar(&paused, "paused", false, "Create the task paused; it starts only when resumed (`vincent task resume`)")
	f.StringVar(&description, "description", "", "Task description")
	f.IntVar(&priority, "priority", 0, "Scheduling priority; higher runs first")
	f.StringVar(&agent, "agent", "", "Agent override (§8.6 level 2)")
	f.StringVar(&model, "model", "", "Model override (§8.6 level 2)")
	f.StringVar(&effort, "effort", "", "Effort override (§8.6 level 2)")
	f.StringArrayVar(&fields, "field", nil, "Task field as name=value; repeat for additional fields")
	f.StringVar(&fieldsFile, "fields-file", "",
		"Read task fields from a JSON object of strings in this file, or `-` for stdin; a --field of the same name wins")
	f.BoolVar(&restricted, "restricted", false, "Run every agent step restricted, even one whose workflow says full-auto")
	f.Float64Var(&maxCostUSD, "max-task-cost-usd", 0,
		"This task's own spend cap in USD; the lower of it and config's max_task_cost_usd applies")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("workflow")
	jsonFlag(cmd)
	return cmd
}

func newHandoffLsCmd() *cobra.Command {
	var opts apiclient.HandoffListOptions
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List handoffs",
		Long: "Lists handoffs, newest first. STATE is live while the handoff's task runs,\n" +
			"waiting once it has settled with an item still waiting, else settled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				list, err := c.ListHandoffs(ctx, opts)
				if err != nil {
					return issueFail(cmd, err)
				}
				if list == nil {
					list = []apiclient.Handoff{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), list)
				}
				// A list row carries no items, so the count is one read per
				// row; a failed read costs only its cell.
				rows := make([][]string, 0, len(list))
				for i := range list {
					h := &list[i]
					items := "-"
					if full, err := c.GetHandoff(ctx, h.ID); err == nil {
						items = strconv.Itoa(len(full.Items))
					}
					rows = append(rows, []string{
						strconv.FormatInt(h.ID, 10), h.State, strconv.FormatInt(h.TaskID, 10), items,
						h.CreatedBy, h.CreatedAt.Local().Format(time.DateTime),
					})
				}
				return table(cmd.OutOrStdout(), []string{"ID", "STATE", "TASK", "ITEMS", "CREATED_BY", "CREATED_AT"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&opts.ProjectID, "project", 0, "Only handoffs in this project")
	cmd.Flags().StringSliceVar(&opts.States, "state", nil, "Only handoffs in this state: live, waiting or settled (repeatable)")
	cmd.Flags().Int64Var(&opts.IssueID, "issue", 0, "Only handoffs that claimed this issue")
	jsonFlag(cmd)
	return cmd
}

func newHandoffShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one handoff and its items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("handoff id must be a number: %q", args[0])
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				h, err := c.GetHandoff(ctx, id)
				if err != nil {
					return issueFail(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), h)
				}
				return printHandoff(cmd.OutOrStdout(), &h)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// printHandoff writes a handoff for a human: its fields, then one row per
// item.
func printHandoff(out io.Writer, h *apiclient.Handoff) error {
	fields := [][2]string{
		{"id", strconv.FormatInt(h.ID, 10)},
		{"state", h.State},
		{"project", strconv.FormatInt(h.ProjectID, 10)},
		{"task", strconv.FormatInt(h.TaskID, 10)},
		{"grant", dash(strings.Join(h.Grant, ", "))},
		{"created", h.CreatedBy + ", " + h.CreatedAt.Local().Format(time.DateTime)},
	}
	if h.TriggerID != "" {
		fields = append(fields, [2]string{"trigger", h.TriggerID})
	}
	for _, f := range fields {
		if _, err := fmt.Fprintf(out, "%-8s %s\n", f[0], f[1]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(h.Items))
	for _, it := range h.Items {
		delivery, from := "-", "-"
		if it.Delivery != nil {
			delivery = it.Delivery.Kind
		}
		if it.DeliveryTaskID != nil {
			from = strconv.FormatInt(*it.DeliveryTaskID, 10)
		}
		rows = append(rows, []string{
			strconv.FormatInt(it.IssueID, 10), delivery, from, dash(it.Outcome), dash(it.OutcomeBy), dash(it.Note),
		})
	}
	return table(out, []string{"ISSUE", "DELIVERY", "FROM_TASK", "OUTCOME", "BY", "NOTE"}, rows)
}

// handoffVerbHelp is each item verb's short and long help (task 137.6).
var handoffVerbHelp = map[string][2]string{
	apiclient.HandoffDeliver: {
		"Record handoff items delivered, closing their issues",
		"Records each item delivered: its issue's delivery is integrated. The issue is\n" +
			"closed with --close — completed, not_planned or duplicate — or, by default,\n" +
			"with the delivery's suggested close reason, else completed. --close none\n" +
			"leaves it open; it is still kept out of later handoffs until new work\n" +
			"replaces the delivery.",
	},
	apiclient.HandoffReturn: {
		"Return handoff items to their workshop",
		"Records each item returned: the claim is released and the issue's readiness\n" +
			"withdrawn, so it waits in in_progress. --follow-up queues PROMPT as a\n" +
			"follow-up on the issue's latest main task (--paused holds it); with --side\n" +
			"it becomes a new side task merged back into the issue's main branch, running\n" +
			"that task's workflow unless --workflow names another. A follow-up the task\n" +
			"cannot take refuses the whole return.",
	},
	apiclient.HandoffWait: {
		"Park handoff items on something outside the handoff",
		"Records each item waiting: it keeps its claim after the handoff's task\n" +
			"settles, and closing the issue — by anyone, an import included — records\n" +
			"it delivered.",
	},
	apiclient.HandoffRelease: {
		"Release handoff items with no verdict",
		"Records each item released: the claim is dropped and the issue stays\n" +
			"eligible for a later handoff.",
	},
}

// newHandoffVerbCmd is `vincent handoff deliver|return|wait|release`. Every
// verb is all or nothing: one refused item, or one refused side effect —
// a close, a comment, a follow-up — records nothing for any of them.
func newHandoffVerbCmd(verb string) *cobra.Command {
	var (
		req         apiclient.HandoffItemRequest
		all         bool
		commentFile string
	)
	help := handoffVerbHelp[verb]
	cmd := &cobra.Command{
		Use:   verb + " <handoff> <issue>... | --all",
		Short: help[0],
		Long: help[1] + "\n\n" +
			"--all takes every unresolved item; waiting items are left alone. An item\n" +
			"already delivered, returned or released with another outcome is refused;\n" +
			"repeating its own outcome succeeds and changes nothing, comment and close\n" +
			"included. --note records why on the item; --comment-file posts a comment\n" +
			"on each issue first.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("handoff id must be a number: %q", args[0])
			}
			var issueIDs []int64
			for _, a := range args[1:] {
				n, err := parseIssueID(a)
				if err != nil {
					return err
				}
				issueIDs = append(issueIDs, n)
			}
			if all == (len(issueIDs) > 0) {
				return fmt.Errorf("name the issues, or pass --all: exactly one of them")
			}
			if cmd.Flags().Changed("comment-file") {
				data, err := readInputFile("--comment-file", commentFile, cmd.InOrStdin())
				if err != nil {
					return err
				}
				req.Comment = string(data)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				h, err := c.ResolveHandoffItems(ctx, id, verb, issueIDs, req)
				if err != nil {
					return issueFail(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), h)
				}
				return printHandoff(cmd.OutOrStdout(), &h)
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "Every unresolved item of the handoff")
	f.StringVar(&req.Note, "note", "", "Why, recorded on the item")
	f.StringVar(&commentFile, "comment-file", "", "Post a comment read from this file (- for stdin) on each issue")
	switch verb {
	case apiclient.HandoffDeliver:
		f.StringVar(&req.Close, "close", "", "Close reason: completed, not_planned, duplicate, or none to leave the issue open")
	case apiclient.HandoffReturn:
		f.StringVar(&req.FollowUp, "follow-up", "", "Queue this prompt as a follow-up on the issue's latest main task")
		f.BoolVar(&req.Paused, "paused", false, "Hold the follow-up or side task paused")
		f.BoolVar(&req.Side, "side", false, "Run the follow-up as a new side task merged back into the issue's main branch")
		f.StringVar(&req.Workflow, "workflow", "", "The side task's workflow (default: the latest main task's)")
	}
	jsonFlag(cmd)
	return cmd
}
