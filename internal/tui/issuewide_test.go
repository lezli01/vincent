package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// wideIssue is an imported issue with a comment and a main worktree, on a
// terminal wide enough for the two panes.
func wideIssue(t *testing.T) *issueView {
	t.Helper()
	v := newIssueView(nil, nil)
	v.client = offlineClient()
	v.id = 8
	task := int64(15)
	v.issue = apiclient.Issue{
		ID: 8, ProjectID: 1, Title: "Header flickers on first paint", State: "open", Lane: "in_progress",
		Body: "The header renders twice.", Author: "mira", Labels: []string{"area/header"},
		Source:       &apiclient.IssueSource{Provider: "github", Ref: "acme/web#142", URL: "https://example.test/142", RemoteState: "open"},
		MainWorktree: &apiclient.IssueMainWorktree{Branch: "feat/15-header", OccupantTaskID: &task},
	}
	at := time.Date(2026, 9, 29, 12, 2, 0, 0, time.UTC)
	v.comments = []apiclient.IssueComment{{Author: "devon", Body: "Reproduced on 18.1.", Remote: true, CreatedAt: at, UpdatedAt: at}}
	v.loaded = true
	v.update(tea.WindowSizeMsg{Width: 140, Height: 40})
	return v
}

// The wide layout fills the pane it is given, every line to the same width, with
// the band, the conversation and the details all on screen.
func TestIssueWideFillsItsPane(t *testing.T) {
	v := wideIssue(t)
	if !v.frameless() {
		t.Fatal("a 140×40 terminal should get the wide layout")
	}
	out := v.render(140, 36)
	lines := strings.Split(out, "\n")
	if len(lines) != 36 {
		t.Fatalf("rendered %d lines, want 36", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 140 {
			t.Errorf("line %d is %d cells wide, want 140: %q", i, w, ansi.Strip(l))
		}
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"#8", "Header flickers", "In progress", "Details", "acme/web#142", "Description", "devon", "Main worktree"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the wide layout lost %q:\n%s", want, plain)
		}
	}
}

// Facts live in the details pane alone: the scrolling page does not repeat them.
func TestIssueWidePageLeavesTheFactsToTheDetails(t *testing.T) {
	v := wideIssue(t)
	page := ansi.Strip(strings.Join(v.pageLines(90), "\n"))
	for _, gone := range []string{"priority", "provider", "Header flickers"} {
		if strings.Contains(page, gone) {
			t.Errorf("the page repeats %q:\n%s", gone, page)
		}
	}
}

// A click lands on what is drawn under it: the strip's tabs, a selectable row of
// the page, and nothing at all in the details pane beside that row.
func TestIssueWideClicks(t *testing.T) {
	v := wideIssue(t)
	v.tasks = []apiclient.Task{{ID: 15, Title: "stop the flicker", State: "running"}}
	screen := strings.Split(ansi.Strip(v.render(140, 36)), "\n")

	work := v.tabHits[1]
	if got := ansi.Cut(screen[v.stripY], work.x0, work.x1); got != "Work" {
		t.Fatalf("the Work tab's hit box covers %q", got)
	}
	v.update(tea.MouseClickMsg{X: work.x0, Y: v.stripY, Button: tea.MouseLeft})
	if v.tab != issueTabWork {
		t.Fatal("a click on Work did not select it")
	}
	v.update(tea.MouseClickMsg{X: v.tabHits[0].x0, Y: v.stripY, Button: tea.MouseLeft})
	v.render(140, 36)

	if len(v.rowLines) < 2 {
		t.Fatalf("want at least two selectable rows, got %v", v.rowLines)
	}
	y := v.viewTop + v.rowLines[1] - v.scroll
	v.update(tea.MouseClickMsg{X: v.paneW, Y: y, Button: tea.MouseLeft})
	if v.cursor != 0 {
		t.Fatalf("a click in the details pane moved the selection to %d", v.cursor)
	}
	v.update(tea.MouseClickMsg{X: 4, Y: y, Button: tea.MouseLeft})
	if v.cursor != 1 {
		t.Fatalf("a click on row 1 selected %d", v.cursor)
	}
}

// A long title wraps in the band rather than being cut at one line (#795).
func TestIssueWideBandWrapsALongTitle(t *testing.T) {
	v := wideIssue(t)
	v.issue.Title = strings.TrimSpace(strings.Repeat("flicker ", 24)) + " tail"
	band := ansi.Strip(strings.Join(v.issueBand(140), "\n"))
	if !strings.Contains(band, "tail") {
		t.Fatalf("the band cut the title:\n%s", band)
	}
}

// Details that do not fit say how much is missing.
func TestIssueWideDetailsSayWhatIsCut(t *testing.T) {
	v := wideIssue(t)
	pane := ansi.Strip(strings.Join(v.issueDetailsPane(40, 8), "\n"))
	if !strings.Contains(pane, "more line") {
		t.Fatalf("a cut details pane did not say so:\n%s", pane)
	}
}

// Too narrow, too short, or with the form open, the page keeps the root's frame.
func TestIssueWideGivesWay(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 119, Height: 40}, {Width: 140, Height: 17}} {
		v := wideIssue(t)
		v.update(size)
		if v.frameless() {
			t.Errorf("%d×%d should keep the framed page", size.Width, size.Height)
		}
		if !shows(renderedLines(v, size.Width-2, size.Height-2), "priority") {
			t.Errorf("%d×%d lost the facts from the page", size.Width, size.Height)
		}
	}
}
