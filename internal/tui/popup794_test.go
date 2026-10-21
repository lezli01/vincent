package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// Task 135.10 (#794): the root's popups sized to the terminal, list cues and
// paging in each, the footer's status-first rule, and the root's transient
// notice for a copy and the mouse toggle.

func specialKey(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// popupTop finds the popup box's top border on a frame, by its title, and
// reports its row and its width in cells.
func popupTop(t *testing.T, frame, title string) (row, width int) {
	t.Helper()
	for i, l := range strings.Split(ansi.Strip(frame), "\n") {
		at := strings.Index(l, "┌─ ▸ "+title)
		if at < 0 {
			continue
		}
		box := l[at:]
		end := strings.Index(box, "┐")
		return i, ansi.StringWidth(box[:end]) + 1
	}
	t.Fatalf("no %q box on the frame:\n%s", title, ansi.Strip(frame))
	return 0, 0
}

// popupHeight counts the box's rows from its top border to its bottom one.
func popupHeight(frame, title string, top int) int {
	lines := strings.Split(ansi.Strip(frame), "\n")
	at := strings.Index(lines[top], "┌─ ▸ "+title)
	if at < 0 {
		return 0
	}
	col := ansi.StringWidth(lines[top][:at])
	for i := top + 1; i < len(lines); i++ {
		if strings.HasPrefix(ansi.Cut(lines[i], col, col+1), "└") {
			return i - top + 1
		}
	}
	return 0
}

func TestPopupBoxGrowsWithTheTerminal(t *testing.T) {
	for _, tc := range []struct{ w, h, top, pw, ph int }{
		{80, 24, 1, 64, 18},
		{200, 60, 1, 100, 30},
		{120, 40, 1, 100, 30},
		{100, 30, 1, 84, 24},
		{60, 15, 1, 52, 11},
		{20, 10, 1, 18, 6},
		{80, 24, 3, 64, 18},
		{80, 20, 3, 64, 16},
	} {
		pw, ph := popupBox(tc.w, tc.h, tc.top)
		if pw != tc.pw || ph != tc.ph {
			t.Errorf("popupBox(%d, %d, %d) = %d×%d, want %d×%d", tc.w, tc.h, tc.top, pw, ph, tc.pw, tc.ph)
		}
	}
}

func TestPaletteBoxIsSizedAndAnchoredUnderTheChrome(t *testing.T) {
	for _, tc := range []struct{ w, h, pw, ph int }{{80, 24, 64, 18}, {200, 60, 100, 30}} {
		m := phaseRoot(t, phaseConnected, tc.w, tc.h)
		m.Update(key(":"))
		frame := content(m)
		row, width := popupTop(t, frame, "commands")
		if width != tc.pw {
			t.Errorf("%dx%d: palette is %d wide, want %d", tc.w, tc.h, width, tc.pw)
		}
		if got := popupHeight(frame, "commands", row); got != tc.ph {
			t.Errorf("%dx%d: palette is %d tall, want %d", tc.w, tc.h, got, tc.ph)
		}
		if row != m.chromeTop() {
			t.Errorf("%dx%d: palette starts on row %d, chromeTop is %d", tc.w, tc.h, row, m.chromeTop())
		}
	}
	// A status line moves the box down rather than being covered.
	m := phaseRoot(t, phaseConnected, 80, 24)
	m.selNotice = "switched to `api`"
	m.palette = newPalette(paletteEntries(ctxTasks, taskActions{}, false, true, false, nil, nil))
	frame := content(m)
	row, _ := popupTop(t, frame, "commands")
	if m.chromeTop() != 2 || row != 2 {
		t.Errorf("with a status line the box starts on row %d, chromeTop %d, want 2", row, m.chromeTop())
	}
	if !strings.Contains(ansi.Strip(frame), "switched to `api`") {
		t.Error("the status line is covered by the box")
	}
	assertFits(t, frame, 80, 24)
}

func TestPaletteRowKeepsAWideKeyWhole(t *testing.T) {
	long := "project overview — every project's figures, and where to add, edit or compare one"
	for _, k := range []string{"shift+down", "backspace", "ctrl+shift+x"} {
		e := paletteEntry{group: "global", label: long, key: k}
		row := ansi.Strip(paletteRow(e, styleDim, 40))
		if !strings.HasSuffix(row, " "+k) {
			t.Errorf("key %q cut: %q", k, row)
		}
		if !strings.Contains(row, "…") {
			t.Errorf("the label was not cut with …: %q", row)
		}
		if w := ansi.StringWidth(row); w > 40 {
			t.Errorf("row is %d cells, budget 40: %q", w, row)
		}
	}
	// A key that leaves no room shrinks the label to "…", never itself.
	row := ansi.Strip(paletteRow(paletteEntry{label: long, key: "ctrl+shift+alt+x"}, styleDim, 18))
	if !strings.HasSuffix(row, "ctrl+shift+alt+x") || !strings.HasPrefix(row, "…") {
		t.Errorf("narrow row: %q", row)
	}
}

func TestPaletteShowsTheFocusedLabelWhole(t *testing.T) {
	long := "project overview — every project's figures, and where to add, edit or compare one"
	entries := []paletteEntry{{group: "screens", label: "short", key: "a"}, {group: "screens", label: long, key: "b"}}
	for i := range 20 {
		entries = append(entries, paletteEntry{group: "global", label: fmt.Sprintf("entry %d", i), key: "x"})
	}
	p := newPalette(entries)
	short := p.render(64, 18)
	p.update(specialKey(tea.KeyDown))
	full := p.render(64, 18)
	joined := strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(full), "│", " ")), " ")
	if !strings.Contains(joined, strings.Join(strings.Fields(long), " ")) {
		t.Errorf("the focused label is not on screen whole:\n%s", ansi.Strip(full))
	}
	if a, b := strings.Count(short, "\n"), strings.Count(full, "\n"); a != b {
		t.Errorf("the box changed height with the cursor: %d → %d lines", a, b)
	}
	// The list rows above the reserve did not move: the same entry sits on
	// the same line.
	sl, fl := strings.Split(ansi.Strip(short), "\n"), strings.Split(ansi.Strip(full), "\n")
	for i := range sl {
		if strings.Contains(sl[i], "entry 3") != strings.Contains(fl[i], "entry 3") {
			t.Errorf("the list moved between a short and a long focused label (line %d)", i)
		}
	}
}

// popupCase opens one of the four list popups with n rows and returns its
// render and its key handler.
type popupCase struct {
	name   string
	render func(w, h int) string
	key    func(k tea.KeyPressMsg)
	cursor func() int
}

func listPopups(n int) []popupCase {
	entries := make([]paletteEntry, 0, n)
	projects := make([]apiclient.Project, 0, n)
	copies := make([]copyItem, 0, n)
	links := make([]linkItem, 0, n)
	for i := range n {
		entries = append(entries, paletteEntry{group: "global", label: fmt.Sprintf("entry %d", i), key: "x"})
		projects = append(projects, testProject(int64(i+1), fmt.Sprintf("p%02d", i)))
		copies = append(copies, copyItem{group: "message 1", label: fmt.Sprintf("block %d", i), snippet: "text"})
		links = append(links, linkItem{group: "message 1", link: mdLink{n: i + 1, label: "l", dest: "https://example.com"}})
	}
	pal := newPalette(entries)
	pp := newProjectPicker(projects, 1, "")
	rp := newReaderPicker(copies)
	lp := newLinkPicker(links)
	return []popupCase{
		{"palette", pal.render, func(k tea.KeyPressMsg) { pal.update(k) }, func() int { return pal.cursor }},
		{"project picker", pp.render, func(k tea.KeyPressMsg) { pp.update(k) }, func() int {
			if pp.overview {
				return len(projects)
			}
			return pp.cursor
		}},
		{"reader picker", rp.render, func(k tea.KeyPressMsg) { rp.update(k) }, func() int { return rp.cursor }},
		{"link picker", lp.render, func(k tea.KeyPressMsg) { lp.update(k) }, func() int { return lp.cursor }},
	}
}

func TestListPopupsCueAndPage(t *testing.T) {
	for _, c := range listPopups(40) {
		t.Run(c.name, func(t *testing.T) {
			first := ansi.Strip(c.render(64, 18))
			if !strings.Contains(first, "▼") || !strings.Contains(first, "more") {
				t.Fatalf("no ▼ cue on a clipped list:\n%s", first)
			}
			if strings.Contains(first, "▲") {
				t.Errorf("a ▲ cue at the top of the list:\n%s", first)
			}
			start := c.cursor()
			c.key(specialKey(tea.KeyPgDown))
			if c.cursor() <= start {
				t.Fatalf("pgdown left the cursor on %d", c.cursor())
			}
			if got := ansi.Strip(c.render(64, 18)); !strings.Contains(got, "▲") {
				t.Errorf("no ▲ cue after paging down:\n%s", got)
			}
			paged := c.cursor()
			c.key(specialKey(tea.KeyPgUp))
			if c.cursor() >= paged {
				t.Errorf("pgup left the cursor on %d", c.cursor())
			}
			c.key(specialKey(tea.KeyEnd))
			end := c.cursor()
			if end < 39 {
				t.Errorf("end put the cursor on %d", end)
			}
			c.key(specialKey(tea.KeyDown))
			if c.cursor() != end {
				t.Errorf("down past the end moved the cursor to %d: the list wrapped", c.cursor())
			}
			c.key(specialKey(tea.KeyHome))
			if c.cursor() != 0 {
				t.Errorf("home put the cursor on %d", c.cursor())
			}
			c.key(specialKey(tea.KeyUp))
			if c.cursor() != 0 {
				t.Errorf("up past the top moved the cursor to %d: the list wrapped", c.cursor())
			}
		})
	}
}

// statusFooter is the action bar's footer at width with a status set.
func statusFooter(width int, status string, bad bool) string {
	bar := &actionBar{}
	bar.setStatus(status, bad)
	line, _ := buildFooter(width, bindingsFor(ctxTasks), bar, taskActions{id: 42}, attentionTally{}, false, false)
	return ansi.Strip(line)
}

func TestFooterStatusLeadsAndIsCutFromTheRight(t *testing.T) {
	pinned := ansi.Strip(footerPinnedSegs(false)[len(footerPinnedSegs(false))-1].text)
	for _, status := range []string{
		"retry: task 42 is not in a state that can be retried (409 invalid_transition)",
		"cancel failed: the daemon refused the request because the task is locked by chat #7",
	} {
		got := statusFooter(80, status, true)
		if !strings.HasPrefix(got, " "+status[:20]) {
			t.Errorf("the status lost its start: %q", got)
		}
		if !strings.HasSuffix(strings.TrimRight(got, " "), pinned) {
			t.Errorf("the pinned escape hatch is gone: %q", got)
		}
		if w := ansi.StringWidth(got); w > 80 {
			t.Errorf("footer is %d cells: %q", w, got)
		}
	}
	// A short status leaves room for hints after it.
	if got := statusFooter(200, "copied", false); !strings.HasPrefix(got, " copied · ") {
		t.Errorf("hints do not follow a short status: %q", got)
	}
}

func TestFooterConfirmationKeepsItsStart(t *testing.T) {
	bar := &actionBar{pending: "cancel"}
	for _, w := range []int{60, 80} {
		line, _ := buildFooter(w, bindingsFor(ctxTasks), bar, taskActions{id: 42, state: "running", actions: []string{"cancel"}}, attentionTally{}, false, false)
		got := ansi.Strip(line)
		if !strings.HasPrefix(strings.TrimLeft(got, " "), "cancel #42?") {
			t.Errorf("%d cols: the question lost its start: %q", w, got)
		}
		if !strings.Contains(got, "q quit") {
			t.Errorf("%d cols: the pinned segment is gone: %q", w, got)
		}
		if lw := ansi.StringWidth(got); lw > w {
			t.Errorf("%d cols: footer is %d cells", w, lw)
		}
	}
}

func TestFooterHintsStillCutFromTheLeft(t *testing.T) {
	line, _ := buildFooter(40, bindingsFor(ctxTasks), &actionBar{}, taskActions{id: 42, actions: []string{"retry", "cancel", "archive"}}, attentionTally{}, false, false)
	got := ansi.Strip(line)
	if !strings.HasPrefix(got, "…") && !strings.HasPrefix(got, " ") {
		t.Errorf("unexpected footer start: %q", got)
	}
	if !strings.Contains(got, "q quit") {
		t.Errorf("the pinned segment is gone: %q", got)
	}
}

func TestCopyOnAViewWithoutANoteSaysSoOnTheStatusLine(t *testing.T) {
	orig := clipboardWrite
	t.Cleanup(func() { clipboardWrite = orig })
	var copied string
	clipboardWrite = func(s string) error { copied = s; return nil }

	m := phaseRoot(t, phaseConnected, 80, 24)
	m.active = viewIssue
	m.Update(openLinkPickerMsg{items: []linkItem{{group: "message 1", link: mdLink{n: 1, label: "docs", dest: "https://example.com/a"}}}})
	if m.linkPick == nil {
		t.Fatal("the link picker did not open")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m.Update(runCmd(t, cmd, 5*time.Second))
	if copied != "https://example.com/a" {
		t.Fatalf("copied %q", copied)
	}
	line, ok := m.statusLine()
	if !ok || !strings.Contains(ansi.Strip(line), "copied") {
		t.Fatalf("no copied notice on the status line: %q", ansi.Strip(line))
	}
	m.Update(specialKey(tea.KeyDown))
	if line, ok := m.statusLine(); ok {
		t.Errorf("the notice outlived the next key: %q", ansi.Strip(line))
	}

	// A failure is a warning carrying the error.
	m.Update(clipboardResultMsg{label: "link [1]", err: errors.New("nothing to copy")})
	line, _ = m.statusLine()
	if got := ansi.Strip(line); !strings.Contains(got, "⚠") || !strings.Contains(got, "nothing to copy") {
		t.Errorf("failure notice: %q", got)
	}
}

func TestViewsWithTheirOwnCopyNoteGetNoRootNotice(t *testing.T) {
	for _, id := range []viewID{viewTask, viewChat} {
		m := phaseRoot(t, phaseConnected, 80, 24)
		m.active = id
		if _, ok := m.views[id].(clipboardNoter); !ok {
			t.Fatalf("view %d does not note a copy itself", id)
		}
		m.Update(clipboardResultMsg{label: "markdown"})
		if line, ok := m.statusLine(); ok {
			t.Errorf("view %d: a root notice beside the view's own: %q", id, ansi.Strip(line))
		}
	}
}

func TestMouseToggleSaysWhatItDid(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 80, 24)
	k := opKey(keymap.Mouse)
	m.Update(key(k))
	line, _ := m.statusLine()
	if got := ansi.Strip(line); m.mouseOn || !strings.Contains(got, "mouse off") || !strings.Contains(got, k+" turns it back on") {
		t.Errorf("after one press: mouse %v, notice %q", m.mouseOn, got)
	}
	m.Update(key(k))
	line, _ = m.statusLine()
	if got := ansi.Strip(line); !m.mouseOn || !strings.Contains(got, "mouse on") {
		t.Errorf("after two presses: mouse %v, notice %q", m.mouseOn, got)
	}
	m.Update(specialKey(tea.KeyDown))
	if line, ok := m.statusLine(); ok {
		t.Errorf("the notice outlived the next key: %q", ansi.Strip(line))
	}
}

// Popups stay keyboard-only (§15 Mouse): a click under an open popup does
// not reach it, and no popup turns the mouse on for itself.
func TestPopupsIgnoreClicks(t *testing.T) {
	m := phaseRoot(t, phaseConnected, 80, 24)
	m.Update(key(":"))
	before := m.palette.cursor
	m.Update(tea.MouseClickMsg{X: 20, Y: 5, Button: tea.MouseLeft})
	if m.palette == nil || m.palette.cursor != before {
		t.Error("a click reached the palette")
	}
	m.mouseOn = false
	if m.View().MouseMode != tea.MouseModeNone {
		t.Error("an open popup turned the mouse on")
	}
}

// groupedPopups are the popups that group their rows under section headers,
// each with groups×per entries whose labels are given by label(g, i).
func groupedPopups(groups, per int, label func(g, i int) string) []popupCase {
	var (
		entries []paletteEntry
		copies  []copyItem
		links   []linkItem
	)
	for g := range groups {
		group := fmt.Sprintf("group %d", g)
		for i := range per {
			entries = append(entries, paletteEntry{group: group, label: label(g, i), key: "x"})
			copies = append(copies, copyItem{group: group, label: label(g, i), snippet: "text"})
			links = append(links, linkItem{group: group, link: mdLink{n: g*per + i + 1, label: label(g, i), dest: "https://example.com"}})
		}
	}
	pal := newPalette(entries)
	rp := newReaderPicker(copies)
	lp := newLinkPicker(links)
	return []popupCase{
		{"palette", pal.render, func(k tea.KeyPressMsg) { pal.update(k) }, func() int { return pal.cursor }},
		{"reader picker", rp.render, func(k tea.KeyPressMsg) { rp.update(k) }, func() int { return rp.cursor }},
		{"link picker", lp.render, func(k tea.KeyPressMsg) { lp.update(k) }, func() int { return lp.cursor }},
	}
}

// Review F1: a page of a grouped list holds fewer entries than rows, so
// paging by rows jumped past entries that never reached the screen. Every
// entry is on screen at some point of a pgdown walk to the end and of a
// pgup walk back.
func TestGroupedPopupsPageThroughEveryEntry(t *testing.T) {
	const groups, per = 8, 3
	label := func(g, i int) string { return fmt.Sprintf("g%d-%d", g, i) }
	for _, c := range groupedPopups(groups, per, label) {
		t.Run(c.name, func(t *testing.T) {
			for _, walk := range []struct {
				key  tea.KeyPressMsg
				goal int
			}{{specialKey(tea.KeyPgDown), groups*per - 1}, {specialKey(tea.KeyPgUp), 0}} {
				seen := ansi.Strip(c.render(64, 18))
				for step := 0; c.cursor() != walk.goal; step++ {
					if step > groups*per {
						t.Fatalf("paging never reached entry %d: stuck on %d", walk.goal, c.cursor())
					}
					c.key(walk.key)
					seen += ansi.Strip(c.render(64, 18))
				}
				for g := range groups {
					for i := range per {
						if !strings.Contains(seen, label(g, i)) {
							t.Errorf("%s never showed %s", walk.key, label(g, i))
						}
					}
				}
			}
		})
	}
}
