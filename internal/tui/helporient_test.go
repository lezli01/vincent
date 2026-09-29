package tui

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// orientationRows is the help sheet's "this screen" header, as key and text
// pairs, or nil when the sheet has none.
func orientationRows(sheet string) [][2]string {
	plain := ansi.Strip(sheet)
	_, after, ok := strings.Cut(plain, "THIS SCREEN\n\n")
	if !ok {
		return nil
	}
	block, _, _ := strings.Cut(after, "\n\n")
	row := regexp.MustCompile(`^  (\S+)\s+(.+)$`)
	var out [][2]string
	for _, line := range strings.Split(block, "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			out = append(out, [2]string{m[1], m[2]})
		}
	}
	return out
}

func orientationKeys(rows [][2]string) []string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r[0])
	}
	return keys
}

// A tab added without a purpose would print an empty clause in the
// orientation header (task 129.17).
func TestEveryTabHasAPurpose(t *testing.T) {
	for tab := range taskTabCount {
		if strings.TrimSpace(taskTabPurpose[tab]) == "" {
			t.Errorf("%v has no purpose for the help sheet", tab)
		}
	}
}

// The workspace's sheet opens on its strip, in drawn order, and names the
// Pull Request tab only while it is on the strip.
func TestHelpOrientsTheWorkspace(t *testing.T) {
	m, v := runningWorkspace(t, "")
	rows := orientationRows(m.helpSheet())
	want := []string{"0", "3", "4", "1", "2", "6", "5"}
	if got := orientationKeys(rows); !slices.Equal(got, want) {
		t.Fatalf("orientation digits = %v, want the drawn order %v", got, want)
	}
	for _, r := range rows {
		var tab taskViewTab = -1
		for candidate := range taskTabCount {
			if taskTabDigits[candidate] == r[0] {
				tab = candidate
			}
		}
		if want := taskTabNames[tab] + " — " + taskTabPurpose[tab]; r[1] != want {
			t.Errorf("tab %s reads %q, want %q", r[0], r[1], want)
		}
	}

	// A fan-out lane has the same strip: no pull request, no 7.
	parent := int64(2)
	v.detail.task.ParentTaskID = &parent
	if got := orientationKeys(orientationRows(m.helpSheet())); slices.Contains(got, "7") {
		t.Errorf("a lane task's sheet lists the Pull Request tab: %v", got)
	}

	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	got := orientationKeys(orientationRows(m.helpSheet()))
	if len(got) == 0 || got[len(got)-1] != "7" {
		t.Errorf("with a linked pull request the sheet lists %v, want 7 last", got)
	}
}

// The board's header is three moves, spelled with the keys in force.
func TestHelpOrientsTheBoard(t *testing.T) {
	check := func(label string) {
		t.Helper()
		m := connectedRoot(t)
		rows := orientationRows(m.helpSheet())
		want := []string{opKey(keymap.OpenRow), opKey(keymap.NextAttention), opKey(keymap.Palette)}
		if got := orientationKeys(rows); !slices.Equal(got, want) {
			t.Errorf("%s: board orientation keys = %v, want %v", label, got, want)
		}
	}
	check("defaults")
	withKeymap(t, map[string]string{"next_attention": "f14", "palette": "ctrl+a", "open_row": "f13"})
	check("rebound")
	if opKey(keymap.Palette) != "ctrl+a" {
		t.Fatal("the override did not take")
	}

	// Takeovers have no header: their own section already leads.
	if rows := orientationRows(helpText(ctxProjects, true, helpState{})); rows != nil {
		t.Errorf("the projects sheet has an orientation header: %v", rows)
	}
}

func actionRowLabel(t *testing.T, action string, op keymap.Op) string {
	t.Helper()
	for _, b := range registry() {
		if b.scope == scopeTaskAction && b.action == action && (op == "" || b.op == op) && (op != "" || b.op != keymap.EditRetry) {
			return b.label
		}
	}
	t.Fatalf("no task action row for %q", action)
	return ""
}

// Help teaches the whole vocabulary and splits it: what the target offers
// now, then the rest, dimmed (task 129.17 decision 2). The palette keeps
// omitting the rest.
func TestHelpSplitsActionsByTarget(t *testing.T) {
	target := taskActions{id: 7, state: stateBlocked, actions: []string{apiclient.ActionRetry, apiclient.ActionCancel}}
	sheet := helpText(ctxTasks, true, helpState{target: target})
	plain := ansi.Strip(sheet)
	now := strings.Index(plain, "ACTIONS NOW (ON #7)")
	later := strings.Index(plain, "NOT AVAILABLE NOW")
	if now < 0 || later < now {
		t.Fatalf("the sheet has no actions-now section ahead of not-available-now:\n%s", plain)
	}
	retry := actionRowLabel(t, apiclient.ActionRetry, "")
	if i := strings.Index(plain, retry); i < now || i > later {
		t.Errorf("retry is not under actions now:\n%s", plain)
	}
	for _, c := range []struct {
		action string
		op     keymap.Op
	}{
		{apiclient.ActionApprove, ""},
		// Edit+retry rides retry, but the step has nothing to edit.
		{apiclient.ActionRetry, keymap.EditRetry},
	} {
		label := actionRowLabel(t, c.action, c.op)
		if i := strings.Index(plain, label); i < later {
			t.Errorf("%q is not under not available now:\n%s", label, plain)
		}
		var row binding
		for _, b := range registry() {
			if b.label == label {
				row = b
			}
		}
		if !strings.Contains(sheet, styleDim.Render(padRight(row.key, 8)+label)) {
			t.Errorf("%q is not dimmed", label)
		}
	}
	if strings.Contains(plain, "offered only when valid") {
		t.Error("the old caption survived the split")
	}

	// No target: nothing is valid now.
	empty := ansi.Strip(helpText(ctxTasks, true, helpState{}))
	if strings.Contains(empty, "ACTIONS NOW") || !strings.Contains(empty, "NOT AVAILABLE NOW") {
		t.Errorf("with no target the sheet should list every action as not available:\n%s", empty)
	}
	// The same target through the palette: approve is omitted, not greyed.
	for _, e := range paletteEntries(ctxTasks, target, false, true, true, nil, nil) {
		if e.label == actionRowLabel(t, apiclient.ActionApprove, "") {
			t.Error("the palette offers approve on a task that does not")
		}
	}
}

// The sheet scrolls with the overlay's own keys, and every other key is
// still refused (task 129.17).
func TestHelpScrolls(t *testing.T) {
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 16})
	m.Update(key("?"))
	if !m.help {
		t.Fatal("? did not open help")
	}
	lines, visible := m.helpWindow()
	if len(lines) <= visible {
		t.Fatalf("the sheet (%d lines) fits the body (%d); the test needs it not to", len(lines), visible)
	}
	lastLine := strings.TrimSpace(ansi.Strip(lines[len(lines)-1]))
	if strings.Contains(ansi.Strip(content(m)), lastLine) {
		t.Fatalf("the last row %q is on screen before scrolling", lastLine)
	}
	if !strings.Contains(ansi.Strip(content(m)), "↓ more") {
		t.Errorf("an overflowing sheet shows no position cue:\n%s", ansi.Strip(content(m)))
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.helpScroll != 1 {
		t.Errorf("down scrolled to %d, want 1", m.helpScroll)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.helpScroll <= 1 {
		t.Errorf("pgdown scrolled to %d", m.helpScroll)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(ansi.Strip(content(m)), lastLine) {
		t.Errorf("end did not bring the last row %q on screen:\n%s", lastLine, ansi.Strip(content(m)))
	}
	end := m.helpScroll
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.helpScroll != end {
		t.Errorf("down past the end moved the window to %d, want it clamped at %d", m.helpScroll, end)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.helpScroll >= end {
		t.Errorf("pgup did not scroll back")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if m.helpScroll != 0 || !strings.Contains(ansi.Strip(content(m)), "THIS SCREEN") {
		t.Errorf("home did not return to the top (scroll %d)", m.helpScroll)
	}

	// Anything else is swallowed: no palette, no action, sheet still open.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	for _, k := range []string{":", opKey(keymap.Cancel), "q"} {
		m.Update(key(k))
		if !m.help || m.palette != nil {
			t.Fatalf("%q reached past the help sheet", k)
		}
	}

	// Each close key closes, and reopening starts at the top.
	for _, k := range []tea.KeyPressMsg{key("?"), {Code: tea.KeyEscape}, {Code: tea.KeyF1}} {
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
		m.Update(k)
		if m.help {
			t.Fatalf("%q did not close help", k.String())
		}
		m.Update(key("?"))
		if !m.help || m.helpScroll != 0 {
			t.Fatalf("reopening help: open %v, scroll %d", m.help, m.helpScroll)
		}
	}
}

func tabEntries(entries []paletteEntry) []paletteEntry {
	var out []paletteEntry
	for _, e := range entries {
		if e.isTab {
			out = append(out, e)
		}
	}
	return out
}

// The workspace palette lists its strip, one "go to" row per tab, and
// running one replays the digit a press would (task 129.17).
func TestPaletteGoesToTabs(t *testing.T) {
	m, v := runningWorkspace(t, "")
	m.openPalette()
	tabs := tabEntries(m.palette.entries)
	if len(tabs) != len(v.tabs()) {
		t.Fatalf("palette lists %d tabs, the strip has %d", len(tabs), len(v.tabs()))
	}
	for i, e := range tabs {
		tab := v.tabs()[i]
		if e.tab != tab || e.key != taskTabDigits[tab] || e.label != "go to the "+taskTabNames[tab]+" tab" || e.group != "tabs" {
			t.Errorf("tab row %d = %+v, want %v in drawn order", i, e, tab)
		}
		if e.tab == taskTabPull {
			t.Error("the palette offers the Pull Request tab with no pull request linked")
		}
	}

	for _, r := range "diff" {
		m.Update(key(string(r)))
	}
	var found bool
	for _, e := range m.palette.matches() {
		if e.isTab && e.tab == taskTabDiff && e.key == "4" {
			found = true
		}
	}
	if !found {
		t.Fatal(`typing "diff" does not offer the Diff tab`)
	}
	m.palette = nil
	m.openPalette()
	for _, r := range "go to the diff" {
		m.Update(key(string(r)))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.palette != nil || v.tab != taskTabDiff {
		t.Fatalf("running the Diff row left the workspace on %v", v.tab)
	}

	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	m.openPalette()
	tabs = tabEntries(m.palette.entries)
	if last := tabs[len(tabs)-1]; last.tab != taskTabPull || last.key != "7" {
		t.Errorf("with a linked pull request the last tab row is %+v, want Pull Request on 7", last)
	}
	m.palette = nil

	// Off the workspace there is no strip, so no tabs group.
	board := connectedRoot(t)
	board.openPalette()
	if got := tabEntries(board.palette.entries); len(got) != 0 {
		t.Errorf("the board palette lists tabs: %+v", got)
	}
}

// Every palette row with a registry key renders it, and a tui.keys override
// is what renders.
func TestPaletteShowsEffectiveKeys(t *testing.T) {
	withKeymap(t, reboundKeys)
	entries := paletteEntries(ctxTasks, everyAction, true, true, true, nil, nil)
	keys := map[string]string{}
	for _, b := range registry() {
		keys[b.label] = b.key
	}
	p := newPalette(entries)
	for _, e := range entries {
		if want, ok := keys[e.label]; ok && want != "" && e.key != want {
			t.Errorf("%q shows %q, want %q", e.label, e.key, want)
		}
	}
	out := ansi.Strip(p.render(80, 200))
	for _, op := range []keymap.Op{keymap.Repair, keymap.FollowUp, keymap.EditRetry} {
		if k := opKey(op); !strings.Contains(out, k) {
			t.Errorf("the rebound %s key %q is not on the palette:\n%s", op, k, out)
		}
	}
}
