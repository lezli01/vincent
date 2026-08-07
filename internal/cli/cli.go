// Package cli implements the vincent command tree (spec §12.1). Subcommands
// beyond version are stubs until their phase lands.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/version"
)

// Execute runs the root command and returns the process exit code.
func Execute() int {
	if err := newRootCmd().Execute(); err != nil {
		return 1
	}
	return 0
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "vincent",
		Short:        "vincent — local AI workload orchestrator",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "The vincent TUI is not implemented yet (Phase 3). Try `vincent version`.")
			return err
		},
	}
	root.AddCommand(newDaemonCmd(), newVersionCmd())
	return root
}

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the vincent daemon in the foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "The vincent daemon is not implemented yet (Phase 1).")
			return err
		},
	}
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
