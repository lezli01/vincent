package cli

// `vincent chat search` — GET /v1/chats/search (task 156, #1022, §13.2): a
// full-text search over every chat's title, prompts and final answers.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newChatSearchCmd is a leaf read: 0 whenever the daemon answered, no
// results included; 1 on a refusal (a query too short for the index); 2 with
// no daemon.
func newChatSearchCmd() *cobra.Command {
	var (
		projectID int64
		archived  string
		limit     int
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search every chat's title, prompts and answers",
		Long: "Search every chat — live, archived, handed off and closed — for text in its " +
			"title, in what was asked, or in the agent's final answers. Tool output in " +
			"transcripts is not searched.\n\n" +
			"Matching is by substring and ignores case, so `429` finds `status429Body`. Every " +
			"whitespace-separated term must appear, and each must be at least three " +
			"characters; quotes, `*`, `-` and words such as NEAR are matched as text.\n\n" +
			"Results are grouped by chat, best match first: the chat's id, state and title, " +
			"then each match's turn number and a snippet around it. --archived narrows the " +
			"scope the way `GET /v1/chats` does — `false` live chats only, `true` terminal " +
			"chats only — and defaults to `all`. --json emits the response's `results` array.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch archived {
			case "", "false", "true", "all":
			default:
				return fmt.Errorf("--archived must be one of: false, true, all")
			}
			q := strings.Join(args, " ")
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				results, err := c.SearchChats(ctx, q, apiclient.SearchChatsOptions{
					ProjectID: projectID, Archived: archived, Limit: limit,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if results == nil {
					results = []apiclient.ChatSearchResult{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), results)
				}
				printChatSearch(cmd.OutOrStdout(), results)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "only this project's chats")
	cmd.Flags().StringVar(&archived, "archived", "",
		"Which chats to search: false (live), true (archived, handed off, closed) or all (default all)")
	cmd.Flags().IntVar(&limit, "limit", 0, "How many chats to list (default 20, at most 100)")
	jsonFlag(cmd)
	return cmd
}

func printChatSearch(w io.Writer, results []apiclient.ChatSearchResult) {
	if len(results) == 0 {
		_, _ = fmt.Fprintln(w, "No matches.")
		return
	}
	for i := range results {
		r := &results[i]
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintf(w, "%-5d %-11s %s\n", r.Chat.ID, r.Chat.State, r.Chat.Title)
		for _, m := range r.Matches {
			where := "title"
			switch m.Field {
			case apiclient.ChatSearchFieldPrompt:
				where = fmt.Sprintf("turn %d asked", m.Seq)
			case apiclient.ChatSearchFieldResult:
				where = fmt.Sprintf("turn %d answer", m.Seq)
			}
			_, _ = fmt.Fprintf(w, "      %-14s %s\n", where, m.Snippet)
		}
		if more := r.MatchCount - len(r.Matches); more > 0 {
			_, _ = fmt.Fprintf(w, "      … %d more\n", more)
		}
	}
}
