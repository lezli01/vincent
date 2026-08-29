package tui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The board's column widths and its row height (task 050). Part 1 is the cap
// on the title and where the surplus goes; part 2 is what a cell too long for
// its column does instead of vanishing.

func withTitle(title string) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.Title = title }
}

func withLoop(iteration, maxIterations int) func(*apiclient.Task) {
	return func(t *apiclient.Task) {
		t.Loop = &apiclient.LoopRollup{Driver: "each", Iteration: iteration, MaxIterations: maxIterations}
	}
}

func withChildren(r apiclient.ChildrenRollup) func(*apiclient.Task) {
	return func(t *apiclient.Task) { t.Children = &r }
}

func withStep(name string, current, total int) func(*apiclient.Task) {
	return func(t *apiclient.Task) {
		t.StepName, t.CurrentStep, t.StepTotal = name, current, total
	}
}

// TestTitleStopsAtItsCap is part 1: past maxTitle the title stops taking the
// whole remainder, and the surplus turns up in the two columns whose content
// demonstrably outgrows them — STEP first, then STATUS.
func TestTitleStopsAtItsCap(t *testing.T) {
	for _, g := range []grouping{nil, {groupProject}, {groupProject, groupWorkflow}} {
		for width := 65; width <= 400; width++ {
			cols, set := boardColumns(width, g, false)
			title := colWidth(cols, "TITLE")
			step := colWidth(cols, "STEP")
			status := colWidth(cols, "STATUS")
			if step > widthStepMax {
				t.Fatalf("width %d %s: STEP %d over its ceiling", width, g.label(), step)
			}
			if status > widthStatusMax {
				t.Fatalf("width %d %s: STATUS %d over its ceiling", width, g.label(), status)
			}
			// The title may only exceed the cap once nothing else has any
			// appetite: that is the give-back, not a soft ceiling.
			if title > maxTitle {
				if set.stepName && step < widthStepMax {
					t.Fatalf("width %d %s: title %d over the cap with STEP at %d",
						width, g.label(), title, step)
				}
				if set.status && status < widthStatusMax {
					t.Fatalf("width %d %s: title %d over the cap with STATUS at %d",
						width, g.label(), title, status)
				}
			}
		}
	}
}

// TestTitleCapSpendsTheSurplus pins the allocation order at the widths that
// exercise each leg of it, including the give-back band — the default
// grouping at 160, where STATUS is gated off, STEP is full and the leftover
// would otherwise render as blank cells on the right.
func TestTitleCapSpendsTheSurplus(t *testing.T) {
	grouped := grouping{groupProject, groupWorkflow}
	for _, tc := range []struct {
		width                 int
		g                     grouping
		title, step, status   int
		wantStatusColumnGated bool
	}{
		// Below the cap nothing changes: the title takes the remainder.
		{width: 120, g: grouped, title: 50, step: widthStepLong, wantStatusColumnGated: true},
		// The give-back band: STATUS is gated off, STEP fills, and the rest
		// comes back to the title rather than rendering as dead cells.
		{width: 160, g: grouped, title: 76, step: widthStepMax, wantStatusColumnGated: true},
		// STATUS admitted: the title drops to the cap and both other columns
		// take the surplus in order.
		{width: 200, g: grouped, title: maxTitle, step: widthStepMax, status: 50},
		{width: 200, g: nil, title: maxTitle, step: 22, status: widthStatus},
		// Both ceilings reached: only now does the title exceed its cap.
		{width: 300, g: grouped, title: 118, step: widthStepMax, status: widthStatusMax},
	} {
		t.Run(strconv.Itoa(tc.width)+" "+tc.g.label(), func(t *testing.T) {
			cols, _ := boardColumns(tc.width, tc.g, false)
			if got := colWidth(cols, "TITLE"); got != tc.title {
				t.Errorf("TITLE = %d, want %d", got, tc.title)
			}
			if got := colWidth(cols, "STEP"); got != tc.step {
				t.Errorf("STEP = %d, want %d", got, tc.step)
			}
			if got := colWidth(cols, "STATUS"); got != tc.status {
				t.Errorf("STATUS = %d, want %d", got, tc.status)
			}
			if tc.wantStatusColumnGated && colWidth(cols, "STATUS") != 0 {
				t.Error("the status column cleared its gate at a width that should not carry it")
			}
			// Nothing is left unspent: a board with blank cells on the right
			// is the bug the give-back exists for.
			total := 0
			for _, c := range cols {
				total += c.Width + colPadding
			}
			if total != tc.width {
				t.Errorf("columns use %d of %d cells", total, tc.width)
			}
		})
	}
}

// TestNarrowBoardsAreUnchangedByTheCap is the issue's narrow-board criterion.
// Below the cap the title still takes the whole remainder, so every width the
// shedding ladder is about renders exactly as it did before the cap existed.
func TestNarrowBoardsAreUnchangedByTheCap(t *testing.T) {
	for _, g := range []grouping{nil, {groupProject}, {groupProject, groupWorkflow}} {
		for width := 20; width <= 400; width++ {
			for _, marking := range []bool{false, true} {
				set := columnsFor(width, g, marking)
				want := max(set.titleWidth(width), minTitle)
				if want > maxTitle {
					continue
				}
				cols, _ := boardColumns(width, g, marking)
				if got := colWidth(cols, "TITLE"); got != want {
					t.Fatalf("width %d %s marking=%v: title %d, want the whole remainder %d",
						width, g.label(), marking, got, want)
				}
				if got, wantStep := colWidth(cols, "STEP"), widthStepShort; !set.stepName && got != wantStep {
					t.Fatalf("width %d %s: STEP %d, want %d", width, g.label(), got, wantStep)
				}
				if got := colWidth(cols, "STATUS"); set.status && got != widthStatus {
					t.Fatalf("width %d %s: STATUS %d, want the base %d", width, g.label(), got, widthStatus)
				}
			}
		}
	}
}

// TestWideBoardShowsTheRollupsWhole is the issue's acceptance criterion in
// one assertion: the two strings that used to be cut are on the board, whole,
// at the default grouping on a wide terminal.
func TestWideBoardShowsTheRollupsWhole(t *testing.T) {
	b := groupedBoard(
		task(1, stateRunning, inProject("api"), inWorkflow("ship"),
			withStep("green", 2, 7), withLoop(4, 10)),
		task(2, stateAwaitingChildren, inProject("api"), inWorkflow("ship"),
			withChildren(apiclient.ChildrenRollup{Total: 5, Settled: 1, Blocked: []int64{7, 8}})),
	)
	out := ansi.Strip(b.render(200, 30))
	for _, want := range []string{"3/7 green · loop 4/10", "awaiting_children (2 blocked)"} {
		if !containsAcrossLines(out, want) {
			t.Errorf("a 200-column board does not show %q whole:\n%s", want, out)
		}
	}
}

// containsAcrossLines matches a phrase that a wrapped cell may have split
// over two lines of the same row: the words are still all there, in order,
// which is what "readable without opening the task" means.
func containsAcrossLines(out, want string) bool {
	if strings.Contains(out, want) {
		return true
	}
	rest := out
	for _, word := range strings.Fields(want) {
		i := strings.Index(rest, word)
		if i < 0 {
			return false
		}
		rest = rest[i+len(word):]
	}
	return true
}

// TestRowHeightIsOneWhenNothingOverflows: a board whose cells all fit renders
// exactly as it did before rows could wrap — one table row per task, and no
// continuations in the row list at all.
func TestRowHeightIsOneWhenNothingOverflows(t *testing.T) {
	b := groupedBoard(task(1, stateRunning), task(2, stateQueued))
	b.render(200, 30)
	rows := b.rows()
	for i, r := range rows {
		if r.line != 0 {
			t.Fatalf("row %d is a continuation on a board with nothing to wrap", i)
		}
	}
	if got := len(rows); got != 4 { // two headers, two tasks
		t.Fatalf("rows = %d, want 4", got)
	}
}

// TestRowHeightGrowsAndIsClamped: the height is the tallest wrapped row on
// screen, and it never passes three lines — including for a status message at
// the 256-byte ceiling the wire permits (task 036 decision 5), which is the
// worst case a daemon can hand the board.
func TestRowHeightGrowsAndIsClamped(t *testing.T) {
	long := strings.Repeat("ab ", 90) + "end" // 273 bytes of ordinary words
	for _, tc := range []struct {
		name string
		task apiclient.Task
		want int
	}{
		{"fits", task(1, stateRunning), 1},
		{"a title one line over", task(1, stateRunning, withTitle(strings.Repeat("word ", 15))), 2},
		{"a 256-byte status", task(1, stateRunning, withStatus(long[:256])), boardRowLines},
		{"longer than the clamp", task(1, stateRunning, withStatus(long), withTitle(long)), boardRowLines},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := groupedBoard(tc.task)
			b.render(200, 30)
			got := 1
			for _, r := range b.rows() {
				got = max(got, r.line+1)
			}
			if got != tc.want {
				t.Fatalf("row height = %d, want %d", got, tc.want)
			}
			if got > boardRowLines {
				t.Fatalf("row height %d passed the clamp", got)
			}
		})
	}
}

// TestOverflowPastTheClampKeepsTheEllipsis: three lines is where the board
// stops, and it says so rather than simply ending mid-word.
func TestOverflowPastTheClampKeepsTheEllipsis(t *testing.T) {
	lines := wrapCellLines(strings.Repeat("word ", 60), 12, boardRowLines)
	if len(lines) != boardRowLines {
		t.Fatalf("lines = %d, want %d", len(lines), boardRowLines)
	}
	if !strings.HasSuffix(lines[len(lines)-1], "…") {
		t.Errorf("last line = %q, want an ellipsis", lines[len(lines)-1])
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > 12 {
			t.Errorf("line %d is %d cells wide, want at most 12: %q", i, ansi.StringWidth(l), l)
		}
	}
}

// TestWrapCellLinesNormalisesEmbeddedNewlines: a newline inside a status
// message must not become a line the row never budgeted for — the table would
// render it straight into the row below.
func TestWrapCellLinesNormalisesEmbeddedNewlines(t *testing.T) {
	for _, l := range wrapCellLines("first\nsecond\tthird", 40, boardRowLines) {
		if strings.ContainsAny(l, "\n\r\t") {
			t.Errorf("wrapped line %q still carries whitespace the table cannot hold", l)
		}
	}
}

// TestContinuationRowsCarryTheIndentAndABlankMark: a continuation belongs to
// the row above it — indented under the same group header, with no second
// tick in the marker column.
func TestContinuationRowsCarryTheIndentAndABlankMark(t *testing.T) {
	b := groupedBoard(task(1, stateRunning, inProject("api"), inWorkflow("ship"),
		withTitle(strings.Repeat("word ", 20))))
	b.marks = markSet{1}
	b.render(200, 30)

	rows := b.rows()
	cols, set := boardColumns(200, b.group, b.hasMarks())
	cells := b.rowsFor(rows, cols, set)
	indent := strings.Repeat(groupIndent, len(b.group))

	marks, continuations := 0, 0
	for i, r := range rows {
		if len(cells[i]) != len(cols) {
			t.Fatalf("row %d has %d cells, %d columns", i, len(cells[i]), len(cols))
		}
		if strings.Contains(cells[i][0], markGlyph) {
			marks++
		}
		if r.header || r.line == 0 {
			continue
		}
		continuations++
		title := cells[i][indexOfColumn(cols, "TITLE")]
		if !strings.HasPrefix(ansi.Strip(title), indent) {
			t.Errorf("continuation %d lost the group indent: %q", i, ansi.Strip(title))
		}
	}
	if continuations == 0 {
		t.Fatal("nothing wrapped, so the continuation assertions proved nothing")
	}
	if marks != 1 {
		t.Errorf("the marker glyph is on %d lines, want 1 — the row's first", marks)
	}
}

func indexOfColumn(cols []table.Column, title string) int {
	for i, c := range cols {
		if c.Title == title {
			return i
		}
	}
	return 0
}

// TestGroupHeadersStayOneRow: a header has nothing to wrap, and padding it to
// the row height would put more air between the groups than they have rows.
func TestGroupHeadersStayOneRow(t *testing.T) {
	b := groupedBoard(
		task(1, stateRunning, inProject("api"), withStatus(strings.Repeat("clause ", 40))),
		task(2, stateRunning, inProject("web")),
	)
	b.render(200, 30)
	rows := b.rows()
	for i, r := range rows {
		if !r.header {
			continue
		}
		if i+1 < len(rows) && rows[i+1].line != 0 {
			t.Fatalf("row %d is a header followed by a continuation", i)
		}
	}
	if height := rowHeightOf(rows); height < 2 {
		t.Fatalf("row height = %d, so the assertion proved nothing", height)
	}
}

func rowHeightOf(rows []boardRow) int {
	h := 1
	for _, r := range rows {
		h = max(h, r.line+1)
	}
	return h
}

// TestKeysNeverRestOnAContinuation: j and k move between tasks, not between
// lines. A cursor parked on a continuation would make one press move nothing
// a reader can see.
func TestKeysNeverRestOnAContinuation(t *testing.T) {
	tasks := make([]apiclient.Task, 0, 4)
	for id := int64(1); id <= 4; id++ {
		tasks = append(tasks, task(id, stateRunning, inProject("api"),
			withTitle(strings.Repeat("word ", 20))))
	}
	b := groupedBoard(tasks...)
	b.render(200, 30)
	if rowHeightOf(b.rows()) < 2 {
		t.Fatal("nothing wrapped, so the navigation assertions prove nothing")
	}

	press := func(key string) {
		b.update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		b.render(200, 30)
	}
	seen := make([]int64, 0, 8)
	for range 6 {
		press("j")
		rows := b.rows()
		i := b.tbl.Cursor()
		if i < 0 || i >= len(rows) || !rows[i].selectable() {
			t.Fatalf("j left the cursor on row %d, which is not selectable", i)
		}
		id, _ := b.selected()
		seen = append(seen, id)
	}
	if seen[0] == seen[1] {
		t.Errorf("j moved within one row: %v", seen)
	}
	for range 6 {
		press("k")
		rows := b.rows()
		if i := b.tbl.Cursor(); i < 0 || i >= len(rows) || !rows[i].selectable() {
			t.Fatalf("k left the cursor on row %d, which is not selectable", i)
		}
	}
}

// TestMarkingIsUnaffectedByRowHeight: `space` and `V` name tasks, and a task
// is one thing however many lines it takes.
func TestMarkingIsUnaffectedByRowHeight(t *testing.T) {
	b := groupedBoard(
		task(1, stateRunning, inProject("api"), withTitle(strings.Repeat("word ", 20))),
		task(2, stateRunning, inProject("api"), withTitle(strings.Repeat("word ", 20))),
	)
	b.render(200, 30)
	if rowHeightOf(b.rows()) < 2 {
		t.Fatal("nothing wrapped, so the marking assertions prove nothing")
	}
	b.update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if len(b.marks) != 1 {
		t.Fatalf("space marked %d tasks, want 1", len(b.marks))
	}
	b.update(tea.KeyPressMsg{Code: 'V', Text: "V"})
	if len(b.marks) != 2 {
		t.Fatalf("V marked %d tasks, want both", len(b.marks))
	}
	if got := len(b.markedTargets()); got != 2 {
		t.Fatalf("the action bar sees %d targets, want 2", got)
	}
	if got := len(b.visible()); got != 2 {
		t.Fatalf("visible() = %d tasks, want 2 — continuations are not tasks", got)
	}
}
