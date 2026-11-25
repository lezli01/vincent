package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// #1080: ` opens the screens menu, and a screen's letter goes there.
func TestGoToMenuJumpsByLetter(t *testing.T) {
	m := connectedRoot(t)
	m.updateKey(keyPress("`"))
	if m.palette == nil || !m.palette.jump {
		t.Fatal("` did not open the screens menu")
	}
	out := ansi.Strip(m.palette.render(80, 30))
	for _, want := range []string{"daemon", "settings", "triggers", "workflows"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screens menu does not list %s:\n%s", want, out)
		}
	}
	m.Update(keyPress("d"))
	if m.palette != nil {
		t.Fatal("the menu stayed open after a letter")
	}
	if m.active != viewDaemon {
		t.Fatalf("d went to %v, want the daemon screen", m.active)
	}
}

// Review finding 3: the menu's n opens the new-task form the way n does —
// told to open, so it loads — rather than switching to a form that waits for
// a load nobody asked for.
func TestGoToMenuOpensTheNewTaskForm(t *testing.T) {
	m := connectedRoot(t)
	m.updateKey(keyPress("`"))
	m.Update(keyPress("n"))
	if m.active != viewNewTask {
		t.Fatalf("n went to %v, want the new-task form", m.active)
	}
	if n, ok := m.views[viewNewTask].(*newTask); !ok || !n.opened {
		t.Fatal("the form was shown without being opened, so it never loads")
	}
}

// #1080: the header names the menu's key.
func TestHeaderNamesTheScreensMenu(t *testing.T) {
	m := connectedRoot(t)
	m.width = 160
	if got := ansi.Strip(m.headerLine()); !strings.Contains(got, "` screens") {
		t.Fatalf("the header does not name the screens menu: %q", got)
	}
}

// Every screen the menu lists has a letter, and no two share one.
func TestScreenLettersAreDistinct(t *testing.T) {
	seen := map[string]viewID{}
	for _, b := range registry() {
		if !b.nav {
			continue
		}
		l := screenLetters[b.navTarget]
		if l == "" {
			t.Errorf("screen %v has no letter", b.navTarget)
			continue
		}
		if prev, ok := seen[l]; ok && prev != b.navTarget {
			t.Errorf("letter %q opens both %v and %v", l, prev, b.navTarget)
		}
		seen[l] = b.navTarget
	}
}

// #1080: the rewind picker names fork and compare, and f switches to fork
// with the cursor kept on its turn; f again comes back.
func TestRewindPickerSwitchesToFork(t *testing.T) {
	v := rewindFixture()
	v.openRewindPicker(false)
	if head := ansi.Strip(strings.Join(v.rewind.render(200)[:2], "\n")); !strings.Contains(head, "f fork") || !strings.Contains(head, "c compare") {
		t.Fatalf("the picker does not name fork and compare:\n%s", head)
	}
	seq := v.rewind.rows[v.rewind.cursor].seq
	v.updateKey(keyPress("f"))
	if v.rewind == nil || !v.rewind.fork {
		t.Fatal("f did not switch the picker to fork")
	}
	if got := v.rewind.rows[v.rewind.cursor].seq; got != seq {
		t.Fatalf("the cursor moved from turn %d to %d", seq, got)
	}
	v.updateKey(keyPress("f"))
	if v.rewind.fork {
		t.Fatal("f again did not come back to rewind")
	}
}

// #1159: under a text field the menu's key is a character the field types,
// so the header does not offer it there.
func TestHeaderOmitsTheScreensMenuUnderATextField(t *testing.T) {
	m := connectedRoot(t)
	m.width = 160
	m.views[viewChatSearch] = newChatSearchView()
	m.active = viewChatSearch
	if !m.activeCapturesInput() {
		t.Fatal("the chat search does not capture input")
	}
	if got := ansi.Strip(m.headerLine()); strings.Contains(got, "screens") {
		t.Fatalf("the header offers the screens menu over a text field: %q", got)
	}
	// Nor while disconnected, where the key opens nothing.
	m.active = viewHome
	m.phase = phaseFailed
	if got := ansi.Strip(m.headerLine()); strings.Contains(got, "screens") {
		t.Fatalf("the header offers the screens menu while disconnected: %q", got)
	}
}
