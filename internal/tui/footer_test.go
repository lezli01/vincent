package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// footerTarget is a task with enough actions to make the line long.
var footerTarget = taskActions{id: 12, state: stateRunning, actions: []string{
	apiclient.ActionPause, apiclient.ActionCancel, apiclient.ActionArchive,
}}

// TestFooterPinnedSurvivesNarrow is the T3.12 done-when: however narrow the
// terminal, the line never wraps, never exceeds the width, and the pinned
// `: commands ? help q quit` segment is never truncated — `:` is the escape
// hatch that makes every other key optional.
func TestFooterPinnedSurvivesNarrow(t *testing.T) {
	bar := &actionBar{}
	for _, width := range []int{120, 80, 60, 40, 30} {
		line := renderFooter(width, bindingsFor(ctxTasks), bar, footerTarget, 3, true)
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d: the footer wrapped", width)
		}
		if w := ansi.StringWidth(line); w > width {
			t.Fatalf("width %d: footer is %d cells wide", width, w)
		}
		plain := ansi.Strip(line)
		for _, pinned := range []string{": commands", "? help", "q quit"} {
			if !strings.Contains(plain, pinned) {
				t.Fatalf("width %d: pinned segment lost %q: %q", width, pinned, plain)
			}
		}
	}
	// The narrow lines actually truncated — from the left, with the marker.
	narrow := ansi.Strip(renderFooter(40, bindingsFor(ctxTasks), bar, footerTarget, 3, true))
	if !strings.HasPrefix(narrow, "…") {
		t.Fatalf("narrow footer did not left-truncate: %q", narrow)
	}
}

// TestFooterFiveKeyCap: the panel segment shows at most five keys, in
// priority order.
func TestFooterFiveKeyCap(t *testing.T) {
	rows := make([]binding, 0, 7)
	for i := range 7 {
		rows = append(rows, binding{
			key: "k", hint: "k hint" + strings.Repeat("!", i+1), priority: 7 - i,
		})
	}
	hints := footerHintSegs(rows)
	if len(hints) != maxFooterHints {
		t.Fatalf("footer shows %d hints, want the cap of %d", len(hints), maxFooterHints)
	}
	// Priority order: the lowest numbers made the cut, lowest first.
	if !strings.Contains(hints[0].text, strings.Repeat("!", 7)) {
		t.Errorf("first hint = %q, want the priority-1 row", ansi.Strip(hints[0].text))
	}
}

// TestFooterAttentionHintGatedOnCount: the `!` hint exists exactly when the
// count is non-zero (§15).
func TestFooterAttentionHintGatedOnCount(t *testing.T) {
	bar := &actionBar{}
	quiet := ansi.Strip(renderFooter(120, nil, bar, taskActions{}, 0, false))
	if strings.Contains(quiet, "next attention") {
		t.Errorf("attention hint shown at zero: %q", quiet)
	}
	busy := ansi.Strip(renderFooter(120, nil, bar, taskActions{}, 2, false))
	if !strings.Contains(busy, "! next attention (2)") {
		t.Errorf("attention hint missing at two: %q", busy)
	}
}

// TestFooterConfirmReplacesLeft: a pending confirmation owns the keyboard,
// so it owns the left of the footer too — the pinned chrome survives.
func TestFooterConfirmReplacesLeft(t *testing.T) {
	bar := &actionBar{}
	bar.handleKey("c", nil, footerTarget) // cancel wants a y/n first
	line := ansi.Strip(renderFooter(120, bindingsFor(ctxTasks), bar, footerTarget, 5, false))
	if !strings.Contains(line, "cancel #12?") {
		t.Fatalf("footer does not ask the pending question: %q", line)
	}
	for _, hidden := range []string{"enter open", "/ filter", "next attention"} {
		if strings.Contains(line, hidden) {
			t.Errorf("footer still shows %q under a confirmation: %q", hidden, line)
		}
	}
	if !strings.Contains(line, ": commands") {
		t.Errorf("pinned segment lost under a confirmation: %q", line)
	}
}

// TestFooterAnswerHint: a task waiting on input advertises the popup key.
func TestFooterAnswerHint(t *testing.T) {
	bar := &actionBar{}
	waiting := taskActions{id: 4, state: stateAwaitingInput, actions: []string{apiclient.ActionAnswer}}
	line := ansi.Strip(renderFooter(120, nil, bar, waiting, 1, false))
	if !strings.Contains(line, "enter answer") {
		t.Errorf("footer misses the answer hint: %q", line)
	}
}

// TestFooterFollowsPanelFocus: the panel segment is the focused panel's —
// the root reads the same context the palette does.
func TestFooterFollowsPanelFocus(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	if got := ansi.Strip(m.footerLine()); !strings.Contains(got, "enter open") {
		t.Fatalf("tasks-panel footer = %q, want its keys", got)
	}
	m.Update(selectTaskMsg{id: 1})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := ansi.Strip(m.footerLine()); !strings.Contains(got, "tab views") {
		t.Fatalf("output-tab footer = %q, want its keys", got)
	}
	m.Update(selectViewMsg{id: viewProjects})
	if got := ansi.Strip(m.footerLine()); !strings.Contains(got, "a add") {
		t.Fatalf("projects footer = %q, want its keys", got)
	}
}
