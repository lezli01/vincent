package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Register and inspect projects",
	}
	cmd.AddCommand(newProjectAddCmd(), newProjectLsCmd())
	return cmd
}

func newProjectAddCmd() *cobra.Command {
	var (
		name          string
		defaultBranch string
		workflow      string
		maxParallel   int
	)
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Register a git repository as a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				req := apiclient.CreateProjectRequest{Path: args[0]}
				// Only send what the user actually set: the daemon derives the
				// name from the directory and detects the default branch
				// itself, so an omitted flag means "you decide" rather than a
				// value the caller had to invent.
				if name != "" {
					req.Name = &name
				}
				if defaultBranch != "" {
					req.DefaultBranch = &defaultBranch
				}
				if cmd.Flags().Changed("workflow") {
					req.DefaultWorkflow = &workflow
				}
				if cmd.Flags().Changed("max-parallel") {
					req.MaxParallelTasks = &maxParallel
				}
				p, err := c.CreateProject(ctx, req)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), p)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "project %d registered: %s (%s, default branch %s)\n",
					p.ID, p.Name, p.Path, p.DefaultBranch)
				return err
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Project name (default: the directory name)")
	cmd.Flags().StringVar(&defaultBranch, "default-branch", "", "Base branch for new tasks (default: detected)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Default workflow for new tasks")
	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "Per-project concurrency cap")
	jsonFlag(cmd)
	return cmd
}

func newProjectLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List registered projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				projects, err := c.ListProjects(ctx)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if projects == nil {
					projects = []apiclient.Project{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), projects)
				}
				rows := make([][]string, 0, len(projects))
				for _, p := range projects {
					parallelCap := "-"
					if p.MaxParallelTasks != nil {
						parallelCap = strconv.Itoa(*p.MaxParallelTasks)
					}
					rows = append(rows, []string{
						strconv.FormatInt(p.ID, 10), p.Name, p.Path,
						p.DefaultBranch, p.Workflow(), parallelCap,
					})
				}
				return table(cmd.OutOrStdout(),
					[]string{"ID", "NAME", "PATH", "BRANCH", "WORKFLOW", "CAP"}, rows)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}
