package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The main-tab strip (task 136.3): the header names the configured tabs on
// a tab root, sheds decoration before navigation, and a click on a label
// switches by the tab key's rule.

// stripHeader renders the header at width and returns it uncoloured.
func stripHeader(m *root, width int) string {
	m.width = width
	return ansi.Strip(m.headerLine())
}

// clickHeader left-clicks row 0 at x, rendering the header first so the
// spans are the ones on screen.
func clickHeader(m *root, x int) {
	m.headerLine()
	m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
}

// tabSpan is the rendered span of the label for id.
func tabSpan(t *testing.T, m *root, id viewID) headerTabHit {
	t.Helper()
	m.headerLine()
	for _, h := range m.headerTabs {
		if h.id == id {
			return h
		}
	}
	t.Fatalf("no strip label for %v in %v", id, m.headerTabs)
	return headerTabHit{}
}

func TestStripOnBothDefaultTabs(t *testing.T) {
	m := tabsRoot(t)
	m.version = "0.9.0"
	for _, id := range []viewID{viewHome, viewIssues} {
		m.Update(selectViewMsg{id: id})
		got := stripHeader(m, 120)
		if !strings.HasSuffix(got, "◆ no project  Issues │ Tasks") {
			t.Errorf("view %v header = %q, want the strip in place of the tag", id, got)
		}
		if strings.Contains(got, "[") {
			t.Errorf("view %v header = %q still carries a tag", id, got)
		}
	}
}

// TestStripShedding walks the widths down: the version goes, then the
// elsewhere badge, then the labels shorten, and only then does the name
// truncate. The strip is never dropped.
func TestStripShedding(t *testing.T) {
	m := attentionRoot(t, task(2, stateBlocked, inProjectID(otherProjectID, "other")))
	m.version = "0.9.0"
	m.sel.name = "a-twenty-char-projec"
	m.views[viewHome].(*shell).setProject(m.sel)

	for _, id := range []viewID{viewHome, viewIssues} {
		m.Update(selectViewMsg{id: id})
		got := stripHeader(m, 80)
		if !strings.Contains(got, "Issues │ Tasks") || !strings.Contains(got, m.sel.name) {
			t.Fatalf("view %v at 80 columns: %q, want the full strip and name", id, got)
		}
	}

	seen := map[string]bool{}
	for w := 80; w >= 32; w-- {
		got := stripHeader(m, w)
		long := strings.HasSuffix(got, "Issues │ Tasks")
		short := strings.HasSuffix(got, "I │ T")
		version := strings.Contains(got, "0.9.0")
		badge := strings.Contains(got, "elsewhere")
		whole := strings.Contains(got, m.sel.name)
		switch {
		case ansi.StringWidth(got) > w:
			t.Fatalf("at %d columns the header is %d wide: %q", w, ansi.StringWidth(got), got)
		case !long && !short:
			t.Fatalf("at %d columns the strip is gone: %q", w, got)
		case version && !badge:
			t.Fatalf("at %d columns the badge went before the version: %q", w, got)
		case badge && !long:
			t.Fatalf("at %d columns the labels shrank before the badge went: %q", w, got)
		case !whole && !short:
			t.Fatalf("at %d columns the name truncated before the labels shrank: %q", w, got)
		}
		seen[map[bool]string{true: "long", false: "short"}[long]] = true
		if !whole {
			seen["truncated"] = true
		}
		if w == 40 && (!short || !whole) {
			t.Fatalf("at 40 columns header = %q, want initials and the whole name", got)
		}
	}
	for _, s := range []string{"long", "short", "truncated"} {
		if !seen[s] {
			t.Errorf("the walk never reached a %s header", s)
		}
	}
}

// The strip lives on the header's one row: no view's chrome grows.
func TestStripKeepsTheChrome(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {60, 15}} {
		for _, id := range []viewID{viewHome, viewIssues, viewChats, viewTask, viewIssue, viewDaemon} {
			m := tabsRoot(t, "issues", "tasks", "chats")
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.switchTo(id)
			top, body := m.chromeTop(), m.bodyHeight()
			m.Update(boardConfigMsg{tabs: []string{"tasks"}})
			if m.chromeTop() != top || m.bodyHeight() != body {
				t.Errorf("%dx%d view %v: strip chrome %d/%d, tag chrome %d/%d",
					size[0], size[1], id, top, body, m.chromeTop(), m.bodyHeight())
			}
		}
	}
}

func TestStripClickSwitches(t *testing.T) {
	m := tabsRoot(t)
	m.switchTo(viewHome)
	issues := tabSpan(t, m, viewIssues)
	clickHeader(m, issues.x1-1)
	if m.active != viewIssues || m.lastTab != viewIssues {
		t.Fatalf("click on Issues: active %v, lastTab %v", m.active, m.lastTab)
	}
	tasks := tabSpan(t, m, viewHome)
	clickHeader(m, tasks.x0)
	if m.active != viewHome || m.lastTab != viewHome {
		t.Fatalf("click on Tasks: active %v, lastTab %v", m.active, m.lastTab)
	}

	// The active label, the separator between labels and a cell past the
	// strip do nothing.
	for _, x := range []int{tasks.x0, issues.x1 + 1, tasks.x1 + 1} {
		clickHeader(m, x)
		if m.active != viewHome {
			t.Fatalf("click at %d moved to %v", x, m.active)
		}
	}

	// `◆` still opens the picker. The fetch it returns is never run.
	m.client = apiclient.New("http://127.0.0.1:1", "t")
	clickHeader(m, m.headerHit[0])
	if m.projPick == nil {
		t.Fatal("a click on ◆ did not open the project picker")
	}
}

func TestStripClickWhileTypingDoesNothing(t *testing.T) {
	m := tabsRoot(t)
	m.switchTo(viewHome)
	m.Update(key("/"))
	if !m.views[viewHome].(*shell).board.filtering {
		t.Fatal("/ did not open the board's filter")
	}
	if got := stripHeader(m, 120); !strings.Contains(got, "Issues │ Tasks") {
		t.Fatalf("typing hid the strip: %q", got)
	}
	clickHeader(m, tabSpan(t, m, viewIssues).x0)
	if m.active != viewHome {
		t.Fatalf("a click while the filter types moved to %v", m.active)
	}
}

func TestStripInEveryConnectionPhase(t *testing.T) {
	for _, phase := range []connPhase{phaseProbing, phaseStarting, phaseReconnecting, phaseFailed} {
		m := tabsRoot(t)
		m.switchTo(viewHome)
		m.phase = phase
		if got := stripHeader(m, 120); !strings.Contains(got, "Issues │ Tasks") {
			t.Errorf("phase %v header = %q, want the strip", phase, got)
			continue
		}
		clickHeader(m, tabSpan(t, m, viewIssues).x0)
		if m.active != viewIssues {
			t.Errorf("phase %v: a click went to %v, want issues", phase, m.active)
		}
	}
}

func TestNoStripOffATabRoot(t *testing.T) {
	m := tabsRoot(t, "issues", "tasks", "chats")
	for _, id := range []viewID{
		viewTask, viewIssue, viewArchived, viewArchivedChats, viewProjects,
		viewWorkflows, viewDaemon, viewPullRequests, viewTriggers, viewChat, viewNewTask,
	} {
		m.switchTo(viewHome)
		m.headerLine()
		m.switchTo(id)
		got := stripHeader(m, 120)
		if strings.Contains(got, " │ ") || len(m.headerTabs) != 0 {
			t.Errorf("view %v header = %q, spans %v: want no strip", id, got, m.headerTabs)
		}
	}

	t.Run("new chat form", func(t *testing.T) {
		m := tabsRoot(t, "chats", "tasks")
		m.switchTo(viewChats)
		m.views[viewChats].(*chatsView).create = newNewChatForm(nil, 1)
		if got := stripHeader(m, 120); !strings.HasSuffix(got, "[Chats]") || len(m.headerTabs) != 0 {
			t.Errorf("new-chat form header = %q, want the tag and no strip", got)
		}
	})
	t.Run("one tab", func(t *testing.T) {
		m := tabsRoot(t, "tasks")
		m.switchTo(viewHome)
		if got := stripHeader(m, 120); !strings.HasSuffix(got, "[Tasks]") || len(m.headerTabs) != 0 {
			t.Errorf("single-tab header = %q, want the tag and no strip", got)
		}
	})
}

func TestStripFollowsTheConfig(t *testing.T) {
	m := tabsRoot(t, "chats", "issues", "tasks")
	m.switchTo(viewIssues)
	if got := stripHeader(m, 120); !strings.HasSuffix(got, "Chats │ Issues │ Tasks") {
		t.Fatalf("header = %q, want three labels in config order", got)
	}
	var ids []viewID
	for _, h := range m.headerTabs {
		ids = append(ids, h.id)
	}
	if want := []viewID{viewChats, viewIssues, viewHome}; !slices.Equal(ids, want) {
		t.Fatalf("spans name %v, want %v", ids, want)
	}
	m.Update(boardConfigMsg{tabs: []string{"tasks", "issues"}})
	if got := stripHeader(m, 120); !strings.HasSuffix(got, "Tasks │ Issues") || m.active != viewIssues {
		t.Fatalf("after a reload header = %q on %v, want the new strip and no move", got, m.active)
	}
}

func TestTheBoardIsCalledTasks(t *testing.T) {
	m := tabsRoot(t, "tasks")
	if got := m.views[viewHome].title(); got != "Tasks" {
		t.Errorf("board title = %q", got)
	}
	if got := m.views[viewHome].(*shell).board.title(); got != "Tasks" {
		t.Errorf("board sub-model title = %q", got)
	}
	if got := strings.Join(m.views[viewTask].(*taskView).crumbs(), crumbSep); !strings.HasPrefix(got, "Tasks"+crumbSep) {
		t.Errorf("workspace breadcrumb = %q, want it rooted at Tasks", got)
	}
}
