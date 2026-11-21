package cli

// `vincent chat shell` — a chat's shell passthrough over POST and GET
// /v1/chats/{id}/shell and its run, output and attach routes (§5.5, §13.2,
// task 170). A shell run is not a turn: it runs in the chat's workspace under
// the daemon's shell, and its output reaches the agent only when the
// finished run is attached to the next prompt.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatShellPoll is how often `vincent chat shell` reads a running command's
// row. The daemon caps a command at two minutes, so a quarter second is a
// few hundred cheap reads at most.
const chatShellPoll = 250 * time.Millisecond

// chatFailure prints the daemon's message for err and exits 1, the way the
// chat diff commands report a refusal.
func chatFailure(cmd *cobra.Command, err error) error {
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
	return exitError{code: 1}
}

func newChatShellCmd() *cobra.Command {
	var attach bool
	cmd := &cobra.Command{
		Use:   "shell <chat-id> <command>",
		Short: "Run a shell command in a chat's workspace; list and attach runs",
		Long: "Run <command> in the chat's workspace through the daemon, wait for it to end, " +
			"print its output and exit with its exit code. <command> is one argument — quote " +
			"it — and runs under the daemon's shell: /bin/sh on Linux and macOS, pwsh on " +
			"Windows. It is not a turn: the agent sees nothing of it unless the finished run " +
			"is attached, and then only on the chat's next turn.\n\n" +
			"A command is stopped after two minutes, and its stored output is capped at " +
			"256 KiB. One command runs at a time per chat; a second is refused, as is any " +
			"command on an archived, closed or handed-off chat.\n\n" +
			"--attach marks the finished run for the chat's next prompt. --json prints the " +
			"final run object instead of the output.\n\n" +
			"Exit: the command's own exit code when it exited; 1 with a one-line reason when " +
			"it timed out, was interrupted or canceled, or failed to start, and on a refusal; " +
			"2 with no daemon. The command's code is passed through as it is, so a command " +
			"that exits 1 or 2 looks like vincent's own failure; a script that must tell them " +
			"apart passes --json, which prints the run object (its state and exit_code) only " +
			"when the command ran.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			command := args[1]
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("a command is required")
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				return runChatShell(ctx, cmd, c, id, command, attach)
			})
		},
	}
	cmd.Flags().BoolVar(&attach, "attach", false, "mark the finished run for the chat's next prompt")
	jsonFlag(cmd)
	cmd.AddCommand(newChatShellListCmd(), newChatShellAttachCmd())
	return cmd
}

func runChatShell(ctx context.Context, cmd *cobra.Command, c *apiclient.Client, id int64, command string, attach bool) error {
	run, err := c.RunChatShell(ctx, id, command)
	if err != nil {
		return chatFailure(cmd, err)
	}
	run, err = awaitChatShell(ctx, c, run)
	if err != nil {
		return chatFailure(cmd, err)
	}
	if attach {
		if run, err = c.AttachChatShell(ctx, id, run.ID, true); err != nil {
			return chatFailure(cmd, err)
		}
	}
	if wantJSON(cmd) {
		if err := emitJSON(cmd.OutOrStdout(), run); err != nil {
			return err
		}
	} else {
		out, err := c.ChatShellOutput(ctx, id, run.ID)
		if err != nil {
			return chatFailure(cmd, err)
		}
		_, _ = fmt.Fprint(cmd.OutOrStdout(), out)
		if out != "" && !strings.HasSuffix(out, "\n") {
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
		}
		if run.Truncated {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Note: the output reached the daemon's cap and was cut")
		}
		if attach {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "run %d is attached to the chat's next prompt\n", run.ID)
		}
	}
	return chatShellExit(cmd, run)
}

// awaitChatShell reads the run until it is no longer running.
func awaitChatShell(ctx context.Context, c *apiclient.Client, run *apiclient.ChatShellRun) (*apiclient.ChatShellRun, error) {
	t := time.NewTicker(chatShellPoll)
	defer t.Stop()
	for run.State == apiclient.ChatShellRunning {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
		next, err := c.ChatShellRun(ctx, run.ChatID, run.ID)
		if err != nil {
			return nil, err
		}
		run = next
	}
	return run, nil
}

// chatShellExit is the process's exit for an ended run: the command's own
// code when it exited, otherwise 1 and a line saying why there is none.
func chatShellExit(cmd *cobra.Command, run *apiclient.ChatShellRun) error {
	if run.State == apiclient.ChatShellExited && run.ExitCode != nil {
		if *run.ExitCode == 0 {
			return nil
		}
		return exitError{code: *run.ExitCode}
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: the command %s\n", chatShellEnding(run.State))
	return exitError{code: 1}
}

func chatShellEnding(state string) string {
	switch state {
	case apiclient.ChatShellTimedOut:
		return "timed out (the daemon stops a command after two minutes)"
	case apiclient.ChatShellInterrupted:
		return "was interrupted: the daemon stopped while it ran"
	case apiclient.ChatShellCanceled:
		return "was canceled"
	case apiclient.ChatShellFailed:
		return "failed to start"
	default:
		return "ended without an exit code (" + state + ")"
	}
}

// chatShellStatus is a run's state column: `exited 3` for an exit, the
// state otherwise.
func chatShellStatus(run *apiclient.ChatShellRun) string {
	if run.State == apiclient.ChatShellExited && run.ExitCode != nil {
		return "exited " + strconv.Itoa(*run.ExitCode)
	}
	return run.State
}

// chatShellSent is whether, and how, the agent got the run.
func chatShellSent(run *apiclient.ChatShellRun) string {
	switch {
	case run.AttachedTurn != nil:
		return "turn " + strconv.Itoa(*run.AttachedTurn)
	case run.Attach:
		return "next prompt"
	default:
		return "-"
	}
}

func newChatShellListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls <chat-id>",
		Aliases: []string{"list"},
		Short:   "List a chat's shell runs, oldest first",
		Long: "List the chat's shell runs, oldest first: id, the turn the run came after, " +
			"its state or exit code, whether the agent got it (`turn N` for the turn that " +
			"took it, `next prompt` for a run marked and not yet sent), and the command.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				runs, err := c.ChatShellRuns(ctx, id)
				if err != nil {
					return chatFailure(cmd, err)
				}
				if runs == nil {
					runs = []apiclient.ChatShellRun{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), runs)
				}
				rows := make([][]string, 0, len(runs))
				for i := range runs {
					r := &runs[i]
					rows = append(rows, []string{
						strconv.FormatInt(r.ID, 10), strconv.Itoa(r.AfterTurn),
						chatShellStatus(r), chatShellSent(r), oneLine(r.Command),
					})
				}
				return table(cmd.OutOrStdout(), []string{"ID", "AFTER TURN", "STATE", "SENT", "COMMAND"}, rows)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// oneLine keeps a multi-line command to its listing's row.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func newChatShellAttachCmd() *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use:   "attach <chat-id> <run-id>",
		Short: "Mark a finished shell run for the chat's next prompt, or unmark it",
		Long: "Mark a finished run so the chat's next turn is sent its command and output, " +
			"ahead of the message. Only that one turn gets it. --undo removes the mark.\n\n" +
			"Refused for a run still running and for one a turn has already taken. Exit 1 " +
			"on a refusal.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			runID, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("run id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				run, err := c.AttachChatShell(ctx, id, runID, !undo)
				if err != nil {
					return chatFailure(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), run)
				}
				if run.Attach {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "run %d is attached to the chat's next prompt\n", run.ID)
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "run %d is no longer attached\n", run.ID)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "remove the run's mark instead")
	jsonFlag(cmd)
	return cmd
}
