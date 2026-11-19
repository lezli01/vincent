package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issues board's columns and cards (task 150.2). Pure model tests, one
// property each (task 129 decision 7).

// A box is exactly the size it was asked for, whatever is put in it.
func TestDrawBoxKeepsItsSize(t *testing.T) {
	body := []string{strings.Repeat("x", 80), "short", "one line too many", "and another"}
	lines := drawBox("a very long title that cannot fit", "99", body, 20, 4, edgesRounded, styleDim)
	if len(lines) != 4 {
		t.Fatalf("drew %d lines, want 4", len(lines))
	}
	for _, l := range lines {
		if got := ansi.StringWidth(l); got != 20 {
			t.Errorf("line %q is %d wide, want 20", ansi.Strip(l), got)
		}
	}
}

// A count that does not fit is dropped whole, never cut into a wrong number.
func TestDrawBoxDropsARightTitleThatDoesNotFit(t *testing.T) {
	top := ansi.Strip(drawBox("abcdefghij", "1234", nil, 16, 2, edgesRounded, styleDim)[0])
	if strings.Contains(top, "1234") || strings.Contains(top, "12 ") {
		t.Errorf("the top edge kept part of the count: %q", top)
	}
}

// Selection is told apart by the edges as well as the color, so it survives
// NO_COLOR.
func TestSelectedCardHasThickEdges(t *testing.T) {
	v := boardFixture()
	selected := ansi.Strip(strings.Join(v.issueCard(v.issues[1], 30), "\n"))
	other := ansi.Strip(strings.Join(v.issueCard(v.issues[4], 30), "\n"))
	if !strings.Contains(selected, "┏") {
		t.Errorf("the selected card has no thick edge:\n%s", selected)
	}
	if strings.Contains(other, "┏") {
		t.Errorf("an unselected card has a thick edge:\n%s", other)
	}
}

func TestIssueCardShowsItsDetails(t *testing.T) {
	v := boardFixture()
	v.now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	iss := apiclient.Issue{
		ID: 42, Title: "A title long enough that it has to wrap onto a second line and then some more",
		State: "open", Lane: "open", Kind: "bug", Labels: []string{"api"},
		UpdatedAt: v.now().Add(-3 * time.Hour),
		Source:    ghSource(&apiclient.IssueSource{Repo: "o/r", Number: 7}),
	}
	lines := v.issueCard(iss, 36)
	if len(lines) != issueCardH {
		t.Fatalf("the card is %d lines, want %d", len(lines), issueCardH)
	}
	out := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"#42", "◆ bug", "◷ 3h", "A title long", "…", "api", "o/r#7", "○ no tasks"} {
		if !strings.Contains(out, want) {
			t.Errorf("the card is missing %q:\n%s", want, out)
		}
	}
}

// Labels give way to a count before the source does: the source is what a
// person matches the card against at its origin (#796).
func TestIssueCardTagsKeepTheSource(t *testing.T) {
	iss := apiclient.Issue{
		Labels: []string{"frontend", "accessibility", "regression"},
		Source: ghSource(&apiclient.IssueSource{Repo: "octo-org/web-frontend", Number: 4127}),
	}
	line := ansi.Strip(issueCardTags(iss, 30, true))
	if !strings.Contains(line, "gh#4127") {
		t.Errorf("a shared repo did not shorten to gh#N: %q", line)
	}
	if !strings.Contains(line, "+") || strings.Contains(line, "regression") {
		t.Errorf("the labels did not give way to a count: %q", line)
	}
	if got := ansi.StringWidth(line); got > 30 {
		t.Errorf("the tags are %d wide, over 30: %q", got, line)
	}
	if mixed := ansi.Strip(issueCardTags(iss, 30, false)); strings.Contains(mixed, "gh#") || !strings.Contains(mixed, "#4127") {
		t.Errorf("mixed repos lost the source's number: %q", mixed)
	}
}

// The status line leads with what most needs a person.
func TestIssueCardStatus(t *testing.T) {
	for _, c := range []struct {
		name string
		iss  apiclient.Issue
		want string
	}{
		{"attention", apiclient.Issue{Lane: "in_progress", TaskCount: 1, Active: true, Attention: true}, "! 1 task · needs you"},
		{"closed", apiclient.Issue{Lane: "done", State: "closed", CloseReason: "not_planned"}, "✓ not planned"},
		{"running", apiclient.Issue{Lane: "in_progress", TaskCount: 2, Active: true}, "▶ 2 tasks"},
		{"handed off", apiclient.Issue{Lane: "hand_off", TaskCount: 3}, "✓ 3 done"},
		{"idle", apiclient.Issue{Lane: "open"}, "○ no tasks"},
	} {
		if got, _ := issueCardStatus(c.iss); got != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, got, c.want)
		}
	}
}

// A terminal too narrow for every column keeps the cursor's column on screen
// and says that others are hidden.
func TestIssuesBoardFollowsTheCursorWhenNarrow(t *testing.T) {
	v := boardFixture()
	v.selectIssue(10)
	out := ansi.Strip(v.render(60, 24))
	if !strings.Contains(out, "✓ Done") || strings.Contains(out, "○ Open") {
		t.Errorf("want the done column on screen and open hidden:\n%s", out)
	}
	if !strings.Contains(out, "‹") {
		t.Errorf("the board does not say columns are hidden to the left:\n%s", out)
	}
}

// The board draws its own boxes, and the forms keep the root's frame.
func TestIssuesFramelessOnlyWhileTheBoardShows(t *testing.T) {
	v := boardFixture()
	if !v.frameless() {
		t.Error("the board asked for the root's frame")
	}
	v.w.form = &issueForm{}
	if v.frameless() {
		t.Error("the form lost the root's frame")
	}
}
