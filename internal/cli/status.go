package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Environment variables `vincent status` addresses the step with. They are
// two of §8.5's block, which reaches `agent` steps as well as `command` ones
// as of task 036 — the whole point of the command is that a step can say
// something without being told which row it is.
const (
	envTaskID = "VINCENT_TASK_ID"
	envStepID = "VINCENT_STEP_ID"
)

// newStatusCmd is the second §6-adjacent command with a command line, after
// `task follow-up` (task 027 decision 11), and it is here for the same kind
// of reason: it is invoked by a program, not by a human at a prompt. A step's
// own script or an agent's shell tool runs it, from inside the worktree, to
// say what it is doing — so it takes its addressing from the environment
// rather than from flags nobody would be there to type.
func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <message>",
		Short: "Report what the current step is doing, from inside that step",
		Long: "Set the status message of the step run this process belongs to (§5.4). " +
			"The task and step are read from " + envTaskID + " and " + envStepID + ", which " +
			"the daemon sets on every agent and command step (§8.5), so this only works " +
			"from inside a running step.\n\n" +
			"The message is one line of free text: it is flattened, stripped of control " +
			"characters and truncated to 256 bytes. An empty message clears the status. " +
			"It is shown live on the board and the task detail view, and the last value " +
			"set stays on the finished attempt — it is never treated as a failure reason.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID, stepID, err := stepFromEnv()
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				stored, err := c.SetStepStatus(ctx, taskID, stepID, args[0])
				if err != nil {
					// A 409 is the daemon refusing because the step is no
					// longer running — a rejected request, not a broken one.
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), map[string]any{
						"task_id": taskID, "step_id": stepID, "message": stored,
					})
				}
				// Quiet on success by default. This runs inside a step whose
				// stdout is the transcript, and a step that reports progress
				// ten times should not add ten lines of vincent's own noise
				// to the record it is trying to summarize.
				return nil
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// stepFromEnv reads the step this process belongs to out of §8.5's variables.
// The error names them, because the overwhelmingly likely cause of a miss is
// that the command was run outside a step at all.
func stepFromEnv() (int64, string, error) {
	raw := strings.TrimSpace(os.Getenv(envTaskID))
	stepID := strings.TrimSpace(os.Getenv(envStepID))
	if raw == "" || stepID == "" {
		return 0, "", fmt.Errorf(
			"%s and %s are not set: `vincent status` runs from inside a step, not from a shell",
			envTaskID, envStepID)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%s is not a task id: %q", envTaskID, raw)
	}
	return id, stepID, nil
}
