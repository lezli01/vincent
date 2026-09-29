package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newTaskCommitsCmd lists the commits a task made on its branch (issue
// #601), from GET /v1/tasks/{id}/commits. The daemon reads them from the
// branch rather than the worktree, so this answers after archive too.
func newTaskCommitsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "commits <id>",
		Short: "List the commits a task made on its branch",
		Long: "List the task's own commits past its recorded base, oldest first: short sha, " +
			"author time, subject, and the lane on a fan-out lane merge. The commits are " +
			"read from the task's branch, so they are still listed after the task is " +
			"archived (§13.2). --json mirrors the wire format.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("task id must be a number: %q", args[0])
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				commits, err := c.TaskCommits(ctx, id)
				if err != nil {
					msg := apiMessage(err)
					if errors.Is(err, apiclient.ErrCommitsUnsupported) {
						msg = err.Error()
					}
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", msg)
					return exitError{code: 1}
				}
				out := cmd.OutOrStdout()
				if wantJSON(cmd) {
					return emitJSON(out, commits)
				}
				for _, cm := range commits {
					_, _ = fmt.Fprintln(out, commitLine(cm))
				}
				return nil
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// commitLine is one commit as `task commits` prints it, the author time in
// local time to the minute.
func commitLine(cm apiclient.Commit) string {
	sha := cm.SHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	at := cm.AuthorTime
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		at = t.Local().Format("2006-01-02 15:04")
	}
	parts := []string{sha, at, cm.Subject}
	if cm.LaneID != "" {
		parts = append(parts, fmt.Sprintf("[lane %s, task %d]", cm.LaneID, cm.ChildTaskID))
	}
	return strings.Join(parts, "  ")
}
