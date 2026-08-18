package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Create, inspect and cancel tasks",
	}
	cmd.AddCommand(newTaskAddCmd(), newTaskLsCmd(), newTaskShowCmd(), newTaskCancelCmd())
	return cmd
}

func newTaskAddCmd() *cobra.Command {
	var (
		projectID   int64
		workflow    string
		title       string
		description string
		baseBranch  string
		priority    int
		branch      string
		agent       string
		model       string
		effort      string
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create a task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				req := apiclient.CreateTaskRequest{ProjectID: projectID, Title: title}
				for _, f := range []struct {
					name string
					dst  **string
					src  *string
				}{
					{"workflow", &req.Workflow, &workflow},
					{"description", &req.Description, &description},
					{"base-branch", &req.BaseBranch, &baseBranch},
					{"branch", &req.BranchName, &branch},
					{"agent", &req.Agent, &agent},
					{"model", &req.Model, &model},
					{"effort", &req.Effort, &effort},
				} {
					if cmd.Flags().Changed(f.name) {
						v := *f.src
						*f.dst = &v
					}
				}
				if cmd.Flags().Changed("priority") {
					p := priority
					req.Priority = &p
				}
				t, err := c.CreateTask(ctx, req)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				out := cmd.OutOrStdout()
				if _, err := fmt.Fprintf(out, "task %d created: %s (%s, branch %s)\n",
					t.ID, t.Title, t.Workflow, t.BranchName); err != nil {
					return err
				}
				// Warnings are advisory — a catalog-unknown model, say. The
				// task exists and will run, so this is not an error exit; but
				// it goes to stderr so `--json` piping stays clean and a human
				// still sees it.
				for _, w := range t.Warnings {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow name (default: the project's)")
	cmd.Flags().StringVar(&title, "title", "", "Task title (required)")
	cmd.Flags().StringVar(&description, "description", "", "Task description")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", "Base branch (default: the project's)")
	cmd.Flags().StringVar(&branch, "branch", "",
		"Name for the task's branch, used verbatim (default: the project or config template)")
	cmd.Flags().IntVar(&priority, "priority", 0, "Scheduling priority; higher runs first")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent override (§8.6 level 2)")
	cmd.Flags().StringVar(&model, "model", "", "Model override (§8.6 level 2)")
	cmd.Flags().StringVar(&effort, "effort", "", "Effort override (§8.6 level 2)")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("title")
	jsonFlag(cmd)
	return cmd
}

func newTaskLsCmd() *cobra.Command {
	var (
		projectID       int64
		state           string
		archived        bool
		includeChildren bool
		parentID        int64
		limit           int
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				opts := apiclient.ListTasksOptions{
					ProjectID: projectID, State: state, Limit: limit,
					// Fan-out lanes are hidden by default (§13.2): the list is
					// the work someone asked for, and a 64-task tree buries it.
					IncludeChildren: includeChildren, ParentID: parentID,
				}
				if archived {
					opts.Archived = apiclient.ArchivedAll
				}
				tasks, err := c.ListTasks(ctx, opts)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if tasks == nil {
					tasks = []apiclient.Task{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), tasks)
				}
				rows := make([][]string, 0, len(tasks))
				for _, t := range tasks {
					rows = append(rows, []string{
						strconv.FormatInt(t.ID, 10), t.State, t.ProjectName,
						t.Workflow, progress(t), t.Title,
					})
				}
				return table(cmd.OutOrStdout(),
					[]string{"ID", "STATE", "PROJECT", "WORKFLOW", "STEP", "TITLE"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Only tasks in this project")
	cmd.Flags().StringVar(&state, "state", "", "Only tasks in this state")
	cmd.Flags().BoolVar(&archived, "archived", false, "Include archived tasks")
	cmd.Flags().BoolVar(&includeChildren, "include-children", false,
		"Include fan-out lanes, which are hidden by default")
	cmd.Flags().Int64Var(&parentID, "parent", 0,
		"List one fan-out task's lanes, in merge order")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum rows")
	jsonFlag(cmd)
	return cmd
}

// progress renders the step cursor as k/n. A finished task's cursor sits one
// past the last step, so it is clamped rather than shown as 4/3.
func progress(t apiclient.Task) string {
	if t.StepTotal == 0 {
		return "-"
	}
	k := min(t.CurrentStep+1, t.StepTotal)
	return fmt.Sprintf("%d/%d", k, t.StepTotal)
}

func newTaskShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a task and its step runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("task id must be a number: %q", args[0])
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, err := c.GetTask(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				out := cmd.OutOrStdout()
				fields := [][2]string{
					{"id", strconv.FormatInt(t.ID, 10)},
					{"title", t.Title},
					{"state", t.State},
					{"project", t.ProjectName},
					{"workflow", t.Workflow},
					{"step", progress(t.Task)},
					{"branch", t.BranchName},
				}
				if t.BlockReason != nil && *t.BlockReason != "" {
					fields = append(fields, [2]string{"blocked", *t.BlockReason})
				}
				if t.InputTokens > 0 || t.OutputTokens > 0 {
					fields = append(fields, [2]string{
						"tokens",
						fmt.Sprintf("%d in / %d out", t.InputTokens, t.OutputTokens),
					})
				}
				// Cost is nil when nothing reported one, which is not the same
				// as free and must not render as $0.00.
				if t.CostUSD != nil {
					fields = append(fields, [2]string{"cost", fmt.Sprintf("$%.4f", *t.CostUSD)})
				}
				for _, f := range fields {
					if _, err := fmt.Fprintf(out, "%-9s %s\n", f[0], f[1]); err != nil {
						return err
					}
				}
				if t.Description != "" {
					if _, err := fmt.Fprintf(out, "\n%s\n", strings.TrimRight(t.Description, "\n")); err != nil {
						return err
					}
				}
				if len(t.Steps) == 0 {
					return nil
				}
				if _, err := fmt.Fprintln(out); err != nil {
					return err
				}
				rows := make([][]string, 0, len(t.Steps))
				for _, s := range t.Steps {
					rows = append(rows, []string{
						strconv.FormatInt(s.ID, 10), s.StepID, s.State,
						dash(deref(s.Agent)), dash(deref(s.FailureReason)),
					})
				}
				if err := table(out, []string{"RUN", "STEP", "STATE", "AGENT", "REASON"}, rows); err != nil {
					return err
				}
				// The transcript is the complete record of what the agent did
				// (§17); the TUI shows only its tail. Naming the file is what
				// lets someone diagnose a failed step at all right now.
				var paths []string
				for _, s := range t.Steps {
					if p := deref(s.TranscriptPath); p != "" {
						paths = append(paths, fmt.Sprintf("  %d  %s", s.ID, p))
					}
				}
				if len(paths) == 0 {
					return nil
				}
				_, err = fmt.Fprintf(out, "\ntranscripts:\n%s\n", strings.Join(paths, "\n"))
				return err
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newTaskCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("task id must be a number: %q", args[0])
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, err := c.Cancel(ctx, id)
				if err != nil {
					// A 409 here is the FSM refusing the action (§6), which is
					// a rejected request, not a broken one: exit 1 with the
					// daemon's own wording.
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "task %d is now %s\n", t.ID, t.State)
				return err
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}
