package tui

import (
	"sort"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
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
	default: // archived, handed_off — both terminal (task 074)
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

// chatRow is one line of the chats board: a chat, or a group header.
type chatRow struct {
	chat *apiclient.Chat
	// header names a project heading; collapsed marks it folded.
	header    bool
	label     string
	count     int
	collapsed bool
	path      foldPath
}

func (r chatRow) selectable() bool { return !r.header || r.collapsed }

// groupChatRows lays the board out: one heading per project, in the order the
// projects were named, with their chats under it.
//
// The chats board groups by project and by nothing else: `tui.board.group_by`
// also offers workflow scopes, and a chat has no workflow, so offering the
// cycle here would offer a grouping that puts every row under one heading
// called "—" (task 067).
func groupChatRows(chats []apiclient.Chat, names map[int64]string, folds foldSet) []chatRow {
	if len(chats) == 0 {
		return nil
	}
	order := make([]int64, 0, len(chats))
	byProject := map[int64][]apiclient.Chat{}
	for _, c := range chats {
		if _, seen := byProject[c.ProjectID]; !seen {
			order = append(order, c.ProjectID)
		}
		byProject[c.ProjectID] = append(byProject[c.ProjectID], c)
	}
	rows := make([]chatRow, 0, len(chats)+len(order))
	for _, pid := range order {
		group := byProject[pid]
		label := names[pid]
		if label == "" {
			label = "—"
		}
		path := foldPath{label}
		collapsed := folds.has(path)
		rows = append(rows, chatRow{
			header: true, label: label, count: len(group),
			collapsed: collapsed, path: path,
		})
		if collapsed {
			continue
		}
		for i := range group {
			rows = append(rows, chatRow{chat: &group[i], path: path})
		}
	}
	return rows
}

// pruneChatFolds drops fold paths that name no project on the board. An empty
// list prunes nothing, for the reason foldSet.prune gives: a TUI whose daemon
// went away holds no news about which projects exist.
func pruneChatFolds(f foldSet, chats []apiclient.Chat, names map[int64]string) foldSet {
	if len(f) == 0 || len(chats) == 0 {
		return f
	}
	known := map[string]struct{}{}
	for _, c := range chats {
		label := names[c.ProjectID]
		if label == "" {
			label = "—"
		}
		known[label] = struct{}{}
	}
	out := make(foldSet, 0, len(f))
	for _, p := range f {
		if len(p) == 1 {
			if _, ok := known[p[0]]; ok {
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// chatActivity is the "last activity" column: how long ago the chat's row was
// last written, which for a chat is the last turn boundary or state change.
func chatActivity(c apiclient.Chat, now time.Time) string {
	if c.UpdatedAt.IsZero() {
		return "—"
	}
	d := now.Sub(c.UpdatedAt)
	if d < 0 {
		d = 0
	}
	return formatElapsed(d)
}
