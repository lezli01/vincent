package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Issue #804's archived-board criteria beyond the three in issue804_test.go:
// the task board's header and columns (AR-9, AR-10) and the two boards' key
// parity (AR-11).

// The archived task board's header names its window and page, never the live
// slot tally (AR-9).
func TestArchivedBoardHeaderNamesTheWindowNotTheSlots(t *testing.T) {
	b := testArchivedBoard()
	head := ansi.Strip(b.headerLine())
	for _, want := range []string{"archived tasks", "last 7 days", "3 tasks"} {
		if !strings.Contains(head, want) {
			t.Errorf("header %q does not name %q", head, want)
		}
	}
	if strings.Contains(head, "running") {
		t.Errorf("header %q still carries the slot tally", head)
	}
	b.page = 1
	if head := ansi.Strip(b.headerLine()); !strings.Contains(head, "page 2") {
		t.Errorf("header %q does not name the page", head)
	}
	// The window moved to the header, so it is not said twice above the rows.
	if lines := b.statusLines(); len(lines) != 0 {
		t.Errorf("status lines %q repeat the header", lines)
	}
}

// The archive has its own columns: no STEP or ELAPSED, an ARCHIVED date, and
// a TITLE wider than the live board's at the same width (AR-10).
func TestArchivedBoardHasItsOwnColumns(t *testing.T) {
	at := testNow.Add(-48 * time.Hour)
	b := testArchivedBoard()
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(1, stateArchived, func(t *apiclient.Task) { t.ArchivedAt = &at }),
	}})
	b.width = 100
	cols, set := b.columns()
	titles := make([]string, 0, len(cols))
	for _, c := range cols {
		titles = append(titles, c.Title)
	}
	got := strings.Join(titles, " ")
	for _, gone := range []string{"STEP", "ELAPSED", "STATUS"} {
		if columnWidth(cols, gone) != 0 {
			t.Errorf("the archive still has a %s column: %s", gone, got)
		}
	}
	if columnWidth(cols, "ARCHIVED") == 0 {
		t.Fatalf("the archive has no ARCHIVED column: %s", got)
	}
	live, _ := boardColumns(100, nil, false, boardContent{})
	if a, l := columnWidth(cols, "TITLE"), columnWidth(live, "TITLE"); a <= l {
		t.Errorf("archived TITLE is %d wide, the live board's %d", a, l)
	}
	total := 0
	for _, c := range cols {
		total += c.Width + colPadding
	}
	if total != 100 {
		t.Errorf("the archive's columns span %d cells, want the board's 100", total)
	}
	rows := b.rowsFor(b.rows(), cols, set)
	want := at.Local().Format("01-02 15:04")
	if !strings.Contains(strings.Join(rows[0], "|"), want) {
		t.Errorf("row %q does not carry the archived date %q", rows[0], want)
	}
	if len(rows[0]) != len(cols) {
		t.Errorf("row has %d cells for %d columns", len(rows[0]), len(cols))
	}
}

// Grouped, a header row on the archive still has one cell per column (AR-10).
func TestArchivedBoardGroupHeaderFitsItsColumns(t *testing.T) {
	b := testArchivedBoard()
	b.group = grouping{groupWorkflow}
	b.width = 100
	cols, set := b.columns()
	for _, r := range b.rowsFor(b.rows(), cols, set) {
		if len(r) != len(cols) {
			t.Fatalf("row %q has %d cells for %d columns", r, len(r), len(cols))
		}
	}
}

// esc leaves both archived boards for the home board once there is no filter
// to clear (AR-11).
func TestArchivedBoardsEscLeavesAfterTheFilter(t *testing.T) {
	b := testArchivedBoard()
	b.filter.SetValue("task")
	if _, cmd := b.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil || b.filter.Value() != "" {
		t.Fatalf("the first esc did not just clear the filter (%q)", b.filter.Value())
	}
	// Then the selection (review F11), the live board's order.
	b.marks = b.marks.add(1)
	if _, cmd := b.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil || b.hasMarks() {
		t.Fatalf("esc with a row marked did not just clear the selection (marks %v, cmd %v)", b.marks, cmd != nil)
	}
	_, cmd := b.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if msg := msgOf(cmd); msg != (backToTabMsg{}) {
		t.Errorf("esc on the archived task board sent %#v, want back to the tab", msg)
	}

	v := archivedChats804()
	_, cmd = v.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if msg := msgOf(cmd); msg != (backToTabMsg{}) {
		t.Errorf("esc on the archived chats board sent %#v, want back to the tab", msg)
	}

	// The live board stays the bottom of the esc stack.
	live := newBoard()
	if _, cmd := live.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		t.Error("esc on the live board did something")
	}
}

// msgOf is the message a command produces, or nil.
func msgOf(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// pageKeys is the `<`/`>` row a surface offers, as key and hint, or "".
func pageKeys(rows []binding, ctx bindingContext) string {
	for _, b := range rows {
		if b.context == ctx && (b.key == ">" || b.key == "<") {
			return b.key + " " + b.hint
		}
	}
	return ""
}

// Both archived boards offer `<`/`>` only where a page lies that way (AR-11).
func TestArchivedBoardsOfferOnlyThePagesThere(t *testing.T) {
	full := make([]apiclient.Task, archivedPageSize)
	for i := range full {
		full[i] = task(int64(i+1), stateArchived)
	}
	fullChats := make([]apiclient.Chat, archivedPageSize)
	for i := range fullChats {
		fullChats[i] = testChat(int64(i+1), "archived", fmt.Sprintf("chat %d", i+1))
	}
	cases := []struct {
		name string
		page int
		full bool
		want string
	}{
		{"first short page", 0, false, ""},
		{"first full page", 0, true, "> > next page"},
		{"later short page", 1, false, "< < previous page"},
		{"later full page", 1, true, "> </> page"},
	}
	for _, tc := range cases {
		b := testArchivedBoard()
		if tc.full {
			b.updateLoaded(boardLoadedMsg{tasks: full})
		}
		b.page = tc.page
		if got := pageKeys(b.liveBindings(bindingsFor(ctxArchived)), ctxArchived); got != tc.want {
			t.Errorf("tasks, %s: offered %q, want %q", tc.name, got, tc.want)
		}

		v := archivedChats804()
		if tc.full {
			v.chats = fullChats
		}
		v.page = tc.page
		if got := pageKeys(v.liveBindings(bindingsFor(ctxArchivedChats)), ctxArchivedChats); got != tc.want {
			t.Errorf("chats, %s: offered %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The archived chats board has no bulk selection, so nothing on it offers
// space or V (AR-11, chats.go askDelete).
func TestArchivedChatsOfferNoBulkSelection(t *testing.T) {
	for _, b := range bindingsFor(ctxArchivedChats) {
		if b.key == "space" || b.key == "V" {
			t.Errorf("ctxArchivedChats offers %q: %s", b.key, b.label)
		}
	}
}

// pgup and pgdown move the chats board a page, clamped at the ends (AR-7).
func TestChatsPageKeysMoveAPage(t *testing.T) {
	v := archivedChats804()
	v.height = 26
	step := v.pageRows()
	v.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if v.cursor != step {
		t.Fatalf("pgdown left the cursor on %d, want %d", v.cursor, step)
	}
	for range 10 {
		v.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if want := len(v.rows()) - 1; v.cursor != want {
		t.Fatalf("pgdown past the end left the cursor on %d, want %d", v.cursor, want)
	}
	for range 10 {
		v.update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	if v.cursor != 0 {
		t.Fatalf("pgup past the start left the cursor on %d", v.cursor)
	}
}

// Review F12: the archived tasks board matches the archived chats board's
// `R` and re-lists on activation, which #804 AR-11 asked for and §15 view 10
// claims.
func TestArchivedTasksBoardReloads(t *testing.T) {
	b := testArchivedBoard()
	b.client = offlineClient()
	if _, cmd := b.updateKey(registryKey(t, "R")); cmd == nil {
		t.Error("R on the archived tasks board reloaded nothing")
	}
	if _, cmd := b.update(viewActivatedMsg{id: viewArchived}); cmd == nil {
		t.Error("the archived tasks board did not re-list on activation")
	}
	if _, cmd := b.update(viewActivatedMsg{id: viewHome}); cmd != nil {
		t.Error("another view's activation re-listed the archived tasks board")
	}
}
