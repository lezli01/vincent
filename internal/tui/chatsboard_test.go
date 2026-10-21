package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The tests in this file hold the chats board's columns (#801 F11): the
// heading above the rows, and the title cell's room on both the live and the
// archived board.

// archivedChatBoard is the archived chats board with one archived chat
// under the cursor.
func archivedChatBoard(title string) *chatsView {
	v := newArchivedChatsView()
	v.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	v.project = projectSel{id: 7, name: "repo"}
	v.applyLoaded(chatsLoadedMsg{chats: []apiclient.Chat{testChat(1, "archived", title)}})
	return v
}

// TestChatsBoardHeadingNamesTheColumns: a dim heading sits above the rows, and
// its "title" starts where every row's title does.
func TestChatsBoardHeadingNamesTheColumns(t *testing.T) {
	for name, v := range map[string]*chatsView{
		"live":     chatsFixture(),
		"archived": archivedChatBoard("first"),
	} {
		t.Run(name, func(t *testing.T) {
			lines := stripLines(v.render(120, 24))
			head := -1
			for i, l := range lines {
				if strings.Contains(l, "last active") {
					head = i
					break
				}
			}
			if head < 0 {
				t.Fatalf("no heading row:\n%s", strings.Join(lines, "\n"))
			}
			before, _, _ := strings.Cut(lines[head], "title")
			want := ansi.StringWidth(before)
			row := lines[head+1]
			i := strings.Index(row, v.rows()[0].chat.Title)
			if i < 0 {
				t.Fatalf("the row under the heading is not the board's first chat: %q", row)
			}
			if got := ansi.StringWidth(row[:i]); got != want {
				t.Errorf("the heading's title is at column %d, the row's at %d\n%s\n%s", want, got, lines[head], row)
			}
		})
	}
}

// TestChatsBoardEmptyHasNoHeading: an empty board says so, with no column
// names over nothing.
func TestChatsBoardEmptyHasNoHeading(t *testing.T) {
	v := chatsFixture()
	v.applyLoaded(chatsLoadedMsg{})
	if out := ansi.Strip(v.render(120, 24)); strings.Contains(out, "last active") {
		t.Fatalf("an empty board drew a heading:\n%s", out)
	}
}

// TestChatsBoardSelectedTitleFillsTheRow: the cursor's room was counted in
// bytes, and "▸ " is four of them in two columns, so a selected row's long
// title stopped two columns short of the edge (AR-8). It reaches the edge on
// both boards.
func TestChatsBoardSelectedTitleFillsTheRow(t *testing.T) {
	long := strings.Repeat("a long chat title ", 10)
	live := chatsFixture()
	live.applyLoaded(chatsLoadedMsg{chats: []apiclient.Chat{testChat(1, "idle", long)}})
	for name, v := range map[string]*chatsView{
		"live":     live,
		"archived": archivedChatBoard(long),
	} {
		t.Run(name, func(t *testing.T) {
			rows := v.rows()
			if len(rows) == 0 {
				t.Fatal("the board has no rows")
			}
			for _, width := range []int{80, 120} {
				line := v.rowLine(rows[0], true, width)
				if got := ansi.StringWidth(line); got != width {
					t.Errorf("at %d columns the selected row is %d wide: %q", width, got, ansi.Strip(line))
				}
			}
		})
	}
}
