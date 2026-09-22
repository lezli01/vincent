package cli

// `vincent chat files` — the files of the directory a chat's next turn would
// start in, read from `GET /v1/chats/{id}/files` (§5.5, §12.1, §13.2, task
// 126.7).
//
// It is a pass-through, the way the route is: the paths are git's own bytes in
// git's own order, and `--mention` prints the daemon's own mention text rather
// than text this command assembled. No client builds a mention — the
// `@"…"`-on-a-space quoting rule has one definition and it lives in the
// adapter (task 126 decision 1) — and nothing here sorts, filters or cases a
// path (task 126 decision 21).

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newChatFilesCmd is a leaf read, so its exit code is `chat skills`': 0
// whenever the daemon answered — an empty listing and a truncated one
// included, because both are answers — 1 on a refusal, 2 with no daemon.
//
// There is no `--refresh` twin. Task 126 decision 5 removed the server cache
// such a flag would invalidate — one `git ls-files` is not an agent-CLI probe
// — and a flag that invalidates nothing is a lie about the route.
func newChatFilesCmd() *cobra.Command {
	var (
		limit   int
		mention bool
	)
	cmd := &cobra.Command{
		Use:   "files <chat-id>",
		Short: "List the files this chat's next turn could be pointed at",
		Long: "List the files of the directory the chat's next turn would start in — its own " +
			"worktree, or its linked task's — and, under --mention, the exact text that " +
			"mentions each one.\n\n" +
			"stdout is one workspace-relative path per line and nothing else, so " +
			"`vincent chat files 12 | wc -l` counts files and the list pipes into xargs. " +
			"--mention switches stdout to the daemon's own mention text, which no client " +
			"rebuilds: the rule for quoting a path with a space in it lives in the agent's " +
			"adapter and nowhere else. --json emits the response object unchanged, with " +
			"`files` always an array.\n\n" +
			"Untracked files are listed, because a file created a minute ago is exactly the " +
			"file someone wants to point an agent at, and .gitignore is honoured. Rows are " +
			"git's own order, unsorted.\n\n" +
			"--limit lowers the daemon's own ceiling and can never raise it; 0 is the ceiling " +
			"itself. A listing that was cut says so on stderr and still exits 0 — a truncated " +
			"answer is an answer. Exit 1 on an unknown or terminal chat, on a linked chat " +
			"whose task no longer has a worktree, and on a workspace that has gone missing " +
			"under the daemon; 2 with no daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				files, err := c.ChatFiles(ctx, id, limit)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				// A daemon too old to send an array must not become a `null`
				// in --json, where a script's `| jq '.files[]'` would fail on
				// a type rather than on an empty list — `chat skills`' guard
				// over `skills` and `problems`, for the same reason.
				if files.Files == nil {
					files.Files = []apiclient.ChatFile{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), files)
				}
				return printChatFiles(cmd.OutOrStdout(), cmd.ErrOrStderr(), files, mention)
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0,
		"How many files to list (default: the daemon's ceiling, which this can only lower)")
	cmd.Flags().BoolVar(&mention, "mention", false,
		"Print the text that mentions each file instead of the path")
	jsonFlag(cmd)
	// Two spellings of stdout cannot both be it, which is how `chat
	// transcript` treats --json and --raw. Nothing is lost by refusing the
	// pair: the response object --json emits already carries every row's
	// `mention`, so `--json | jq -r '.files[].mention'` is the same answer.
	cmd.MarkFlagsMutuallyExclusive("json", "mention")
	return cmd
}

// printChatFiles writes the listing to out and everything else to errOut (task
// 124 decision 63's rule, which `chat skills` states for its own table): a
// truncation warning and an adapter that cannot mention are both commentary on
// the list, and a `vincent chat files 12 | wc -l` that counted either would be
// wrong.
func printChatFiles(out, errOut io.Writer, f *apiclient.ChatFiles, mention bool) error {
	switch {
	case mention && f.MentionSigil == "":
		// An empty sigil is how an adapter says it cannot mention files (task
		// 126 decision 38) — a signal, never a refusal, which is why the
		// paths were served at all and why this still exits 0. A column of
		// empty strings would answer the question with nothing; saying so
		// once answers it.
		if _, err := fmt.Fprintf(errOut,
			"no mention text: %s cannot mention files; the paths are listed without --mention\n",
			f.Agent); err != nil {
			return err
		}
	case mention:
		for _, file := range f.Files {
			if _, err := fmt.Fprintln(out, file.Mention); err != nil {
				return err
			}
		}
	default:
		for _, file := range f.Files {
			if _, err := fmt.Fprintln(out, file.Path); err != nil {
				return err
			}
		}
	}
	if f.Truncated {
		// `truncated` exists to make a cut visible, and a silent cut is the
		// failure mode it was added against (task 126 decision 36). It is not
		// a failure of its own: the exit code stays 0, because a truncated
		// answer is still an answer.
		if _, err := fmt.Fprintf(errOut,
			"warning: the listing was cut at %d file(s); --limit lowers the daemon's ceiling "+
				"and cannot raise it\n", len(f.Files)); err != nil {
			return err
		}
	}
	return nil
}
