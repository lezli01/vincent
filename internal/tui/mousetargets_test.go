package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// screenAt finds text on the root's rendered screen.
func screenAt(t *testing.T, m *root, text string) (x, y int) {
	t.Helper()
	for i, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if at := strings.Index(line, text); at >= 0 {
			return ansi.StringWidth(line[:at]), i
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, ansi.Strip(m.View().Content))
	return 0, 0
}

// #1065: a click on a palette row runs it, as enter does.
func TestPaletteRowIsClickable(t *testing.T) {
	m := connectedRoot(t)
	m.openPalette()
	for _, r := range "settings" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	x, y := screenAt(t, m, "settings — everything")
	m.Update(tea.MouseClickMsg{X: x + 2, Y: y, Button: tea.MouseLeft})
	if m.palette != nil || m.active != viewSettings {
		t.Fatalf("the click did not run the row: palette open %v, active %v", m.palette != nil, m.active)
	}
}

// #1065: the first click on a card selects it, the second opens it.
func TestSecondClickOpensTheCard(t *testing.T) {
	b := taskBoardFixture()
	b.cardColumns(200, 40, 0)
	var hit taskCardHit
	for _, h := range b.cardHits {
		if !h.stop.header && h.stop.id == 21 {
			hit = h
		}
	}
	if hit.stop.id == 0 {
		t.Fatal("no hit for card 21")
	}
	if b.cardClick(hit.x0, hit.y0) {
		t.Fatal("the first click opened the card")
	}
	if id, _ := b.cardSelected(); id != 21 {
		t.Fatalf("the first click selected %d", id)
	}
	if !b.cardClick(hit.x0, hit.y0) {
		t.Fatal("a second click on the selected card did not open it")
	}
}

// #1065: on Settings a click picks a category, a setting, and steps the
// selected setting with its − and +.
func TestSettingsAreClickable(t *testing.T) {
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	v := m.views[viewSettings].(*settingsView)
	v.update(daemonConfigMsg{config: defaultClientConfig()})
	v.client = offlineClient()
	m.switchNow(viewSettings)
	if !v.frameless() {
		t.Fatal("settings is not wide at 200×50")
	}
	x, y := screenAt(t, m, "Timeouts")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if settingsCatalog[v.category].name != "Timeouts" {
		t.Fatalf("a click on Timeouts selected %q", settingsCatalog[v.category].name)
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}) // back to a known category
	v.category = 0
	v.selectFirst()
	x, y = screenAt(t, m, "Chats at once")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if v.selected != "max_parallel_chats" {
		t.Fatalf("a click on Chats at once selected %q", v.selected)
	}
	k := mustSetting(t, v, v.selected)
	before := v.value(k)
	py, line := -1, ""
	for i, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if strings.Contains(l, "Chats at once") && strings.Contains(l, "−") {
			py, line = i, l
			break
		}
	}
	if py < 0 {
		t.Fatal("no stepper line for Chats at once")
	}
	at := strings.Index(line, "Chats at once")
	plus := strings.Index(line[at:], "+")
	m.Update(tea.MouseClickMsg{X: ansi.StringWidth(line[:at+plus]), Y: py, Button: tea.MouseLeft})
	if v.value(k) == before {
		t.Fatalf("a click on + left %s at %s", k.path, before)
	}
}

// #1065: on the wide new-task form a click on a workflow card chooses it,
// and a click on the create button submits.
func TestNewTaskIsClickable(t *testing.T) {
	n := loadedForm(t)
	selectProject(n, 1)
	n.update(tea.WindowSizeMsg{Width: 200, Height: 50})
	if !n.frameless() {
		t.Fatal("the form is not wide at 200×50")
	}
	n.render(200, 50)
	at := func(text string) (int, int) {
		for y, line := range n.wideText {
			if i := strings.Index(line, text); i >= 0 {
				return len([]rune(line[:i])), y
			}
		}
		t.Fatalf("%q is not on the form:\n%s", text, strings.Join(n.wideText, "\n"))
		return 0, 0
	}
	x, y := at("two-step")
	n.update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if n.workflow != "two-step" {
		t.Fatalf("a click on two-step chose %q", n.workflow)
	}
	n.render(200, 50)
	x, y = at("[ create task ]")
	n.update(tea.MouseClickMsg{X: x + 2, Y: y, Button: tea.MouseLeft})
	if n.rowErr[ntTitle] == "" {
		t.Fatal("a click on create did not submit (no title was flagged)")
	}
}
