package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newChatCmd is `vincent chat` (spec §5.5, §12.1, task 063): the CLI half of
// free chat, so the feature is not TUI-only.
//
// The verbs are the four §5.5 actions plus a read: start, send, list, show,
// archive. `send` blocks until the turn ends and prints the answer, which is
// what a conversation in a terminal has to do — a fire-and-forget send would
// leave the human polling `show`.
func newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Talk to an agent in its own worktree",
		Long: "Chats are conversations with an agent, each in its own git worktree and " +
			"vincent/{id}-{slug} branch. Every turn resumes the agent's own session, so " +
			"turn N sees turns 1..N-1 (spec §7.3, amended for chats).\n\n" +
			"Chats are not tasks: they never appear on the board, they run no workflow, " +
			"and a chat turn never waits for a scheduler slot.",
	}
	cmd.AddCommand(newChatStartCmd(), newChatSendCmd(), newChatListCmd(),
		newChatShowCmd(), newChatArchiveCmd())
	return cmd
}

func newChatStartCmd() *cobra.Command {
	var (
		projectID  int64
		agentName  string
		model      string
		effort     string
		baseBranch string
		message    string
	)
	cmd := &cobra.Command{
		Use:   "start <title>",
		Short: "Start a chat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, err := c.CreateChat(ctx, apiclient.CreateChatRequest{
					ProjectID: projectID, Title: args[0], Agent: agentName,
					Model: model, Effort: effort, BaseBranch: baseBranch,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "chat %d  %s  [%s]  %s\n",
					chat.ID, chat.Title, chat.Agent, chat.Branch)
				if message == "" {
					return nil
				}
				return runChatTurn(ctx, cmd, c, chat.ID, message)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "project id (required)")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent adapter; default is one that can resume a session")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&effort, "effort", "", "effort override")
	cmd.Flags().StringVar(&baseBranch, "base", "", "base branch; default is the project's")
	cmd.Flags().StringVar(&message, "message", "", "send this first message straight away")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func newChatSendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send <chat-id> <message>",
		Short: "Send a message and wait for the answer",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				return runChatTurn(ctx, cmd, c, id, args[1])
			})
		},
	}
	return cmd
}

// runChatTurn sends a message and polls the chat until the turn ends, then
// prints the agent's answer or the reason the turn failed.
//
// Polling rather than the SSE stream is deliberate for the CLI: `vincent chat
// send` is one round trip a human waits on, and a poll has no reconnect
// semantics to get wrong. The TUI, which renders the turn as it arrives, uses
// the stream.
func runChatTurn(ctx context.Context, cmd *cobra.Command, c *apiclient.Client, id int64, message string) error {
	turn, err := c.SendChat(ctx, id, message)
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
		return exitError{code: 1}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		_, turns, err := c.GetChat(ctx, id)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
			return exitError{code: 1}
		}
		for i := range turns {
			t := &turns[i]
			if t.ID != turn.ID || t.State == "running" {
				continue
			}
			if t.State != "done" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "turn %d %s: %s %s\n",
					t.Seq, t.State, t.FailReason, t.ErrorMessage)
				return exitError{code: 1}
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(t.ResultText, "\n"))
			return nil
		}
	}
}

func newChatListCmd() *cobra.Command {
	var projectID int64
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List chats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chats, err := c.ListChats(ctx, projectID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if len(chats) == 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No chats.")
					return nil
				}
				for i := range chats {
					ch := &chats[i]
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-5d %-14s %-10s %s\n",
						ch.ID, ch.State, ch.Agent, ch.Title)
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "only this project's chats")
	return cmd
}

func newChatShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <chat-id>",
		Short: "Show a chat and its turns",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, turns, err := c.GetChat(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				out := cmd.OutOrStdout()
				_, _ = fmt.Fprintf(out, "chat %d  %s\nstate: %s  agent: %s  branch: %s\n",
					chat.ID, chat.Title, chat.State, chat.Agent, chat.Branch)
				for i := range turns {
					t := &turns[i]
					_, _ = fmt.Fprintf(out, "\n--- turn %d (%s) ---\n> %s\n", t.Seq, t.State, t.Prompt)
					if t.ResultText != "" {
						_, _ = fmt.Fprintln(out, strings.TrimRight(t.ResultText, "\n"))
					}
					if t.FailReason != "" {
						_, _ = fmt.Fprintf(out, "failed: %s %s\n", t.FailReason, t.ErrorMessage)
					}
				}
				return nil
			})
		},
	}
	return cmd
}

func newChatArchiveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "archive <chat-id>",
		Short: "Archive a chat, removing its worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, err := c.ArchiveChat(ctx, id, force)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "chat %d archived\n", chat.ID)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "archive even if the worktree has local changes")
	return cmd
}
