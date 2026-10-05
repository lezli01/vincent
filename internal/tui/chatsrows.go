package tui

import (
	"sort"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// Row shaping for the chats board (§15, task 067). It is boardrows.go's job
// for a different entity, and it is separate code rather than a generic over
// both because a chat is not a task: it has no workflow, no step, no cost
// rollup and no §6 action set, and the four functions that would survive the
// abstraction are the four trivial ones.

// chatNeedsAttention reports whether a chat is waiting on a human. This is the
// chats board's own notion and stops here: `!` and the home board's
// needs-attention header stay task-only, so decision row 29's "a chat never
// appears on the task board" stays literal (task 067 decision 4).
func chatNeedsAttention(state string) bool { return state == "awaiting_input" }

// chatBand orders states on the board: what needs a human first, then what is
// working, then what is idle, then what is done with.
func chatBand(state string) int {
	switch state {
	case "awaiting_input":
		return 0
	case "running":
		return 1
	case "idle":
		return 2
	default: // archived, handed_off, closed — all terminal (tasks 074, 119)
		return 3
	}
}

// sortChats orders the board: attention, then running, then idle, then
// archived; within a band the most recently touched first, so the
// conversation you were just in is at the top. Ties break on id so the order
// is total and a re-render never reshuffles equal rows.
func sortChats(chats []apiclient.Chat) {
	sort.SliceStable(chats, func(i, j int) bool {
		a, b := chats[i], chats[j]
		if ba, bb := chatBand(a.State), chatBand(b.State); ba != bb {
			return ba < bb
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID > b.ID
	})
}

// filterChats narrows on a case-insensitive substring of the title, the agent
// or the branch — the three things a human remembers a conversation by.
func filterChats(chats []apiclient.Chat, query string) []apiclient.Chat {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return chats
	}
	out := make([]apiclient.Chat, 0, len(chats))
	for _, c := range chats {
		if strings.Contains(strings.ToLower(c.Title), q) ||
			strings.Contains(strings.ToLower(c.Agent), q) ||
			strings.Contains(strings.ToLower(c.Branch), q) {
			out = append(out, c)
		}
	}
	return out
}

// countChatsAwaiting is the badge on the chats board's header.
func countChatsAwaiting(chats []apiclient.Chat) int {
	n := 0
	for _, c := range chats {
		if chatNeedsAttention(c.State) {
			n++
		}
	}
	return n
}

// chatRow is one line of the chats board: always a chat. The board is flat
// (task 132.10): it lists the selected project's chats only, so a project
// heading would be one heading over every row, and with it went the fold set
// the headings carried — superseding task 067 decision 6's grouping.
type chatRow struct {
	chat *apiclient.Chat
}

// chatRows lays the board out: one row per chat, in the order sortChats left
// them.
func chatRows(chats []apiclient.Chat) []chatRow {
	if len(chats) == 0 {
		return nil
	}
	rows := make([]chatRow, len(chats))
	for i := range chats {
		rows[i] = chatRow{chat: &chats[i]}
	}
	return rows
}

// chatActivity is the "last activity" column: how long ago the chat's row was
// last written, which for a chat is the last turn boundary or state change.
//
// For a terminal chat it is when instead of how long ago (issue #298). A
// duration that keeps growing reads as a running conversation, which is
// exactly what an `archived` or `handed_off` chat is not; the task board's
// equivalent stops for the same reason, by clamping at FinishedAt
// (apiclient.Task.Elapsed). No stored timestamp is missing here: a terminal
// transition is the last write a chat row takes, so UpdatedAt already *is* the
// moment it ended (internal/store/chats.go, task 074 decision 6) — only the
// rendering had to stop.
func chatActivity(c apiclient.Chat, now time.Time) string {
	if c.UpdatedAt.IsZero() {
		return "—"
	}
	if chatstate.Terminal(chatstate.State(c.State)) {
		// Date and time, never "today"/"yesterday": the cell must not depend
		// on now at all, or it would tick over a day boundary — a slower
		// clock, but still a clock.
		return c.UpdatedAt.Local().Format("01-02 15:04")
	}
	d := now.Sub(c.UpdatedAt)
	if d < 0 {
		d = 0
	}
	return formatElapsed(d)
}
