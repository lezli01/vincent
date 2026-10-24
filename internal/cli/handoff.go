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
	cmd.AddCommand(newHandoffStartCmd(), newHandoffLsCmd(), newHandoffShowCmd())
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
			"caps them. A selection takes the most urgent first — priority 1, then 4,\n" +
			"then issues with none — then the oldest delivery.\n\n" +
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
