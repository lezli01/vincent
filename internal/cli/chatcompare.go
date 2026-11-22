package cli

// `vincent chat compare` (task 171 decisions 6 and 7): one prompt answered
// by two agents, each in its own fork, side by side.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

func newChatCompareCmd() *cobra.Command {
	var (
		turn               int
		agents             []string
		prompt, promptFile string
		mode               string
	)
	cmd := &cobra.Command{
		Use:   "compare <chat-id> --turn N --agents A,B [--prompt P | --prompt-file F]",
		Short: "Ask two agents the same thing, each in its own fork of a chat",
		Long: "Forks the chat twice from the state before turn N ran — once onto each of the two " +
			"agents, each on its own branch — and sends both forks the same prompt: --prompt, " +
			"--prompt-file (`-` reads stdin), or by default turn N's own prompt, which asks how " +
			"each agent would have answered it. Both start, or neither: the daemon refuses when " +
			"max_parallel_chats has no room for two more.\n\n" +
			"Each side runs restricted where its agent can keep a chat restricted, and full-auto " +
			"where it cannot — this prints which, and why. --permission-mode full-auto runs both " +
			"full-auto. Follow the two with `vincent chat show`, or side by side in the TUI.\n\n" +
			"Exit 1 on a refusal; 2 with no daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			if len(agents) != 2 {
				return errors.New("--agents takes exactly two agents, left then right (e.g. --agents claude,codex)")
			}
			if prompt != "" && promptFile != "" {
				return errors.New("give at most one of --prompt or --prompt-file")
			}
			if promptFile != "" {
				data, err := readInputFile("--prompt-file", promptFile, cmd.InOrStdin())
				if err != nil {
					return err
				}
				if prompt = string(data); strings.TrimSpace(prompt) == "" {
					return errors.New("the prompt file is empty; omit it to send turn N's own prompt")
				}
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				res, err := c.CompareChat(ctx, id, apiclient.CompareRequest{
					Turn: turn, Prompt: prompt, Agents: agents, PermissionMode: mode,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", compareErrorMessage(err, id))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), res)
				}
				printCompare(cmd.OutOrStdout(), res)
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&turn, "turn", 0, "fork both sides from the state before this turn ran, as `chat show` numbers it")
	cmd.Flags().StringSliceVar(&agents, "agents", nil, "the two agents, left then right (e.g. claude,codex)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "what both sides are sent (default: turn N's own prompt)")
	cmd.Flags().StringVar(&promptFile, "prompt-file", "", "read the prompt from this file (`-` for stdin)")
	cmd.Flags().StringVar(&mode, "permission-mode", "", "restricted (default: where each agent can) or full-auto")
	_ = cmd.MarkFlagRequired("turn")
	_ = cmd.MarkFlagRequired("agents")
	jsonFlag(cmd)
	return cmd
}

// printCompare says what started: both sides, each with the mode it really
// runs under (decision 7), and where to follow them.
func printCompare(w io.Writer, res *apiclient.CompareResult) {
	cmp := res.Compare
	_, _ = fmt.Fprintf(w, "compare %d: chat %d, from before turn %d\n", cmp.ID, cmp.SourceChatID, cmp.Seq)
	_, _ = fmt.Fprintf(w, "prompt: %s\n", oneLinePrompt(cmp.Prompt))
	for _, s := range []struct {
		name    string
		side    apiclient.CompareSide
		started apiclient.CompareStarted
	}{{"left", cmp.Left, res.Left}, {"right", cmp.Right, res.Right}} {
		line := fmt.Sprintf("%-5s  chat %d  %s  %s", s.name, s.started.Chat.ID, s.side.Agent, s.side.PermissionMode)
		if s.started.Seeded {
			line += "  (" + seededNote + ")"
		}
		_, _ = fmt.Fprintln(w, line)
		if s.side.ModeNote != "" {
			_, _ = fmt.Fprintf(w, "       full-auto because: %s\n", s.side.ModeNote)
		}
		printSkipped(w, s.started.Skipped)
	}
	_, _ = fmt.Fprintf(w, "follow: vincent chat show %d; vincent chat show %d\n", res.Left.Chat.ID, res.Right.Chat.ID)
}

// oneLinePrompt is a prompt's first line, marked when there is more.
func oneLinePrompt(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexAny(p, "\r\n"); i >= 0 {
		return p[:i] + " …"
	}
	return p
}

// compareErrorMessage is a compare refusal in plain words: the capacity
// refusal names the setting, the rest read as a fork's do.
func compareErrorMessage(err error, chatID int64) string {
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) && apiErr.Code == "chat_cap_reached" {
		return apiErr.Message + " — raise max_parallel_chats or wait for a chat to finish; nothing was created"
	}
	return rewindErrorMessage(err, chatID)
}
