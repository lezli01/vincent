package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// bigCatalog is a cursor-sized model list (§9.7): far more options than any
// terminal can show, which is the condition the window exists for.
func bigCatalog(n int) []pickerOption {
	out := make([]pickerOption, 0, n)
	for i := range n {
		out = append(out, pickerOption{
			value: fmt.Sprintf("model-%03d", i),
			label: fmt.Sprintf("model-%03d", i),
			note:  "cli",
		})
	}
	// Two recognizable ids to filter for: one near the top and one deep in
	// the list, so a filter hit past the first window is exercised.
	sonnet, opus := min(7, n-1), n-1
	out[sonnet].label, out[sonnet].value = "claude-sonnet-5-thinking-high", "claude-sonnet-5-thinking-high"
	out[opus].label, out[opus].value = "claude-opus-5-max", "claude-opus-5-max"
	return out
}

func pick(p *picker, keys ...string) pickerResult {
	var res pickerResult
	for _, k := range keys {
		res = p.update(tea.KeyPressMsg{Code: keyCodeFor(k), Text: k})
	}
	return res
}

// keyCodeFor maps the single characters and named keys these tests send.
func keyCodeFor(k string) rune {
	switch k {
	case "enter":
		return tea.KeyEnter
	case "esc":
		return tea.KeyEscape
	case "down":
		return tea.KeyDown
	case "up":
		return tea.KeyUp
	default:
		return []rune(k)[0]
	}
}

// TestPickerWindowsALargeCatalog is the T5.4 done-when: a 180-option catalog
// must be navigable without pushing the rest of the form off an 80x24
// terminal.
func TestPickerWindowsALargeCatalog(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(180), true, "")
	body := p.renderBody()
	if len(body) > pickerWindow+6 {
		t.Fatalf("picker drew %d lines for 180 options; the window is %d", len(body), pickerWindow)
	}
	optionLines := 0
	for _, l := range body {
		if strings.Contains(l, "model-") || strings.Contains(l, "claude-") {
			optionLines++
		}
	}
	if optionLines > pickerWindow {
		t.Errorf("drew %d option rows, want at most %d", optionLines, pickerWindow)
	}
	if !strings.Contains(strings.Join(body, "\n"), "more") {
		t.Error("no scroll indicator; the user cannot tell the list continues")
	}
}

// TestPickerWindowFollowsTheCursor: paging past the window edge must scroll
// rather than move a cursor nobody can see.
func TestPickerWindowFollowsTheCursor(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(50), true, "")
	for range pickerWindow + 4 {
		pick(p, "down")
	}
	if p.top == 0 {
		t.Fatalf("window never scrolled: cursor=%d top=%d", p.cursor, p.top)
	}
	if p.cursor < p.top || p.cursor >= p.top+pickerWindow {
		t.Errorf("cursor %d is outside the window [%d,%d)", p.cursor, p.top, p.top+pickerWindow)
	}
	body := strings.Join(p.renderBody(), "\n")
	if !strings.Contains(body, "▲") || !strings.Contains(body, "▼") {
		t.Errorf("mid-list view lacks both scroll indicators:\n%s", body)
	}
	// Walking back up returns to the top rather than stranding the window.
	for range 60 {
		pick(p, "up")
	}
	if p.top != 0 || p.cursor != 0 {
		t.Errorf("cursor=%d top=%d after walking up, want 0/0", p.cursor, p.top)
	}
}

// TestPickerFilterNarrowsAndSelects walks the whole filter flow: open it,
// type, and select from the narrowed list — and confirms the value chosen is
// the filtered one, not the option at that index in the full catalog.
func TestPickerFilterNarrowsAndSelects(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(180), true, "")
	pick(p, "/")
	if !p.filtering {
		t.Fatal("/ did not open the filter")
	}
	pick(p, "o", "p", "u", "s")
	if len(p.matches) != 1 {
		t.Fatalf("matches = %d, want the single opus id", len(p.matches))
	}
	pick(p, "enter") // accept the filter, back to navigating
	if p.filtering {
		t.Error("enter left the filter focused")
	}
	res := pick(p, "enter") // select the highlighted match
	if !res.chosen || res.value != "claude-opus-5-max" {
		t.Errorf("selected %+v, want claude-opus-5-max — the match, not the 0th option", res)
	}
}

// TestPickerFilterKeepsTheFreeTextRow: the likeliest reason a catalog has no
// match is that the value is newer than the catalog (§9.6), so filtering to
// nothing must not strip the escape hatch.
func TestPickerFilterKeepsTheFreeTextRow(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(20), true, "")
	pick(p, "/")
	pick(p, "z", "z", "z")
	if len(p.matches) != 0 {
		t.Fatalf("matches = %d, want none", len(p.matches))
	}
	pick(p, "enter")
	body := strings.Join(p.renderBody(), "\n")
	if !strings.Contains(body, "type a value not listed") {
		t.Errorf("free-text row vanished with the last match:\n%s", body)
	}
	if !p.onFreeRow() {
		t.Errorf("cursor = %d, want it parked on the only selectable row", p.cursor)
	}
}

// TestPickerEscClearsFilterBeforeClosing: escaping a typo must not throw away
// the selection the user came to make.
func TestPickerEscClearsFilterBeforeClosing(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(20), true, "")
	pick(p, "/")
	pick(p, "x")
	pick(p, "enter")
	if res := pick(p, "esc"); res.closed {
		t.Error("esc closed the picker while a filter was active; it must clear first")
	}
	if p.filter.Value() != "" || len(p.matches) != 20 {
		t.Errorf("filter not cleared: value=%q matches=%d", p.filter.Value(), len(p.matches))
	}
	if res := pick(p, "esc"); !res.closed {
		t.Error("esc on an unfiltered list did not close the picker")
	}
}

// TestPickerFilterDoesNotStealNavigationKeys guards the mode split: j/k/e are
// navigation outside the filter and plain text inside it.
func TestPickerFilterDoesNotStealNavigationKeys(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(20), true, "")
	pick(p, "j", "j")
	if p.cursor != 2 {
		t.Fatalf("cursor = %d after two j, want 2", p.cursor)
	}
	pick(p, "/")
	pick(p, "j")
	if !strings.Contains(p.filter.Value(), "j") {
		t.Errorf("filter value = %q, want the typed j", p.filter.Value())
	}
}

// TestPickerPreselectsTheCurrentValue keeps the pre-window behavior: opening
// a picker lands on what is already chosen, wherever it sits in the catalog.
func TestPickerPreselectsTheCurrentValue(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(180), true, "claude-opus-5-max")
	opt, ok := p.current()
	if !ok || opt.value != "claude-opus-5-max" {
		t.Fatalf("current = %+v ok=%v, want the preselected value", opt, ok)
	}
	if p.cursor < p.top || p.cursor >= p.top+pickerWindow {
		t.Errorf("preselected cursor %d is outside the initial window [%d,%d)",
			p.cursor, p.top, p.top+pickerWindow)
	}
}
