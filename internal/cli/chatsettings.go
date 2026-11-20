package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newChatSetCmd is `vincent chat set` (task 162): a chat's title, model,
// effort and permission mode between turns, over PATCH /v1/chats/{id}. A
// change made while a turn runs applies from the next turn.
//
// Raising a chat to full-auto asks first, as the TUI does (§16, decision 4):
// --yes answers it, a terminal is asked, and anything else is refused rather
// than silently escalated. Lowering to restricted needs no confirmation.
func newChatSetCmd() *cobra.Command {
	var title, model, effort, mode string
	var yes bool
	cmd := &cobra.Command{
		Use:   "set <chat-id>",
		Short: "Change a chat's title, model, effort or permission mode",
		Long: "Changes apply from the chat's next turn; a live turn keeps what it started with.\n\n" +
			"--model and --effort may change after the first turn only on an adapter whose resumed " +
			"session honours a change (see `vincent agents`); otherwise open a new chat. " +
			"--permission-mode full-auto lets the agent run any command as you, and asks first: " +
			"pass --yes to confirm without a prompt.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			var req apiclient.PatchChatRequest
			for name, dst := range map[string]**string{
				"title": &req.Title, "model": &req.Model, "effort": &req.Effort, "permission-mode": &req.PermissionMode,
			} {
				if cmd.Flags().Changed(name) {
					v, _ := cmd.Flags().GetString(name)
					*dst = &v
				}
			}
			if req == (apiclient.PatchChatRequest{}) {
				return errors.New("nothing to change: give --title, --model, --effort or --permission-mode")
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if req.PermissionMode != nil && *req.PermissionMode == "full-auto" && !yes {
					chat, _, err := c.GetChat(ctx, id)
					if err != nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
						return exitError{code: 1}
					}
					if chat.PermissionMode != "full-auto" && !confirmFullAuto(cmd, id) {
						return exitError{code: 1}
					}
				}
				chat, err := c.PatchChat(ctx, id, req)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), chat)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "chat %d: %s\n", chat.ID, chatSettingsLine(chat))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&model, "model", "", "model for the next turns (empty for the adapter's default)")
	cmd.Flags().StringVar(&effort, "effort", "", "effort for the next turns (empty for the adapter's default)")
	cmd.Flags().StringVar(&mode, "permission-mode", "", "full-auto or restricted")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm raising the chat to full-auto without a prompt")
	jsonFlag(cmd)
	return cmd
}

// chatSettingsLine is a chat's model · effort · mode, with the adapter's
// default named as such.
func chatSettingsLine(c *apiclient.Chat) string {
	model, effort := c.Model, c.Effort
	if model == "" {
		model = "default model"
	}
	if effort == "" {
		effort = "default effort"
	}
	return model + " · " + effort + " · " + c.PermissionMode
}

// confirmFullAuto asks on a terminal before a chat is raised to full-auto, and
// refuses anywhere else: a script that wants it passes --yes.
func confirmFullAuto(cmd *cobra.Command, id int64) bool {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok || !isTTY(in) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"Error: raising chat %d to full-auto lets its agent run any command as you; pass --yes to confirm\n", id)
		return false
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
		"Chat %d's agent will run any command as you, without asking (spec §16). Raise it to full-auto? [y/N] ", id)
	return readYes(in)
}

func readYes(in io.Reader) bool {
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
