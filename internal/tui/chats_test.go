package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatsFixture is a chats board scoped to project 7 with two chats, loaded: one
// idle, one waiting on a human, which is what every assertion below needs.
func chatsFixture() *chatsView {
	v := newChatsView()
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	v.project = projectSel{id: 7, name: "repo"}
	v.applyLoaded(chatsLoadedMsg{
		chats: []apiclient.Chat{
			testChat(1, "idle", "first"),
			testChat(2, "awaiting_input", "second"),
		},
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

// TestChatsBoardIsFlat holds task 132.10: whatever projects the chats name,
// every row is a chat — no heading, no fold — in sortChats' order.
func TestChatsBoardIsFlat(t *testing.T) {
	other := testChat(3, "running", "elsewhere")
	other.ProjectID = 8
	chats := []apiclient.Chat{testChat(1, "idle", "a"), other, testChat(2, "awaiting_input", "b")}
	sortChats(chats)
	rows := chatRows(chats)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want one per chat and nothing else", len(rows))
	}
	for i, want := range []int64{2, 3, 1} {
		if rows[i].chat == nil || rows[i].chat.ID != want {
			t.Fatalf("row %d = %+v, want chat #%d", i, rows[i].chat, want)
		}
	}
}

// TestChatsBoardListsTheSelectedProject holds both modes to the server-side
// filter (task 132.10): the request carries the selected project's id.
func TestChatsBoardListsTheSelectedProject(t *testing.T) {
	for _, v := range []*chatsView{newChatsView(), newArchivedChatsView()} {
		v.project = projectSel{id: 9, name: "web"}
		if got := v.listOptions().ProjectID; got != 9 {
			t.Errorf("archived=%v: listOptions().ProjectID = %d, want 9", v.archived, got)
		}
	}
}

// TestChatsBoardFetchesNothingWithoutAProject: id 0 must never become an
// unfiltered, every-project listing — the board fetches nothing and says so.
func TestChatsBoardFetchesNothingWithoutAProject(t *testing.T) {
	for _, v := range []*chatsView{newChatsView(), newArchivedChatsView()} {
		v.client = apiclient.New("http://127.0.0.1:1", "t")
		if cmd := v.loadCmd(); cmd != nil {
			t.Fatalf("archived=%v: a board with no project selected issued a fetch", v.archived)
		}
		if out := v.render(80, 10); !strings.Contains(out, "no project selected") {
			t.Errorf("archived=%v: the board does not say no project is selected:\n%s", v.archived, out)
		}
	}
}

// TestChatsBoardSwitchClearsAndReloads is the switch feedback: the old
// project's rows go at once, the header names the project being loaded, the
// archived page returns to the first, and a late answer stamped for the old
// project is dropped.
func TestChatsBoardSwitchClearsAndReloads(t *testing.T) {
	v := newArchivedChatsView()
	v.now = func() time.Time { return testNow }
	v.client = apiclient.New("http://127.0.0.1:1", "t")
	v.setProject(projectSel{id: 7, name: "repo"})
	old := v.stamps.next(7)
	v.applyLoaded(chatsLoadedMsg{archived: true, stamp: old, chats: []apiclient.Chat{testChat(1, "archived", "old")}})
	v.page = 2
	v.filter.SetValue("ol")
	v.window = 1

	if cmd := v.setProject(projectSel{id: 8, name: "web"}); cmd == nil {
		t.Fatal("a switch issued no reload")
	}
	if len(v.chats) != 0 || len(v.rows()) != 0 {
		t.Fatalf("the old project's rows survived the switch: %+v", v.chats)
	}
	if v.page != 0 {
		t.Errorf("page = %d after a switch, want 0", v.page)
	}
	if v.filter.Value() != "ol" || v.window != 1 {
		t.Errorf("filter %q / window %d did not survive the switch", v.filter.Value(), v.window)
	}
	if out := v.render(80, 10); !strings.Contains(out, "loading web…") {
		t.Errorf("the header does not name the project being loaded:\n%s", out)
	}
	v.applyLoaded(chatsLoadedMsg{archived: true, stamp: old, chats: []apiclient.Chat{testChat(1, "archived", "old")}})
	if len(v.chats) != 0 {
		t.Fatal("a late answer for the previous project was applied")
	}
}

// TestChatsBoardNSeedsTheSelectedProject: the form opens on the selected
// project even with nothing under the cursor (task 132.10).
func TestChatsBoardNSeedsTheSelectedProject(t *testing.T) {
	v := newChatsView()
	v.project = projectSel{id: 7, name: "repo"}
	v.applyLoaded(chatsLoadedMsg{})
	v.updateKey(registryKey(t, "n"))
	if v.create == nil {
		t.Fatal("n did not open the new-chat form")
	}
	if v.create.projectID != 7 {
		t.Fatalf("the form opened on project %d, want the selected 7", v.create.projectID)
	}
}

// TestTUIStateWithRetiredChatFoldsReads: a tui.json written before the board
// went flat still reads, and the fields that are still read survive.
func TestTUIStateWithRetiredChatFoldsReads(t *testing.T) {
	dir := t.TempDir()
	raw := `{"full_auto_notice_ack": true, "board_folds": [["web"]], "chat_folds": [["web"]], "selected_project": {"id": 4, "name": "web"}}`
	if err := os.WriteFile(statePath(dir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readTUIState(dir)
	if !st.FullAutoNoticeAck || len(st.BoardFolds) != 1 ||
		st.SelectedProject == nil || st.SelectedProject.ID != 4 {
		t.Fatalf("an old tui.json lost fields: %+v", st)
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

// With no selection the board says "no project" and `n` refuses only once a
// listing came back empty; before that the selection is still resolving
// (review F9).
func TestChatsNoSelectionWaitsForAnEmptyListing(t *testing.T) {
	v := newChatsView()
	v.client = &apiclient.Client{}
	if out := strings.Join(firstOf(v.bodyLines(80)), "\n"); strings.Contains(out, "No project selected") {
		t.Errorf("before any listing the board claims no project: %q", out)
	}
	v.update(registryKey(t, "n"))
	if v.create != nil || strings.Contains(v.note, "register a project") {
		t.Errorf("before any listing n: create %v, note %q; want a resolving note", v.create != nil, v.note)
	}
	v.setProjects([]apiclient.Project{})
	if out := strings.Join(firstOf(v.bodyLines(80)), "\n"); !strings.Contains(out, "No project selected. The project overview adds one.") {
		t.Errorf("an empty listing does not say so: %q", out)
	}
	v.update(registryKey(t, "n"))
	if !strings.Contains(v.note, "the project overview adds one") {
		t.Errorf("n after an empty listing: note %q", v.note)
	}
}

func firstOf(lines []string, _ int) []string { return lines }
