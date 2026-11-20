package cli

// `vincent chat find` — which turns of a chat mention a string, read from
// `GET /v1/chats/{id}/search` (task 160 decision 2, §13.2). The chat
// workspace's search runs on the same route, and the CLI keeps parity with
// what the TUI asks the daemon.

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newChatFindCmd is a leaf read: exit 0 whenever the daemon answered, no
// matches included — an empty answer is an answer — 1 on a refusal, 2 with
// no daemon.
func newChatFindCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "find <chat-id> <text>",
		Short: "List the turns of a chat that mention some text",
		Long: "Search every turn of a chat for text, case-insensitively, as a plain substring: " +
			"each turn's prompt and its whole transcript — tool output included — or, once the " +
			"transcript has gone to transcript_retention_days, its result text.\n\n" +
			"stdout is one line per matching turn, `turn N  M match(es)`, in turn order, so " +
			"`vincent chat transcript 12 --turn N` reads one of them. --json emits the response " +
			"object unchanged, with `turns` always an array.\n\n" +
			"Exit 0 whenever the daemon answered, no matches included; 1 on an unknown chat or " +
			"an empty or over-256-byte query; 2 with no daemon.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				res, err := c.SearchChat(ctx, id, args[1])
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if res.Turns == nil {
					res.Turns = []apiclient.ChatSearchTurn{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), res)
				}
				return printChatFind(cmd.OutOrStdout(), cmd.ErrOrStderr(), res)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func printChatFind(out, errOut io.Writer, res *apiclient.ChatSearch) error {
	for _, t := range res.Turns {
		line := fmt.Sprintf("turn %d  %d %s", t.Seq, t.Matches, pluralWord(t.Matches, "match", "matches"))
		if t.Source == "result_text" {
			line += "  (result text only: the transcript is gone)"
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
		if t.Truncated {
			_, _ = fmt.Fprintf(errOut, "warning: turn %d's transcript was searched only in part\n", t.Seq)
		}
	}
	return nil
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
