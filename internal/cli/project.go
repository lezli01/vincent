package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Register, inspect, edit and remove projects",
	}
	cmd.AddCommand(newProjectAddCmd(), newProjectLsCmd(), newProjectEditCmd(), newProjectRmCmd())
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

// projectEditFlags are the flags of `project edit` that change a field, one
// per field PATCH /v1/projects/{id} accepts, in the order the help lists them.
var projectEditFlags = []string{
	"name", "path", "default-branch", "workflow", "max-parallel", "branch-template",
}

// errEmptyProjectPatch refuses an edit that names no field. PATCH would accept
// an empty body and answer the project unchanged, which reads as success to a
// caller whose flag was mistyped into an argument, so the CLI says so instead.
var errEmptyProjectPatch = errors.New("nothing to change: pass at least one of --" +
	strings.Join(projectEditFlags, ", --"))

func newProjectEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Change a registered project's settings",
		Long: "Changes only the fields whose flags are given; every other field is left\n" +
			"as it is. An empty value (--workflow \"\", --max-parallel \"\",\n" +
			"--branch-template \"\") clears that setting. --path is sent as typed and\n" +
			"must be absolute; when the new repository lacks the stored default branch,\n" +
			"pass --default-branch in the same command.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("project id must be a number: %q", args[0])
			}
			// Refused before a client exists: an edit that changes nothing
			// never needs a daemon, so it exits 1 with or without one.
			req, err := projectPatchFromFlags(cmd)
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				p, err := c.PatchProject(ctx, id, req)
				if err != nil {
					// The daemon re-runs registration's checks on a new path and
					// explains a repoint that lost the default branch; its own
					// wording is the useful part, so it is printed unchanged.
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), p)
				}
				parallelCap := "none"
				if p.MaxParallelTasks != nil {
					parallelCap = strconv.Itoa(*p.MaxParallelTasks)
				}
				template := "inherited"
				if p.BranchTemplate != nil && *p.BranchTemplate != "" {
					template = *p.BranchTemplate
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(),
					"project %d updated: %s (%s, default branch %s, workflow %s, cap %s, branch template %s)\n",
					p.ID, p.Name, p.Path, p.DefaultBranch, p.Workflow(), parallelCap, template)
				return err
			})
		},
	}
	cmd.Flags().String("name", "", "New project name")
	cmd.Flags().String("path", "", "New absolute path to the repository")
	cmd.Flags().String("default-branch", "", "Base branch for new tasks; must exist in the repository")
	cmd.Flags().String("workflow", "", `Default workflow for new tasks; "" falls back to adhoc`)
	cmd.Flags().String("max-parallel", "", `Per-project concurrency cap; "" removes the cap`)
	cmd.Flags().String("branch-template", "", `Branch name template; "" inherits config.yaml`)
	jsonFlag(cmd)
	return cmd
}

// projectPatchFromFlags builds the PATCH body from the flags the user set,
// leaving every other field absent so an edit never stomps a field it did not
// name (§13.2). It reads flags and nothing else, which is what lets it be
// tested without a daemon.
//
// An empty or whitespace-only value clears an optional field — the TUI
// project form's rule, since it trims its rows, and `config set`'s rule for
// emptying a value. Name, path and default branch have no cleared state, so
// their value goes out as typed and the daemon is the one that refuses an
// empty one. --max-parallel is a string for the same reason: an integer is
// sent as given, so 0 comes back with the daemon's own message, and anything
// else is refused here because there is no number to send.
func projectPatchFromFlags(cmd *cobra.Command) (apiclient.PatchProjectRequest, error) {
	flags := cmd.Flags()
	var req apiclient.PatchProjectRequest
	changed := false
	required := func(flag string) apiclient.Opt[string] {
		v, _ := flags.GetString(flag)
		return apiclient.SetOpt(v)
	}
	optional := func(flag string) apiclient.Opt[string] {
		v, _ := flags.GetString(flag)
		if strings.TrimSpace(v) == "" {
			return apiclient.NullOpt[string]()
		}
		return apiclient.SetOpt(v)
	}
	for _, flag := range projectEditFlags {
		if !flags.Changed(flag) {
			continue
		}
		changed = true
		switch flag {
		case "name":
			req.Name = required(flag)
		case "path":
			req.Path = required(flag)
		case "default-branch":
			req.DefaultBranch = required(flag)
		case "workflow":
			req.DefaultWorkflow = optional(flag)
		case "branch-template":
			req.BranchTemplate = optional(flag)
		case "max-parallel":
			v, _ := flags.GetString(flag)
			v = strings.TrimSpace(v)
			if v == "" {
				req.MaxParallelTasks = apiclient.NullOpt[int]()
				continue
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return apiclient.PatchProjectRequest{}, fmt.Errorf(
					"--max-parallel must be a whole number, or \"\" to remove the cap: %q", v)
			}
			req.MaxParallelTasks = apiclient.SetOpt(n)
		}
	}
	if !changed {
		return apiclient.PatchProjectRequest{}, errEmptyProjectPatch
	}
	return req, nil
}

func newProjectRmCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a project registration",
		Long: "Removes the project and its task rows. A project still holding\n" +
			"non-archived tasks is refused until --force is passed — force is the\n" +
			"confirmation, and it archives them on the way out. A project holding a\n" +
			"*running* task is refused either way: cancel it first.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("project id must be a number: %q", args[0])
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if err := c.DeleteProject(ctx, id, force); err != nil {
					// Two different 409s reach here — "N non-archived task(s)"
					// and one naming a running task — and they want opposite
					// things from the caller, so the daemon's own wording is
					// printed rather than a summary that would blur them.
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					// The endpoint answers 204, so there is no task to print.
					// A `--json` caller still gets an object: an empty stdout
					// is a parse error in every wrapper that pipes this.
					return emitJSON(cmd.OutOrStdout(), struct {
						ID      int64 `json:"id"`
						Removed bool  `json:"removed"`
					}{ID: id, Removed: true})
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "project %d removed\n", id)
				return err
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Remove even when the project still holds non-archived tasks, archiving them")
	jsonFlag(cmd)
	return cmd
}
