package cli

// `vincent chat diff` — a chat's diff and its per-hunk review, over
// GET /v1/chats/{id}/diff and its reject, accept and unaccept routes (§5.5,
// §12.1, §13.2, task 169).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatHunkPrefixLen is how much of a hunk's 64-character hash the listing
// prints: enough to be unique in any real diff, and what `reject` and
// `accept` take back.
const chatHunkPrefixLen = 12

func chatDiffScopeFlag(cmd *cobra.Command, scope *string) {
	cmd.Flags().StringVar(scope, "scope", apiclient.ChatDiffScopeBase,
		"what to diff from: base (the chat's base) or turn (the latest turn's checkpoint)")
}

func checkChatDiffScope(scope string) error {
	if scope != apiclient.ChatDiffScopeBase && scope != apiclient.ChatDiffScopeTurn {
		return fmt.Errorf("--scope must be %s or %s; got %q",
			apiclient.ChatDiffScopeBase, apiclient.ChatDiffScopeTurn, scope)
	}
	return nil
}

func newChatDiffCmd() *cobra.Command {
	var (
		scope string
		stat  bool
	)
	cmd := &cobra.Command{
		Use:   "diff <chat-id>",
		Short: "Show a chat's diff hunk by hunk; reject or accept a hunk",
		Long: "List what the chat's directory holds against where the chat started: each file " +
			"with its added and removed lines, and under it each hunk's hash prefix, its " +
			"accepted mark (`*`) and its @@ header. Untracked files are included; ignored " +
			"ones are not.\n\n" +
			"--scope base (the default) diffs from the merge-base with the chat's base; " +
			"--scope turn from the checkpoint taken before the chat's latest turn. --stat " +
			"prints only the base scope's totals, in one cheap call.\n\n" +
			"A chat with no directory, no base, or — for --scope turn — no checkpoint has no " +
			"diff, and says why. Exit 1 then and on an unknown chat; 2 with no daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseChatID(args[0])
			if err != nil {
				return err
			}
			if err := checkChatDiffScope(scope); err != nil {
				return err
			}
			if stat && cmd.Flags().Changed("scope") && scope != apiclient.ChatDiffScopeBase {
				return errors.New("--stat is always the base scope; drop --scope")
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if stat {
					st, err := c.ChatDiffStat(ctx, id)
					if err != nil {
						return chatDiffFailure(cmd, err)
					}
					if wantJSON(cmd) {
						return emitJSON(cmd.OutOrStdout(), st)
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d files\t+%d\t-%d\n", st.Files, st.Added, st.Removed)
					return nil
				}
				d, err := c.ChatDiff(ctx, id, scope)
				if err != nil {
					return chatDiffFailure(cmd, err)
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), d)
				}
				printChatDiff(cmd, d)
				return nil
			})
		},
	}
	chatDiffScopeFlag(cmd, &scope)
	cmd.Flags().BoolVar(&stat, "stat", false, "print only the base scope's file and line totals")
	jsonFlag(cmd)
	cmd.AddCommand(newChatDiffRejectCmd(), newChatDiffAcceptCmd())
	return cmd
}

func chatDiffFailure(cmd *cobra.Command, err error) error {
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
	return exitError{code: 1}
}

func printChatDiff(cmd *cobra.Command, d *apiclient.ChatDiff) {
	out := cmd.OutOrStdout()
	if len(d.Files) == 0 {
		_, _ = fmt.Fprintln(out, "no changes")
		return
	}
	for _, f := range d.Files {
		name := f.Path
		if f.OldPath != "" {
			name = f.OldPath + " -> " + f.Path
		}
		note := ""
		switch {
		case f.Binary:
			note = "\tbinary"
		case !f.Rejectable:
			note = "\tnot rejectable"
		}
		_, _ = fmt.Fprintf(out, "%s\t%s\t+%d\t-%d%s\n", f.Status, name, f.Added, f.Removed, note)
		for _, h := range f.Hunks {
			mark := " "
			if h.Accepted {
				mark = "*"
			}
			_, _ = fmt.Fprintf(out, "  %s %s\t%s\n", mark, shortChatHunk(h.Hash), h.Header)
		}
	}
	_, _ = fmt.Fprintf(out, "%d files\t+%d\t-%d\n", len(d.Files), d.Added, d.Removed)
}

func shortChatHunk(hash string) string {
	if len(hash) > chatHunkPrefixLen {
		return hash[:chatHunkPrefixLen]
	}
	return hash
}

// resolveChatHunk turns a full hash or a unique prefix of one into the full
// hash of a hunk of path in d. The daemon takes only full hashes; the prefix
// is this command's convenience, resolved against the diff it is about to
// act on.
func resolveChatHunk(d *apiclient.ChatDiff, path, prefix string) (string, error) {
	prefix = strings.ToLower(prefix)
	for _, f := range d.Files {
		if f.Path != path {
			continue
		}
		var found []string
		for _, h := range f.Hunks {
			if h.Hash == prefix {
				return h.Hash, nil
			}
			if strings.HasPrefix(h.Hash, prefix) {
				found = append(found, h.Hash)
			}
		}
		switch len(found) {
		case 1:
			return found[0], nil
		case 0:
			return "", fmt.Errorf("%s has no hunk %q in the chat's current diff", path, prefix)
		default:
			return "", fmt.Errorf("%q matches %d hunks of %s; give more of the hash", prefix, len(found), path)
		}
	}
	return "", fmt.Errorf("%s is not in the chat's current diff", path)
}

// withChatHunk reads the scope's diff, resolves <hunk> in it and hands the
// full hash to act.
func withChatHunk(
	cmd *cobra.Command, args []string, scope string,
	act func(ctx context.Context, c *apiclient.Client, id int64, path, hash string) error,
) error {
	id, err := parseChatID(args[0])
	if err != nil {
		return err
	}
	if err := checkChatDiffScope(scope); err != nil {
		return err
	}
	if args[2] == "" {
		return errors.New("a hunk hash is required")
	}
	// A patch's paths are /-separated on every OS; a path typed with the
	// host's separator means the same file.
	path := strings.ReplaceAll(args[1], `\`, "/")
	return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
		d, err := c.ChatDiff(ctx, id, scope)
		if err != nil {
			return chatDiffFailure(cmd, err)
		}
		hash, err := resolveChatHunk(d, path, args[2])
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
			return exitError{code: 1}
		}
		if err := act(ctx, c, id, path, hash); err != nil {
			return chatDiffFailure(cmd, err)
		}
		return nil
	})
}

// fullChatHunkHash reports whether s is a whole hunk hash — 64 hex digits —
// and returns it in the daemon's lowercase.
func fullChatHunkHash(s string) (string, bool) {
	s = strings.ToLower(s)
	if len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return "", false
	}
	return s, true
}

// unacceptChatHunk removes a mark named by its full hash without reading the
// diff (review F9). The route names no scope and reads no diff on purpose: a
// mark for a hunk that has since changed, or that only the other scope
// lists, is exactly the one a reader may want gone, and resolving the hash
// against the current diff first refused it. A prefix still has to be
// resolved, and goes through withChatHunk.
func unacceptChatHunk(cmd *cobra.Command, args []string, scope, hash string) error {
	id, err := parseChatID(args[0])
	if err != nil {
		return err
	}
	if err := checkChatDiffScope(scope); err != nil {
		return err
	}
	path := strings.ReplaceAll(args[1], `\`, "/")
	return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
		if err := c.UnacceptChatHunk(ctx, id, path, hash); err != nil {
			return chatDiffFailure(cmd, err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unaccepted %s %s\n", path, shortChatHunk(hash))
		return nil
	})
}

func newChatDiffRejectCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "reject <chat-id> <path> <hunk>",
		Short: "Revert one hunk in the chat's working tree and note it in the draft",
		Long: "Reverse-apply one hunk of the chat's diff. Only the working tree changes: the " +
			"index and HEAD are left alone, and nothing is committed. A note naming the hunk " +
			"goes to the top of the chat's draft, so the next message tells the agent not to " +
			"reapply it.\n\n" +
			"<hunk> is the hash `vincent chat diff` lists, or a unique prefix of it. <path> " +
			"is the file's path as listed, /-separated.\n\n" +
			"Refused while the chat has a live turn, on a chat that has ended, for a hunk " +
			"that is no longer in the diff or that git cannot reverse, and for a binary, " +
			"renamed, mode-only or empty file or a nested git repository. Exit 1 on any of those.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withChatHunk(cmd, args, scope,
				func(ctx context.Context, c *apiclient.Client, id int64, path, hash string) error {
					res, err := c.RejectChatHunk(ctx, id, scope, path, hash)
					if err != nil {
						return err
					}
					if wantJSON(cmd) {
						return emitJSON(cmd.OutOrStdout(), res)
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rejected %s %s\n", path, shortChatHunk(hash))
					if res.Warning != "" {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", res.Warning)
					}
					return nil
				})
		},
	}
	chatDiffScopeFlag(cmd, &scope)
	jsonFlag(cmd)
	return cmd
}

func newChatDiffAcceptCmd() *cobra.Command {
	var (
		scope string
		undo  bool
	)
	cmd := &cobra.Command{
		Use:   "accept <chat-id> <path> <hunk>",
		Short: "Mark one hunk of the chat's diff accepted, or take the mark back",
		Long: "Mark a hunk accepted. It is only a mark for whoever reads the diff next: " +
			"nothing in git changes, and a hunk whose content changes afterwards is a new " +
			"hunk without the mark. --undo removes the mark; given the full hash it " +
			"removes the mark of a hunk that is no longer in the diff too.\n\n" +
			"<hunk> is the hash `vincent chat diff` lists, or a unique prefix of it. " +
			"Refused while the chat has a live turn, and for a hunk that is not in the " +
			"scope's current diff.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if hash, ok := fullChatHunkHash(args[2]); undo && ok {
				return unacceptChatHunk(cmd, args, scope, hash)
			}
			return withChatHunk(cmd, args, scope,
				func(ctx context.Context, c *apiclient.Client, id int64, path, hash string) error {
					verb := "accepted"
					if undo {
						verb = "unaccepted"
						if err := c.UnacceptChatHunk(ctx, id, path, hash); err != nil {
							return err
						}
					} else if err := c.AcceptChatHunk(ctx, id, scope, path, hash); err != nil {
						return err
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", verb, path, shortChatHunk(hash))
					return nil
				})
		},
	}
	chatDiffScopeFlag(cmd, &scope)
	cmd.Flags().BoolVar(&undo, "undo", false, "remove the hunk's accepted mark instead")
	return cmd
}
