package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issue preview (task 150.3). Pure model tests, one property each (task 129
// decision 7).

func previewFixture() *issuesView {
	v := boardFixture()
	v.render(180, 40)
	return v
}

// previewKeyOf is the read the selection calls for.
func previewKeyOf(t *testing.T, v *issuesView) issuePreviewKey {
	t.Helper()
	row, ok := v.current()
	if !ok {
		t.Fatal("nothing is selected")
	}
	return issuePreviewKey{row.issue.ID, row.issue.Version}
}

// The pane appears only where the four columns can spare the width.
func TestIssuePreviewNeedsAWideTerminal(t *testing.T) {
	v := boardFixture()
	if out := ansi.Strip(v.render(issuePreviewMinW-1, 40)); strings.Contains(out, "≡ Description") {
		t.Errorf("a narrow terminal drew the preview:\n%s", out)
	}
	if out := ansi.Strip(v.render(issuePreviewMinW, 40)); !strings.Contains(out, "≡ Description") {
		t.Errorf("a wide terminal drew no preview:\n%s", out)
	}
}

// Nothing runs past the terminal's edges with the pane up.
func TestIssuePreviewFitsTheScreen(t *testing.T) {
	v := previewFixture()
	key := previewKeyOf(t, v)
	v.applyPreview(issuePreviewMsg{key: key, body: strings.Repeat("A long description line. ", 200)})
	lines := strings.Split(v.render(180, 40), "\n")
	if len(lines) != 40 {
		t.Errorf("drew %d lines, want 40", len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 180 {
			t.Errorf("line %d is %d wide, want 180", i, got)
		}
	}
}

// The facts come from the row, with no read needed.
func TestIssuePreviewShowsTheCard(t *testing.T) {
	v := boardFixture()
	v.now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	v.issues = []apiclient.Issue{{
		ID: 9, Title: "Every service needs a tenant column", State: "open", Lane: "in_progress",
		Kind: "chore", Labels: []string{"migrations"}, Author: "lena", Priority: 2, TaskCount: 1, Active: true,
		Attention: true, CreatedAt: v.now().Add(-48 * time.Hour),
		MainWorktree: &apiclient.IssueMainWorktree{Branch: "vincent/9-tenant"},
	}}
	v.clampCursor()
	out := ansi.Strip(strings.Join(v.previewPane(50, 34), "\n"))
	for _, want := range []string{
		"#9", "Every service needs", "◐ In progress", "! needs you", "by lena · opened 2d ago · high priority",
		"● chore", "migrations", "▶ Work", "1 task · needs you", "vincent/9-tenant", "Loading…",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview is missing %q:\n%s", want, out)
		}
	}
}

func TestIssuePreviewDescriptionStates(t *testing.T) {
	for _, c := range []struct {
		name string
		msg  issuePreviewMsg
		want string
	}{
		{"text", issuePreviewMsg{body: "The **whole** story."}, "The whole story."},
		{"empty", issuePreviewMsg{}, "No description."},
		{"failed", issuePreviewMsg{err: errors.New("daemon said no")}, "Could not read the description: daemon said no"},
	} {
		v := previewFixture()
		c.msg.key = previewKeyOf(t, v)
		v.applyPreview(c.msg)
		if out := ansi.Strip(strings.Join(v.previewPane(56, 34), "\n")); !strings.Contains(out, c.want) {
			t.Errorf("%s: the preview is missing %q:\n%s", c.name, c.want, out)
		}
	}
}

// A description is remote text: an escape sequence in it must never reach the
// terminal, and its line breaks must survive.
func TestIssuePreviewSanitizesTheDescription(t *testing.T) {
	v := previewFixture()
	key := previewKeyOf(t, v)
	v.applyPreview(issuePreviewMsg{key: key, body: "first\x1b[2J\x1b]0;owned\x07\n\nsecond"})
	if got := v.previews[key.id].body; strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x07') {
		t.Fatalf("the kept description carries a control: %q", got)
	}
	out := ansi.Strip(strings.Join(v.previewPane(56, 34), "\n"))
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview lost %q:\n%s", want, out)
		}
	}
}

// A description too long for the pane says where the rest is.
func TestIssuePreviewCutsALongDescription(t *testing.T) {
	v := previewFixture()
	v.applyPreview(issuePreviewMsg{key: previewKeyOf(t, v), body: strings.Repeat("line\n\n", 100)})
	lines := v.previewPane(56, 30)
	if len(lines) != 30 {
		t.Fatalf("the pane is %d lines, want 30", len(lines))
	}
	if out := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(out, "enter opens the full issue") {
		t.Errorf("the cut description has no cue:\n%s", out)
	}
}

// Holding an arrow key waits for the selection to rest: a wait that ends on a
// card since left reads nothing.
func TestIssuePreviewReadsOnlyWhereTheSelectionRests(t *testing.T) {
	v := previewFixture()
	first := previewKeyOf(t, v)
	if cmd := v.previewCmd(); cmd == nil {
		t.Fatal("selecting a card armed no read")
	}
	if cmd := v.previewCmd(); cmd != nil {
		t.Fatal("a read armed twice for one card")
	}
	v.moveCursor(1)
	if cmd := v.previewCmd(); cmd == nil {
		t.Fatal("moving to another card armed no read")
	}
	if cmd := v.previewDue(first); cmd != nil {
		t.Fatal("a wait for a card since left read it")
	}
	if cmd := v.previewDue(previewKeyOf(t, v)); cmd == nil {
		t.Fatal("the card the selection rests on was not read")
	}
}

// A kept description is not read again until its issue changes.
func TestIssuePreviewKeepsAReadUntilTheVersionMoves(t *testing.T) {
	v := previewFixture()
	key := previewKeyOf(t, v)
	v.applyPreview(issuePreviewMsg{key: key, body: "kept"})
	if cmd := v.previewCmd(); cmd != nil {
		t.Fatal("a kept description was read again")
	}
	for i := range v.issues {
		if v.issues[i].ID == key.id {
			v.issues[i].Version++
		}
	}
	if cmd := v.previewCmd(); cmd == nil {
		t.Fatal("a new version of the issue was not read")
	}
	if out := ansi.Strip(strings.Join(v.previewPane(56, 34), "\n")); !strings.Contains(out, "kept") {
		t.Errorf("the older text left the pane while the new one loads:\n%s", out)
	}
}

// Narrow terminals never read a description they cannot show.
func TestIssuePreviewReadsNothingWhenHidden(t *testing.T) {
	v := boardFixture()
	v.render(120, 40)
	if cmd := v.previewCmd(); cmd != nil {
		t.Fatal("a hidden preview armed a read")
	}
	if _, cmd := v.update(tea.WindowSizeMsg{Width: 180, Height: 40}); cmd == nil {
		t.Fatal("growing the terminal did not arm the read")
	}
}

// `R` re-reads the descriptions along with the list.
func TestIssuePreviewRefreshForgetsWhatItKept(t *testing.T) {
	v := previewFixture()
	v.applyPreview(issuePreviewMsg{key: previewKeyOf(t, v), err: errors.New("refused")})
	v.updateKey(registryKey(t, "R"))
	if len(v.previews) != 0 {
		t.Fatalf("R kept %v", v.previews)
	}
}
