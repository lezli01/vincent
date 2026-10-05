// Package cli implements the vincent command tree (spec §12.1). Subcommands
// beyond version are stubs until their phase lands.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/tui"
	"github.com/lezli01/vincent/internal/version"
)

// Execute runs the root command and returns the process exit code. Errors
// are printed here (not by cobra) so exitError can exit silently with its
// specific code after the command already reported the situation.
func Execute() int {
	root := newRootCmd()
	root.SilenceErrors = true
	err := root.Execute()
	var ee exitError
	if err != nil && !errors.As(err, &ee) {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	return asExitCode(err)
}

func newRootCmd() *cobra.Command {
	var project string
	root := &cobra.Command{
		Use:          "vincent",
		Short:        "vincent — local AI workload orchestrator",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Bare `vincent` is the TUI (§12.1); it auto-starts the daemon
			// in the background when unreachable.
			var opts []tui.Option
			if project != "" {
				opts = append(opts, tui.WithProject(project))
			}
			return tui.Run(cmd.Context(), opts...)
		},
	}
	// Local, not persistent: it is the TUI's, and a subcommand that takes a
	// project names it its own way (task 132 decision 3).
	root.Flags().StringVar(&project, "project", "",
		"open the TUI on this project, by name or id (a name match wins)")
	root.AddCommand(
		newDaemonCmd(), newVersionCmd(), newDoctorCmd(),
		newProjectCmd(), newTaskCmd(), newChatCmd(), newWorkflowCmd(), newTriggerCmd(), newServiceCmd(),
		newGCCmd(), newGitHubCmd(), newIssueCmd(), newSkillsCmd(), newStatusCmd(), newStatusLineCmd(), newUpdateCmd(),
		newConfigCmd(), newAgentsCmd(),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}
