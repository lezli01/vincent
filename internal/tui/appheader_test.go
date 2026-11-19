package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The app header and status bar (task 150.4). Pure model tests, one property
// each (task 129 decision 7).

// A tab shows its count once its screen has loaded, and not before: no count is
// not the same as a count of nothing.
func TestTabsCountWhatTheyHold(t *testing.T) {
	m := tabsRoot(t, "issues", "chats")
	m.switchTo(viewIssues)
	if got := stripHeader(m, 120); strings.Contains(got, "(") {
		t.Fatalf("a tab counted before it loaded: %q", got)
	}
	issues := m.views[viewIssues].(*issuesView)
	issues.issues, issues.loaded = []apiclient.Issue{{ID: 1}, {ID: 2}}, true
	chats := m.views[viewChats].(*chatsView)
	chats.loaded = true
	got := stripHeader(m, 120)
	for _, want := range []string{"▦ Issues (2)", "◌ Chats (0)"} {
		if !strings.Contains(got, want) {
			t.Errorf("header %q is missing %q", got, want)
		}
	}
}

// Counts go before the labels shorten, and a click still lands on the label.
func TestTabCountsShedBeforeLabels(t *testing.T) {
	m := tabsRoot(t, "issues", "tasks")
	m.sel = projectSel{id: 1, name: strings.Repeat("n", 20)}
	m.switchTo(viewIssues)
	issues := m.views[viewIssues].(*issuesView)
	issues.issues, issues.loaded = []apiclient.Issue{{ID: 1}}, true
	got := stripHeader(m, 46)
	if strings.Contains(got, "(1)") || !strings.Contains(got, "▦ Issues │ ▶ Tasks") {
		t.Fatalf("at 46 columns header = %q, want the names without counts", got)
	}
	clickHeader(m, tabSpan(t, m, viewHome).x0)
	if m.active != viewHome {
		t.Fatalf("a click on Tasks went to %v", m.active)
	}
}

// The rule is drawn where the terminal can spare the row, and is nobody's
// click.
func TestHeaderRule(t *testing.T) {
	m := tabsRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: minShellH})
	if m.headerRuleShown() || m.chromeTop() != 1 {
		t.Fatalf("a %d-row terminal drew the rule (chrome %d)", minShellH, m.chromeTop())
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: headerRuleMinH})
	if !m.headerRuleShown() || m.chromeTop() != 2 {
		t.Fatalf("a %d-row terminal has no rule (chrome %d)", headerRuleMinH, m.chromeTop())
	}
	lines := strings.Split(ansi.Strip(content(m)), "\n")
	if lines[1] != strings.Repeat("─", 100) {
		t.Errorf("row 1 = %q, want the rule", lines[1])
	}
	spy := &clickSpy{}
	m.views[m.active] = spy
	m.Update(tea.MouseClickMsg{X: 3, Y: 1, Button: tea.MouseLeft})
	if spy.clicks != 0 {
		t.Error("a click on the rule reached the screen")
	}
	m.Update(tea.MouseClickMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	if spy.clicks != 1 || spy.y != 0 {
		t.Errorf("a click under the rule: %d clicks at y %d, want one at 0", spy.clicks, spy.y)
	}
}

type clickSpy struct{ clicks, y int }

func (c *clickSpy) title() string { return "spy" }

func (c *clickSpy) update(msg tea.Msg) (panel, tea.Cmd) {
	if click, ok := msg.(tea.MouseClickMsg); ok {
		c.clicks++
		c.y = click.Y
	}
	return c, nil
}

func (c *clickSpy) render(_, _ int) string { return "" }

// However wide the terminal, the status bar offers the main keys, and `+N`
// accounts for the rest.
func TestStatusBarCapsTheKeys(t *testing.T) {
	rows := bindingsFor(ctxIssues)
	line, _ := buildFooter(400, rows, nil, taskActions{}, attentionTally{}, false, false)
	plain := ansi.Strip(line)
	if got := len(footerShown(plain, rows)); got > footerMainKeys+1 {
		t.Errorf("%d keys on the line, want at most %d and tab: %q", got, footerMainKeys, plain)
	}
	if footerMore(plain) == 0 {
		t.Errorf("the keys held back are not counted: %q", plain)
	}
}

// A form's own keys cannot be reached through the palette, so the cap never
// hides one.
func TestStatusBarNeverHoldsBackAFormsKeys(t *testing.T) {
	hints := make([]footerSeg, 0, footerMainKeys+3)
	for range footerMainKeys + 3 {
		hints = append(hints, footerSeg{text: "x"})
	}
	if got := len(footerMainHints(hints)); got != footerMainKeys+3 {
		t.Errorf("kept %d of %d palette-less hints", got, footerMainKeys+3)
	}
}

// The status bar names the screen, and its clickable keys stay under the
// pointer with that name in front of them.
func TestStatusBarNamesTheScreen(t *testing.T) {
	m := tabsRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.switchTo(viewIssues)
	line := ansi.Strip(m.footerLine())
	if !strings.HasPrefix(line, " ▦ Issues ") {
		t.Fatalf("status bar = %q, want it to start with the screen's name", line)
	}
	if got := ansi.StringWidth(line); got > 120 {
		t.Errorf("status bar is %d wide", got)
	}
	for _, h := range m.footerHits {
		if h.key == "/" && !strings.HasPrefix(ansi.Cut(line, h.x0, h.x1), "/ filter") {
			t.Errorf("the / hit covers %q", ansi.Cut(line, h.x0, h.x1))
		}
	}
	m.Update(tea.WindowSizeMsg{Width: footerPillMinW - 1, Height: 30})
	if line := ansi.Strip(m.footerLine()); strings.HasPrefix(line, " ▦ Issues ") {
		t.Errorf("a narrow status bar spent room on the name: %q", line)
	}
}
