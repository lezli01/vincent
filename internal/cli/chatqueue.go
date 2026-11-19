package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newChatQueueCmd is `vincent chat queue` (task 156): the follow-up messages
// waiting behind a chat's live turn, and the pause a failed, interrupted or
// cancelled turn leaves on them.
func newChatQueueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "List, edit and remove messages queued behind a chat's live turn",
		Long: "A message sent while a chat's turn runs is queued, and runs as the next turn " +
			"once the live one ends done. A turn that fails, is cancelled or is interrupted " +
			"pauses the queue instead: resume it, or clear it.",
	}
	cmd.AddCommand(newChatQueueListCmd(), newChatQueueEditCmd(), newChatQueueRmCmd(),
		newChatQueueResumeCmd(), newChatQueueClearCmd())
	return cmd
}

func parseChatID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("chat id: %w", err)
	}
	return id, nil
}

func newChatQueueListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <chat-id>",
		Short: "List a chat's queued messages, in the order they will run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				q, err := c.ChatQueue(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return printChatQueue(cmd, q)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func printChatQueue(cmd *cobra.Command, q *apiclient.ChatQueue) error {
	if wantJSON(cmd) {
		return emitJSON(cmd.OutOrStdout(), q)
	}
	out := cmd.OutOrStdout()
	if q.Paused {
		_, _ = fmt.Fprintf(out, "paused (%s): resume with `vincent chat queue resume`, or clear it\n", q.PauseReason)
	}
	if len(q.Messages) == 0 {
		_, _ = fmt.Fprintln(out, "no queued messages")
		return nil
	}
	for _, m := range q.Messages {
		first, _, _ := strings.Cut(m.Prompt, "\n")
		_, _ = fmt.Fprintf(out, "%d\t%s\n", m.ID, first)
	}
	return nil
}

func newChatQueueEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <chat-id> <message-id> <message>",
		Short: "Replace a queued message's text before it starts",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			qid, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("message id: %w", err)
			}
			if args[2] == "" {
				return errors.New("a message is required")
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				m, err := c.EditChatQueued(ctx, id, qid, args[2])
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), m)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "edited queued message %d\n", m.ID)
				return nil
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newChatQueueRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <chat-id> <message-id>",
		Short: "Remove a queued message before it starts",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			qid, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("message id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if err := c.DeleteChatQueued(ctx, id, qid); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed queued message %d\n", qid)
				return nil
			})
		},
	}
}

func newChatQueueResumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <chat-id>",
		Short: "Resume a paused queue; on an idle chat the next message starts now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				q, err := c.ResumeChatQueue(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return printChatQueue(cmd, q)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newChatQueueClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear <chat-id>",
		Short: "Delete every queued message and lift the pause",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if err := c.ClearChatQueue(ctx, id); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cleared chat %d's queue\n", id)
				return nil
			})
		},
	}
}
