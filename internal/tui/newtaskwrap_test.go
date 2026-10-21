package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Issue #797 N2: the form's text rows wrap at the pane the root hands
// render, not at the terminal width. At 80×24 the root draws the form in
// 78 columns inside its frame, so a title sized for 80 overruns each wrapped
// row by two cells and frame() cuts them with "…" — spec §15 "Text fields
// wrap" forbids exactly that.
func TestNewTaskTitleWrapsToThePaneAt80x24(t *testing.T) {
	n := loadedForm(t)
	n.projectID = 1
	n.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	title := strings.Repeat("abcdefghij", 15)
	moveTo(n, ntTitle)
	press(n, "enter")
	typeText(n, title)

	const paneW, paneH = 80 - 2, 24 - 2 // root.go: render(m.width-2, h-2)
	out := ansi.Strip(n.render(paneW, paneH))
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > paneW {
			t.Errorf("line %d is %d cells wide in a %d-cell pane: %q", i, w, paneW, line)
		}
	}
	var visible strings.Builder
	for _, line := range strings.Split(out, "\n") {
		visible.WriteString(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "title")))
	}
	if !strings.Contains(visible.String(), title) {
		t.Errorf("the wrapped title is not fully visible in the pane:\n%s", out)
	}
}

// Issue #797 N6: spec §15 (task 020) says ctrl+s "still submits from
// anywhere", and the create button says so too, but only the navigation mode
// handles it. Each case leaves the form in a mode that owns the keys and
// presses ctrl+s; submitting either posts or reports why it will not, and a
// silent no-op is the bug.
func TestNewTaskCtrlSSubmitsFromEveryMode(t *testing.T) {
	cases := map[string]func(t *testing.T, n *newTask){
		"editing the title": func(t *testing.T, n *newTask) {
			moveTo(n, ntTitle)
			press(n, "enter")
			if n.mode != ntEditing {
				t.Fatalf("enter on the title row left mode %v, want editing", n.mode)
			}
		},
		"in the workflow picker": func(t *testing.T, n *newTask) {
			moveTo(n, ntWorkflow)
			press(n, "enter")
			if n.mode != ntPicking {
				t.Fatalf("enter on the workflow row left mode %v, want picking", n.mode)
			}
		},
		"in the fields editor": func(t *testing.T, n *newTask) {
			moveTo(n, ntFields)
			press(n, "enter")
			if n.mode != ntFieldsOpen {
				t.Fatalf("enter on the fields row left mode %v, want the fields editor", n.mode)
			}
		},
	}
	for name, enter := range cases {
		t.Run(name, func(t *testing.T) {
			n := loadedForm(t)
			n.projectID = 1
			n.client = offlineClient()
			n.titleIn.SetValue("Improve takeover UX")
			enter(t, n)
			cmd := press(n, "ctrl+s")
			if cmd == nil && !n.submitting && n.err == "" && len(n.rowErr) == 0 {
				t.Fatalf("ctrl+s %s neither submitted nor reported why it would not (mode %v)", name, n.mode)
			}
		})
	}
}
