package cli

// `vincent chat rewind` and `vincent chat fork` — going back to the state
// before a turn ran, in place or in a new chat (task 167, §5.5, §13.2).
//
// Both are thin: the daemon decides what is allowed and does all the git
// work, and these commands only say what it did — above all the files it left
// alone because the snapshot could not read them (task 167 decision D), and
// whether the next turn will be seeded rather than resumed (decision 1),
// which is never presented as anything else.

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

// seededNote is the one wording every CLI surface uses for a seeded turn:
// the agent started a fresh session given the earlier turns, and saying
// "resumed" would be false (task 167 decision 1).
const seededNote = "seeded, not resumed"

// rewindModes are the --mode values, in the order the help lists them.
var rewindModes = []string{"both", "code", "conversation"}

func newChatRewindCmd() *cobra.Command {
	var (
		turn    int
		rewound int
		mode    string
	)
	cmd := &cobra.Command{
		Use:   "rewind <chat-id> (--turn N [--mode both|code|conversation] | --rewound K)",
		Short: "Go back to the state before a chat turn ran",
		Long: "Rewinds a chat to the state before turn N ran (the number `vincent chat show` " +
			"prints). --mode code restores the branch, index and worktree to turn N's " +
			"checkpoint; --mode conversation keeps turns 1..N-1 of the conversation and puts " +
			"turn N's prompt back in the chat's draft; --mode both (the default) does both.\n\n" +
			"A code rewind first saves the state it replaces as refs/vincent/<install id>/chats/<id>/rewound-<k>, " +
			"and --rewound K goes forward to that saved state again (code only). Files the " +
			"pre-rewind snapshot could not read are never deleted: untracked ones are left " +
			"where they are and listed, and a tracked one, which the rewind would overwrite, " +
			"refuses it until the file is readable.\n\n" +
			"Nothing runs: a conversation rewind takes effect on the chat's next turn, which " +
			"either resumes the agent's session from that point or, when the agent cannot, " +
			"starts a fresh session seeded with the earlier turns — said as \"" + seededNote + "\".\n\n" +
			"Refused while a turn is live, for a turn without a checkpoint or already rewound, " +
			"on an ended chat, and — code only — when someone else has committed on the branch " +
			"since turn N or a tracked file it would overwrite cannot be read. Exit 1 on a " +
			"refusal; 2 with no daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			req, err := rewindRequest(turn, rewound, mode,
				cmd.Flags().Changed("turn"), cmd.Flags().Changed("rewound"), cmd.Flags().Changed("mode"))
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				res, err := c.RewindChat(ctx, id, req)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", rewindErrorMessage(err, id))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), res)
				}
				printRewindResult(cmd.OutOrStdout(), id, req, res)
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&turn, "turn", 0, "rewind to the state before this turn ran, as `chat show` numbers it")
	cmd.Flags().IntVar(&rewound, "rewound", 0,
		"go forward to the state the k-th code rewind saved (refs/vincent/<install id>/chats/<id>/rewound-<k>)")
	cmd.Flags().StringVar(&mode, "mode", "both", "what to rewind: "+strings.Join(rewindModes, ", "))
	cmd.MarkFlagsMutuallyExclusive("turn", "rewound")
	cmd.MarkFlagsOneRequired("turn", "rewound")
	// A forward rewind restores code only; a --mode beside it would be a
	// question with no second answer.
	cmd.MarkFlagsMutuallyExclusive("rewound", "mode")
	jsonFlag(cmd)
	return cmd
}

// rewindRequest validates the flags into the request body. Local validation
// is a convenience — the daemon checks the same rules — but it lets a typo
// fail before a daemon is even needed.
func rewindRequest(turn, rewound int, mode string, turnSet, rewoundSet, modeSet bool) (apiclient.RewindRequest, error) {
	if rewoundSet {
		if rewound < 1 {
			return apiclient.RewindRequest{}, errors.New("--rewound must be 1 or more")
		}
		return apiclient.RewindRequest{Rewound: rewound}, nil
	}
	if !turnSet || turn < 1 {
		return apiclient.RewindRequest{}, errors.New("--turn must be 1 or more")
	}
	valid := false
	for _, m := range rewindModes {
		valid = valid || m == mode
	}
	if !valid {
		return apiclient.RewindRequest{}, fmt.Errorf("--mode %q: want one of %s", mode, strings.Join(rewindModes, ", "))
	}
	req := apiclient.RewindRequest{Turn: turn}
	// The default is the daemon's to apply; sending it only when asked keeps
	// one definition of what an omitted mode means.
	if modeSet {
		req.Mode = mode
	}
	return req, nil
}

func printRewindResult(w io.Writer, chatID int64, req apiclient.RewindRequest, res *apiclient.RewindResult) {
	if req.Rewound > 0 {
		_, _ = fmt.Fprintf(w, "chat %d went forward to rewound state %d\n", chatID, req.Rewound)
	} else {
		mode := req.Mode
		if mode == "" {
			mode = "both"
		}
		_, _ = fmt.Fprintf(w, "chat %d rewound to before turn %d (%s)\n", chatID, req.Turn, mode)
	}
	if res.RewindID != "" {
		_, _ = fmt.Fprintf(w, "rewind id: %s\n", res.RewindID)
	}
	if res.Ref != "" {
		_, _ = fmt.Fprintf(w, "the replaced state is saved as %s\n", res.Ref)
	}
	if len(res.RewoundTurns) > 0 {
		seqs := make([]string, len(res.RewoundTurns))
		for i, s := range res.RewoundTurns {
			seqs[i] = strconv.Itoa(s)
		}
		_, _ = fmt.Fprintf(w, "rewound turns: %s (hidden; `vincent chat show --rewound %d` shows them)\n",
			strings.Join(seqs, ", "), chatID)
	}
	printSkipped(w, res.Skipped)
	if res.Seeded {
		_, _ = fmt.Fprintf(w, "next turn: %s\n", seededNote)
	}
}

// printSkipped names the files a snapshot could not read (task 167 decision
// D). They were left untouched rather than deleted, so a person has to know
// the worktree is not exactly the checkpoint there.
func printSkipped(w io.Writer, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "left as they were (the snapshot could not read them):")
	for _, p := range skipped {
		_, _ = fmt.Fprintf(w, "  %s\n", p)
	}
}

func newChatForkCmd() *cobra.Command {
	var (
		turn                         int
		title, agentName, model, eff string
		mode                         string
	)
	cmd := &cobra.Command{
		Use:   "fork <chat-id> --turn N [--title T] [--agent A [--model M] [--effort E]]",
		Short: "Start a new chat from the state before a chat turn ran",
		Long: "Creates a new free chat from the state before turn N of a chat ran: its own " +
			"branch, cut from that turn's checkpoint with the uncommitted changes of the " +
			"time left unstaged, and the source's agent, model, effort and permission mode. " +
			"The new chat is idle — no turn starts — and the source chat is untouched.\n\n" +
			"Its first turn resumes the agent's session from that point, or, when the agent " +
			"cannot, starts a fresh session seeded with the earlier turns — said as \"" +
			seededNote + "\". Files the checkpoint could not read are listed.\n\n" +
			"Refused while a turn is live, for a turn without a checkpoint or already rewound, " +
			"and by the restricted-mode checks a new chat is held to. Exit 1 on a refusal; 2 " +
			"with no daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			if turn < 1 {
				return errors.New("--turn must be 1 or more")
			}
			if cmd.Flags().Changed("title") && strings.TrimSpace(title) == "" {
				return errors.New("the title must not be empty")
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				res, err := c.ForkChat(ctx, id, apiclient.ForkRequest{
					Turn: turn, Title: title, Agent: agentName, Model: model, Effort: eff, PermissionMode: mode,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", rewindErrorMessage(err, id))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), res)
				}
				out := cmd.OutOrStdout()
				_, _ = fmt.Fprintf(out, "chat %d  %s  [%s]  %s\n",
					res.Chat.ID, res.Chat.Title, res.Chat.Agent, res.Chat.Branch)
				_, _ = fmt.Fprintf(out, "forked from chat %d, before turn %d\n", id, turn)
				printSkipped(out, res.Skipped)
				if res.Seeded {
					_, _ = fmt.Fprintf(out, "first turn: %s\n", seededNote)
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&turn, "turn", 0, "fork from the state before this turn ran, as `chat show` numbers it")
	cmd.Flags().StringVar(&title, "title", "", "the new chat's title (default: the daemon's)")
	cmd.Flags().StringVar(&agentName, "agent", "", "fork onto this agent instead of the source's (always seeded)")
	cmd.Flags().StringVar(&model, "model", "", "the new agent's model (with --agent; default: its own)")
	cmd.Flags().StringVar(&eff, "effort", "", "the new agent's effort (with --agent; default: its own)")
	cmd.Flags().StringVar(&mode, "permission-mode", "", "full-auto or restricted (default: the source's)")
	_ = cmd.MarkFlagRequired("turn")
	jsonFlag(cmd)
	return cmd
}

// turnZeroHeading heads a seeded chat's pending context wherever a client
// shows it (task 171 decision 4): it is what the next turn receives ahead of
// the message, and it is a summary, never the earlier session itself.
const turnZeroHeading = "── turn 0 · seeded context (summary) ──"

// turnZeroEnd closes turn 0 where the turn's own records follow it.
const turnZeroEnd = "── end of turn 0 ──"

func newChatPreambleCmd() *cobra.Command {
	var (
		file string
		drop bool
	)
	cmd := &cobra.Command{
		Use:   "preamble <chat-id> (--file F | --clear)",
		Short: "Replace or clear the seeded context a chat's next turn starts with",
		Long: "A chat left seeded by a rewind or a fork starts its next turn in a fresh " +
			"session with a summary of the earlier turns — turn 0, which `vincent chat show` " +
			"prints. This replaces that summary with the contents of --file (`-` reads stdin), " +
			"or with --clear drops it, so the next turn starts with no earlier context at all.\n\n" +
			"Clearing is final: the chat is then no longer seeded, so it cannot be edited again. " +
			"To replace the summary with a short note, edit it with --file instead of clearing it.\n\n" +
			"Only while the next turn is seeded and no turn has started: a chat that resumes " +
			"its agent's session natively has no summary to edit. Exit 1 on a refusal; 2 with " +
			"no daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			if (file == "") == !drop {
				return errors.New("give exactly one of --file or --clear")
			}
			text := ""
			if file != "" {
				data, err := readInputFile("--file", file, cmd.InOrStdin())
				if err != nil {
					return err
				}
				if text = string(data); strings.TrimSpace(text) == "" {
					return errors.New("the file is empty; use --clear to drop the seeded context")
				}
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, err := c.PatchChat(ctx, id, apiclient.PatchChatRequest{PendingPreamble: &text})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", preambleErrorMessage(err, id))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), chat)
				}
				if text == "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(),
						"chat %d: seeded context cleared, for good; its next turn starts a fresh session with no earlier context\n", id)
					return nil
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "chat %d: seeded context replaced (%d bytes)\n", id, len(text))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "read the new seeded context from this file (`-` for stdin)")
	cmd.Flags().BoolVar(&drop, "clear", false, "drop the seeded context")
	jsonFlag(cmd)
	return cmd
}

func preambleErrorMessage(err error, chatID int64) string {
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) && apiErr.Code == "chat_preamble_not_pending" {
		return fmt.Sprintf("chat %d's next turn is not seeded — it resumes natively, was never "+
			"rewound or forked, its seeded context was cleared, or a turn has already started — "+
			"so there is no seeded context to edit", chatID)
	}
	return rewindErrorMessage(err, chatID)
}

// rewindErrorMessage turns a rewind or fork refusal into a sentence that says
// what to do next. The codes are the API's 409 reasons (task 167); anything
// else, a 400 or 404 included, keeps the daemon's own message.
func rewindErrorMessage(err error, chatID int64) string {
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	code := apiErr.Code
	if r := apiErr.Details["reason"]; r != "" {
		code = r
	}
	switch code {
	case "chat_turn_live":
		return fmt.Sprintf("chat %d has a live turn; wait for it to end, or stop it with `vincent chat cancel %d`",
			chatID, chatID)
	case "chat_checkpoint_unavailable":
		return "that turn has no checkpoint to go back to — it predates checkpoints, its " +
			"checkpoint failed, or the commit is gone — so it cannot be rewound to or forked from"
	case "chat_rewind_foreign_commits":
		return "someone else has committed on this chat's branch since that turn, and a code " +
			"rewind would discard their work; --mode conversation and `vincent chat fork` are still possible"
	case "chat_turn_rewound":
		return "that turn was already rewound; `vincent chat show --rewound` lists the turns still in play"
	case "chat_terminal":
		return fmt.Sprintf("chat %d has ended (archived, handed off or closed); its turns cannot be rewound", chatID)
	}
	return apiMessage(err)
}

// visibleTurns is the turns a listing shows: every turn with --rewound,
// otherwise only those no rewind left behind (task 167 decision 9). The
// daemon serves them all; hiding is the client's.
func visibleTurns(turns []apiclient.ChatTurn, withRewound bool) (shown []apiclient.ChatTurn, hidden int) {
	if withRewound {
		return turns, 0
	}
	shown = make([]apiclient.ChatTurn, 0, len(turns))
	for i := range turns {
		if turns[i].RewoundAt != nil {
			hidden++
			continue
		}
		shown = append(shown, turns[i])
	}
	return shown, hidden
}

// turnMarks is the parenthesised state of a turn header: its state, then
// whether a rewind left it behind and whether it was seeded.
func turnMarks(t *apiclient.ChatTurn) string {
	marks := []string{t.State}
	if t.RewoundAt != nil {
		m := "rewound"
		if t.RewindID != "" {
			m += " " + t.RewindID
		}
		marks = append(marks, m)
	}
	if t.Seeded {
		marks = append(marks, seededNote)
	}
	return strings.Join(marks, ", ")
}

// printChatRewindState adds to `chat show`'s header what task 167 records on
// the chat itself: where a fork came from, how its next turn will pick up
// after a conversation rewind, and the states a forward rewind can restore.
func printChatRewindState(w io.Writer, chat *apiclient.Chat) {
	if f := chat.ForkedFrom; f != nil {
		line := fmt.Sprintf("forked from chat %d", f.ChatID)
		if f.Agent != "" {
			line += " (" + f.Agent + ")"
		}
		_, _ = fmt.Fprintf(w, "%s, before turn %d\n", line, f.Seq)
	}
	switch chat.ResumePending {
	case "seeded":
		_, _ = fmt.Fprintf(w, "next turn: %s\n", seededNote)
		if chat.PendingPreamble != "" {
			_, _ = fmt.Fprintf(w, "\n%s\n%s\n", turnZeroHeading, strings.TrimRight(chat.PendingPreamble, "\n"))
		}
	case "resumed":
		_, _ = fmt.Fprintln(w, "next turn: resumes the agent's session from the rewind point")
	}
	for _, s := range chat.RewoundStates {
		line := fmt.Sprintf("rewound state %d: %s", s.K, s.Ref)
		if s.Blocked != "" {
			line += " (cannot go forward: " + s.Blocked + ")"
		}
		_, _ = fmt.Fprintln(w, line)
	}
}
