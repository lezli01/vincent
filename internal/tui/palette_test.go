package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// everyAction is a target the daemon offers everything on, so gating cannot
// hide an entry from the reachability sweep.
var everyAction = taskActions{id: 9, state: stateRunning, actions: []string{
	apiclient.ActionPause, apiclient.ActionResume, apiclient.ActionApprove,
	apiclient.ActionReject, apiclient.ActionRetry, apiclient.ActionRepair,
	apiclient.ActionSkip, apiclient.ActionCancel, apiclient.ActionArchive,
	apiclient.ActionFollowUp,
}}

// TestPaletteReachesEveryRegistryEntry is the T3.11 done-when: every
// registry row surfaces in the palette in its owning context. The palette's
// own controls and the two popups' keys are exempt by construction
// (noPalette) — a form prints its keys inside the popup that owns the
// keyboard.
func TestPaletteReachesEveryRegistryEntry(t *testing.T) {
	contexts := []bindingContext{
		ctxTasks, ctxTimeline, ctxOutput, ctxDiff,
		ctxNewTask, ctxProjects, ctxWorkflows, ctxWorkflowGraph, ctxDaemon,
	}
	for _, b := range bindings {
		if b.noPalette {
			continue
		}
		found := false
		for _, ctx := range contexts {
			for _, e := range paletteEntries(ctx, everyAction, true, true) {
				if e.label == b.label {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("registry entry %q (key %q) is unreachable from the palette", b.label, b.key)
		}
	}
}

// TestPaletteOmitsInvalidActions holds §15's invariant: an action that
// cannot happen is not on screen — omitted, never greyed.
func TestPaletteOmitsInvalidActions(t *testing.T) {
	target := taskActions{id: 3, state: stateAwaitingGate, actions: []string{
		apiclient.ActionApprove, apiclient.ActionReject,
	}}
	labels := strings.Builder{}
	for _, e := range paletteEntries(ctxTasks, target, false, true) {
		labels.WriteString(e.label + "\n")
	}
	got := labels.String()
	if !strings.Contains(got, "approve the gate") {
		t.Errorf("palette misses the valid approve:\n%s", got)
	}
	for _, invalid := range []string{"cancel the task", "pause the running", "edit the step's prompt"} {
		if strings.Contains(got, invalid) {
			t.Errorf("palette offers %q, which the daemon does not:\n%s", invalid, got)
		}
	}
}

// TestPaletteEditGate: E rides the retry action but needs a step with text
// to edit — a gate has none, so the palette must not offer it.
func TestPaletteEditGate(t *testing.T) {
	target := taskActions{id: 3, state: stateBlocked, actions: []string{apiclient.ActionRetry}}
	for _, e := range paletteEntries(ctxTasks, target, false, true) {
		if strings.Contains(e.label, "edit the step's prompt") {
			t.Fatal("palette offers edit+retry on a step with nothing to edit")
		}
	}
}

// TestPaletteDisconnectedKeepsNavigation: while the daemon is unreachable
// nothing can act on a task, but `:` is how §15 says the daemon view stays
// reachable — navigation and panel commands survive.
func TestPaletteDisconnectedKeepsNavigation(t *testing.T) {
	entries := paletteEntries(ctxTasks, everyAction, true, false)
	var nav, actions int
	for _, e := range entries {
		if e.nav {
			nav++
		}
		if strings.HasPrefix(e.group, "actions") {
			actions++
		}
	}
	if actions != 0 {
		t.Errorf("palette offers %d task actions while disconnected, want 0", actions)
	}
	if nav != 4 {
		t.Errorf("palette lists %d navigation entries while disconnected, want all 4", nav)
	}
}

// TestPaletteSectionsAreVisiblySeparate is a T3.8 finding: the group
// headings looked exactly like the entries, so the list read as one flat
// wall — and navigation needs its own section, since reaching the takeover
// screens is why the digits could be retired.
func TestPaletteSectionsAreVisiblySeparate(t *testing.T) {
	p := newPalette(paletteEntries(ctxTasks, everyAction, true, true))
	out := p.render(60, 18)
	plain := ansi.Strip(out)

	if !strings.Contains(plain, "VIEWS") {
		t.Errorf("no views section:\n%s", plain)
	}
	// Headings are styled and ruled; entries are not.
	if !strings.Contains(out, styleTitle.Render(" VIEWS ")) {
		t.Error("the views heading is not rendered as a heading")
	}
	if !strings.Contains(plain, "─") {
		t.Errorf("no section rule:\n%s", plain)
	}
	// Section names, one per group present.
	for _, want := range []string{"ACTIONS ON #9", "VIEWS"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing section %q:\n%s", want, plain)
		}
	}
}

// TestPaletteRunsKeyedEntryThroughItsKey: executing an entry replays its
// direct keypress — one path, so the palette cannot diverge from the
// shortcut it teaches.
func TestPaletteRunsKeyedEntryThroughItsKey(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	m.Update(key(":"))
	if m.palette == nil {
		t.Fatal(": did not open the palette")
	}
	for _, r := range "quit" {
		m.Update(key(string(r)))
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.palette != nil {
		t.Fatal("running an entry did not close the palette")
	}
	if cmd == nil {
		t.Fatal("the quit entry produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("the quit entry produced %T, want tea.Quit — the synthetic key did not route", cmd())
	}
}

// TestPaletteSearchNarrowsAndEscCloses: the palette is a popup — the top of
// the esc stack — and its search owns the keys while open.
func TestPaletteSearchNarrowsAndEscCloses(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	m.Update(key(":"))
	for _, r := range "zzzz-no-such-command" {
		m.Update(key(string(r)))
	}
	if got := m.palette.matches(); len(got) != 0 {
		t.Fatalf("nonsense query matches %d entries, want none", len(got))
	}
	// q must type into the search, not quit.
	m.Update(key("q"))
	if m.palette == nil {
		t.Fatal("q closed the palette instead of typing")
	}
	if !strings.HasSuffix(m.palette.input.Value(), "q") {
		t.Fatalf("q did not type into the search: %q", m.palette.input.Value())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.palette != nil {
		t.Fatal("esc did not close the palette")
	}
	if m.active != viewHome {
		t.Fatalf("closing the palette moved the screen to %v", m.active)
	}
}

// TestHelpRendersFromRegistry: the ? overlay is generated, not hand-written
// — a registry row's label must appear verbatim.
func TestHelpRendersFromRegistry(t *testing.T) {
	got := helpText(ctxTasks)
	for _, want := range []string{
		"jump to the next task needing a human",
		"open the command palette",
		"open the selected task",
		"filter by id",
		"GO TO (FROM THE : PALETTE)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("help overlay missing %q", want)
		}
	}
}

// TestHelpIsContextual is a T3.8 finding: the sheet answers "what can I do
// here", so it carries the focused surface's keys — not all eight
// surfaces' sections at once.
func TestHelpIsContextual(t *testing.T) {
	tasks := helpText(ctxTasks)
	if !strings.Contains(tasks, "filter by id") {
		t.Error("the task table's help lacks its own filter key")
	}
	for _, elsewhere := range []string{"register a repository", "re-read the registry", "re-probe the adapters"} {
		if strings.Contains(tasks, elsewhere) {
			t.Errorf("the task table's help carries another surface's key: %q", elsewhere)
		}
	}

	projects := helpText(ctxProjects)
	if !strings.Contains(projects, "register a repository") {
		t.Error("the projects help lacks its own add key")
	}
	for _, elsewhere := range []string{"filter by id", "follow the live output"} {
		if strings.Contains(projects, elsewhere) {
			t.Errorf("the projects help carries a panel key: %q", elsewhere)
		}
	}
	// Task actions belong to the panels, where a task is selected.
	if strings.Contains(projects, "approve the gate") {
		t.Error("the projects help offers task actions")
	}
	if !strings.Contains(helpText(ctxTasks), "approve the gate") {
		t.Error("the task table's help omits the task actions")
	}
	// The globals are everywhere, because they work everywhere.
	for _, ctx := range []bindingContext{ctxTasks, ctxProjects, ctxDaemon, ctxNewTask} {
		if !strings.Contains(helpText(ctx), "quit the TUI") {
			t.Errorf("%s help omits the global keys", ctx)
		}
	}
}
