package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// The daemon-free half of a global update-workflows run (task 123): `ls
// --global` is its inventory and `apply` installs what it staged. Neither
// talks to the daemon, the way `vincent trigger ls` and `vincent trigger
// apply` do not (098 decision 4): the registry's watcher reloads the global
// scope when a file is written, the same as it does an editor's save.

// globalWorkflowsDir is {config_dir}/workflows, the global scope.
func globalWorkflowsDir() (config.Dirs, string, error) {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return config.Dirs{}, "", err
	}
	return dirs, filepath.Join(dirs.Config, workflow.GlobalDirName), nil
}

func runWorkflowLsGlobal(cmd *cobra.Command) error {
	_, dir, err := globalWorkflowsDir()
	if err != nil {
		return err
	}
	all, problems := workflow.ListGlobal(dir, localValidateOptions())
	for _, p := range problems {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", p)
	}
	if all == nil {
		all = []workflow.GlobalListing{}
	}
	if wantJSON(cmd) {
		if err := emitJSON(cmd.OutOrStdout(), all); err != nil {
			return err
		}
	} else {
		for _, l := range all {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), l.File); err != nil {
				return err
			}
		}
	}
	if len(all) == 0 {
		return exitError{code: 1}
	}
	return nil
}

func newWorkflowApplyCmd() *cobra.Command {
	var (
		proposal int64
		check    bool
	)
	cmd := &cobra.Command{
		Use:   "apply --proposal <task_id> [--check]",
		Short: "Install a staged global workflow proposal (no daemon required)",
		Long: "Write the workflow files staged in {data_dir}/workflow-proposals/<task_id>/\n" +
			"into {config_dir}/workflows/, then remove the staging directory. A global\n" +
			"update-workflows run stages them, and a person approves them first.\n\n" +
			"The directory holds whole *.yaml/*.yml files named by their live base name\n" +
			"and manifest.json, a JSON object mapping each to the version\n" +
			"`vincent workflow ls --global --json` reported for it, or \"absent\" for a new\n" +
			"file. Every file is checked before any is written, and one refusal writes\n" +
			"nothing: a file must validate, pair with a manifest entry, match what the\n" +
			"manifest recorded on disk, keep its workflow's name, and declare a name no\n" +
			"other global workflow has. A new file must be named <name>.yaml. Each file is\n" +
			"written atomically; an existing file keeps its mode and a new one is 0644.\n\n" +
			"--check runs every check, writes and removes nothing, and prints the staged\n" +
			"files' absolute paths, one per line.\n\n" +
			"Exit 0 when the proposal was installed or checked; 1 when it was refused,\n" +
			"nothing is staged at that path, or a write failed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorkflowApply(cmd, proposal, check)
		},
	}
	cmd.Flags().Int64Var(&proposal, "proposal", 0, "the id of the task that staged the proposal")
	cmd.Flags().BoolVar(&check, "check", false, "check the proposal and list its files without writing anything")
	_ = cmd.MarkFlagRequired("proposal")
	return cmd
}

func runWorkflowApply(cmd *cobra.Command, proposal int64, check bool) error {
	stderr := cmd.ErrOrStderr()
	if proposal <= 0 {
		_, _ = fmt.Fprintln(stderr, "Error: --proposal takes a positive task id")
		return exitError{code: 1}
	}
	dirs, dir, err := globalWorkflowsDir()
	if err != nil {
		return err
	}
	staging := workflow.ProposalDir(dirs.Data, proposal)
	paths, refusals, err := workflow.ApplyProposal(staging, dir, localValidateOptions(), check)
	for _, path := range paths {
		if check {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), path)
		} else {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)
		}
	}
	if len(refusals) > 0 {
		for _, r := range refusals {
			_, _ = fmt.Fprintln(stderr, "  refused:", r)
		}
		_, _ = fmt.Fprintf(stderr, "%s: refused (%d reason(s)); nothing was written\n", staging, len(refusals))
		return exitError{code: 1}
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "Error:", err)
		return exitError{code: 1}
	}
	if check {
		return nil
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "removed", staging)
	return err
}
