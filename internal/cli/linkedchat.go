package cli

// `vincent task chat` and `vincent chat close` — the CLI half of task 119, so
// talking to an agent inside a stopped task's worktree is not a TUI-only
// feature. Opening is a §6 action and hangs off `task`; closing ends a chat
// and hangs off `chat`, beside `archive`, which is how a free chat ends.

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newTaskChatCmd opens a chat linked to a stopped task. It prints the chat
// rather than the task: the task does not move, and the chat's id is what
// every next command takes.
func newTaskChatCmd() *cobra.Command {
	var title, agent, model, effort string
	cmd := &cobra.Command{
		Use:   "chat <id>",
		Short: "Open a chat in a stopped task's worktree",
		Long: "Opens a chat that works in the task's own worktree and branch, with the\n" +
			"task's context — its objective, and the failure, gate or last step it stopped\n" +
			"on — as its opening. The task does not move: it keeps its state, and while the\n" +
			"chat is open every action on it but cancel is refused. Talk with\n" +
			"`vincent chat send`, and end the chat with `vincent chat close`, which lifts\n" +
			"the lock. Valid from blocked, awaiting_gate, done and aborted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			req := apiclient.OpenTaskChatRequest{Title: title, Agent: agent, Model: model, Effort: effort}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, err := c.OpenTaskChat(ctx, id, req)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					// One chat per task at a time: the refusal names the open
					// one, and that id is both ways out, so it is printed as
					// commands rather than left in `details`.
					if open, ok := apiclient.TaskLockedByChat(err); ok {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
							"  chat %d is open on it: continue with `vincent chat send %d MESSAGE`, "+
								"or end it with `vincent chat close %d`\n", open, open, open)
					}
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), chat)
				}
				return printLinkedChat(cmd.OutOrStdout(), chat, id)
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Chat title (default: the task's)")
	cmd.Flags().StringVar(&agent, "agent", "",
		"Agent for the chat (§8.6, request level); it must be able to resume a session")
	cmd.Flags().StringVar(&model, "model", "", "Model for the chat (§8.6, request level)")
	cmd.Flags().StringVar(&effort, "effort", "", "Effort for the chat (§8.6, request level)")
	jsonFlag(cmd)
	return cmd
}

// printLinkedChat is the human line for a chat just opened on a task, and the
// two commands a reader needs next.
func printLinkedChat(w io.Writer, chat *apiclient.Chat, taskID int64) error {
	if _, err := fmt.Fprintf(w, "chat %d opened on task %d  %s  [%s]  %s\n",
		chat.ID, taskID, chat.Title, chat.Agent, chat.Branch); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "  talk with `vincent chat send %d MESSAGE`; end it with `vincent chat close %d`\n",
		chat.ID, chat.ID)
	return err
}

// newChatCloseCmd ends a chat opened on a task. It is not a second spelling
// of `chat archive`: the worktree belongs to the task, so closing removes
// nothing, and archive is refused on such a chat for exactly that reason.
func newChatCloseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <chat-id>",
		Short: "End a chat opened on a task, unlocking the task",
		Long: "Ends a chat opened with `vincent task chat`. A live turn is cancelled first;\n" +
			"the chat becomes closed and the task's actions come back. The worktree and\n" +
			"branch are the task's and are left exactly as the chat left them. A chat\n" +
			"started with `vincent chat start` cannot be closed — archive it instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, err := c.CloseChat(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), chat)
				}
				line := fmt.Sprintf("chat %d closed", chat.ID)
				if chat.LinkedTaskID != nil {
					line += fmt.Sprintf("; task %d is unlocked", *chat.LinkedTaskID)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), line)
				return err
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}
