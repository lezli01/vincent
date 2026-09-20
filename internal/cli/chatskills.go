package cli

// `vincent chat skills` — the skills a chat's agent CLI would load in the
// chat's directory, read from `GET /v1/chats/{id}/skills` (§5.5, §12.1, §13.2,
// task 124.11).
//
// It is a pass-through, the way the route is: every cell is the CLI's own
// word, the INVOKE column is the adapter's own invocation string, and nothing
// here validates a name, normalizes a scope or builds an invocation of its own
// (task 124 decisions 9, 17 and 18).

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Skill positions as the wire spells them (§9.6, task 124 decision 18). A
// position this build has never heard of drops the clause rather than
// guessing: a wrong sentence about where an invocation works is worse than no
// sentence.
const (
	skillPositionLeading  = "leading"
	skillPositionAnywhere = "anywhere"
)

// newChatSkillsCmd is a leaf read, so its exit code is `vincent agents`':
// 0 whenever the daemon answered, whatever the verdicts say (task 041
// decision 4, task 006 decision 7). An adapter that cannot list is the normal
// state of a healthy machine, and an exit code that fires on the normal state
// is no use in a script.
func newChatSkillsCmd() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "skills <chat-id>",
		Short: "List the skills this chat's agent CLI would load",
		Long: "List the skills the chat's agent CLI would load in the chat's directory — " +
			"its own worktree, or its linked task's — and how a message invokes one.\n\n" +
			"This is not `vincent skills`, which reports the skills vincent publishes for " +
			"you to install into your agents. These are the agent's own, discovered by " +
			"asking its CLI.\n\n" +
			"INVOKE is the exact text to put in a message, built by the daemon's adapter: " +
			"paste it into `vincent chat send`. Two skills may share a name, which is why " +
			"the invocation is a column rather than something a reader assembles.\n\n" +
			"The answer comes from the daemon's per-directory cache; --refresh asks the " +
			"CLI again first. An adapter that cannot list, or a probe that failed, is " +
			"said so on stderr and still exits 0 — as does a chat whose agent can invoke " +
			"skills but not list them. Exit 1 on an unknown or terminal chat, 2 with no " +
			"daemon.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				skills, err := c.ChatSkills(ctx, id, refresh)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				// A daemon too old to send an array must not become a `null`
				// in --json, where a script's `| jq '.skills[]'` would fail
				// on a type rather than on an empty list.
				if skills.Skills == nil {
					skills.Skills = []apiclient.ChatSkill{}
				}
				if skills.Problems == nil {
					skills.Problems = []apiclient.ChatSkillProblem{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), skills)
				}
				return printChatSkills(cmd.OutOrStdout(), cmd.ErrOrStderr(), id, skills)
			})
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false,
		"Ask the agent CLI again instead of answering from the daemon's cache")
	jsonFlag(cmd)
	return cmd
}

var chatSkillsHeader = []string{"SKILL", "INVOKE", "ARGS", "DESCRIPTION"}

// printChatSkills writes the table to out and everything else to errOut (task
// 124 decision 63): a verdict, a probe error, the invocation line and the
// problems are all commentary on the list, and a `vincent chat skills 12 |
// wc -l` that counted them would be wrong. `chat handoff`'s warnings and
// `chat transcript`'s "no transcript" notice already sit on stderr at exit 0.
func printChatSkills(out, errOut io.Writer, id int64, s *apiclient.ChatSkills) error {
	// The table prints only under a real list. Under any other verdict there
	// is nothing to tabulate, and a lone header would read as "none".
	if s.ListVerdict == apiclient.InputVerdictSupported {
		rows := make([][]string, 0, len(s.Skills))
		for _, sk := range s.Skills {
			// Verbatim, and blank where the CLI said nothing (task 124
			// decision 8): a "-" here would be vincent's word in a table
			// whose whole claim is that none of it is.
			rows = append(rows, []string{sk.Name, sk.Invocation, sk.ArgumentHint, sk.Description})
		}
		if err := table(out, chatSkillsHeader, rows); err != nil {
			return err
		}
	}
	for _, line := range chatSkillsNotes(id, s) {
		if _, err := fmt.Fprintln(errOut, line); err != nil {
			return err
		}
	}
	return nil
}

// chatSkillsNotes is every stderr line, in the order they are printed: why
// there is no list, how to invoke one, and what the CLI could not load.
func chatSkillsNotes(id int64, s *apiclient.ChatSkills) []string {
	var notes []string
	switch s.ListVerdict {
	case apiclient.InputVerdictUnsupported:
		notes = append(notes, "no skill list: "+chatSkillsReason(s, s.Agent+" does not report the skills it loads"))
	case apiclient.InputVerdictUnknown:
		notes = append(notes, "skill list unknown: "+chatSkillsReason(s, "the daemon could not say why"))
	case apiclient.InputVerdictSupported:
		// A probe error beside a real list means the list is an earlier one
		// the cache kept (T4.22). Saying so is the difference between a list
		// and a list someone can trust.
		if s.ProbeError != nil {
			notes = append(notes, "warning: this list is an earlier one; the latest probe failed: "+*s.ProbeError)
		}
	}
	// Printed from the sigil and the position, never from a row, so it
	// survives an empty list and an adapter that invokes without listing —
	// cursor's shape (task 124 decisions 2 and 61).
	if s.InvokeVerdict == apiclient.InputVerdictSupported && s.InvokeSigil != "" {
		notes = append(notes, chatSkillsInvokeLines(id, s.InvokeSigil, s.InvokePosition)...)
	}
	for _, p := range s.Problems {
		notes = append(notes, "warning: "+p.Path+": "+p.Message)
	}
	return notes
}

// chatSkillsReason is the daemon's own words for why there is no list, or
// fallback when a daemon sent none.
func chatSkillsReason(s *apiclient.ChatSkills, fallback string) string {
	switch {
	case s.UnavailableReason != "":
		return s.UnavailableReason
	case s.ProbeError != nil && *s.ProbeError != "":
		return *s.ProbeError
	default:
		return fallback
	}
}

// chatSkillsInvokeLines is the example and its quoting note.
//
// The note keys off the **sigil**, not the agent's name (task 124 decision
// 61): the hazard belongs to the character a shell sees, and an adapter name
// hard-coded here goes stale the day an adapter changes its syntax — which
// the response already describes structurally.
func chatSkillsInvokeLines(id int64, sigil, position string) []string {
	example := fmt.Sprintf("invoke: vincent chat send %d '%sNAME your message'", id, sigil)
	switch position {
	case skillPositionLeading:
		example += " — the invocation must start the message"
	case skillPositionAnywhere:
		example += " — the invocation is recognized anywhere in the message"
	}
	lines := []string{example}
	switch sigil {
	case "$":
		lines = append(lines, "  keep the single quotes: bash, zsh and pwsh expand "+
			"$NAME inside double quotes, and vincent sends what the shell left")
	case "/":
		lines = append(lines, "  keep the single quotes: Git Bash rewrites a leading "+
			"/NAME into a Windows path — set MSYS_NO_PATHCONV=1, or use `chat send --message-file`")
	}
	return lines
}
