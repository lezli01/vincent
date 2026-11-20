package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// hunkyDiff is every shape the hunk parse has to read: a file with several
// hunks (one ending without a newline), a new file, a deleted file and a
// binary one.
const hunkyDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@ package main
 package main
-func old() {}
+func new() {}
@@ -20,2 +20,3 @@ func tail() {
 // tail
+// more tail
@@ -40,2 +40,2 @@
-last
\ No newline at end of file
+last line
\ No newline at end of file
diff --git a/added.txt b/added.txt
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/added.txt
@@ -0,0 +1,2 @@
+one
+two
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 4444444..0000000
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/logo.png b/logo.png
index 5555555..6666666 100644
Binary files a/logo.png and b/logo.png differ
`

// TestParseDiffFilesFindsHunks: each `@@` line opens a hunk at its place in
// the body, whatever the file is, and a binary file has none.
func TestParseDiffFilesFindsHunks(t *testing.T) {
	_, files := parseDiffFiles(splitDiff(hunkyDiff))
	if len(files) != 4 {
		t.Fatalf("files = %d, want 4", len(files))
	}
	want := []struct {
		path    string
		headers []string
	}{
		{"main.go", []string{"@@ -1,3 +1,3 @@ package main", "@@ -20,2 +20,3 @@ func tail() {", "@@ -40,2 +40,2 @@"}},
		{"added.txt", []string{"@@ -0,0 +1,2 @@"}},
		{"gone.txt", []string{"@@ -1 +0,0 @@"}},
		{"logo.png", nil},
	}
	for i, w := range want {
		f := files[i]
		if f.path != w.path {
			t.Fatalf("file %d = %q, want %q", i, f.path, w.path)
		}
		if len(f.hunks) != len(w.headers) {
			t.Fatalf("%s: %d hunks, want %d", f.path, len(f.hunks), len(w.headers))
		}
		for h, header := range w.headers {
			if got := f.body[f.hunks[h].start]; got != header {
				t.Errorf("%s hunk %d starts at %q, want %q", f.path, h, got, header)
			}
		}
	}
	// The `\ No newline` lines belong to the last hunk and are not changes.
	main := files[0]
	if end := main.hunkEnd(2); end != len(main.body) || main.body[end-1] != `\ No newline at end of file` {
		t.Errorf("last hunk ends at %d of %d", end, len(main.body))
	}
	if main.hunkEnd(0) != main.hunks[1].start {
		t.Errorf("first hunk does not end where the second starts")
	}
	if main.added != 3 || main.removed != 2 {
		t.Errorf("main.go counts = +%d -%d, want +3 -2", main.added, main.removed)
	}
	// A new file's mode line sits ahead of its only hunk.
	if files[1].hunks[0].start != 1 {
		t.Errorf("added.txt hunk starts at %d, want 1", files[1].hunks[0].start)
	}
}

// selectedHeader is the header of the hunk under the cursor, or "".
func selectedHeader(p *diffPane) string {
	ref, ok := p.selectedHunk()
	if !ok {
		return ""
	}
	return ref.path + " " + ref.header
}

// TestMoveHunkWalksAcrossFiles: the hunk cursor walks every hunk in row
// order, opening each folded file it lands in and taking the file cursor with
// it, and stops at both ends. The binary file is stepped over.
func TestMoveHunkWalksAcrossFiles(t *testing.T) {
	p := loadedDiff(t, hunkyDiff)
	if _, ok := p.selectedHunk(); ok {
		t.Fatal("a hunk is selected before any move")
	}
	if p.moveHunk(-1) {
		t.Fatal("moved backwards from the first row")
	}
	want := []string{
		"main.go @@ -1,3 +1,3 @@ package main",
		"main.go @@ -20,2 +20,3 @@ func tail() {",
		"main.go @@ -40,2 +40,2 @@",
		"added.txt @@ -0,0 +1,2 @@",
		"gone.txt @@ -1 +0,0 @@",
	}
	for i, w := range want {
		if !p.moveHunk(1) {
			t.Fatalf("move %d: no next hunk", i)
		}
		if got := selectedHeader(p); got != w {
			t.Fatalf("move %d: on %q, want %q", i, got, w)
		}
	}
	if p.moveHunk(1) {
		t.Fatalf("moved past the last hunk, onto %q", selectedHeader(p))
	}
	if got := selectedHeader(p); got != want[len(want)-1] {
		t.Fatalf("a refused move left the cursor on %q", got)
	}
	if p.cursorPath != "gone.txt" {
		t.Errorf("file cursor on %q, want gone.txt", p.cursorPath)
	}
	for _, path := range []string{"main.go", "added.txt", "gone.txt"} {
		if !p.open[path] {
			t.Errorf("%s was walked through and is still folded", path)
		}
	}
	if p.open["logo.png"] {
		t.Error("the binary file was opened by a walk that has nothing in it")
	}
	for i := len(want) - 2; i >= 0; i-- {
		if !p.moveHunk(-1) || selectedHeader(p) != want[i] {
			t.Fatalf("back to %d: on %q, want %q", i, selectedHeader(p), want[i])
		}
	}
	if p.moveHunk(-1) {
		t.Fatal("moved before the first hunk")
	}
}

// TestMoveHunkStartsFromTheFileCursor: a reader who walked the file list
// elsewhere means the hunks there.
func TestMoveHunkStartsFromTheFileCursor(t *testing.T) {
	p := loadedDiff(t, hunkyDiff)
	p.moveHunk(1)
	diffPress(p, "down") // added.txt
	diffPress(p, "down") // gone.txt
	if !p.moveHunk(1) || selectedHeader(p) != "gone.txt @@ -1 +0,0 @@" {
		t.Fatalf("on %q, want gone.txt's hunk", selectedHeader(p))
	}
	diffPress(p, "up") // added.txt's own row sorts ahead of its hunk
	if !p.moveHunk(-1) || selectedHeader(p) != "main.go @@ -40,2 +40,2 @@" {
		t.Fatalf("on %q, want main.go's last hunk", selectedHeader(p))
	}
}

// TestSelectedHunkIsDrawnWithAGlyph: the selected header is told apart with
// colour stripped, and it is scrolled into view in a pane shorter than the
// file above it.
func TestSelectedHunkIsDrawnWithAGlyph(t *testing.T) {
	p := loadedDiff(t, hunkyDiff)
	if strings.Contains(diffView(p, 40), "▶") {
		t.Fatal("a hunk is marked before any is selected")
	}
	p.moveHunk(1)
	p.moveHunk(1)
	view := diffView(p, 40)
	if !strings.Contains(view, "▶ @@ -20,2 +20,3 @@ func tail() {") {
		t.Fatalf("selected hunk not marked:\n%s", view)
	}
	if strings.Count(view, "▶") != 1 {
		t.Fatalf("more than one hunk marked:\n%s", view)
	}
	if !strings.Contains(view, "\n@@ -1,3 +1,3 @@ package main") {
		t.Fatalf("an unselected header changed:\n%s", view)
	}
	p.moveHunk(1)
	p.moveHunk(1)
	p.moveHunk(1)
	if short := diffView(p, 5); !strings.Contains(short, "▶ @@ -1 +0,0 @@") {
		t.Fatalf("the selected hunk is off screen:\n%s", short)
	}
	// Folding the file hides the selection rather than acting on it unseen;
	// opening it again shows it where it was.
	diffPress(p, "left")
	if _, ok := p.selectedHunk(); ok {
		t.Fatal("a hunk inside a folded file is still selected")
	}
	diffPress(p, "right")
	if got := selectedHeader(p); got != "gone.txt @@ -1 +0,0 @@" {
		t.Fatalf("reopened onto %q", got)
	}
}

// TestMoveHunkWithLanes: on a fan-out the walk crosses lanes, opening the
// lane as well as the file, and a file two lanes touched is two stops.
func TestMoveHunkWithLanes(t *testing.T) {
	p := loadedLaneDiff(t)
	var got []string
	for p.moveHunk(1) {
		ref, _ := p.selectedHunk()
		lane, _ := p.selectedLane()
		got = append(got, strconv.FormatInt(lane, 10)+" "+ref.path)
		if len(got) > 20 {
			t.Fatal("the walk does not end")
		}
	}
	if len(got) < 4 || got[0] != "7 main.go" || got[1] != "7 README.md" || got[2] != "8 main.go" {
		t.Fatalf("walk = %v", got)
	}
	if !strings.HasPrefix(got[len(got)-1], "0 ") {
		t.Fatalf("walk ends on %q, want the remainder", got[len(got)-1])
	}
	for i := range p.sections {
		if !p.open[p.sectionKey(i)] {
			t.Errorf("section %d was walked through and is still folded", i)
		}
	}
	if view := diffView(p, 60); strings.Count(view, "▶") != 1 {
		t.Fatalf("want exactly one marked hunk:\n%s", view)
	}
	// A lane's own row sorts ahead of everything in it.
	p.foldAll(false)
	p.cursor = 0
	p.syncCursorPath()
	diffPress(p, "down") // lane docs
	if !p.moveHunk(1) {
		t.Fatal("no hunk after the lane row")
	}
	if lane, _ := p.selectedLane(); lane != 8 {
		t.Fatalf("landed in lane task %d, want 8", lane)
	}
	if !p.moveHunk(-1) {
		t.Fatal("no hunk before lane docs")
	}
	if lane, _ := p.selectedLane(); lane != 7 {
		t.Fatalf("stepped back into lane task %d, want 7", lane)
	}
}

// TestHunkCursorSurvivesARefresh: a refetch re-parses the diff, and the hunk
// cursor is kept by path and index as the folds are kept by path.
func TestHunkCursorSurvivesARefresh(t *testing.T) {
	p := loadedDiff(t, hunkyDiff)
	p.moveHunk(1)
	p.moveHunk(1)
	extra := "diff --git a/aaa.txt b/aaa.txt\nindex 1..2 100644\n--- a/aaa.txt\n+++ b/aaa.txt\n@@ -1 +1 @@\n-a\n+b\n"
	p.apply(diffLoadedMsg{taskID: 1, text: extra + hunkyDiff})
	if got := selectedHeader(p); got != "main.go @@ -20,2 +20,3 @@ func tail() {" {
		t.Fatalf("after a refresh the cursor is on %q", got)
	}
	if !strings.Contains(diffView(p, 40), "▶ @@ -20,2 +20,3 @@") {
		t.Fatal("the kept cursor is not drawn")
	}
	// The file went away: so does the cursor, rather than landing somewhere
	// the reader never chose.
	p.apply(diffLoadedMsg{taskID: 1, text: extra})
	if _, ok := p.selectedHunk(); ok {
		t.Fatalf("cursor survived its file, on %q", selectedHeader(p))
	}
	// And another task's diff starts with none.
	p.moveHunk(1)
	p.openTask(2)
	p.apply(diffLoadedMsg{taskID: 2, text: extra})
	if _, ok := p.selectedHunk(); ok {
		t.Fatal("the hunk cursor followed the pane to another task")
	}
}

// reviewFiles is what the chat's pane hands in: files with daemon-made hunk
// ids, a binary file and a file that carries a note.
func reviewFiles() []diffFileInput {
	return []diffFileInput{
		{path: "main.go", added: 2, removed: 1, hunks: []diffHunkInput{
			{id: "h-a", header: "@@ -1,2 +1,2 @@", body: []string{" package main", "-old", "+new"}},
			{id: "h-b", header: "@@ -9,1 +9,2 @@", body: []string{" tail", "+more"}},
		}},
		{path: "logo.png", binary: true},
		{path: "big.sql", added: 1, note: "too large to reject", hunks: []diffHunkInput{
			{id: "h-n", header: "@@ -1 +1,2 @@", body: []string{" x", "+y"}},
		}},
		{path: "z.txt", added: 1, hunks: []diffHunkInput{
			{id: "h-z", header: "@@ -0,0 +1 @@", body: []string{"+z"}},
		}},
	}
}

func reviewPane() *diffPane {
	p := newDiffPane()
	p.setFiles(reviewFiles(), time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC))
	return &p
}

// TestSetFilesLoadsStructuredFiles: the pane is loaded without a fetch, opens
// folded like a task's, and the hunk under the cursor carries the caller's id.
func TestSetFilesLoadsStructuredFiles(t *testing.T) {
	p := reviewPane()
	if !p.loaded || p.grouped || !p.review {
		t.Fatalf("loaded=%v grouped=%v review=%v", p.loaded, p.grouped, p.review)
	}
	view := diffView(p, 20)
	for _, want := range []string{
		"4 files", "as of 09:30:00", "▸ main.go  +2 -1  ✓ 0/2", "▸ logo.png  binary",
		"too large to reject", "▸ z.txt  +1 -0  ✓ 0/1",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "reopen the tab") {
		t.Errorf("a structured load names the task tab's refresh:\n%s", view)
	}
	if strings.Contains(view, "@@") {
		t.Errorf("files did not open folded:\n%s", view)
	}

	var ids []string
	for p.moveHunk(1) {
		ref, ok := p.selectedHunk()
		if !ok {
			t.Fatal("moved and nothing is selected")
		}
		ids = append(ids, ref.path+":"+ref.id+":"+strconv.Itoa(ref.index))
	}
	// The binary file and the noted one have nothing to walk.
	if got := strings.Join(ids, " "); got != "main.go:h-a:0 main.go:h-b:1 z.txt:h-z:0" {
		t.Fatalf("walk = %q", got)
	}
	ref, _ := p.selectedHunk()
	if ref.header != "@@ -0,0 +1 @@" || ref.accepted {
		t.Fatalf("ref = %+v", ref)
	}
	// The noted file still shows its lines when opened.
	p.foldAll(true)
	if view := diffView(p, 40); !strings.Contains(view, "@@ -1 +1,2 @@") || !strings.Contains(view, "+y") {
		t.Fatalf("noted file's body missing:\n%s", view)
	}
}

// TestSetHunkAcceptedRedrawsTheMarks: one mark flips in place — the check
// glyph on the header and the count on the file's row — with colour stripped.
func TestSetHunkAcceptedRedrawsTheMarks(t *testing.T) {
	p := reviewPane()
	p.moveHunk(1)
	p.moveHunk(1) // h-b selected, main.go open
	if view := diffView(p, 30); strings.Contains(view, "✓ @@") {
		t.Fatalf("a hunk is checked before any is accepted:\n%s", view)
	}
	if !p.setHunkAccepted("main.go", "h-a", true) {
		t.Fatal("no such hunk")
	}
	view := diffView(p, 30)
	if !strings.Contains(view, "\n✓ @@ -1,2 +1,2 @@") || !strings.Contains(view, "main.go  +2 -1  ✓ 1/2") {
		t.Fatalf("accepted hunk not drawn:\n%s", view)
	}
	// Its lines are still there to read, and the styled cache says dimmed.
	if !strings.Contains(view, "+new") {
		t.Fatalf("accepted hunk lost its body:\n%s", view)
	}
	if got, want := p.sections[0].styledBody[0][2], styleDim.Render("-old"); got != want {
		t.Errorf("accepted line = %q, want it dimmed", got)
	}
	// Selected and accepted at once shows both glyphs.
	p.setHunkAccepted("main.go", "h-b", true)
	view = diffView(p, 30)
	if !strings.Contains(view, "▶ ✓ @@ -9,1 +9,2 @@") || !strings.Contains(view, "✓ 2/2") {
		t.Fatalf("selected accepted hunk not drawn:\n%s", view)
	}
	if ref, _ := p.selectedHunk(); !ref.accepted {
		t.Error("selectedHunk does not report the mark")
	}
	// And back.
	p.setHunkAccepted("main.go", "h-a", false)
	view = diffView(p, 30)
	if strings.Contains(view, "\n✓ @@ -1,2") || !strings.Contains(view, "✓ 1/2") {
		t.Fatalf("mark not cleared:\n%s", view)
	}
	if got, want := p.sections[0].styledBody[0][2], colorDiffLine("-old"); got != want {
		t.Errorf("cleared line = %q, want it coloured again", got)
	}
	for _, miss := range [][2]string{{"main.go", "nope"}, {"other.go", "h-a"}, {"big.sql", "h-n"}} {
		if p.setHunkAccepted(miss[0], miss[1], true) {
			t.Errorf("setHunkAccepted(%q, %q) found a hunk", miss[0], miss[1])
		}
	}
	// A task's diff has no ids to mark.
	if loadedDiff(t, sampleDiff).setHunkAccepted("main.go", "", true) {
		t.Error("a task's diff took a review mark")
	}
}

// TestSetFilesKeepsStateByIdentity: a second load keeps the folds, the file
// cursor and the hunk cursor — by id, so a hunk that moved is still selected,
// and when the selected hunk is gone the cursor is on what took its place.
func TestSetFilesKeepsStateByIdentity(t *testing.T) {
	p := reviewPane()
	p.moveHunk(1)
	p.moveHunk(1) // h-b

	// Accepted marks arrive with the files, and h-b is now first.
	files := reviewFiles()
	files[0].hunks = []diffHunkInput{files[0].hunks[1], files[0].hunks[0]}
	files[0].hunks[1].accepted = true
	p.setFiles(files, time.Time{})
	ref, ok := p.selectedHunk()
	if !ok || ref.id != "h-b" || ref.index != 0 {
		t.Fatalf("after a reload ref = %+v, %v", ref, ok)
	}
	if !p.open["main.go"] || p.cursorPath != "main.go" {
		t.Fatalf("fold or file cursor lost: open=%v cursor=%q", p.open, p.cursorPath)
	}
	if view := diffView(p, 30); !strings.Contains(view, "\n✓ @@ -1,2 +1,2 @@") || !strings.Contains(view, "✓ 1/2") {
		t.Fatalf("loaded marks not drawn:\n%s", view)
	}

	// h-b is gone (rejected): the cursor stays in the file, on a real hunk.
	files = reviewFiles()
	files[0].hunks = files[0].hunks[:1]
	p.setFiles(files, time.Time{})
	ref, ok = p.selectedHunk()
	if !ok || ref.path != "main.go" || ref.id != "h-a" {
		t.Fatalf("after its hunk went ref = %+v, %v", ref, ok)
	}
	if !p.moveHunk(1) {
		t.Fatal("the walk does not continue from the kept cursor")
	}
	if ref, _ = p.selectedHunk(); ref.id != "h-z" {
		t.Fatalf("next = %+v, want h-z", ref)
	}

	// The whole file is gone: no cursor, and the walk starts over.
	p.setFiles(reviewFiles()[:1], time.Time{})
	if _, ok := p.selectedHunk(); ok {
		t.Fatal("cursor survived its file")
	}
	if !p.moveHunk(1) {
		t.Fatal("no hunk to walk after the reload")
	}
	if ref, _ = p.selectedHunk(); ref.id != "h-a" {
		t.Fatalf("walk restarted on %+v", ref)
	}

	// Nothing left to review is said, not drawn as an empty list.
	p.setFiles(nil, time.Time{})
	if view := diffView(p, 10); !strings.Contains(view, "no changes to review") {
		t.Fatalf("empty review = %q", view)
	}
	if p.moveHunk(1) {
		t.Fatal("moved in an empty pane")
	}
	// A task fetch on the same pane switches review drawing off again.
	p.apply(diffLoadedMsg{taskID: p.taskID, sections: []apiclient.DiffSection{{Remainder: true, Diff: sampleDiff}}})
	if view := diffView(p, 10); strings.Contains(view, "✓") {
		t.Fatalf("a task's diff draws review marks:\n%s", view)
	}
}

// TestSetFilesKeepsTheLineCap: structured files spend the same budget a
// fetched diff does, and walking the cursor through the capped result
// assembles cached lines rather than restyling them.
func TestSetFilesKeepsTheLineCap(t *testing.T) {
	body := make([]string, 3000)
	for i := range body {
		body[i] = "+line " + strconv.Itoa(i)
	}
	pane := newDiffPane()
	p := &pane
	p.setFiles([]diffFileInput{
		{path: "a.txt", hunks: []diffHunkInput{{id: "1", header: "@@ -0,0 +1,3000 @@", body: body}}},
		{path: "b.txt", hunks: []diffHunkInput{
			{id: "2", header: "@@ -0,0 +1,3000 @@", body: body},
			{id: "3", header: "@@ -9 +9 @@", body: body[:1]},
		}},
		{path: "c.txt", hunks: []diffHunkInput{{id: "4", header: "@@ -1 +1 @@", body: body[:1]}}},
	}, time.Time{})
	if !p.truncated || len(p.lines) != maxDiffLines {
		t.Fatalf("lines = %d, truncated = %v; want a capped load", len(p.lines), p.truncated)
	}
	if n := p.fileCount(); n != 2 {
		t.Fatalf("files = %d, want the two the cap reached", n)
	}
	p.foldAll(true)
	view := diffView(p, 20)
	if !strings.Contains(view, "2 files") {
		t.Fatalf("view = %q", view)
	}
	if last := p.rows[len(p.rows)-1].text; !strings.Contains(last, "diff truncated") {
		t.Fatalf("last row = %q, want the truncation notice", last)
	}
	styled := &p.sections[0].styledBody[1][0]
	for p.moveHunk(1) {
		p.render(diffTestWidth, 20)
	}
	if ref, _ := p.selectedHunk(); ref.id != "2" {
		t.Fatalf("walk ended on %+v, want the cut hunk", ref)
	}
	if styled != &p.sections[0].styledBody[1][0] {
		t.Fatal("walking the hunk cursor rebuilt the styled cache")
	}
	// Every body line is still one row: 5000 lines, two file rows, the notice.
	if len(p.rows) != maxDiffLines+3 {
		t.Fatalf("rows = %d, want %d", len(p.rows), maxDiffLines+3)
	}
}
