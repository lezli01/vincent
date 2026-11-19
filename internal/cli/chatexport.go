package cli

// `vincent chat export` — a chat as one Markdown, HTML or JSON document, read
// from `GET /v1/chats/{id}/export` (§5.5, §12.1, §13.2, task 156).
//
// The daemon renders; this command only carries the bytes to stdout or to a
// file, so the CLI, the TUI and curl write the same document.

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/fileswap"
)

// exportFilePerm is an export file's mode: a document the reader means to
// share, written as any editor would write it.
const exportFilePerm = 0o644

func newChatExportCmd() *cobra.Command {
	var (
		format   string
		withDiff bool
		outPath  string
	)
	cmd := &cobra.Command{
		Use:   "export <chat-id>",
		Short: "Export a chat as Markdown, HTML or JSON",
		Long: "Export a chat — its metadata, then every turn's prompt, tool calls, answer and " +
			"usage — as one document. --format md (the default) is Markdown, html a single " +
			"self-contained page with no scripts and nothing loaded from elsewhere, json the " +
			"document model with a top-level \"version\".\n\n" +
			"--with-diff appends the base-to-tip diff of the directory the chat works in " +
			"today: its own worktree, or its linked task's. A chat with no directory left " +
			"still exports, saying why its diff is unavailable.\n\n" +
			"Every chat state exports, archived and handed off included. A turn whose " +
			"transcript is gone renders from its answer, with a note in place of its tool " +
			"calls. Tool output is never included.\n\n" +
			"The document goes to stdout, or with -o to that file, written atomically. " +
			"--json is --format json; combining it with another --format is refused. " +
			"Exit 1 on an unknown chat or a file that cannot be written; 2 with no daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			f, err := exportFormat(format, cmd.Flags().Changed("format"), wantJSON(cmd))
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				exp, err := c.ChatExport(ctx, id, apiclient.ChatExportOptions{
					Format: string(f), WithDiff: withDiff,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if outPath == "" {
					_, err := cmd.OutOrStdout().Write(exp.Body)
					return err
				}
				if err := fileswap.WriteFile(outPath, exp.Body, exportFilePerm); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
					return exitError{code: 1}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&format, "format", "md", "Document format: md, html or json")
	cmd.Flags().BoolVar(&withDiff, "with-diff", false,
		"Append the base-to-tip diff of the directory the chat works in")
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "Write the export to this file instead of stdout")
	jsonFlag(cmd)
	return cmd
}

// errExportJSONFormat refuses --json beside a non-JSON --format: two answers
// to one question, neither of which wins by precedence.
var errExportJSONFormat = errors.New("--json is --format json; it cannot be combined with --format md or html")

// exportFormat resolves --format and the CLI-wide --json, which here is a
// synonym for --format json (task 156).
func exportFormat(format string, formatSet, asJSON bool) (chatexport.Format, error) {
	f, err := chatexport.ParseFormat(format)
	if err != nil {
		return "", err
	}
	if asJSON {
		if formatSet && f != chatexport.FormatJSON {
			return "", errExportJSONFormat
		}
		return chatexport.FormatJSON, nil
	}
	return f, nil
}
