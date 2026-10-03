package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newIssueCmd is `vincent issue` (spec §5.6, task 130). Task 130.8 gives it
// its first leaf, `sync`; the rest of the tree grows around it.
func newIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Work with a project's issues",
	}
	cmd.AddCommand(newIssueSyncCmd())
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
