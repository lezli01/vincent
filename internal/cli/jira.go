package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent jira` (task 142.12). It is read-only: `status` asks the daemon to
// probe a project's Jira binding, and the daemon — never this process —
// makes the Jira calls with the credential its own environment holds. The
// command neither reads nor prints the credential.
func newJiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jira",
		Short: "Check a project's Jira binding",
	}
	cmd.AddCommand(newJiraStatusCmd())
	return cmd
}

// `vincent jira status` is the Jira counterpart of `vincent github status`:
// whether this project's issues would be imported from Jira, and if not, the
// first check that failed (the site, the credential, the bound project).
// `vincent issue sync --status` says how the import is actually going.
func newJiraStatusCmd() *cobra.Command {
	var projectID int64
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether a project's Jira binding can be reached and read",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				status, err := c.ProjectJira(ctx, projectID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), status)
				}
				return table(cmd.OutOrStdout(), []string{"CHECK", "VALUE"}, jiraStatusRows(status))
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

// jiraStatusRows renders `vincent jira status`. The server and account rows
// appear once the probe got that far, so a failure reads as where it
// stopped.
func jiraStatusRows(status apiclient.JiraStatus) [][]string {
	rows := [][]string{
		{"enabled", boolWord(status.Enabled)},
		{"site", dash(status.Site)},
		{"project", dash(status.ProjectKey)},
	}
	if status.Server != nil {
		rows = append(rows, []string{"server", dash(jiraServerLine(*status.Server))})
	}
	if status.Account != nil {
		rows = append(rows, []string{"account", dash(status.Account.DisplayName)})
	}
	if status.ProjectID != "" {
		rows = append(rows, []string{"project id", status.ProjectID})
	}
	if status.Available {
		return append(rows, []string{"issue import/sync", "available"})
	}
	return append(rows, []string{"issue import/sync", "unavailable: " + status.Unavailable()})
}

func jiraServerLine(s apiclient.JiraServer) string {
	switch {
	case s.Version != "" && s.DeploymentType != "":
		return s.Version + " (" + s.DeploymentType + ")"
	case s.DeploymentType != "":
		return s.DeploymentType
	default:
		return s.Version
	}
}
