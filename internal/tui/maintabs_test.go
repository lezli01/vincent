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
		switch msg.(type) {
		case selectViewMsg, backToTabMsg:
			m.Update(msg)
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

// A view that is not a configured tab has no reason to trap the human: it
// leaves to the tab they came from (#789).
func TestEscLeavesAListThatIsNotATab(t *testing.T) {
	m := tabsRoot(t, "tasks")
	for _, id := range []viewID{viewIssues, viewChats} {
		m.Update(selectViewMsg{id: id})
		pressEsc(m)
		if m.active != viewHome {
			t.Errorf("esc on %v (not a tab) went to %v, want the board", id, m.active)
		}
	}
	m = tabsRoot(t, "issues")
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(selectViewMsg{id: viewChats})
	pressEsc(m)
	if m.active != viewIssues {
		t.Errorf("esc on chats (not a tab) went to %v, want issues", m.active)
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

// A reload that drops the active tab still never moves the human (task 136
// decision 13, kept by #789); esc on the screen it left behind then goes to
// the first configured tab, since the last one is not a tab any more.
func TestDroppedTabEscGoesToTheFirstTab(t *testing.T) {
	m := tabsRoot(t, "issues", "tasks")
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(boardConfigMsg{tabs: []string{"chats", "tasks"}})
	if m.active != viewIssues {
		t.Fatalf("the reload moved the human to %v", m.active)
	}
	pressEsc(m)
	if m.active != viewChats {
		t.Fatalf("esc on the dropped tab went to %v, want chats, the first tab", m.active)
	}
}

// landedRoot is a connected root after Run's landing, before any config
// answer.
func landedRoot(t *testing.T) *root {
	t.Helper()
	m := connectedRoot(t)
	m.land()
	return m
}

// Run lands on the first default tab before the daemon answers (#789); the
// default config answer then leaves the human where they are.
func TestLandingOnTheDefaultFirstTab(t *testing.T) {
	m := landedRoot(t)
	if m.active != viewIssues || m.lastTab != viewIssues {
		t.Fatalf("landed on %v (lastTab %v), want issues", m.active, m.lastTab)
	}
	m.Update(boardConfigMsg{tabs: []string{"issues", "tasks"}})
	if m.active != viewIssues {
		t.Fatalf("the default config moved the landing to %v", m.active)
	}
}

// The first config answer moves an unmoved human to the configured first
// tab, once; a second answer never moves anyone (task 136 decision 13).
func TestLandingFollowsTheFirstConfigAnswer(t *testing.T) {
	for _, first := range []tea.Msg{
		boardConfigMsg{tabs: []string{"tasks", "issues"}},
		func() tea.Msg {
			var cfg apiclient.Config
			cfg.TUI.Tabs = []string{"tasks", "issues"}
			return daemonConfigMsg{config: cfg}
		}(),
	} {
		m := landedRoot(t)
		m.Update(boardConfigMsg{err: errors.New("boom")})
		if m.active != viewIssues {
			t.Fatalf("a failed answer moved the landing to %v", m.active)
		}
		m.Update(first)
		if m.active != viewHome || m.lastTab != viewHome {
			t.Fatalf("after %T the landing is %v (lastTab %v), want the board", first, m.active, m.lastTab)
		}
		m.Update(boardConfigMsg{tabs: []string{"chats", "issues"}})
		if m.active != viewHome {
			t.Fatalf("a second answer moved the human to %v", m.active)
		}
	}
}

// A human who navigated before the first answer stays where they went.
func TestLandingNeverOverridesTheHuman(t *testing.T) {
	m := landedRoot(t)
	m.Update(tabKey())
	if m.active != viewHome {
		t.Fatalf("tab from the landing went to %v, want the board", m.active)
	}
	m.Update(boardConfigMsg{tabs: []string{"chats", "issues"}})
	if m.active != viewHome {
		t.Fatalf("the first answer moved a human who had navigated to %v", m.active)
	}
}

// Every takeover's esc goes back to the tab it was opened from (#789): the
// issues tab or the tasks tab alike.
func TestTakeoversEscBackToTheirTab(t *testing.T) {
	for _, tab := range []viewID{viewIssues, viewHome} {
		for _, id := range []viewID{
			viewProjects, viewWorkflows, viewDaemon, viewPullRequests,
			viewTriggers, viewArchived, viewArchivedChats, viewNewTask, viewChats,
		} {
			m := tabsRoot(t, "issues", "tasks")
			m.Update(selectViewMsg{id: tab})
			m.Update(selectViewMsg{id: id})
			pressEsc(m)
			if m.active != tab {
				t.Errorf("esc on %v opened from %v went to %v", id, tab, m.active)
			}
		}
	}
}

// With the board left out of tui.tabs, it is a takeover too: reached by a
// switch, esc leaves it for the human's tab.
func TestBoardOffTheTabsLeavesByEsc(t *testing.T) {
	m := tabsRoot(t, "issues")
	m.Update(selectViewMsg{id: viewIssues})
	m.Update(selectViewMsg{id: viewHome})
	pressEsc(m)
	if m.active != viewIssues {
		t.Fatalf("esc on the board (not a tab) went to %v, want issues", m.active)
	}
	m = tabsRoot(t, "issues", "tasks")
	m.Update(selectViewMsg{id: viewHome})
	pressEsc(m)
	if m.active != viewHome {
		t.Fatalf("esc on the board as a tab went to %v", m.active)
	}
}

// A task's esc still goes back to the screen it was opened from; after a
// project switch emptied that, to the human's tab.
func TestTaskEscBackTargets(t *testing.T) {
	tv := newTaskView(newDetail(testCtx(t), newLevelHolder(), newRawHolder(), newHyperlinkHolder()))
	for back, want := range map[viewID]tea.Msg{
		viewHome:  selectViewMsg{id: viewHome},
		viewIssue: selectViewMsg{id: viewIssue},
	} {
		tv.back = back
		if got := tv.popCmd()(); got != want {
			t.Errorf("esc with back %v sent %#v, want %#v", back, got, want)
		}
	}
	tv.leaveForSwitch()
	if got := tv.popCmd()(); got != (backToTabMsg{}) {
		t.Errorf("esc after a switch sent %#v, want back to the tab", got)
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

// Every open that names no back and is not the board's own returns to the
// human's tab (review F1): the palette, the pull requests takeover, a new
// task. The board's own open still returns to the board.
func TestTaskOpenedOffTheBoardEscsToTheTab(t *testing.T) {
	for _, tc := range []struct {
		name string
		tabs []string
		from viewID
		open tea.Msg
		want viewID
	}{
		{"palette, board off the tabs", []string{"issues"}, viewIssues, openTaskMsg{id: 7}, viewIssues},
		{"pull requests from issues", []string{"issues", "tasks"}, viewIssues, selectTaskMsg{id: 7}, viewIssues},
		{"new task from issues", nil, viewIssues, taskCreatedMsg{task: apiclient.TaskDetail{Task: apiclient.Task{ID: 7}}}, viewIssues},
		{"the board's own row", []string{"issues"}, viewHome, selectTaskMsg{id: 7, fromBoard: true}, viewHome},
	} {
		m := tabsRoot(t, tc.tabs...)
		m.Update(selectViewMsg{id: viewIssues})
		m.Update(selectViewMsg{id: tc.from})
		m.Update(tc.open)
		if m.active != viewTask {
			t.Fatalf("%s: the open landed on %v, want the workspace", tc.name, m.active)
		}
		pressEsc(m)
		if m.active != tc.want {
			t.Errorf("%s: esc went to %v, want %v", tc.name, m.active, tc.want)
		}
	}
}

// A project switch on the workspace falls back to the board while it is a
// tab, and to the human's tab when it is not (review F2).
func TestSwitchOnTheWorkspaceLandsOnATab(t *testing.T) {
	for _, tc := range []struct {
		tabs []string
		want viewID
	}{
		{[]string{"issues", "chats"}, viewIssues},
		{[]string{"issues", "tasks"}, viewHome},
	} {
		m := tabsRoot(t, tc.tabs...)
		m.Update(selectViewMsg{id: viewIssues})
		m.Update(selectTaskMsg{id: 7, back: viewIssue})
		if m.active != viewTask {
			t.Fatalf("tabs %v: the open landed on %v", tc.tabs, m.active)
		}
		m.sel = projectSel{id: 1, name: "one"}
		m.applySwitch(apiclient.Project{ID: 2, Name: "two"}, "test")
		if m.active != tc.want {
			t.Errorf("tabs %v: a switch on the workspace landed on %v, want %v", tc.tabs, m.active, tc.want)
		}
	}
}
