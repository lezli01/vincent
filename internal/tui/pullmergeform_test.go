package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func pullMergeFixture() *pullMergeForm {
	f := newPullMergeForm(7)
	f.facts = pullMergeFacts{
		subject:    "#12",
		title:      "Refactor the session cache so a reconnect no longer drops the subscriber it was holding (end)",
		headBranch: "vincent/7-session-cache",
		baseBranch: "master",
		head:       "0123456789abcdef0123456789abcdef01234567",
		checks:     "2 passed, 1 failing: integration (ubuntu-latest) · lint (windows-latest) (checks-end)",
		mergeable:  true,
	}
	return f
}

// A long title and checks summary wrap rather than ellipsising (#804 PL-1).
func TestPullMergeWrapsTitleAndChecks(t *testing.T) {
	f := pullMergeFixture()
	text := strings.Join(strings.Fields(ansi.Strip(f.render(50, 40))), " ")
	for _, want := range []string{"(end)", "(checks-end)", "vincent/7-session-cache → master"} {
		if !strings.Contains(text, want) {
			t.Errorf("the 50-column merge popup lost %q:\n%s", want, f.render(50, 40))
		}
	}
	for _, line := range strings.Split(f.render(50, 40), "\n") {
		if w := ansi.StringWidth(line); w > 50 {
			t.Errorf("line is %d cells wide in a 50-column popup: %q", w, ansi.Strip(line))
		}
	}
}

// The wrap is capped at three lines each, so a runaway summary cannot take
// the popup's whole budget.
func TestPullMergeCapsTheWrap(t *testing.T) {
	f := pullMergeFixture()
	f.facts.checks = strings.Repeat("a failing check ", 40)
	f.facts.title = strings.Repeat("a long title ", 40)
	if h := f.height(50); h > 3+4+3+4 {
		t.Errorf("height = %d, want the subject and the checks capped at three lines each", h)
	}
}

// The key line survives a popup too short for the whole form (#804 PL-1).
func TestPullMergeKeepsKeysInAShortPopup(t *testing.T) {
	f := pullMergeFixture()
	f.method = 1
	out := f.render(50, 6)
	if n := len(strings.Split(out, "\n")); n > 6 {
		t.Errorf("render(50, 6) is %d lines tall", n)
	}
	if !strings.Contains(strings.Join(strings.Fields(ansi.Strip(out)), " "), "n/esc cancel") {
		t.Errorf("the key line is off-screen in a 6-row popup:\n%s", ansi.Strip(out))
	}
}
