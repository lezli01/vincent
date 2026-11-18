package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
)

// The issues list as a board of the daemon's four lanes (task 134.8). Pure
// model tests, one property each (task 129 decision 7).

// Every lane the daemon can serve has a section, in board order, so no row
// can fall off the list.
func TestIssueSectionsCoverEveryLane(t *testing.T) {
	var got []string
	for _, s := range issueSections {
		got = append(got, s.lane)
	}
	var want []string
	for _, l := range issuestate.Lanes {
		want = append(want, string(l))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sections %v, want issuestate.Lanes %v", got, want)
	}
	if issueSections[len(issueSections)-1].lane != issueLaneDone {
		t.Fatalf("done is not the last section: %v", got)
	}
}

// boardFixture is one issue per lane and a second open one, interleaved the
// way the daemon's recency order might serve them.
func boardFixture() *issuesView {
	v := newIssuesView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	v.showDone = true
	v.issues = []apiclient.Issue{
		{ID: 10, Title: "shipped", State: "closed", CloseReason: "completed", Lane: "done"},
		{ID: 11, Title: "fresh", State: "open", Lane: "open"},
		{ID: 12, Title: "handing", State: "open", Lane: "hand_off", TaskCount: 1, Attention: true},
		{ID: 13, Title: "working", State: "open", Lane: "in_progress", TaskCount: 1, Active: true, Attention: true},
		{ID: 14, Title: "older", State: "open", Lane: "open"},
		{ID: 15, Title: "dropped", State: "closed", CloseReason: "not_planned", Lane: "done"},
		{ID: 16, Title: "copy", State: "closed", CloseReason: "duplicate", Lane: "done"},
		{ID: 17, Title: "busy", State: "open", Lane: "in_progress", TaskCount: 1, Active: true},
	}
	v.loaded = true
	v.clampCursor()
	return v
}

// issueLayout is lines() as "header:label" and "#id" strings.
func issueLayout(v *issuesView) []string {
	var out []string
	for _, r := range v.lines() {
		if r.header {
			out = append(out, "header:"+issueSections[r.section].label)
			continue
		}
		out = append(out, "#"+strconv.FormatInt(r.issue.ID, 10))
	}
	return out
}

func TestIssuesPartitionIntoSectionsInBoardOrder(t *testing.T) {
	v := boardFixture()
	want := []string{
		"header:open", "#11", "#14",
		"header:in progress", "#13", "#17",
		"header:hand-off", "#12",
		"header:done", "#10", "#15", "#16",
	}
	if got := issueLayout(v); !slices.Equal(got, want) {
		t.Fatalf("lines = %v\nwant    %v", got, want)
	}
	seen := map[int64]int{}
	for _, r := range v.rows() {
		seen[r.issue.ID]++
	}
	for _, iss := range v.issues {
		if seen[iss.ID] != 1 {
			t.Errorf("#%d is in %d sections, want exactly one", iss.ID, seen[iss.ID])
		}
	}
}

func TestIssuesEmptyColumnsSaySo(t *testing.T) {
	v := boardFixture()
	v.showDone = false
	v.issues = []apiclient.Issue{{ID: 1, Title: "only", State: "open", Lane: "open"}}
	v.clampCursor()
	out := ansi.Strip(v.render(100, 20))
	if got := strings.Count(out, "no issues"); got != 2 {
		t.Errorf("%d placeholders, want one per empty column:\n%s", got, out)
	}
}

// A column's edge carries its title, its count and the attention badge, so a
// card scrolled out of the column cannot hide that someone is needed.
func TestIssuesColumnEdgesCountAndWarn(t *testing.T) {
	v := boardFixture()
	out := ansi.Strip(v.render(120, 30))
	for _, want := range []string{"○ Open", "◐ In progress", "⇄ Hand-off", "✓ Done", "! 1  2", "! 1  1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the board is missing %q:\n%s", want, out)
		}
	}
}

// `done` is a column from the start and lists every state; `s` hides it,
// listing open issues only, and shows it again.
func TestIssuesDoneToggle(t *testing.T) {
	v := newIssuesView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	if !v.showDone || len(v.listOptions().States) != 0 {
		t.Fatalf("a fresh board has showDone=%v states=%v, want shown with no state filter", v.showDone, v.listOptions().States)
	}
	shown := boardFixture()
	out := ansi.Strip(shown.render(140, 40))
	for _, want := range []string{"✓ Done", "✓ completed", "✓ not planned", "✓ duplicate"} {
		if !strings.Contains(out, want) {
			t.Errorf("the done column is missing %q:\n%s", want, out)
		}
	}

	if _, cmd := v.updateKey(registryKey(t, "s")); cmd == nil {
		t.Fatal("s did not re-list")
	}
	if v.showDone || !slices.Equal(v.listOptions().States, []string{"open"}) {
		t.Fatal("s did not hide done")
	}
	v.issues, v.loaded = []apiclient.Issue{{ID: 1, State: "open", Lane: "open"}}, true
	if got := strings.Join(issueLayout(v), ","); strings.Contains(got, "done") || strings.Count(got, "header:") != 3 {
		t.Fatalf("hidden done drew %s, want three columns", got)
	}
	v.updateKey(registryKey(t, "s"))
	if !v.showDone || len(v.listOptions().States) != 0 {
		t.Fatal("s again did not show done")
	}
}

// ↑ and ↓ stay inside a column; ← and → cross to the next column with a card,
// at the same height or the last card above it.
func TestIssuesLaneKeys(t *testing.T) {
	v := boardFixture()
	if got := selectedID(v); got != 11 {
		t.Fatalf("a fresh board starts on #%d, want #11", got)
	}
	v.updateKey(registryKey(t, "down"))
	v.updateKey(registryKey(t, "down"))
	if got := selectedID(v); got != 14 {
		t.Fatalf("down stopped on #%d, want #14, the column's last card", got)
	}
	v.updateKey(registryKey(t, "right"))
	if got := selectedID(v); got != 17 {
		t.Fatalf("right went to #%d, want #17 at the same height", got)
	}
	v.updateKey(registryKey(t, "right"))
	if got := selectedID(v); got != 12 {
		t.Fatalf("right went to #%d, want #12, the shorter column's last card", got)
	}
	v.updateKey(registryKey(t, "left"))
	v.updateKey(registryKey(t, "left"))
	v.updateKey(registryKey(t, "left"))
	if got := selectedID(v); got != 11 {
		t.Fatalf("left at the first column went to #%d, want it to stay on #11", got)
	}
}

// An empty column is stepped over rather than landed on.
func TestIssuesLaneKeysSkipEmptyColumns(t *testing.T) {
	v := boardFixture()
	v.issues = []apiclient.Issue{
		{ID: 1, Title: "a", State: "open", Lane: "open"},
		{ID: 2, Title: "b", State: "open", Lane: "hand_off"},
	}
	v.clampCursor()
	v.updateKey(registryKey(t, "right"))
	if got := selectedID(v); got != 2 {
		t.Fatalf("right landed on #%d, want #2 past the empty column", got)
	}
}

func TestIssuesFilterKeepsColumns(t *testing.T) {
	v := boardFixture()
	v.filter.SetValue("working")
	v.clampCursor()
	want := []string{"header:open", "header:in progress", "#13", "header:hand-off", "header:done"}
	if got := issueLayout(v); !slices.Equal(got, want) {
		t.Fatalf("filtered lines = %v, want %v", got, want)
	}
	out := ansi.Strip(v.render(120, 30))
	if strings.Count(out, "none match") != 3 {
		t.Errorf("the empty columns do not say none match:\n%s", out)
	}
	if row, ok := v.current(); !ok || row.issue.ID != 13 {
		t.Errorf("the filter left the cursor on %+v, want the one match", row)
	}
}

// The cursor follows the issue across a re-list that moves it to another
// section, and lands on the nearest row when it leaves for the hidden done.
func TestIssuesReselectAcrossSections(t *testing.T) {
	v := boardFixture()
	v.showDone = false
	v.issues = slices.DeleteFunc(slices.Clone(v.issues), func(i apiclient.Issue) bool { return i.Lane == "done" })
	v.selectIssue(14)

	moved := slices.Clone(v.issues)
	for i := range moved {
		if moved[i].ID == 14 {
			moved[i].Lane, moved[i].Active = "in_progress", true
		}
	}
	v.applyLoaded(issuesLoadedMsg{showDone: false, stamp: v.stamps.next(1), issues: moved})
	if row, ok := v.current(); !ok || row.issue.ID != 14 || issueSections[row.section].lane != "in_progress" {
		t.Fatalf("the cursor did not follow #14 into in progress: %+v", row)
	}

	gone := slices.DeleteFunc(slices.Clone(moved), func(i apiclient.Issue) bool { return i.ID == 14 })
	v.applyLoaded(issuesLoadedMsg{showDone: false, stamp: v.stamps.next(1), issues: gone})
	row, ok := v.current()
	if !ok || row.issue.ID == 14 {
		t.Fatalf("the cursor did not move to a neighbour of the closed issue: %+v", row)
	}
	if row.issue.ID != 17 {
		t.Errorf("the cursor went to #%d, want #17, the row now at its index", row.issue.ID)
	}
}

// A project switch keeps the toggle, and a listing for the other scope that
// lands after `s` is dropped.
func TestIssuesToggleSurvivesSwitchAndDropsStale(t *testing.T) {
	v := boardFixture()
	v.showDone = false
	v.setProject(projectSel{id: 2, name: "web"})
	v.updateKey(registryKey(t, "s"))
	if !v.showDone {
		t.Fatal("the toggle did not survive a switch")
	}

	v.showDone = false
	stale := issuesLoadedMsg{showDone: false, stamp: v.stamps.next(2), issues: []apiclient.Issue{{ID: 99, Lane: "open"}}}
	v.toggleDone()
	v.applyLoaded(stale)
	if len(v.issues) != 0 {
		t.Fatalf("a listing for the hidden-done scope landed after s: %+v", v.issues)
	}
}

// The board stays legible at §15's floors: the cursor's card and its column
// are on screen, with nothing past the edges.
func TestIssuesLegibleAtTheFloors(t *testing.T) {
	for _, size := range [][2]int{{80, 20}, {60, 15}} {
		v := boardFixture()
		v.selectIssue(16)
		out := ansi.Strip(v.render(size[0], size[1]))
		lines := strings.Split(out, "\n")
		if len(lines) > size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > size[0] {
				t.Errorf("%dx%d: line wider than the screen: %q", size[0], size[1], l)
			}
		}
		if !strings.Contains(out, "#16") {
			t.Errorf("%dx%d: the cursor's row is off screen:\n%s", size[0], size[1], out)
		}
		if !strings.Contains(out, "✓ Done") {
			t.Errorf("%dx%d: the cursor's column is off screen:\n%s", size[0], size[1], out)
		}
	}
}

// longIssuesFixture is forty issues, far taller than an 80×20 pane: #1-#30
// open and #31-#40 in progress, with hand-off empty.
func longIssuesFixture() *issuesView {
	v := newIssuesView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	for i := 1; i <= 40; i++ {
		lane := "open"
		if i > 30 {
			lane = "in_progress"
		}
		v.issues = append(v.issues, apiclient.Issue{ID: int64(i), Title: "issue " + strconv.Itoa(i), State: "open", Lane: lane})
	}
	v.loaded = true
	v.clampCursor()
	return v
}

func selectedID(v *issuesView) int64 {
	row, _ := v.current()
	return row.issue.ID
}

// #796: pgdown and pgup move by the cards on screen, home/g and end/G reach the
// ends of the column, and none of them leaves it.
func TestIssuesPagingKeys(t *testing.T) {
	v := longIssuesFixture()
	v.render(80, 20)
	page := v.page
	if page < 1 {
		t.Fatalf("an 80x20 pane pages by %d cards", page)
	}
	v.updateKey(keyPress("pgdown"))
	if got := selectedID(v); got != int64(1+page) {
		t.Errorf("pgdown from #1 landed on #%d, want #%d", got, 1+page)
	}
	v.updateKey(keyPress("pgup"))
	if got := selectedID(v); got != 1 {
		t.Errorf("pgup came back to #%d, want #1", got)
	}
	v.updateKey(keyPress("pgup"))
	if got := selectedID(v); got != 1 {
		t.Errorf("pgup at the top moved to #%d", got)
	}
	for _, k := range []string{"end", "G"} {
		v.updateKey(keyPress("home"))
		v.updateKey(keyPress(k))
		if got := selectedID(v); got != 30 {
			t.Errorf("%s selected #%d, want #30, the column's last card", k, got)
		}
	}
	for _, k := range []string{"home", "g"} {
		v.updateKey(keyPress("end"))
		v.updateKey(keyPress(k))
		if got := selectedID(v); got != 1 {
			t.Errorf("%s selected #%d, want #1", k, got)
		}
	}
	v.selectIssue(30)
	v.updateKey(keyPress("pgdown"))
	if got := selectedID(v); got != 30 {
		t.Errorf("pgdown left the column for #%d", got)
	}
	// The filter owns the keys while it types.
	v.updateKey(keyPress("/"))
	v.updateKey(keyPress("home"))
	if got := selectedID(v); got != 30 {
		t.Errorf("home moved the selection to #%d while the filter was typing", got)
	}
}

// #796: a column taller than its pane says how many cards it hides, above and
// below, and a click on the cue selects nothing while a click on a card does.
func TestIssuesOverflowCuesAndClicks(t *testing.T) {
	v := longIssuesFixture()
	lines := renderedLines(v, 80, 20)
	if len(lines) != 20 {
		t.Errorf("the board drew %d lines in a 20-line pane", len(lines))
	}
	want := fmt.Sprintf("▼ %d more", 30-v.page)
	if shows(lines, "▲") || !shows(lines, want) {
		t.Fatalf("at the top, want only %q:\n%s", want, strings.Join(lines, "\n"))
	}

	v.selectIssue(20)
	lines = renderedLines(v, 80, 20)
	cue := lineOf(lines, "▲")
	if cue < 0 || !shows(lines, "▼") {
		t.Fatalf("in the middle, want both cues:\n%s", strings.Join(lines, "\n"))
	}
	v.update(tea.MouseClickMsg{X: 10, Y: cue, Button: tea.MouseLeft})
	if got := selectedID(v); got != 20 {
		t.Errorf("a click on a cue moved the selection to #%d", got)
	}
	var other issueCardHit
	for _, h := range v.cardHits {
		if h.id != 20 && h.id <= 30 {
			other = h
		}
	}
	if other.id == 0 {
		t.Fatalf("no other card drawn in the column: %+v", v.cardHits)
	}
	v.update(tea.MouseClickMsg{X: other.x0, Y: other.y0, Button: tea.MouseLeft})
	if got := selectedID(v); got != other.id {
		t.Errorf("a click on the card for #%d selected #%d", other.id, got)
	}

	short := issuesFixture()
	if lines = renderedLines(short, 160, 40); shows(lines, "▲") || shows(lines, "▼") {
		t.Errorf("a board that fits drew a cue:\n%s", strings.Join(lines, "\n"))
	}
}
