package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The main tabs (task 136.2): tab and shift+tab switch between the screens
// tui.tabs names, on those screens only, and never while one is typing.

func tabKey() tea.KeyPressMsg      { return tea.KeyPressMsg{Code: tea.KeyTab} }
func shiftTabKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift} }
func escKey() tea.KeyPressMsg      { return tea.KeyPressMsg{Code: tea.KeyEscape} }

// tabsRoot is a connected root with tui.tabs set to names, the way a config
// answer sets it.
func tabsRoot(t *testing.T, names ...string) *root {
	t.Helper()
	m := connectedRoot(t)
	if names != nil {
		m.Update(boardConfigMsg{tabs: names})
	}
	return m
}

// pressEsc presses esc and routes a screen change the view asks for, the
// way the runtime would, without running anything that reaches a daemon.
func pressEsc(m *root) {
	_, cmd := m.Update(escKey())
	for _, msg := range tabFlatten(cmd) {
		if sv, ok := msg.(selectViewMsg); ok {
			m.Update(sv)
		}
	}
}

func tabFlatten(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, tabFlatten(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestTabCyclesTheDefaultTabs(t *testing.T) {
	m := tabsRoot(t)
	if !slices.Equal(m.tabs, []viewID{viewIssues, viewHome}) {
		t.Fatalf("default tabs = %v, want issues then tasks", m.tabs)
	}
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(tabKey())
	if m.active != viewHome {
		t.Fatalf("tab on issues went to %v, want the board", m.active)
	}
	m.Update(tabKey())
	if m.active != viewIssues {
		t.Fatalf("tab on the board went to %v, want issues (it wraps)", m.active)
	}
	m.Update(shiftTabKey())
	if m.active != viewHome {
		t.Fatalf("shift+tab on issues went to %v, want the board", m.active)
	}
	if m.lastTab != viewHome {
		t.Fatalf("lastTab = %v, want the board", m.lastTab)
	}
}

func TestTabCyclesThreeTabsInConfiguredOrder(t *testing.T) {
	m := tabsRoot(t, "issues", "tasks", "chats")
	m.Update(selectViewMsg{id: viewIssues})
	var forward []viewID
	for range 3 {
		m.Update(tabKey())
		forward = append(forward, m.active)
	}
	if want := []viewID{viewHome, viewChats, viewIssues}; !slices.Equal(forward, want) {
		t.Fatalf("tab walked %v, want %v", forward, want)
	}
	var back []viewID
	for range 3 {
		m.Update(shiftTabKey())
		back = append(back, m.active)
	}
	if want := []viewID{viewChats, viewHome, viewIssues}; !slices.Equal(back, want) {
		t.Fatalf("shift+tab walked %v, want %v", back, want)
	}
}

func TestOneTabNeverSwitches(t *testing.T) {
	m := tabsRoot(t, "tasks")
	m.Update(tabKey())
	m.Update(shiftTabKey())
	if m.active != viewHome {
		t.Fatalf("with one tab, tab moved to %v", m.active)
	}
}

// Typing in the board's filter owns tab: the first commits the filter, the
// second switches — the screenshot tapes' `/ … Tab` idiom.
func TestBoardFilterTakesTheFirstTab(t *testing.T) {
	m := tabsRoot(t)
	m.Update(key("/"))
	m.Update(key("x"))
	s := m.views[viewHome].(*shell)
	if !s.board.filtering {
		t.Fatal("/ did not open the board's filter")
	}
	m.Update(tabKey())
	if m.active != viewHome || s.board.filtering {
		t.Fatalf("first tab: active %v, filtering %v — want the filter committed in place", m.active, s.board.filtering)
	}
	if s.board.filter.Value() != "x" {
		t.Fatalf("the committed filter is %q, want x", s.board.filter.Value())
	}
	m.Update(tabKey())
	if m.active != viewIssues {
		t.Fatalf("second tab went to %v, want issues", m.active)
	}
}

func TestIssuesFilterKeepsTab(t *testing.T) {
	m := tabsRoot(t)
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(key("/"))
	m.Update(tabKey())
	if m.active != viewIssues {
		t.Fatalf("tab while typing the issues filter went to %v", m.active)
	}
}

func TestOpenFormsKeepTab(t *testing.T) {
	t.Run("new chat", func(t *testing.T) {
		m := tabsRoot(t, "chats", "tasks")
		m.Update(selectViewMsg{id: viewChats})
		m.views[viewChats].(*chatsView).create = newNewChatForm(nil, 1)
		m.Update(tabKey())
		if m.active != viewChats {
			t.Fatalf("tab in the new-chat form went to %v", m.active)
		}
	})
	t.Run("issue form", func(t *testing.T) {
		m := tabsRoot(t)
		m.Update(selectViewMsg{id: viewIssues})
		v := m.views[viewIssues].(*issuesView)
		v.w.form = newIssueForm(nil, v.w.exec, nil, 1)
		m.Update(tabKey())
		if m.active != viewIssues {
			t.Fatalf("tab in the issue form went to %v", m.active)
		}
	})
}

// Outside the tab roots tab keeps whatever it meant there: the root never
// switches from them.
func TestTabNeverSwitchesOffATabRoot(t *testing.T) {
	m := tabsRoot(t, "issues", "tasks", "chats")
	for _, id := range []viewID{
		viewTask, viewNewTask, viewProjects, viewWorkflows, viewDaemon, viewPullRequests,
		viewArchived, viewArchivedChats, viewTriggers, viewChat, viewIssue,
	} {
		m.switchTo(id)
		if m.tabSwitches() {
			t.Errorf("view %v: tab would switch the main tab", id)
		}
		m.Update(tabKey())
		m.Update(shiftTabKey())
		if m.active != id {
			t.Errorf("view %v: tab moved to %v", id, m.active)
		}
	}
}

func TestTabSwitchesWhileDisconnected(t *testing.T) {
	for _, phase := range []connPhase{phaseReconnecting, phaseFailed} {
		m := tabsRoot(t)
		m.phase = phase
		m.Update(tabKey())
		if m.active != viewIssues {
			t.Errorf("phase %v: tab went to %v, want issues", phase, m.active)
		}
		m.Update(tabKey())
		if m.active != viewHome {
			t.Errorf("phase %v: tab back went to %v, want the board", phase, m.active)
		}
	}
}

func TestEscStaysOnATabRoot(t *testing.T) {
	m := tabsRoot(t, "issues", "tasks", "chats")
	for _, id := range []viewID{viewIssues, viewChats} {
		m.Update(selectViewMsg{id: id})
		pressEsc(m)
		if m.active != id {
			t.Errorf("esc on tab %v left to %v", id, m.active)
		}
	}
}

// A view that is not a configured tab has no reason to trap the human.
func TestEscLeavesAListThatIsNotATab(t *testing.T) {
	m := tabsRoot(t, "tasks")
	for _, id := range []viewID{viewIssues, viewChats} {
		m.Update(selectViewMsg{id: id})
		pressEsc(m)
		if m.active != viewHome {
			t.Errorf("esc on %v (not a tab) went to %v, want the board", id, m.active)
		}
	}
}

func TestApplyTabs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		want  []viewID
	}{
		{"missing", nil, []viewID{viewIssues, viewHome}},
		{"empty", []string{}, []viewID{viewIssues, viewHome}},
		{"unknown only", []string{"pull_requests"}, []viewID{viewIssues, viewHome}},
		{"unknown dropped", []string{"chats", "nope", "tasks"}, []viewID{viewChats, viewHome}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tabsRoot(t, "tasks")
			m.Update(boardConfigMsg{tabs: tc.names})
			if !slices.Equal(m.tabs, tc.want) {
				t.Fatalf("tabs = %v, want %v", m.tabs, tc.want)
			}
		})
	}
	t.Run("failed fetch", func(t *testing.T) {
		m := tabsRoot(t, "tasks", "chats")
		m.Update(boardConfigMsg{err: errors.New("boom")})
		m.Update(daemonConfigMsg{err: errors.New("boom")})
		m.Update(configSavedMsg{err: errors.New("refused")})
		if want := []viewID{viewHome, viewChats}; !slices.Equal(m.tabs, want) {
			t.Fatalf("a failed answer changed tabs to %v", m.tabs)
		}
	})
	t.Run("saved and fetched", func(t *testing.T) {
		m := tabsRoot(t, "tasks")
		var cfg apiclient.Config
		cfg.TUI.Tabs = []string{"chats", "issues"}
		m.Update(configSavedMsg{cfg: cfg})
		if want := []viewID{viewChats, viewIssues}; !slices.Equal(m.tabs, want) {
			t.Fatalf("after a save, tabs = %v, want %v", m.tabs, want)
		}
		cfg.TUI.Tabs = []string{"tasks", "issues"}
		m.Update(daemonConfigMsg{config: cfg})
		if want := []viewID{viewHome, viewIssues}; !slices.Equal(m.tabs, want) {
			t.Fatalf("after a daemon fetch, tabs = %v, want %v", m.tabs, want)
		}
	})
}

// A reload that drops the active view never moves the human; it only stops
// tab from switching there.
func TestDroppingTheActiveTabLeavesTheHumanThere(t *testing.T) {
	m := tabsRoot(t)
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(boardConfigMsg{tabs: []string{"tasks", "chats"}})
	if m.active != viewIssues {
		t.Fatalf("the reload moved the human to %v", m.active)
	}
	m.Update(tabKey())
	if m.active != viewIssues {
		t.Fatalf("tab on a dropped tab moved to %v", m.active)
	}
}

func TestOwningTab(t *testing.T) {
	m := tabsRoot(t)
	for id, want := range map[viewID]viewID{viewIssues: viewIssues, viewIssue: viewIssues, viewTask: viewHome, viewHome: viewHome} {
		if got, ok := m.owningTab(id); !ok || got != want {
			t.Errorf("owningTab(%v) = %v, %v; want %v", id, got, ok, want)
		}
	}
	for _, id := range []viewID{viewChat, viewChats, viewDaemon} {
		if got, ok := m.owningTab(id); ok {
			t.Errorf("owningTab(%v) = %v, want none with chats not a tab", id, got)
		}
	}
}

// The footer names the tab tab goes to; the help lists the row; the palette
// offers the board as a screen and no tab entry.
func TestMainTabRowsOnTheSurfaces(t *testing.T) {
	m := tabsRoot(t)
	if f := ansi.Strip(m.footerLine()); !strings.Contains(f, "tab issues") {
		t.Errorf("board footer %q does not name the issues tab", f)
	}
	if h := m.helpSheet(); !strings.Contains(h, mainTabLabel) {
		t.Errorf("board help does not carry the main-tab row")
	}
	m.openPalette()
	board := false
	for _, e := range m.palette.entries {
		if e.key == "tab" {
			t.Errorf("the palette offers a tab entry: %q", e.label)
		}
		if e.nav && e.navTarget == viewHome {
			board = true
		}
	}
	if !board {
		t.Error("the palette offers no tasks screen row")
	}
	m.palette = nil

	m.Update(selectViewMsg{id: viewIssues})
	if f := ansi.Strip(m.footerLine()); !strings.Contains(f, "tab tasks") {
		t.Errorf("issues footer %q does not name the tasks tab", f)
	}

	// Chats is not a tab by default, so its row is withheld.
	m.Update(selectViewMsg{id: viewChats})
	if f := ansi.Strip(m.footerLine()); strings.Contains(f, "tab ") {
		t.Errorf("chats footer %q offers tab though chats is not a tab", f)
	}
	if h := m.helpSheet(); strings.Contains(h, mainTabLabel) {
		t.Error("chats help carries the main-tab row though chats is not a tab")
	}
}

func init() {
	// The main-tab rows' probes: the key is the root's, so each drives a
	// root with every screen a tab.
	for ctx, id := range map[bindingContext]viewID{ctxTasks: viewHome, ctxIssues: viewIssues, ctxChats: viewChats} {
		if panelKeyProbes[ctx] == nil {
			panelKeyProbes[ctx] = map[string]func(*testing.T){}
		}
		panelKeyProbes[ctx]["tab"] = func(t *testing.T) {
			m := tabsRoot(t, "issues", "tasks", "chats")
			m.Update(selectViewMsg{id: id})
			m.Update(registryKey(t, "tab"))
			if m.active == id {
				t.Fatalf("tab on %v did not switch the main tab", id)
			}
		}
	}
}
