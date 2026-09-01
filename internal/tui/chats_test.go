package tui

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatsFixture is a chats board with two chats in one project, loaded: one
// idle, one waiting on a human, which is what every assertion below needs.
func chatsFixture() *chatsView {
	v := newChatsView()
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	v.applyLoaded(chatsLoadedMsg{
		chats: []apiclient.Chat{
			testChat(1, "idle", "first"),
			testChat(2, "awaiting_input", "second"),
		},
		names: map[int64]string{7: "repo"},
	})
	return v
}

func testChat(id int64, state, title string) apiclient.Chat {
	return apiclient.Chat{
		ID: id, ProjectID: 7, Title: title, State: state, Agent: "claude",
		Branch: "vincent/1-x", UpdatedAt: time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC),
	}
}

// TestChatsBoardSortsAttentionFirst holds the chats board's own attention
// rule: a chat waiting on a human is at the top of *this* board (task 067
// decision 4).
func TestChatsBoardSortsAttentionFirst(t *testing.T) {
	v := chatsFixture()
	c, ok := v.current()
	if !ok {
		t.Fatal("the board has no selectable row")
	}
	if c.State != "awaiting_input" {
		t.Fatalf("cursor is on a %s chat, want the one awaiting input", c.State)
	}
	if n := countChatsAwaiting(v.chats); n != 1 {
		t.Fatalf("the header badge counts %d waiting, want 1", n)
	}
}

// TestChatsBoardFiltersOnTitleAgentBranch covers the `/` filter's three
// fields.
func TestChatsBoardFiltersOnTitleAgentBranch(t *testing.T) {
	chats := []apiclient.Chat{testChat(1, "idle", "alpha"), testChat(2, "idle", "beta")}
	for _, q := range []string{"alph", "claude", "vincent/1"} {
		got := filterChats(chats, q)
		if q == "alph" && len(got) != 1 {
			t.Fatalf("filter %q matched %d chats, want 1", q, len(got))
		}
		if q != "alph" && len(got) != 2 {
			t.Fatalf("filter %q matched %d chats, want both", q, len(got))
		}
	}
}

// TestChatsBoardGroupsByProjectOnly holds the grouping decision: a chat has
// no workflow, so the board offers one level and no cycle.
func TestChatsBoardGroupsByProjectOnly(t *testing.T) {
	rows := groupChatRows([]apiclient.Chat{testChat(1, "idle", "a")},
		map[int64]string{7: "repo"}, nil)
	if len(rows) != 2 || !rows[0].header || rows[0].label != "repo" {
		t.Fatalf("rows = %+v, want one project heading and one chat", rows)
	}
	if rows[1].chat == nil {
		t.Fatal("the second row is not a chat")
	}
}

// TestChatsBoardFoldSurvivesReload proves the fold set is the chats board's
// own and that folding hides the group's rows.
func TestChatsBoardFoldSurvivesReload(t *testing.T) {
	v := chatsFixture()
	v.cursor = 0 // the project heading
	v.collapseAtCursor()
	rows := v.rows()
	if len(rows) != 1 || !rows[0].collapsed {
		t.Fatalf("after folding, rows = %d, want one collapsed heading", len(rows))
	}
	v.applyLoaded(chatsLoadedMsg{chats: v.chats, names: v.names})
	if !v.folds.has(foldPath{"repo"}) {
		t.Fatal("the fold did not survive a reload")
	}
}

// TestChatsBoardDropsTaskEvents is decision row 29 in the other direction: a
// task event is not this board's news.
func TestChatsBoardDropsTaskEvents(t *testing.T) {
	v := chatsFixture()
	v.client = nil
	if cmd := v.applyNote(noteMsg{note: apiclient.EventNote{
		Event: apiclient.Event{Type: "task.state_changed"},
	}}); cmd != nil {
		t.Fatal("a task event reloaded the chats board")
	}
}

// TestChatsArchiveOffersForceOnDirtyWorktree covers the 409 the archive takes
// when the worktree has local changes.
func TestChatsArchiveOffersForceOnDirtyWorktree(t *testing.T) {
	v := chatsFixture()
	v.applyArchived(chatArchivedMsg{id: 2, err: &apiclient.Error{
		Code: "conflict", Details: map[string]string{"reason": "worktree_dirty"},
	}})
	if v.confirm == nil || !v.confirm.force {
		t.Fatalf("a dirty worktree did not re-offer the archive with the force: %+v", v.confirm)
	}
}

// TestChatActivityStopsForTerminalChats is issue #298's second defect.
// chatActivity is `now - UpdatedAt` with no upper bound, so an archived or
// handed-off chat's last column ticks up second by second and reads exactly
// like a live one — the task board's equivalent clamps at FinishedAt
// (apiclient.Task.Elapsed). No stored timestamp is missing: a terminal
// transition is the last write a chat row takes, so `updated_at` already *is*
// the moment it ended (internal/store/chats.go:557, task 074 decision 6).
// Only the rendering has to stop, which it does by showing terminal rows when
// they ended rather than how long ago that was.
func TestChatActivityStopsForTerminalChats(t *testing.T) {
	ended := time.Date(2026, 9, 1, 14, 2, 0, 0, time.UTC)
	for _, state := range []string{"archived", "handed_off"} {
		c := apiclient.Chat{ID: 1, State: state, UpdatedAt: ended}
		soon := chatActivity(c, ended.Add(time.Minute))
		later := chatActivity(c, ended.Add(9*time.Hour))
		if soon != later {
			t.Errorf("chatActivity(%s) = %q one minute on and %q nine hours on — a terminal chat's clock keeps running",
				state, soon, later)
		}
	}
	// An idle chat is still "how long ago", which is what the column means
	// for a conversation that can be resumed.
	idle := apiclient.Chat{ID: 2, State: "idle", UpdatedAt: ended}
	if a, b := chatActivity(idle, ended.Add(time.Minute)), chatActivity(idle, ended.Add(9*time.Hour)); a == b {
		t.Errorf("chatActivity(idle) = %q at both one minute and nine hours — the live reading must not be frozen too", a)
	}
}
