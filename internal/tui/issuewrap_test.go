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

// Issue #795: the issue detail and the issue form clipped their text with a
// final ansi.Truncate instead of wrapping it (spec §15's wrap-or-truncate
// convention, task 135.1), the detail never showed an imported issue's sync
// state (§15 view 13: "the sync and write-back fields appear"), its link
// picker read the description only, and the form pinned its window to the
// top so a long labels picker pushed the key line off the pane.

const longIssueTitle = "A very long issue title that describes the problem in far more words " +
	"than any terminal row can hold, so that it must wrap onto continuation endmarker"

// longFixtureIssue is an imported issue with a 150-character title, seven
// labels, a long GitHub URL, a sync failure and a comment carrying a link.
func longFixtureIssue() *issueView {
	v := issueFixture()
	synced := time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
	v.issue.Title = longIssueTitle
	v.issue.Labels = []string{
		"area/tui", "needs-triage", "good-first-issue", "platform/windows",
		"platform/macos", "platform/linux", "lastlabelmarker",
	}
	v.issue.Source = &apiclient.IssueSource{
		Provider: "github", Repo: "octo-organisation/a-repository-with-a-long-name", Number: 41,
		URL:         "https://github.com/octo-organisation/a-repository-with-a-long-name/issues/41",
		RemoteState: "open",
	}
	v.issue.Sync = &apiclient.IssueSync{State: "failed", Reason: "rate_limited", LastSyncedAt: &synced}
	v.comments = []apiclient.IssueComment{{
		ID: 1, Author: "octocat", Body: "Repro is at [the gist](https://example.com/comment-gist).",
		CreatedAt: synced, UpdatedAt: synced,
	}}
	return v
}

// joinedPage is the whole page, stripped, with its lines joined by spaces so
// a wrapped phrase reads as one string.
func joinedPage(v *issueView, width int) string {
	page := v.pageLines(width)
	for i, l := range page {
		page[i] = strings.TrimSpace(ansi.Strip(l))
	}
	return strings.Join(page, " ")
}

func assertNoEllipsis(t *testing.T, lines []string) {
	t.Helper()
	for i, l := range lines {
		if strings.HasSuffix(strings.TrimRight(l, " "), "…") {
			t.Errorf("line %d was truncated: %q", i, l)
		}
	}
}

// F5: at 80 columns the title, every label and the URL are fully readable,
// and the source badge is a `source` fact rather than a tail of the head.
func TestIssueDetailWrapsHeadAndFacts(t *testing.T) {
	v := longFixtureIssue()
	const w = 80
	lines := renderedLines(v, w, 200)
	assertNoEllipsis(t, lines)
	page := joinedPage(v, w)
	if !strings.Contains(page, longIssueTitle) {
		t.Errorf("the 150-character title is not fully readable at %d columns", w)
	}
	for _, l := range v.issue.Labels {
		if !strings.Contains(page, l) {
			t.Errorf("label %q is not shown", l)
		}
	}
	if !strings.Contains(page, "lastlabelmarker") {
		t.Error("the seventh label was cut")
	}
	url := v.issue.Source.URL
	if !strings.Contains(strings.ReplaceAll(page, " ", ""), url) {
		t.Errorf("the source URL %q is not fully shown", url)
	}
	desc := lineOf(lines, "Description")
	src := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "source") && strings.Contains(l, "#41") {
			src = i
			break
		}
	}
	if src < 0 || (desc >= 0 && src > desc) {
		t.Errorf("no `source  owner/repo#N` fact in the facts block (source at %d, Description at %d):\n%s",
			src, desc, strings.Join(lines, "\n"))
	}
}

// F10: an imported issue shows its sync state, last-synced time and reason.
func TestIssueDetailShowsSyncState(t *testing.T) {
	v := longFixtureIssue()
	lines := renderedLines(v, 120, 200)
	i := -1
	for n, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "sync") {
			i = n
			break
		}
	}
	if i < 0 {
		t.Fatalf("no sync fact on an imported issue:\n%s", strings.Join(lines, "\n"))
	}
	near := strings.Join(lines[i:min(i+3, len(lines))], " ")
	if !strings.Contains(near, "failed") {
		t.Errorf("the sync fact does not carry the state: %q", near)
	}
	if !strings.Contains(near, "2026-10-20") {
		t.Errorf("the sync fact does not carry the last-synced time: %q", near)
	}
	if !strings.Contains(strings.ToLower(near), "rate") {
		t.Errorf("the sync fact does not carry the reason: %q", near)
	}
}

// F6: the delete prompt on an imported issue at 60×5 keeps the body plus the
// prompt within the pane and keeps its consequence sentence whole.
func TestIssueDeletePromptFitsAndWraps(t *testing.T) {
	v := longFixtureIssue()
	v.w.act = newIssueDelete(v.client, v.issue)
	v.w.act.stage = iaConfirm
	const w, h = 60, 5
	lines := renderedLines(v, w, h)
	if len(lines) > h {
		t.Errorf("the prompt drew %d lines in a %d-line pane:\n%s", len(lines), h, strings.Join(lines, "\n"))
	}
	wide := renderedLines(v, w, 20)
	assertNoEllipsis(t, wide)
	joined := make([]string, len(wide))
	for i, l := range wide {
		joined[i] = strings.TrimSpace(l)
	}
	if s := "vincent remembers the deletion, so a sync will not import it again."; !strings.Contains(strings.Join(joined, " "), s) {
		t.Errorf("the consequence sentence lost its end:\n%s", strings.Join(wide, "\n"))
	}
}

// A prompt whose note comes after its key line keeps the keys when the
// footer is cut: the note is the tail, not the key line (review F10).
func TestIssuePromptKeepsItsKeysUnderANote(t *testing.T) {
	v := longFixtureIssue()
	v.w.act = newIssueDelete(v.client, v.issue)
	v.w.act.stage = iaConfirm
	v.w.act.note = "the daemon refused the delete because the remote is live and it says so at length"
	for _, h := range []int{2, 4, 6} {
		lines := renderedLines(v, 40, h)
		if len(lines) > h {
			t.Errorf("40x%d: the prompt drew %d lines", h, len(lines))
		}
		if !shows(lines, "y confirms") {
			t.Errorf("40x%d: the answer keys are gone:\n%s", h, strings.Join(lines, "\n"))
		}
	}
}

// The issues list shares the detail's prompts, and fits them the same way:
// the list draws no more lines than its pane and keeps the keys (review F11).
func TestIssuesListFitsAWrappedPrompt(t *testing.T) {
	v := issuesFixture()
	v.cursor = 1
	v.w.act = newIssueDelete(v.client, v.issues[1])
	v.w.act.stage = iaConfirm
	for _, h := range []int{4, 6, 8, 12} {
		lines := renderedLines(v, 60, h)
		if len(lines) > h {
			t.Errorf("60x%d: the list drew %d lines:\n%s", h, len(lines), strings.Join(lines, "\n"))
		}
		if !shows(lines, "y confirms") {
			t.Errorf("60x%d: the answer keys are gone:\n%s", h, strings.Join(lines, "\n"))
		}
	}
}

// F6: a long note wraps rather than losing its end.
func TestIssueDetailNoteWraps(t *testing.T) {
	v := longFixtureIssue()
	note := "comment on issue #5: the daemon refused the request because the remote is live and noteendmarker"
	v.setNote(note, true)
	lines := renderedLines(v, 80, 24)
	assertNoEllipsis(t, lines)
	if !shows(lines, "noteendmarker") {
		t.Errorf("the note lost its end:\n%s", strings.Join(lines, "\n"))
	}
}

// F7: ctrl+l lists the links in the comments and the source URL, not only
// the description's.
func TestIssueLinkPickerCoversThreadAndSource(t *testing.T) {
	v := longFixtureIssue()
	_, cmd := v.update(keyPress(linkPickKey))
	if cmd == nil {
		t.Fatal("ctrl+l did nothing")
	}
	msg, ok := cmd().(openLinkPickerMsg)
	if !ok {
		t.Fatal("ctrl+l did not open the link picker")
	}
	dests := map[string]bool{}
	for _, it := range msg.items {
		dests[it.link.dest] = true
	}
	for _, want := range []string{"https://example.com/mock", "https://example.com/comment-gist", v.issue.Source.URL} {
		if !dests[want] {
			t.Errorf("the link picker does not list %q (has %v)", want, dests)
		}
	}
}

// F7: ctrl+y opens the copy picker on the detail.
func TestIssueCopyPickerOpens(t *testing.T) {
	v := longFixtureIssue()
	_, cmd := v.update(keyPress(copyPickKey))
	if cmd == nil {
		t.Fatal("ctrl+y did nothing on the issue detail")
	}
	msg, ok := cmd().(openCopyPickerMsg)
	if !ok {
		t.Fatal("ctrl+y did not open the copy picker")
	}
	if len(msg.items) < 4 {
		t.Errorf("the copy picker offers %d items, want the title, description, comment and URL", len(msg.items))
	}
}

// F9: with fifteen labels and the picker open on its last option, the form
// keeps the highlighted option and the key line on screen at 80×24 and 80×18.
func TestIssueFormWindowsAroundThePicker(t *testing.T) {
	for _, h := range []int{24, 18} {
		t.Run(fmt.Sprintf("80x%d", h), func(t *testing.T) {
			_, f := issueFormFixture(t)
			for i := 1; i <= 15; i++ {
				f.catalog = append(f.catalog, apiclient.IssueLabel{Name: fmt.Sprintf("label-%02d", i)})
			}
			f.cursor = ifLabels
			f.activate()
			if f.pick == nil {
				t.Fatal("enter on labels did not open the picker")
			}
			for range 14 {
				f.updatePicking(keyPress("down"))
			}
			lines := strings.Split(ansi.Strip(f.render(80, h)), "\n")
			if len(lines) > h {
				t.Errorf("the form drew %d lines in a %d-line pane", len(lines), h)
			}
			if !shows(lines, "label-15") {
				t.Errorf("the highlighted option label-15 is off screen:\n%s", strings.Join(lines, "\n"))
			}
			if !shows(lines, "ctrl+s save") {
				t.Errorf("the key line is off screen:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// While the description is typed into, the window keeps the row its cursor
// is on, even where the heading and eight textarea rows are taller than the
// room (review F12).
func TestIssueFormKeepsTheDescriptionCursorOnScreen(t *testing.T) {
	for _, h := range []int{24, 14, 10, 8} {
		t.Run(fmt.Sprintf("80x%d", h), func(t *testing.T) {
			_, f := issueFormFixture(t)
			rows := make([]string, 30)
			for i := range rows {
				rows[i] = fmt.Sprintf("row%02d", i)
			}
			f.body.SetValue(strings.Join(rows, "\n"))
			f.cursor = ifBody
			f.activate()
			if !f.editing {
				t.Fatal("enter on the description did not start editing it")
			}
			f.body.ta.MoveToEnd()
			// A key through the textarea scrolls its view to the cursor,
			// the way typing does.
			f.body, _ = f.body.Update(keyPress("down"))
			lines := strings.Split(ansi.Strip(f.render(80, h)), "\n")
			if len(lines) > h {
				t.Errorf("the form drew %d lines in a %d-line pane", len(lines), h)
			}
			if !shows(lines, "row29") {
				t.Errorf("the cursor's row, row29, is off screen:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// Typing a label the catalog lacks, or filtering it, keeps the input on
// screen: the window follows the field the keys go to, not the picker's
// heading (review F2).
func TestIssueFormWindowsAroundThePickersInputs(t *testing.T) {
	for _, h := range []int{24, 18, 14} {
		for _, mode := range []string{"free", "filter"} {
			t.Run(fmt.Sprintf("%s 80x%d", mode, h), func(t *testing.T) {
				_, f := issueFormFixture(t)
				for i := 1; i <= 15; i++ {
					f.catalog = append(f.catalog, apiclient.IssueLabel{Name: fmt.Sprintf("label-%02d", i)})
				}
				f.cursor = ifLabels
				f.activate()
				if f.pick == nil {
					t.Fatal("enter on labels did not open the picker")
				}
				if mode == "free" {
					f.pick.startFree()
					f.pick.input.SetValue("typedmarker")
				} else {
					f.pick.filtering = true
					f.pick.filter.SetValue("typedmarker")
				}
				lines := strings.Split(ansi.Strip(f.render(80, h)), "\n")
				if len(lines) > h {
					t.Errorf("the form drew %d lines in a %d-line pane", len(lines), h)
				}
				if !shows(lines, "typedmarker") {
					t.Errorf("the %s input is off screen:\n%s", mode, strings.Join(lines, "\n"))
				}
			})
		}
	}
}

// Through the root model at 80×24, the detail of the long imported issue
// leaves nothing for frame() to cut, at every scroll position.
func TestIssueDetailNeverClipsAt80x24(t *testing.T) {
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v := longFixtureIssue()
	m.views[viewIssue] = v
	m.active = viewIssue
	if !strings.Contains(ansi.Strip(content(m)), "lastlabelmarker") {
		t.Fatalf("the root is not drawing the issue detail:\n%s", ansi.Strip(content(m)))
	}
	for range 40 {
		assertNoFrameClip(t, content(m))
		v.scrollBy(3)
	}
	assertNoFrameClip(t, content(m))
}
