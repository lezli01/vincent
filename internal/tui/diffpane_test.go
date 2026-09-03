package tui

import (
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

const sampleDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,4 @@
 package main
-func old() {}
+func new() {}
 // trailing context
`

// twoFileDiff is the shape the grouping exists for: more than one file, so
// "which one am I opening" is a question.
const twoFileDiff = sampleDiff + `diff --git a/README.md b/README.md
index 3333333..4444444 100644
--- a/README.md
+++ b/README.md
@@ -1,3 +1,3 @@
 # readme
-old line
+new line
+one more
`

// loadedDiff is a pane with a diff in it, as the tab looks the moment it
// opens: every file collapsed.
func loadedDiff(t *testing.T, text string) *diffPane {
	t.Helper()
	p := newDiffPane()
	p.openTask(1)
	p.apply(diffLoadedMsg{taskID: 1, text: text})
	return &p
}

// diffView is the pane as a reader sees it, without the styling: the ±
// counts are coloured per number, so a raw view has an escape between them.
// The width is fixed — every assertion here is about which lines are on
// screen, never about where they wrap.
func diffView(p *diffPane, height int) string {
	return ansi.Strip(p.render(diffTestWidth, height))
}

// diffTestWidth is wide enough that no path or hunk line wraps.
const diffTestWidth = 80

// diffPress drives the pane with one key, named the way the registry names it.
func diffPress(p *diffPane, name string) {
	switch name {
	case "enter", "up", "down", "esc":
		p.updateKey(namedKey(name))
	case "left":
		p.updateKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	case "right":
		p.updateKey(tea.KeyPressMsg{Code: tea.KeyRight})
	case "space":
		p.updateKey(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	default:
		p.updateKey(key(name))
	}
}

// TestDiffClassification: the ± file markers carry the same first character
// as content, and reading them as one added and one removed line per file is
// exactly the mistake a naive prefix check makes.
func TestDiffClassification(t *testing.T) {
	want := []diffClass{
		diffHeader, diffHeader, diffHeader, diffHeader,
		diffHunk, diffContext, diffDel, diffAdd, diffContext,
	}
	lines := splitDiff(sampleDiff)
	if len(lines) != len(want) {
		t.Fatalf("split %d lines, want %d", len(lines), len(want))
	}
	for i, line := range lines {
		if got := classifyDiffLine(line); got != want[i] {
			t.Errorf("line %d (%q) = %v, want %v", i, line, got, want[i])
		}
	}
}

// TestDiffOpensWithEveryFileCollapsed is the default the tab is built around
// (task 012): the first screen is the file list with its ± counts, not the
// first eighty lines of the first file.
func TestDiffOpensWithEveryFileCollapsed(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)

	view := diffView(p, 20)
	for _, want := range []string{"main.go", "README.md", "+1 -1", "+2 -1", "2 files"} {
		if !strings.Contains(view, want) {
			t.Errorf("the folded list is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "func new() {}") {
		t.Errorf("a collapsed file showed its diff:\n%s", view)
	}
	if strings.Contains(view, diffFoldOpen) {
		t.Errorf("a file rendered as expanded:\n%s", view)
	}
}

// TestDiffTogglesTheFileUnderTheCursor covers the fold keys the label
// promises: they are one control, so all of them have to move the same state.
func TestDiffTogglesTheFileUnderTheCursor(t *testing.T) {
	for _, tc := range []struct{ name, open, shut string }{
		{name: "enter", open: "enter", shut: "enter"},
		{name: "space", open: "space", shut: "space"},
		{name: "arrows", open: "right", shut: "left"},
		{name: "hl", open: "l", shut: "h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := loadedDiff(t, twoFileDiff)
			diffPress(p, tc.open)
			if view := diffView(p, 20); !strings.Contains(view, "func new() {}") {
				t.Fatalf("%s did not expand the file under the cursor:\n%s", tc.name, view)
			}
			diffPress(p, tc.shut)
			if view := diffView(p, 20); strings.Contains(view, "func new() {}") {
				t.Fatalf("%s did not collapse it again:\n%s", tc.name, view)
			}
		})
	}
}

// TestDiffCursorWalksFiles: ↑/↓ move between files — the pane's own axis is
// the file list, which is the only thing there is to move when everything is
// folded shut.
func TestDiffCursorWalksFiles(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	if p.cursorPath != "main.go" {
		t.Fatalf("cursor starts on %q, want the first file", p.cursorPath)
	}
	diffPress(p, "j")
	if p.cursorPath != "README.md" {
		t.Fatalf("j moved the cursor to %q, want the second file", p.cursorPath)
	}
	diffPress(p, "enter")
	view := diffView(p, 20)
	if !strings.Contains(view, "one more") {
		t.Errorf("the second file did not expand:\n%s", view)
	}
	if strings.Contains(view, "func new() {}") {
		t.Errorf("expanding the second file also expanded the first:\n%s", view)
	}

	diffPress(p, "up")
	if p.cursorPath != "main.go" {
		t.Errorf("↑ moved the cursor to %q, want back to the first file", p.cursorPath)
	}
	// The ends hold rather than wrap: the list is short enough to see, and
	// wrapping past the end of it reads as a jump.
	diffPress(p, "k")
	if p.cursorPath != "main.go" {
		t.Errorf("k past the first file moved to %q", p.cursorPath)
	}
}

// TestDiffExpandAllAndCollapseAll: the two keys the whole list answers to.
func TestDiffExpandAllAndCollapseAll(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)

	diffPress(p, "O")
	view := diffView(p, 40)
	if !strings.Contains(view, "func new() {}") || !strings.Contains(view, "one more") {
		t.Fatalf("O did not expand every file:\n%s", view)
	}
	if got := strings.Count(view, diffFoldOpen); got != 2 {
		t.Errorf("O left %d files reading as open, want 2:\n%s", got, view)
	}

	diffPress(p, "C")
	view = diffView(p, 40)
	if strings.Contains(view, "func new() {}") || strings.Contains(view, "one more") {
		t.Fatalf("C did not collapse every file:\n%s", view)
	}
	if len(p.open) != 0 {
		t.Errorf("C left fold state behind: %v", p.open)
	}
}

// TestDiffFoldsSurviveARefresh: leaving and re-entering the tab re-fetches
// (the endpoint runs git per call), and a file folding shut under the reader
// because the agent touched a different one is the bug this guards.
func TestDiffFoldsSurviveARefresh(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	diffPress(p, "down") // onto README.md
	diffPress(p, "enter")

	// The same diff, with a file added ahead of both.
	grown := `diff --git a/first.go b/first.go
index 5555555..6666666 100644
--- a/first.go
+++ b/first.go
@@ -1,1 +1,1 @@
-first old
+first new
` + twoFileDiff
	p.apply(diffLoadedMsg{taskID: 1, text: grown})

	if p.cursorPath != "README.md" {
		t.Errorf("the cursor moved to %q across the refresh, want the file it was on", p.cursorPath)
	}
	if p.cursor != 2 {
		t.Errorf("cursor index = %d, want 2 — the file moved down by the one added above it", p.cursor)
	}
	if view := diffView(p, 40); !strings.Contains(view, "one more") {
		t.Errorf("the expanded file folded shut across the refresh:\n%s", view)
	}
}

// TestDiffForgetsFoldsOfFilesThatWentAway keeps the fold map the size of the
// diff: a session parked on one task all day would otherwise remember every
// file the agent touched and reverted.
func TestDiffForgetsFoldsOfFilesThatWentAway(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	diffPress(p, "O")
	p.apply(diffLoadedMsg{taskID: 1, text: sampleDiff})
	if len(p.open) != 1 || !p.open["main.go"] {
		t.Errorf("fold state = %v, want main.go alone", p.open)
	}
}

// TestDiffCollapsesAgainForAnotherTask: the folds belong to the diff, and
// another task's files are not these files.
func TestDiffCollapsesAgainForAnotherTask(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	diffPress(p, "O")
	p.openTask(2)
	p.apply(diffLoadedMsg{taskID: 2, text: twoFileDiff})
	if len(p.open) != 0 {
		t.Errorf("the next task opened with %v expanded", p.open)
	}
	if view := diffView(p, 20); strings.Contains(view, "func new() {}") {
		t.Errorf("the next task's diff opened expanded:\n%s", view)
	}
}

// TestDiffBinaryFileSaysSoInsteadOfCountingNothing: `+0 -0` on a binary file
// reads as "nothing changed here".
func TestDiffBinaryFileSaysSoInsteadOfCountingNothing(t *testing.T) {
	p := loadedDiff(t, `diff --git a/bin.dat b/bin.dat
index 366fd40..f68ed80 100644
Binary files a/bin.dat and b/bin.dat differ
`)
	view := diffView(p, 10)
	if !strings.Contains(view, "binary") {
		t.Errorf("the header row does not say the file is binary:\n%s", view)
	}
	if strings.Contains(view, "+0 -0") {
		t.Errorf("a binary file rendered ± counts:\n%s", view)
	}
}

// TestDiffClickFoldsTheHeaderItLandsOn is the mouse half of the fold (§15:
// click a row). A click on a line of code selects its file and leaves it open —
// clicking code is not a request to make it disappear.
func TestDiffClickFoldsTheHeaderItLandsOn(t *testing.T) {
	p := loadedDiff(t, twoFileDiff)
	p.render(80, 20) // the rows and the summary's offset are render-time state

	p.clickRow(2) // summary line, main.go, README.md
	if p.cursorPath != "README.md" || !p.open["README.md"] {
		t.Fatalf("clicking the second header left cursor %q and folds %v", p.cursorPath, p.open)
	}
	p.render(80, 20)
	p.clickRow(3) // the first line of README.md's body
	if p.cursorPath != "README.md" || !p.open["README.md"] {
		t.Errorf("clicking a body line changed the fold: cursor %q, folds %v", p.cursorPath, p.open)
	}
}

// TestDiffRendersFetchedContent: the body is what git said, once it is open.
func TestDiffRendersFetchedContent(t *testing.T) {
	p := loadedDiff(t, sampleDiff)
	diffPress(p, "O")

	view := diffView(p, 20)
	if !strings.Contains(view, "func new() {}") {
		t.Errorf("diff body missing:\n%s", view)
	}
}

// TestDiffTruncatesLongOutput bounds the terminal, not the truth: the whole
// change stays on the branch.
func TestDiffTruncatesLongOutput(t *testing.T) {
	p := loadedDiff(t, strings.Repeat("+line\n", maxDiffLines+100))

	if !p.truncated || len(p.lines) != maxDiffLines {
		t.Fatalf("lines = %d, truncated = %v; want a capped render", len(p.lines), p.truncated)
	}
	if !strings.Contains(diffView(p, maxDiffLines+10), "truncated") {
		t.Error("the truncation is not visible")
	}
}

// TestDiffEmptyStatesAreDistinct: the endpoint's two conflicts are different
// situations, and only one of them is worth waiting on.
func TestDiffEmptyStatesAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*diffPane)
		want string
	}{
		{"never fetched", func(*diffPane) {}, "press d"},
		{"fetching", func(p *diffPane) { p.fetching = true }, "loading"},
		{"no changes", func(p *diffPane) {
			p.apply(diffLoadedMsg{taskID: 1, text: ""})
		}, "no changes"},
		{"not started", func(p *diffPane) {
			p.apply(diffLoadedMsg{taskID: 1, err: &apiclient.Error{
				Status: http.StatusConflict, Code: "invalid_state",
				Message: "task has no worktree yet",
			}})
		}, "not started"},
		{"worktree removed", func(p *diffPane) {
			p.apply(diffLoadedMsg{taskID: 1, err: &apiclient.Error{
				Status: http.StatusConflict, Code: "invalid_state",
				Message: "worktree no longer exists",
			}})
		}, "removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newDiffPane()
			p.openTask(1)
			tc.set(&p)
			if got := diffView(&p, 10); !strings.Contains(got, tc.want) {
				t.Errorf("state = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// TestDiffDropsAnotherTasksResponse: a late fetch for the task the view has
// left must not paint over the one it moved to.
func TestDiffDropsAnotherTasksResponse(t *testing.T) {
	p := newDiffPane()
	p.openTask(2)
	p.apply(diffLoadedMsg{taskID: 1, text: sampleDiff})
	if p.loaded {
		t.Error("a diff for another task was installed")
	}
}

// --------------------------------------------------------------------------
// The lane level (§7.6, issue #316): a fan-out parent's diff attributed to
// the lanes that produced it, one level above task 012's file grouping.
// --------------------------------------------------------------------------

// sharedFileDiff touches main.go the way sampleDiff does but with a different
// hunk, so a file that two lanes both changed is recognisable in each.
const sharedFileDiff = `diff --git a/main.go b/main.go
index 5555555..6666666 100644
--- a/main.go
+++ b/main.go
@@ -20,3 +20,4 @@ func other() {}
 // second lane's region
+func alsoNew() {}
`

// remainderDiff is the parent's own work, in a file no lane touched — so an
// assertion about a lane's file cannot be satisfied by the remainder.
const remainderDiff = `diff --git a/notes.txt b/notes.txt
index 7777777..8888888 100644
--- a/notes.txt
+++ b/notes.txt
@@ -1,2 +1,2 @@
 notes
-before the join
+after the join
`

// laneSections is a two-lane fan-out where both lanes touched main.go and only
// one touched README.md, plus a remainder the parent committed itself.
func laneSections() []apiclient.DiffSection {
	return []apiclient.DiffSection{
		{LaneID: "api", ChildTaskID: 7, MergeCommit: "aaaaaaa", Diff: twoFileDiff},
		{LaneID: "docs", ChildTaskID: 8, MergeCommit: "bbbbbbb", Diff: sharedFileDiff},
		{Remainder: true, Diff: remainderDiff},
	}
}

// loadedLaneDiff is the pane as the tab looks on a joined fan-out.
func loadedLaneDiff(t *testing.T) *diffPane {
	t.Helper()
	p := newDiffPane()
	p.openTask(1)
	p.apply(diffLoadedMsg{taskID: 1, sections: laneSections()})
	return &p
}

// TestDiffLanesOpenAsTheLaneList: the first screen of a joined fan-out is the
// lanes, not the files — the merged wall is exactly what the grouping exists
// to stop a reviewer scrolling through.
func TestDiffLanesOpenAsTheLaneList(t *testing.T) {
	p := loadedLaneDiff(t)
	view := diffView(p, 30)
	for _, want := range []string{
		diffFoldClosed + " lane api", "task 7",
		diffFoldClosed + " lane docs", "task 8",
		diffFoldClosed + " the task's own commits",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the lane list is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "README.md") {
		t.Errorf("a file is on screen before its lane was opened:\n%s", view)
	}
	// The summary counts the lanes, and the files across all of them.
	if !strings.Contains(view, "2 lanes") {
		t.Errorf("the summary does not count the lanes:\n%s", view)
	}
}

// TestDiffLanesFoldToTheirFiles: the tab's existing fold keys drive the new
// outer level — no key was added for it, so `enter` on a lane has to mean the
// same thing it means on a file.
func TestDiffLanesFoldToTheirFiles(t *testing.T) {
	p := loadedLaneDiff(t)
	diffPress(p, "enter")
	view := diffView(p, 30)
	if !strings.Contains(view, diffFoldOpen+" lane api") {
		t.Fatalf("enter did not open the lane under the cursor:\n%s", view)
	}
	for _, want := range []string{"main.go", "README.md"} {
		if !strings.Contains(view, want) {
			t.Errorf("the open lane does not list %s:\n%s", want, view)
		}
	}
	// Its files are still folded: the lane level is above the file level, it
	// does not replace it.
	if strings.Contains(view, "func new() {}") {
		t.Errorf("opening a lane expanded a file's body too:\n%s", view)
	}

	// The cursor walks into the lane's files, and the file it lands on folds
	// on the same key.
	diffPress(p, "down")
	diffPress(p, "enter")
	if view = diffView(p, 30); !strings.Contains(view, "func new() {}") {
		t.Errorf("enter on a file inside a lane did not expand it:\n%s", view)
	}

	// Closing the lane takes its files off screen and leaves the cursor on the
	// lane rather than pointing into a fold that is shut.
	p.cursor = 0
	p.syncCursorPath()
	diffPress(p, "enter")
	view = diffView(p, 30)
	if strings.Contains(view, "README.md") {
		t.Errorf("closing the lane left its files on screen:\n%s", view)
	}
	if !strings.HasPrefix(p.cursorPath, diffKeySep+"lane") {
		t.Errorf("the cursor is on %q, want the lane it just closed", p.cursorPath)
	}
}

// TestDiffFileTouchedByTwoLanesAppearsUnderBoth. Two lanes really did change
// main.go, and a single row would have to pick one of them to credit. The
// counts differ per lane because each row is that lane's own hunks.
func TestDiffFileTouchedByTwoLanesAppearsUnderBoth(t *testing.T) {
	p := loadedLaneDiff(t)
	diffPress(p, "O")
	view := diffView(p, 60)

	if n := strings.Count(view, "main.go"); n < 2 {
		t.Errorf("main.go appears %d times, want a row under each lane that touched it:\n%s", n, view)
	}
	// Both lanes' own hunks are on screen at once, which is only possible if
	// the file was not collapsed into one row.
	for _, want := range []string{"func new() {}", "func alsoNew() {}"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanding everything lost %q:\n%s", want, view)
		}
	}
	// The fold state is per lane, so folding main.go in one lane leaves the
	// other lane's copy open.
	p.cursor, p.cursorPath = 0, ""
	p.syncCursorPath()
	diffPress(p, "down") // the api lane's main.go
	diffPress(p, "enter")
	view = diffView(p, 60)
	if strings.Contains(view, "func new() {}") {
		t.Errorf("folding one lane's main.go did not hide its body:\n%s", view)
	}
	if !strings.Contains(view, "func alsoNew() {}") {
		t.Errorf("folding one lane's main.go folded the other lane's copy too:\n%s", view)
	}
}

// TestDiffWithoutLanesStaysFlat: the daemon answers a task that fanned nothing
// out with a single remainder section, and that must render as the file list
// it has always been — no outer level, and the fold state still keyed by path
// so nothing about task 012's tab changed for the tasks it was written for.
func TestDiffWithoutLanesStaysFlat(t *testing.T) {
	p := newDiffPane()
	p.openTask(1)
	p.apply(diffLoadedMsg{taskID: 1, sections: []apiclient.DiffSection{
		{Remainder: true, Diff: twoFileDiff},
	}})
	view := diffView(&p, 30)
	if strings.Contains(view, "lane") || strings.Contains(view, "own commits") {
		t.Errorf("a task with no lanes grew an outer level:\n%s", view)
	}
	if !strings.Contains(view, "2 files") || strings.Contains(view, "lanes") {
		t.Errorf("the summary counts lanes that do not exist:\n%s", view)
	}
	diffPress(&p, "enter")
	if !p.open["main.go"] {
		t.Errorf("the flat fold state is no longer keyed by path: %v", p.open)
	}
}

// TestDiffLaneWithNoChangesSaysSo: a lane that merged cleanly and changed
// nothing is a fact worth a row. `+0 -0` over an empty list would read as a
// rendering fault instead.
func TestDiffLaneWithNoChangesSaysSo(t *testing.T) {
	p := newDiffPane()
	p.openTask(1)
	p.apply(diffLoadedMsg{taskID: 1, sections: []apiclient.DiffSection{
		{LaneID: "empty", ChildTaskID: 9, MergeCommit: "ccccccc"},
		{LaneID: "real", ChildTaskID: 10, MergeCommit: "ddddddd", Diff: sampleDiff},
		{Remainder: true},
	}})
	view := diffView(&p, 30)
	if !strings.Contains(view, "lane empty") || !strings.Contains(view, "no changes") {
		t.Errorf("an empty lane is not stated as one:\n%s", view)
	}
}

// TestDiffLaneFoldsSurviveARefresh: the tab refetches on every refresh, and a
// lane the reader opened must not fold shut because the diff was re-parsed.
func TestDiffLaneFoldsSurviveARefresh(t *testing.T) {
	p := loadedLaneDiff(t)
	diffPress(p, "enter") // open the api lane
	diffPress(p, "down")  // onto its main.go
	diffPress(p, "enter") // open the file
	before := p.cursorPath

	p.apply(diffLoadedMsg{taskID: 1, sections: laneSections()})
	view := diffView(p, 40)
	if !strings.Contains(view, diffFoldOpen+" lane api") {
		t.Errorf("the refresh folded the open lane shut:\n%s", view)
	}
	if !strings.Contains(view, "func new() {}") {
		t.Errorf("the refresh folded the open file shut:\n%s", view)
	}
	if p.cursorPath != before {
		t.Errorf("the cursor moved to %q across the refresh, want %q", p.cursorPath, before)
	}
}
