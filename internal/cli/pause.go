package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// pausePollInterval paces `vincent daemon pause --wait`.
const pausePollInterval = time.Second

// newDaemonPauseCmd is `vincent daemon pause` (task 149): the global pause,
// for maintenance. --wait is the half a script needs — it returns once no
// task is still finishing a step, which is the moment it is safe to stop
// the daemon, upgrade or reboot without interrupting anything mid-step.
func newDaemonPauseCmd() *cobra.Command {
	var (
		reason  string
		wait    bool
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause all work: admit nothing new and park every running task at its next step boundary",
		Long: "Take the global pause. The daemon stops admitting queued tasks, and every " +
			"running task finishes the step it is on and then parks as paused — the same " +
			"soft pause `vincent task pause` is, so no step is cut short. Tasks waiting on " +
			"a human (gates, blocks, questions) are left where they are. The pause survives " +
			"a daemon restart.\n\n" +
			"`vincent daemon continue` lifts it and resumes exactly the tasks it parked; a " +
			"task you paused yourself stays paused. Pausing an already-paused daemon keeps " +
			"the original time and reason.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				p, err := c.PauseAll(ctx, reason)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wait && p.State == apiclient.PausePausing {
					if !wantJSON(cmd) {
						_ = renderPause(cmd.OutOrStdout(), p)
					}
					if p, err = waitPaused(ctx, c, timeout); err != nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
						return exitError{code: 1}
					}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), p)
				}
				return renderPause(cmd.OutOrStdout(), p)
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "A one-line note shown wherever the pause is, e.g. \"upgrading vincent\"")
	cmd.Flags().BoolVar(&wait, "wait", false, "Return only once no task is still finishing its step")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "With --wait, give up after this long (exit 1; the pause stays)")
	jsonFlag(cmd)
	return cmd
}

// newDaemonContinueCmd is `vincent daemon continue` (task 149).
func newDaemonContinueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "continue",
		Short: "Lift the global pause and resume the tasks it parked",
		Long: "Lift the global pause taken by `vincent daemon pause`. Queued tasks are " +
			"admitted again, running tasks whose pause had not taken effect yet carry on, " +
			"and every task the pause parked is resumed. A task you paused yourself — " +
			"before the pause or during it — stays paused.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				p, err := c.ContinueAll(ctx)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), p)
				}
				return renderPause(cmd.OutOrStdout(), p)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// waitPaused polls until the pause has taken effect everywhere, the pause
// is lifted by someone else, or the timeout passes.
func waitPaused(ctx context.Context, c *apiclient.Client, timeout time.Duration) (apiclient.GlobalPause, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(pausePollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return apiclient.GlobalPause{}, fmt.Errorf("tasks were still finishing their step after %s; the pause stays in effect", timeout)
		case <-tick.C:
		}
		p, err := c.GlobalPause(ctx)
		if err != nil {
			return apiclient.GlobalPause{}, fmt.Errorf("read the pause: %s", apiMessage(err))
		}
		if p.State != apiclient.PausePausing {
			if p.Active() {
				return p, fmt.Errorf("the pause was lifted while waiting")
			}
			return p, nil
		}
	}
}

// renderPause prints the global pause as one or two lines a human reads.
func renderPause(w io.Writer, p apiclient.GlobalPause) error {
	_, err := fmt.Fprintln(w, pauseSummary(p))
	return err
}

// pauseSummary is the pause in one line — what `daemon pause`, `daemon
// continue` and `daemon status` all print.
func pauseSummary(p apiclient.GlobalPause) string {
	switch p.State {
	case apiclient.PausePausing, apiclient.PausePaused:
		head := "paused"
		if p.State == apiclient.PausePausing {
			head = fmt.Sprintf("pausing — %s still finishing %s step", taskCount(p.Running), pick(p.Running == 1, "its", "their"))
		}
		if p.PausedAt != nil {
			head += " (since " + p.PausedAt.Local().Format(time.RFC3339) + ")"
		}
		if p.Reason != "" {
			head += ": " + p.Reason
		}
		return fmt.Sprintf("%s; %s held, %s queued", head, taskCount(p.Held), taskCount(p.Queued))
	default:
		line := "active — work is being admitted"
		if p.Resumed != nil {
			line = fmt.Sprintf("active — resumed %s", taskCount(*p.Resumed))
		}
		return line
	}
}

// taskCount is "1 task" / "3 tasks".
func taskCount(n int) string {
	if n == 1 {
		return "1 task"
	}
	return fmt.Sprintf("%d tasks", n)
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
