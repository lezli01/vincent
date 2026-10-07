package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

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
		switch {
		case r.header:
			out = append(out, "header:"+issueSections[r.section].label)
		case r.none:
			out = append(out, "none")
		default:
			out = append(out, "#"+strconv.FormatInt(r.issue.ID, 10))
		}
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

func TestIssuesEmptySectionsDrawZeroAndNone(t *testing.T) {
	v := boardFixture()
	v.showDone = false
	v.issues = []apiclient.Issue{{ID: 1, Title: "only", State: "open", Lane: "open"}}
	v.clampCursor()
	out := ansi.Strip(v.render(100, 20))
	for _, want := range []string{"▾ in progress  0", "▾ hand-off  0", "none"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list is missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "none"); got != 2 {
		t.Errorf("%d placeholders, want one per empty section:\n%s", got, out)
	}
}

func TestIssuesTotalsLineAndAttentionBadge(t *testing.T) {
	v := boardFixture()
	out := ansi.Strip(v.render(120, 30))
	if !strings.Contains(out, "2 open · 2 in progress · 1 hand-off · 3 done") {
		t.Errorf("the totals line is wrong:\n%s", out)
	}
	for _, want := range []string{"▾ in progress  2  ! 1", "▾ hand-off  1  ! 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list is missing the header %q:\n%s", want, out)
		}
	}
	v.showDone = false
	if out := ansi.Strip(v.render(120, 30)); !strings.Contains(out, "1 hand-off · done hidden (s)") {
		t.Errorf("the hidden-done totals do not say how to show it:\n%s", out)
	}
}

// `done` is hidden by default and lists open only; `s` shows it with every
// state, every close reason drawn, and hides it again.
func TestIssuesDoneToggle(t *testing.T) {
	v := newIssuesView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	if v.showDone {
		t.Fatal("done is shown on a fresh list")
	}
	if got := v.listOptions().States; !slices.Equal(got, []string{"open"}) {
		t.Fatalf("hidden done listed states %v, want [open]", got)
	}
	v.issues, v.loaded = []apiclient.Issue{{ID: 1, State: "open", Lane: "open"}}, true
	if got := strings.Join(issueLayout(v), ","); strings.Contains(got, "done") || strings.Count(got, "header:") != 3 {
		t.Fatalf("hidden done drew %s, want three sections", got)
	}

	if _, cmd := v.updateKey(registryKey(t, "s")); cmd == nil {
		t.Fatal("s did not re-list")
	}
	if !v.showDone || len(v.listOptions().States) != 0 {
		t.Fatalf("s left showDone=%v states=%v, want shown with no state filter", v.showDone, v.listOptions().States)
	}
	shown := boardFixture()
	out := ansi.Strip(shown.render(140, 30))
	for _, want := range []string{"▾ done  3", "closed · completed", "closed · not planned", "closed · duplicate"} {
		if !strings.Contains(out, want) {
			t.Errorf("the shown done section is missing %q:\n%s", want, out)
		}
	}

	v.updateKey(registryKey(t, "s"))
	if v.showDone || !slices.Equal(v.listOptions().States, []string{"open"}) {
		t.Fatal("s again did not hide done")
	}
}

func TestIssuesFoldKeys(t *testing.T) {
	v := boardFixture()
	if row, ok := v.current(); !ok || row.issue.ID != 11 {
		t.Fatalf("a fresh list starts on %+v, want #11 (expanded headers are skipped)", row)
	}
	v.updateKey(registryKey(t, "left"))
	if got := issueLayout(v)[:2]; !slices.Equal(got, []string{"header:open", "header:in progress"}) {
		t.Fatalf("← did not fold the open section: %v", issueLayout(v))
	}
	if v.cursor != 0 {
		t.Fatalf("← left the cursor at %d, want on the folded header", v.cursor)
	}
	if !strings.Contains(ansi.Strip(v.render(120, 30)), "▸ open  2") {
		t.Errorf("the folded header lost its glyph or count:\n%s", ansi.Strip(v.render(120, 30)))
	}
	v.updateKey(registryKey(t, "down"))
	if row, ok := v.current(); !ok || row.issue.ID != 13 {
		t.Fatalf("down from a folded header went to %+v, want #13 past the expanded header", row)
	}
	v.updateKey(registryKey(t, "up"))
	v.updateKey(registryKey(t, "right"))
	if row, ok := v.current(); !ok || row.issue.ID != 11 {
		t.Fatalf("→ left the cursor on %+v, want the section's first issue", row)
	}

	v.updateKey(registryKey(t, "C"))
	if got := issueLayout(v); !slices.Equal(got, []string{"header:open", "header:in progress", "header:hand-off", "header:done"}) {
		t.Fatalf("C folded to %v, want only headers", got)
	}
	v.updateKey(registryKey(t, "O"))
	if got := len(issueLayout(v)); got != 12 {
		t.Fatalf("O left %d lines, want all 12", got)
	}
}

// Hiding done and showing it again opens it expanded, even if it was folded.
func TestIssuesShownDoneOpensExpanded(t *testing.T) {
	v := boardFixture()
	v.folded[issueLaneDone] = true
	v.toggleDone()
	v.toggleDone()
	if v.folded[issueLaneDone] {
		t.Fatal("done came back folded")
	}
}

func TestIssuesFilterKeepsHeaders(t *testing.T) {
	v := boardFixture()
	v.filter.SetValue("working")
	v.clampCursor()
	want := []string{"header:open", "none", "header:in progress", "#13", "header:hand-off", "none", "header:done", "none"}
	if got := issueLayout(v); !slices.Equal(got, want) {
		t.Fatalf("filtered lines = %v, want %v", got, want)
	}
	out := ansi.Strip(v.render(120, 30))
	if strings.Count(out, "none match") != 3 {
		t.Errorf("the empty sections do not say none match:\n%s", out)
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
	v.folded["hand_off"] = true
	v.setProject(projectSel{id: 2, name: "web"})
	v.updateKey(registryKey(t, "s"))
	if !v.showDone || !v.folded["hand_off"] {
		t.Fatal("the toggle or the folds did not survive a switch")
	}

	v.showDone = false
	stale := issuesLoadedMsg{showDone: false, stamp: v.stamps.next(2), issues: []apiclient.Issue{{ID: 99, Lane: "open"}}}
	v.toggleDone()
	v.applyLoaded(stale)
	if len(v.issues) != 0 {
		t.Fatalf("a listing for the hidden-done scope landed after s: %+v", v.issues)
	}
}

// The list stays legible at §15's floors: the totals, every header and the
// cursor's row are on screen.
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
		if !strings.Contains(out, "▾ done") {
			t.Errorf("%dx%d: the cursor's section header is off screen:\n%s", size[0], size[1], out)
		}
	}
}
