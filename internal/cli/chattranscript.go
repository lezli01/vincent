package cli

// `vincent chat transcript` — `vincent task transcript` for a chat turn
// (task 103), so reading what an agent did in a chat is not TUI-only.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
)

// newChatTranscriptCmd prints one turn's transcript. It is task 047's command
// with a turn where that one has a step run, flag for flag, and it prints
// through the same printer (task 103 decision 5): a turn's transcript is the
// same record vocabulary over the same byte-range contract, and a second
// renderer would be a second way for the two to read differently.
func newChatTranscriptCmd() *cobra.Command {
	var (
		turn   int
		follow bool
		raw    bool
	)
	cmd := &cobra.Command{
		Use:   "transcript <chat-id>",
		Short: "Print a chat turn's transcript",
		Long: "Print the complete record of what the agent did in one turn (§17), rendered " +
			"as text. --turn takes the turn number `vincent chat show` prints as " +
			"`--- turn N ---`. Omitted, it selects the running turn if there is one and " +
			"the newest turn otherwise.\n\n" +
			"--json emits the normalized records as NDJSON, in vincent's own vocabulary " +
			"including its `vincent.*` annotations; --raw streams the agent's untouched " +
			"dialect, byte for byte. Everything a human reads goes to stdout. --follow " +
			"keeps printing until the turn ends — a turn waiting on an answer is still " +
			"running — and does not wait for a later send.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				_, turns, err := c.GetChat(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				t, err := selectChatTurn(turns, id, turn)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
					return exitError{code: 1}
				}
				// Decided from the turn row rather than from the endpoint's
				// 404, which says the same thing for a turn that never opened
				// its file and for a file that is gone (task 103 decision 4,
				// after task 047 decision 6). The first is not an error: the
				// fail reason is the whole answer.
				if chatrun.TurnWritesNoTranscript(t.FailReason) {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"turn %d (%s) has no transcript\n", t.Seq, t.FailReason)
					return nil
				}
				p := &transcriptPrinter{
					out:    cmd.OutOrStdout(),
					errOut: cmd.ErrOrStderr(),
					raw:    raw,
					json:   wantJSON(cmd),
				}
				src := chatTurnSource{c: c, chatID: id, seq: t.Seq}
				opts := apiclient.TranscriptOptions{}
				if follow {
					// A follow opens on a tail, as the task command's does.
					opts.Tail = apiclient.DefaultTailBytes
				}
				next, err := p.printFrom(ctx, src, opts)
				if err != nil {
					return transcriptError(p.errOut, err)
				}
				if !follow {
					return nil
				}
				return p.followFrom(ctx, src, next, transcriptPollInterval)
			})
		},
	}
	cmd.Flags().IntVar(&turn, "turn", 0,
		"turn number to print, as `chat show` numbers it (default: the running turn, else the newest)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false,
		"Keep printing as the turn writes, until it is no longer running")
	cmd.Flags().BoolVar(&raw, "raw", false,
		"Emit the agent's own JSONL, byte for byte, instead of rendering it")
	jsonFlag(cmd)
	// As on `task transcript`: --json keeps its CLI-wide meaning (task 047
	// decision 1).
	cmd.MarkFlagsMutuallyExclusive("json", "raw")
	return cmd
}

// selectChatTurn resolves which turn to print (task 103 decision 2).
//
// With no --turn, the running turn wins, because that is the one a person
// asking about a live chat means; otherwise the newest. A chat's turns are
// strictly sequential, so seq is creation order and needs no tiebreak — unlike
// a task's step runs, where parallel lanes make the id the only reliable one.
func selectChatTurn(turns []apiclient.ChatTurn, chatID int64, want int) (*apiclient.ChatTurn, error) {
	if want > 0 {
		for i := range turns {
			if turns[i].Seq == want {
				return &turns[i], nil
			}
		}
		return nil, fmt.Errorf("turn %d not found on chat %d", want, chatID)
	}
	var newest, running *apiclient.ChatTurn
	for i := range turns {
		t := &turns[i]
		if newest == nil || t.Seq > newest.Seq {
			newest = t
		}
		if t.State == "running" && (running == nil || t.Seq > running.Seq) {
			running = t
		}
	}
	switch {
	case running != nil:
		return running, nil
	case newest != nil:
		return newest, nil
	}
	return nil, fmt.Errorf("chat %d has no turns yet", chatID)
}

// chatTurnSource is one turn of a chat, for `vincent chat transcript`.
type chatTurnSource struct {
	c      *apiclient.Client
	chatID int64
	seq    int
}

func (s chatTurnSource) normalized(
	ctx context.Context, opts apiclient.TranscriptOptions,
) ([]apiclient.TranscriptRecord, int64, error) {
	return s.c.ChatTurnTranscript(ctx, s.chatID, s.seq, opts)
}

func (s chatTurnSource) raw(ctx context.Context, opts apiclient.TranscriptOptions) ([]byte, int64, error) {
	return s.c.ChatTurnTranscriptRaw(ctx, s.chatID, s.seq, opts)
}

// state reads the turn row, not the chat's: a chat parked on a question is
// `awaiting_input` while its turn is still `running`, and the follow is about
// the turn (task 103 decision 3).
func (s chatTurnSource) state(ctx context.Context) (bool, string, error) {
	_, turns, err := s.c.GetChat(ctx, s.chatID)
	if err != nil {
		return false, "", err
	}
	for i := range turns {
		if turns[i].Seq == s.seq {
			state := turns[i].State
			return state == "running", fmt.Sprintf("turn %d is %s", s.seq, state), nil
		}
	}
	return false, "", fmt.Errorf("turn %d is no longer on chat %d", s.seq, s.chatID)
}
