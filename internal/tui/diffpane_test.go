package tui

import (
	"net/http"
	"strings"
	"testing"

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

// TestDiffClassification: the ± file markers carry the same first character
// as content, and reading them as one added and one removed line per file is
// exactly the mistake a naive prefix check makes.
func TestDiffClassification(t *testing.T) {
	want := []diffClass{
		diffFile, diffFile, diffFile, diffFile,
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

func TestDiffRendersFetchedContent(t *testing.T) {
	p := newDiffPane()
	p.open(1)
	p.apply(diffLoadedMsg{taskID: 1, text: sampleDiff})

	view := p.render(80, 20)
	if !strings.Contains(view, "func new() {}") {
		t.Errorf("diff body missing:\n%s", view)
	}
}

// TestDiffTruncatesLongOutput bounds the terminal, not the truth: the whole
// change stays on the branch.
func TestDiffTruncatesLongOutput(t *testing.T) {
	p := newDiffPane()
	p.open(1)
	p.apply(diffLoadedMsg{taskID: 1, text: strings.Repeat("+line\n", maxDiffLines+100)})

	if !p.truncated || len(p.lines) != maxDiffLines {
		t.Fatalf("lines = %d, truncated = %v; want a capped render", len(p.lines), p.truncated)
	}
	if !strings.Contains(p.render(80, maxDiffLines+10), "truncated") {
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
			p.open(1)
			tc.set(&p)
			if got := p.render(80, 10); !strings.Contains(got, tc.want) {
				t.Errorf("state = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// TestDiffDropsAnotherTasksResponse: a late fetch for the task the view has
// left must not paint over the one it moved to.
func TestDiffDropsAnotherTasksResponse(t *testing.T) {
	p := newDiffPane()
	p.open(2)
	p.apply(diffLoadedMsg{taskID: 1, text: sampleDiff})
	if p.loaded {
		t.Error("a diff for another task was installed")
	}
}
