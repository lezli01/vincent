package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// paste drives the root the way a terminal does: bracketed paste arrives as
// a tea.PasteMsg, not as keystrokes.
func paste(m *root, text string) {
	m.Update(tea.PasteMsg{Content: text})
}

// connectedRoot is a root past the notice and the connect flow, sized, with
// nothing selected — the state every paste test starts from.
func connectedRoot(t *testing.T) *root {
	t.Helper()
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.notice.active = false
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

// TestPasteFillsTheProjectPathField is the M3 gate finding: registering a
// project means pasting a repository path, and paste reached no field at all
// — bracketed paste took the broadcast route every background message takes,
// and no view knew the message type.
func TestPasteFillsTheProjectPathField(t *testing.T) {
	m := connectedRoot(t)
	m.Update(selectViewMsg{id: viewProjects})
	m.Update(key("a")) // the add form
	m.Update(enterKey())

	form := m.views[viewProjects].(*projectsView).form
	if form == nil {
		t.Fatal("the add form did not open")
	}
	// The row opens seeded with the working directory; clear it so the
	// assertion is about the paste and not about the seed.
	form.path.SetValue("")

	const repo = "/Users/someone/src/vincent"
	paste(m, repo)

	if got := form.path.Value(); got != repo {
		t.Fatalf("path = %q after pasting, want %q", got, repo)
	}
}

// TestPasteReachesOnlyTheActiveView: a paste is input for one field. The
// broadcast path would have typed the repository path into the new-task
// form's title at the same time.
func TestPasteReachesOnlyTheActiveView(t *testing.T) {
	m := connectedRoot(t)
	m.Update(selectViewMsg{id: viewProjects})
	m.Update(key("a"))
	m.Update(enterKey())
	paste(m, "/tmp/repo")

	nt := m.views[viewNewTask].(*newTask)
	if nt.titleIn.Value() != "" || nt.desc.Value() != "" {
		t.Fatalf("the paste leaked into the new-task form: title %q desc %q",
			nt.titleIn.Value(), nt.desc.Value())
	}
}

// TestPasteWithNoFieldCapturingIsDropped: pasting onto the board must not be
// replayed as keystrokes — "a" and "c" are task actions there.
func TestPasteWithNoFieldCapturingIsDropped(t *testing.T) {
	m := connectedRoot(t)
	paste(m, "archive cancel")
	s := m.views[viewHome].(*shell)
	if got := s.board.filter.Value(); got != "" {
		t.Fatalf("a paste with no field focused reached the filter: %q", got)
	}
	if s.board.filtering {
		t.Error("a paste opened the filter")
	}
}

// TestPasteIntoTheBoardFilter: the filter is a text field, so it takes a
// paste like one.
func TestPasteIntoTheBoardFilter(t *testing.T) {
	m := connectedRoot(t)
	m.Update(key("/"))
	paste(m, "running")
	if got := m.views[viewHome].(*shell).board.filter.Value(); got != "running" {
		t.Fatalf("filter = %q after pasting, want %q", got, "running")
	}
}

// TestPasteIntoTheDescription: the description is a textarea, and a pasted
// description is the multi-line case.
func TestPasteIntoTheDescription(t *testing.T) {
	m := connectedRoot(t)
	m.Update(selectViewMsg{id: viewNewTask})
	nt := m.views[viewNewTask].(*newTask)
	nt.cursor = ntDescription
	m.Update(enterKey())

	const body = "first line\nsecond line"
	paste(m, body)
	if got := nt.desc.Value(); got != body {
		t.Fatalf("description = %q after pasting, want %q", got, body)
	}
}

// TestPasteIntoThePalette: the palette overlays every screen and owns the
// keyboard while it is open, so it takes the paste rather than the view
// underneath.
func TestPasteIntoThePalette(t *testing.T) {
	m := connectedRoot(t)
	m.Update(key(":"))
	if m.palette == nil {
		t.Fatal("the palette did not open")
	}
	paste(m, "workflows")
	if got := m.palette.input.Value(); got != "workflows" {
		t.Fatalf("palette search = %q after pasting, want %q", got, "workflows")
	}
}

// TestCtrlVReadsTheClipboardOnlyForAField: the fallback for terminals that
// hand ctrl+v to the app. With nothing capturing text there is nowhere to
// paste, so it must not shell out to read a clipboard it would discard.
func TestCtrlVReadsTheClipboardOnlyForAField(t *testing.T) {
	m := connectedRoot(t)
	if _, cmd := m.Update(ctrlV()); cmd != nil {
		t.Error("ctrl+v read the clipboard with no field focused")
	}
	m.Update(key("/"))
	if _, cmd := m.Update(ctrlV()); cmd == nil {
		t.Error("ctrl+v with the filter focused produced no clipboard read")
	}
}

func ctrlV() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}
}

func enterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter}
}

// TestPasteIsDocumented keeps the fallback discoverable: ? renders from the
// registry, so a key that is not a row is a key nobody finds.
func TestPasteIsDocumented(t *testing.T) {
	if !strings.Contains(helpText(ctxProjects), "ctrl+v") {
		t.Error("ctrl+v is not in the help overlay")
	}
}
