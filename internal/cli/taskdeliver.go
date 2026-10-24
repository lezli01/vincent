package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newTaskDeliverCmd is `vincent task deliver` (task 137.2): a run says what
// it delivered. Like `vincent status` it is invoked by a program, from inside
// a step, so it addresses the task with §8.5's VINCENT_TASK_ID rather than an
// argument nobody would be there to type; `--task` is for a human correcting
// a finished task. The daemon fills the branch, the base and — unless
// `--head` names one — the head commit, because clients never touch git.
//
// A containerized step cannot run it — the image carries no vincent binary
// and no route to the daemon (task 062.2 decision 4) — and records its
// delivery through the `task_deliver` tool on its per-step MCP endpoint.
func newTaskDeliverCmd() *cobra.Command {
	var (
		taskID                                         int64
		kind, title, body, bodyFile                    string
		summary, summaryFile, head, closeReason, proof string
		proofFile                                      string
	)
	cmd := &cobra.Command{
		Use:   "deliver",
		Short: "Record what this run delivered, from inside one of its steps",
		Long: "Record the task's delivery (§5.3): what its run produced, which a handoff reads. " +
			"From inside a step the task is read from " + envTaskID + "; --task names one " +
			"by hand. A later call replaces the delivery, and a run that finishes without one " +
			"gets an implicit delivery built from the task and its branch.\n\n" +
			"--kind is change (the default: commits to integrate), reply (an answer or " +
			"questions to post) or none. --head must be a commit on the task's branch and " +
			"defaults to its tip. The daemon accepts a delivery while the task runs or once " +
			"it is done.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := deliverTaskID(taskID)
			if err != nil {
				return err
			}
			in := apiclient.DeliveryInput{Kind: kind, Title: title, HeadSHA: head, CloseReason: closeReason}
			for _, f := range []struct {
				dst           *string
				literal, file string
			}{{&in.Body, body, bodyFile}, {&in.Summary, summary, summaryFile}, {&in.Proof, proof, proofFile}} {
				if *f.dst, err = flagText(cmd, f.literal, f.file); err != nil {
					return err
				}
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				out, err := c.SetTaskDelivery(ctx, id, in)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), out)
				}
				// Quiet from inside a step, whose stdout is the transcript;
				// a human who named the task by hand is told what was stored.
				if taskID != 0 {
					printDelivery(cmd.OutOrStdout(), out)
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&taskID, "task", 0, "Task id; defaults to "+envTaskID+", set inside a step")
	cmd.Flags().StringVar(&kind, "kind", "", "change (default), reply or none")
	cmd.Flags().StringVar(&title, "title", "", "Plain-language title, as a pull request or comment would carry")
	cmd.Flags().StringVar(&body, "body", "", "Markdown body")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read the body from a file (- for stdin)")
	cmd.Flags().StringVar(&summary, "summary", "", "What the change is for")
	cmd.Flags().StringVar(&summaryFile, "summary-file", "", "Read the summary from a file (- for stdin)")
	cmd.Flags().StringVar(&head, "head", "", "The commit the run verified; the branch tip by default")
	cmd.Flags().StringVar(&closeReason, "close-reason", "", "Suggested close reason: completed, not_planned or duplicate")
	cmd.Flags().StringVar(&proof, "proof", "", "What proved the change")
	cmd.Flags().StringVar(&proofFile, "proof-file", "", "Read the proof from a file (- for stdin)")
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
	cmd.MarkFlagsMutuallyExclusive("summary", "summary-file")
	cmd.MarkFlagsMutuallyExclusive("proof", "proof-file")
	jsonFlag(cmd)
	return cmd
}

// deliverTaskID is the task `vincent task deliver` addresses: --task when
// given, else VINCENT_TASK_ID. The error names both, because the likely
// cause of a miss is a shell outside any step.
func deliverTaskID(flag int64) (int64, error) {
	if flag != 0 {
		return flag, nil
	}
	raw := strings.TrimSpace(os.Getenv(envTaskID))
	if raw == "" {
		return 0, fmt.Errorf("%s is not set and --task was not given: "+
			"`vincent task deliver` runs from inside a step, or names the task", envTaskID)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is not a task id: %q", envTaskID, raw)
	}
	return id, nil
}

// printDelivery writes a delivery in `task show`'s row style, the long
// fields last because they are the long ones. Nil prints nothing.
func printDelivery(w io.Writer, d *apiclient.Delivery) {
	if d == nil {
		return
	}
	rows := [][2]string{{"delivery", deliveryLine(d)}}
	for _, r := range [][2]string{{"branch", d.Branch}, {"base", d.Base}, {"close as", d.CloseReason}} {
		if r[1] != "" {
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "%-9s %s\n", r[0], r[1])
	}
	for _, block := range [][2]string{{"summary", d.Summary}, {"proof", d.Proof}, {"body", d.Body}} {
		if text := strings.TrimSpace(block[1]); text != "" {
			_, _ = fmt.Fprintf(w, "\n%s:\n%s\n", block[0], text)
		}
	}
}

// deliveryLine is a delivery on one line — its kind, whether the daemon
// wrote it, the commit and the title — the `delivered` row of `task show`.
func deliveryLine(d *apiclient.Delivery) string {
	parts := []string{d.Kind}
	if d.Implicit {
		parts[0] += " (implicit)"
	}
	if d.HeadSHA != "" {
		parts = append(parts, "at "+shortSHA(d.HeadSHA))
	}
	line := strings.Join(parts, " ")
	if d.Title != "" {
		line += ": " + d.Title
	}
	return line
}

// shortSHA is the twelve-character form a person reads; the full one is
// in --json.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
