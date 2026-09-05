package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/tui"
)

// newChatCmd is `vincent chat` (spec §5.5, §12.1, task 063): the CLI half of
// free chat, so the feature is not TUI-only.
//
// The verbs are the §5.5 actions plus a read: start, send, answer, cancel,
// list, show, archive. `send` blocks until the turn ends and prints the
// answer, which is what a conversation in a terminal has to do — a
// fire-and-forget send would leave the human polling `show`.
//
// `answer` and `cancel` exist because interrupting `send` stops the CLI and
// not the turn: without them a parked or runaway chat could only be reached
// from the TUI or with curl, which is exactly the TUI-only feature this
// command exists to prevent (task 067).
func newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Talk to an agent in its own worktree",
		Long: "Chats are conversations with an agent, each in its own git worktree and " +
			"vincent/{id}-{slug} branch. Every turn resumes the agent's own session, so " +
			"turn N sees turns 1..N-1 (spec §7.3, amended for chats).\n\n" +
			"Chats are not tasks: they never appear on the board, they run no workflow, " +
			"and a chat turn never waits for a scheduler slot.",
	}
	cmd.AddCommand(newChatStartCmd(), newChatSendCmd(), newChatAnswerCmd(),
		newChatCancelCmd(), newChatListCmd(), newChatShowCmd(), newChatArchiveCmd(),
		newChatHandoffCmd())
	return cmd
}

func newChatStartCmd() *cobra.Command {
	var (
		projectID  int64
		agentName  string
		model      string
		effort     string
		baseBranch string
		message    string
	)
	cmd := &cobra.Command{
		Use:   "start <title>",
		Short: "Start a chat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, err := c.CreateChat(ctx, apiclient.CreateChatRequest{
					ProjectID: projectID, Title: args[0], Agent: agentName,
					Model: model, Effort: effort, BaseBranch: baseBranch,
				})
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if message == "" {
					return printChat(cmd, chat)
				}
				if !wantJSON(cmd) {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "chat %d  %s  [%s]  %s\n",
						chat.ID, chat.Title, chat.Agent, chat.Branch)
				}
				return runChatTurn(ctx, cmd, c, chat.ID, message)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "project id (required)")
	cmd.Flags().StringVar(&agentName, "agent", "", "agent adapter; default is one that can resume a session")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&effort, "effort", "", "effort override")
	cmd.Flags().StringVar(&baseBranch, "base", "", "base branch; default is the project's")
	cmd.Flags().StringVar(&message, "message", "", "send this first message straight away")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

func newChatSendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send <chat-id> <message>",
		Short: "Send a message and wait for the answer",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				return runChatTurn(ctx, cmd, c, id, args[1])
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// chatPollInterval is how often `vincent chat send` asks whether the turn has
// ended. It is unchanged by the in-progress indicator: the frame moves ten
// times faster than this, and it moves without asking the daemon anything.
const chatPollInterval = 500 * time.Millisecond

// turnSpinner is the in-progress indicator `vincent chat send` draws while it
// waits for a turn (task 089, issue #330).
//
// The rules are requirements, not decoration. It writes to stderr and never to
// stdout, because stdout carries the agent's answer and is what a script
// consumes. It is off entirely under --json and when stderr is not a terminal,
// so a piped or redirected send emits exactly the bytes it emitted before this
// existed. And the line is erased before anything else is written, on every
// exit path, so no residue can precede the answer or an error.
type turnSpinner struct {
	w     io.Writer
	on    bool
	start time.Time
	frame int
	// width is how many columns the drawn line occupies, and zero when
	// nothing is drawn. It is what erase blanks and what a shrinking clock
	// would otherwise leave behind.
	width int
}

func newTurnSpinner(cmd *cobra.Command, start time.Time) *turnSpinner {
	w := cmd.ErrOrStderr()
	return &turnSpinner{w: w, on: !wantJSON(cmd) && isTTY(w), start: start}
}

// draw repaints the indicator in place.
//
// Erasing with spaces and a carriage return rather than an ANSI erase-to-
// end-of-line is the cross-platform choice: a Windows console without
// ENABLE_VIRTUAL_TERMINAL_PROCESSING prints the escape bytes literally, and
// garbage on the line this exists to keep clean is worse than a few spaces.
// A rune count is the right measure of the line's width here because every
// rune in it — the braille frame, the ellipsis, the digits and letters — is
// single-width.
func (s *turnSpinner) draw(now time.Time) {
	if !s.on {
		return
	}
	line := tui.ProgressLabel(s.frame, now.Sub(s.start))
	s.frame++
	n := utf8.RuneCountInString(line)
	pad := ""
	if s.width > n {
		pad = strings.Repeat(" ", s.width-n)
	}
	_, _ = fmt.Fprint(s.w, "\r"+line+pad)
	s.width = n
}

// erase takes the indicator off the line. It is idempotent, so the deferred
// call after an explicit one writes nothing.
func (s *turnSpinner) erase() {
	if !s.on || s.width == 0 {
		return
	}
	_, _ = fmt.Fprint(s.w, "\r"+strings.Repeat(" ", s.width)+"\r")
	s.width = 0
}

// runChatTurn sends a message and polls the chat until the turn ends, then
// prints the agent's answer or the reason the turn failed.
//
// Polling rather than the SSE stream is deliberate for the CLI: `vincent chat
// send` is one round trip a human waits on, and a poll has no reconnect
// semantics to get wrong. The TUI, which renders the turn as it arrives, uses
// the stream.
//
// The two tickers live in this one goroutine on purpose (task 089 decision).
// A writer goroutine drawing frames while this one prints the answer would
// race on stderr for no gain; a select over three channels costs nothing and
// leaves `-race` nothing to find.
func runChatTurn(ctx context.Context, cmd *cobra.Command, c *apiclient.Client, id int64, message string) error {
	turn, err := c.SendChat(ctx, id, message)
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
		return exitError{code: 1}
	}
	// Elapsed is measured from here rather than from turn.StartedAt: this is
	// how long the human at the terminal has been waiting, and it needs no
	// agreement between two machines' clocks to be true.
	spin := newTurnSpinner(cmd, time.Now())
	defer spin.erase()
	poll := time.NewTicker(chatPollInterval)
	defer poll.Stop()
	// A nil channel blocks forever, so a suppressed indicator arms no ticker
	// at all rather than waking this loop ten times a second to do nothing.
	var frameC <-chan time.Time
	if spin.on {
		frames := time.NewTicker(tui.SpinnerTick)
		defer frames.Stop()
		frameC = frames.C
	}
	for {
		select {
		case <-ctx.Done():
			spin.erase()
			return ctx.Err()
		case now := <-frameC:
			spin.draw(now)
			continue
		case <-poll.C:
		}
		_, turns, err := c.GetChat(ctx, id)
		if err != nil {
			spin.erase()
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
			return exitError{code: 1}
		}
		for i := range turns {
			t := &turns[i]
			if t.ID != turn.ID || t.State == "running" {
				continue
			}
			spin.erase()
			if wantJSON(cmd) {
				if err := emitJSON(cmd.OutOrStdout(), t); err != nil {
					return err
				}
				if t.State != "done" {
					return exitError{code: 1}
				}
				return nil
			}
			if t.State != "done" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "turn %d %s: %s %s\n",
					t.Seq, t.State, t.FailReason, t.ErrorMessage)
				return exitError{code: 1}
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(t.ResultText, "\n"))
			return nil
		}
	}
}

func newChatListCmd() *cobra.Command {
	var projectID int64
	var archived bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List chats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				// Terminal chats are hidden by default, the way archived
				// tasks are (§13.2): `archived` and `handed_off` alike, since
				// both are done with (§5.5, task 074 decision 5).
				scope := apiclient.ArchivedExclude
				if archived {
					scope = apiclient.ArchivedAll
				}
				chats, err := c.ListChats(ctx, projectID, scope)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					// An initialized slice, never nil: a script doing
					// `| jq '.[]'` should see an empty list, not a type error.
					if chats == nil {
						chats = []apiclient.Chat{}
					}
					return emitJSON(cmd.OutOrStdout(), chats)
				}
				if len(chats) == 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No chats.")
					return nil
				}
				for i := range chats {
					ch := &chats[i]
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-5d %-14s %-10s %s\n",
						ch.ID, ch.State, ch.Agent, ch.Title)
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "only this project's chats")
	cmd.Flags().BoolVar(&archived, "archived", false,
		"Include archived and handed-off chats")
	jsonFlag(cmd)
	return cmd
}

func newChatShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <chat-id>",
		Short: "Show a chat and its turns",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, turns, err := c.GetChat(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				out := cmd.OutOrStdout()
				if wantJSON(cmd) {
					if turns == nil {
						turns = []apiclient.ChatTurn{}
					}
					return emitJSON(out, struct {
						Chat  *apiclient.Chat      `json:"chat"`
						Turns []apiclient.ChatTurn `json:"turns"`
					}{chat, turns})
				}
				_, _ = fmt.Fprintf(out, "chat %d  %s\nstate: %s  agent: %s  branch: %s\n",
					chat.ID, chat.Title, chat.State, chat.Agent, chat.Branch)
				// The pending request is printed before the turns because it
				// is the thing that needs doing: `chat answer` numbers its
				// questions off exactly this list.
				if req, ok, err := chat.PendingRequest(); err != nil {
					return err
				} else if ok {
					if err := printInputRequest(out, req,
						fmt.Sprintf("vincent chat answer %d", chat.ID)); err != nil {
						return err
					}
				}
				for i := range turns {
					t := &turns[i]
					_, _ = fmt.Fprintf(out, "\n--- turn %d (%s) ---\n> %s\n", t.Seq, t.State, t.Prompt)
					if t.ResultText != "" {
						_, _ = fmt.Fprintln(out, strings.TrimRight(t.ResultText, "\n"))
					}
					if t.FailReason != "" {
						_, _ = fmt.Fprintf(out, "failed: %s %s\n", t.FailReason, t.ErrorMessage)
					}
				}
				return nil
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newChatArchiveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "archive <chat-id>",
		Short: "Archive a chat, removing its worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				chat, err := c.ArchiveChat(ctx, id, force)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), chat)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "chat %d archived\n", chat.ID)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "archive even if the worktree has local changes")
	jsonFlag(cmd)
	return cmd
}

// printChat renders one chat, as a line or as JSON.
func printChat(cmd *cobra.Command, chat *apiclient.Chat) error {
	if wantJSON(cmd) {
		return emitJSON(cmd.OutOrStdout(), chat)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "chat %d  %s  [%s]  %s\n",
		chat.ID, chat.Title, chat.Agent, chat.Branch)
	return nil
}

// newChatAnswerCmd is `vincent chat answer`: the §7.4 answer, reached from a
// terminal.
//
// It is `task answer` for a chat, flag for flag, because the request is the
// same request — internal/chatrun stores what the adapter emitted, unaltered
// — and a second vocabulary for answering one would be two ways to say the
// same thing. It is not optional surface: a chat parked on a question is a
// chat holding a `max_parallel_chats` slot, and until now nothing but the API
// could release it.
func newChatAnswerCmd() *cobra.Command {
	var (
		answers []string
		allow   bool
		deny    bool
	)
	cmd := &cobra.Command{
		Use:   "answer <chat-id>",
		Short: "Answer the input request a chat is waiting on",
		Long: "Answers the §7.4 request an awaiting_input chat is parked on; the turn\n" +
			"resumes in place. Questions are answered by the number 'vincent chat show'\n" +
			"prints them under — repeat --answer for one index to give a multi-select\n" +
			"several values — and a permission request takes --allow or --deny.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				// The request has to be read before it can be answered: the
				// wire format is keyed by question *text* (§13.2), and the
				// index exists only so nobody retypes quoted prose.
				chat, _, err := c.GetChat(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				req, ok, err := chat.PendingRequest()
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("chat %d is %s and is not waiting for input", id, chat.State)
				}
				resp, err := answerResponse(req, answers, allow, deny)
				if err != nil {
					return err
				}
				// Local validation is a convenience, never the authority: the
				// daemon checks the same rules and its answer is the one that
				// counts.
				if err := req.Validate(resp); err != nil {
					return err
				}
				if err := c.AnswerChat(ctx, id, resp); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				updated, _, err := c.GetChat(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return printChat(cmd, updated)
			})
		},
	}
	cmd.Flags().StringArrayVar(&answers, "answer", nil,
		"Answer as <n>=<value>, n being the question number 'vincent chat show' prints; repeat for more")
	cmd.Flags().BoolVar(&allow, "allow", false, "Allow a permission request")
	cmd.Flags().BoolVar(&deny, "deny", false, "Deny a permission request")
	cmd.MarkFlagsMutuallyExclusive("answer", "allow", "deny")
	cmd.MarkFlagsOneRequired("answer", "allow", "deny")
	jsonFlag(cmd)
	return cmd
}

// newChatCancelCmd is `vincent chat cancel`: stop the live turn and kill its
// process tree, returning the chat to idle and releasing its slot.
//
// Interrupting `vincent chat send` stops the CLI and not the turn — the turn
// belongs to the daemon, which is the whole point of the daemon — so this is
// the command that actually stops one.
func newChatCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <chat-id>",
		Short: "Stop a chat's live turn",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if err := c.CancelChat(ctx, id); err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				chat, _, err := c.GetChat(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return printChat(cmd, chat)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}
