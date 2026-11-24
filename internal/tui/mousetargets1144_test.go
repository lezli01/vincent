package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// clickText clicks the first place text is drawn on the root's screen,
// offset cells to its right.
func clickText(t *testing.T, m *root, text string, offset int) tea.Cmd {
	t.Helper()
	x, y := screenAt(t, m, text)
	_, cmd := m.Update(tea.MouseClickMsg{X: x + offset, Y: y, Button: tea.MouseLeft})
	return cmd
}

// formRoot is a connected root on a loaded new-task form at w×h.
func formRoot(t *testing.T, w, h int) (*root, *newTask) {
	t.Helper()
	m := connectedRoot(t)
	n := loadedForm(t)
	selectProject(n, 1)
	m.views[viewNewTask] = n
	m.switchNow(viewNewTask)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m, n
}

// #1144: a row of the new-task form's pickers runs on a click, as enter does.
func TestNewTaskPickerRowIsClickable(t *testing.T) {
	m, n := formRoot(t, 120, 40)
	n.cursor = ntWorkflow
	m.Update(enterKey())
	if n.mode != ntPicking || n.pick == nil {
		t.Fatalf("enter on the workflow row opened no picker: mode %v", n.mode)
	}
	clickText(t, m, "two-step", 1)
	if n.workflow != "two-step" || n.pick != nil {
		t.Fatalf("a click on two-step chose %q, picker open %v", n.workflow, n.pick != nil)
	}
}

// #1144: a click does not choose a row while a question owns the keyboard.
func TestPickerClickWaitsOnAQuestion(t *testing.T) {
	m, n := formRoot(t, 120, 40)
	n.cursor = ntWorkflow
	m.Update(enterKey())
	x, y := screenAt(t, m, "two-step")
	m.leave = &pendingLeave{}
	m.Update(tea.MouseClickMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
	if n.pick == nil || n.workflow == "two-step" {
		t.Fatal("a click chose a row under a question")
	}
}

// #1144: the copy picker's rows run on a click.
func TestCopyPickerRowIsClickable(t *testing.T) {
	m := connectedRoot(t)
	m.reader = newReaderPicker([]copyItem{
		{group: "message 1", label: "markdown", snippet: "the whole answer", text: "a"},
		{group: "message 1", label: "first code block", snippet: "go run", text: "b"},
	})
	clickText(t, m, "first code block", 1)
	if m.reader != nil {
		t.Fatal("a click on a copy row did not run it")
	}
}

// #1144: the link picker's rows run on a click.
func TestLinkPickerRowIsClickable(t *testing.T) {
	m := connectedRoot(t)
	m.linkPick = newLinkPicker([]linkItem{
		{group: "message 1", link: mdLink{n: 1, label: "first", dest: "https://example.com/a"}},
		{group: "message 1", link: mdLink{n: 2, label: "second", dest: "https://example.com/b"}},
	})
	clickText(t, m, "[2] second", 1)
	if m.linkPick != nil {
		t.Fatal("a click on a link row did not run it")
	}
}

// #1144: a chat's alt+p list runs a row on a click.
func TestChatSettingsPickerRowIsClickable(t *testing.T) {
	m := connectedRoot(t)
	v := chatViewFixture()
	v.chat.Model = "haiku"
	v.picker = newChatSettingsPicker(apiclient.Agent{
		Name: "claude", Models: []apiclient.AgentOption{{Value: "haiku"}, {Value: "opus"}},
	}, v.chat)
	m.views[viewChat] = v
	m.switchNow(viewChat)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if clickText(t, m, "model: opus", 1); v.picker != nil {
		t.Fatalf("a click on model: opus did not choose it: picker open %v", v.picker != nil)
	}
}

// #1144: `[ create task ]` takes a click on the narrow form, and while a row
// is being typed into, which it commits first as ctrl+s does.
func TestCreateButtonIsClickableEverywhere(t *testing.T) {
	m, n := formRoot(t, 120, 40)
	if n.frameless() {
		t.Fatal("the form is wide at 120 columns")
	}
	clickText(t, m, "[ create task ]", 2)
	if n.rowErr[ntTitle] == "" {
		t.Fatal("a click on create did not submit on the narrow form (no title was flagged)")
	}

	m, n = formRoot(t, 200, 50)
	n.cursor = ntTitle
	m.Update(enterKey())
	if n.mode != ntEditing {
		t.Fatalf("enter on the title row did not edit it: mode %v", n.mode)
	}
	clickText(t, m, "[ create task ]", 2)
	if n.mode != ntNavigating || n.rowErr[ntTitle] == "" {
		t.Fatalf("a click on create while editing did not commit and submit: mode %v", n.mode)
	}
}

// #1144: on the narrow Settings layout a click on a setting selects it.
func TestNarrowSettingsRowIsClickable(t *testing.T) {
	m := connectedRoot(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	v := m.views[viewSettings].(*settingsView)
	v.update(daemonConfigMsg{config: defaultClientConfig()})
	v.client = offlineClient()
	m.switchNow(viewSettings)
	if v.frameless() {
		t.Fatal("settings is wide at 100 columns")
	}
	v.category = 0
	v.selectFirst()
	clickText(t, m, "Chats at once", 0)
	if v.selected != "max_parallel_chats" {
		t.Fatalf("a click on Chats at once selected %q:\n%s", v.selected, ansi.Strip(m.View().Content))
	}
	k := mustSetting(t, v, v.selected)
	before := v.value(k)
	py, line := -1, ""
	for i, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if strings.Contains(l, "Chats at once") && strings.Contains(l, "+") {
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

// #1144: a row is found by its words standing alone on the clicked line, the
// cursor mark before them included, and not inside a longer word.
func TestRowAtFindsWholeRows(t *testing.T) {
	rows := []clickRow{{text: "opus", index: 1}, {text: "claude-opus", index: 2}}
	line := "│ model  claude-opus      ▸ opus  curated │"
	before := func(s string) int { b, _, _ := strings.Cut(line, s); return ansi.StringWidth(b) }
	x, cx := before("▸"), before("claude")
	for _, c := range []struct {
		x    int
		want int
		ok   bool
	}{
		{x, 1, true},      // the cursor mark
		{x + 2, 1, true},  // the word
		{x + 6, 0, false}, // past it
		{cx + 8, 2, true},
	} {
		got, ok := rowAt(line, c.x, rows)
		if ok != c.ok || got != c.want {
			t.Errorf("rowAt(x=%d) = %d, %v; want %d, %v", c.x, got, ok, c.want, c.ok)
		}
	}
}

// #1144: a click on a rewind-list row only points at it, in every mode of
// the picker — rewinding drops later turns, and a fork or compare starts
// chats, so only enter does them.
func TestRewindRowClickOnlySelects(t *testing.T) {
	for _, mode := range []string{"rewind", "fork", "compare"} {
		t.Run(mode, func(t *testing.T) {
			m := connectedRoot(t)
			v := chatViewFixture()
			v.client = offlineClient()
			turns := []apiclient.ChatTurn{
				{Seq: 1, Prompt: "first ask", Rewind: &apiclient.ChatTurnRewind{}},
				{Seq: 2, Prompt: "second ask", Rewind: &apiclient.ChatTurnRewind{}},
			}
			p := newChatRewindPicker(v.chat, turns, mode != "rewind", nil)
			if mode == "compare" {
				p.compareMode()
				p.agents = []string{"codex"}
			}
			v.rewind = p
			m.views[viewChat] = v
			m.switchNow(viewChat)
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			if p.cursor != 1 {
				t.Fatalf("the picker opened on row %d, want the newest", p.cursor)
			}
			if cmd := clickText(t, m, "turn 1  first ask", 1); cmd != nil || p.busy || v.rewind != p {
				t.Fatalf("a click sent something: cmd %v, busy %v", cmd != nil, p.busy)
			}
			if p.cursor != 0 {
				t.Fatalf("a click on turn 1 left the cursor on row %d", p.cursor)
			}
			if _, cmd := m.Update(enterKey()); cmd == nil || !p.busy {
				t.Fatalf("enter after the click did not %s", mode)
			}
		})
	}
}
