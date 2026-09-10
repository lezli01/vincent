package tui

import (
	"regexp"
	"sort"
	"strconv"
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

// footerShown reports which of rows' hints the rendered line is actually
// advertising — the question a reader asks of the line, rather than a
// question about the footer's internals.
func footerShown(line string, rows []binding) map[string]bool {
	shown := make(map[string]bool, len(rows))
	for _, b := range rows {
		if b.hint != "" && strings.Contains(line, b.hint) {
			shown[b.key] = true
		}
	}
	return shown
}

// footerMore reads the `+N` back off the rendered line; -1 when there is
// none. `+/- priority` is the only other `+` in the registry and it is never
// followed by a digit.
var footerMorePattern = regexp.MustCompile(`\+(\d+)`)

func footerMore(line string) int {
	m := footerMorePattern.FindAllStringSubmatch(line, -1)
	if len(m) == 0 {
		return 0
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	if err != nil {
		return -1
	}
	return n
}

// footerHiddenRows is `+N`'s definition read straight off the registry (task
// 094 decision 3): the palette-reachable rows of this surface whose hint is
// not on the line. It is computed rather than written as a literal, so a new
// binding cannot silently make the footer lie.
func footerHiddenRows(line string, rows []binding) int {
	n := 0
	for _, b := range rows {
		if b.noPalette || b.aliased {
			continue
		}
		if b.hint != "" && strings.Contains(line, b.hint) {
			continue
		}
		n++
	}
	return n
}

// TestFooterAdmitsWhatTheWidthHolds is task 094's first half: how many hinted
// keys reach the line is decided by the width, never by a constant. The
// assertions are against widths — a cap that had merely moved from five to
// eight would pass a "more than five" check and fail this one.
func TestFooterAdmitsWhatTheWidthHolds(t *testing.T) {
	bar := &actionBar{}
	rows := bindingsFor(ctxTasks)
	widths := []int{60, 80, 100, 140, 200}
	counts := make([]int, len(widths))
	for i, width := range widths {
		line := ansi.Strip(renderFooter(width, rows, bar, footerTarget, 0, false))
		counts[i] = len(footerShown(line, rows))
		if i > 0 && counts[i] < counts[i-1] {
			t.Errorf("width %d shows %d hints, width %d showed %d — the wider line shows fewer",
				width, counts[i], widths[i-1], counts[i-1])
		}
	}
	if last := counts[len(counts)-1]; last <= 5 {
		t.Fatalf("200 columns show %d hints — the five-key cap is still in force", last)
	}
	if counts[0] >= counts[len(counts)-1] {
		t.Fatalf("60 and 200 columns both show %d hints — the width is not deciding", counts[0])
	}
}

// TestFooterAdmitsAPriorityPrefix: admission walks registry priority order
// and stops, so a wide row is never skipped to squeeze in a narrow one behind
// it, and `+N` says exactly how many rows that left over.
func TestFooterAdmitsAPriorityPrefix(t *testing.T) {
	bar := &actionBar{}
	rows := bindingsFor(ctxTasks)
	line := ansi.Strip(renderFooter(120, rows, bar, footerTarget, 0, false))
	if strings.HasPrefix(line, "…") {
		t.Fatalf("width 120 truncated; this case is about the width contest: %q", line)
	}

	hinted := make([]binding, 0, len(rows))
	for _, b := range rows {
		if b.hint != "" {
			hinted = append(hinted, b)
		}
	}
	sort.SliceStable(hinted, func(i, j int) bool { return hinted[i].priority < hinted[j].priority })
	shown := footerShown(line, rows)
	if len(shown) == 0 || len(shown) == len(hinted) {
		t.Fatalf("width 120 shows %d of %d hints; the case needs some of each", len(shown), len(hinted))
	}
	prev := -1
	for i, b := range hinted {
		at := strings.Index(line, b.hint)
		switch {
		case i < len(shown) && at < 0:
			t.Fatalf("%q is missing while a lower-priority hint is shown: %q", b.hint, line)
		case i >= len(shown) && at >= 0:
			t.Fatalf("%q is shown while a higher-priority hint is not: %q", b.hint, line)
		case at >= 0 && at < prev:
			t.Fatalf("hints are out of priority order: %q", line)
		}
		if at >= 0 {
			prev = at
		}
	}

	want := footerHiddenRows(line, rows)
	if want == 0 {
		t.Fatalf("nothing left over at width 120: %q", line)
	}
	if got := footerMore(line); got != want {
		t.Fatalf("footer says +%d, the registry says %d rows are hidden: %q", got, want, line)
	}
}

// TestFooterNoMoreWhenNothingIsHidden: every projects row carries a hint and
// the palette lists all five, so a wide line is hiding nothing and says so by
// saying nothing.
func TestFooterNoMoreWhenNothingIsHidden(t *testing.T) {
	rows := bindingsFor(ctxProjects)
	line := ansi.Strip(renderFooter(200, rows, &actionBar{}, taskActions{}, 0, false))
	for _, b := range rows {
		if !strings.Contains(line, b.hint) {
			t.Fatalf("200 columns did not fit %q: %q", b.hint, line)
		}
	}
	if got := footerMore(line); got != 0 {
		t.Errorf("footer says +%d with every key on the line: %q", got, line)
	}
}

// TestFooterAliasRowsDoNotCount: `right` and `O` are advertised by `←/→ fold`
// and `C/O fold all`, so a grouped board showing every hint is hiding `V`
// alone — not `V`, `right` and `O` (task 094 decision 4).
func TestFooterAliasRowsDoNotCount(t *testing.T) {
	rows := bindingsFor(ctxTasks)
	line := ansi.Strip(renderFooter(200, rows, &actionBar{}, taskActions{}, 0, false))
	for _, b := range rows {
		if b.hint != "" && !strings.Contains(line, b.hint) {
			t.Fatalf("200 columns did not fit %q: %q", b.hint, line)
		}
	}
	naive := 0
	for _, b := range rows {
		if b.noPalette || (b.hint != "" && strings.Contains(line, b.hint)) {
			continue
		}
		naive++
	}
	want := footerHiddenRows(line, rows)
	if naive != want+2 {
		t.Fatalf("expected `right` and `O` to be the board's alias rows: %d unadvertised rows, %d counted", naive, want)
	}
	if got := footerMore(line); got != want {
		t.Fatalf("footer says +%d, want +%d — the alias rows are being counted: %q", got, want, line)
	}
}

// TestFooterFlatBoardDropsFoldRows: with `group_by: []` the fold keys do
// nothing, so shell.liveBindings drops them and the footer neither names them
// nor counts them — naming a press that does nothing and counting one are the
// same lie (task 054 decision 5, task 094 decision 5).
func TestFooterFlatBoardDropsFoldRows(t *testing.T) {
	s := &shell{board: &board{}}
	rows := s.liveBindings(bindingsFor(ctxTasks))
	line := ansi.Strip(renderFooter(200, rows, &actionBar{}, taskActions{}, 0, false))
	for _, b := range bindingsFor(ctxTasks) {
		if b.fold && b.hint != "" && strings.Contains(line, b.hint) {
			t.Errorf("a flat board still advertises %q: %q", b.hint, line)
		}
	}
	if got, want := footerMore(line), footerHiddenRows(line, rows); got != want {
		t.Fatalf("footer says +%d, want +%d on a flat board: %q", got, want, line)
	}
	if got, want := footerMore(line), footerHiddenRows(line, bindingsFor(ctxTasks)); got == want {
		t.Fatalf("+%d counts the fold rows the board dropped: %q", got, line)
	}
}

// TestFooterNarrowCountsWhatTheCutTook: a narrow line still truncates from
// the left with `…`, the `+N` survives it — it is the last segment before the
// pinned chrome — and its count includes the action segments the cut removed.
func TestFooterNarrowCountsWhatTheCutTook(t *testing.T) {
	bar := &actionBar{}
	rows := bindingsFor(ctxTasks)
	narrow := ansi.Strip(renderFooter(50, rows, bar, footerTarget, 0, false))
	if !strings.HasPrefix(narrow, "…") {
		t.Fatalf("width 50 did not left-truncate: %q", narrow)
	}
	got := footerMore(narrow)
	if got <= 0 {
		t.Fatalf("no +N survived the truncation: %q", narrow)
	}
	countable := 0
	for _, b := range rows {
		if !b.noPalette && !b.aliased {
			countable++
		}
	}
	if got <= countable {
		t.Fatalf("+%d at 50 columns: %d rows are hidden already, so the cut action segments are not in the count: %q",
			got, countable, narrow)
	}
	if wide := footerMore(ansi.Strip(renderFooter(200, rows, bar, footerTarget, 0, false))); got <= wide {
		t.Errorf("+%d at 50 columns is not more than +%d at 200", got, wide)
	}
}

// TestFooterMoreIsClickable: the `+N` fires `:` like every other span
// (§15 Mouse), and one the `…` cut cannot be clicked.
func TestFooterMoreIsClickable(t *testing.T) {
	bar := &actionBar{}
	rows := bindingsFor(ctxTasks)
	line, hits := buildFooter(120, rows, bar, footerTarget, 0, false)
	plain := []rune(ansi.Strip(line))
	var colons []footerHit
	for _, h := range hits {
		if h.key == ":" {
			colons = append(colons, h)
		}
	}
	if len(colons) != 2 {
		t.Fatalf("want the +N span and the pinned `: commands`, got %d spans firing `:`", len(colons))
	}
	if colons[0].x0 >= len(plain) || plain[colons[0].x0] != '+' {
		t.Fatalf("the first `:` span does not start on the +N: %q", string(plain))
	}
	// Two columns of room: the `+N` is composed but the cut takes it, and a
	// fragment must not be clickable.
	_, tight := buildFooter(29, rows, bar, footerTarget, 0, false)
	for _, h := range tight {
		if h.key == ":" && h.x0 < 3 {
			t.Errorf("a cut +N is still clickable at x0=%d", h.x0)
		}
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
