package workflowgraph

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

func newModel(t *testing.T, wf *apiclient.WorkflowBody, w, h int) Model {
	t.Helper()
	m := New()
	m.SetDefinition(wf)
	m.SetSize(w, h)
	return m
}

// keyPress builds the message the root would deliver for a named key, so the
// tests press what a human presses rather than what the struct happens to
// hold.
func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "shift+down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	case "shift+right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
	}
	return tea.KeyPressMsg{Code: rune(name[0]), Text: name}
}

func send(m Model, name string) Model {
	m, _ = m.Update(keyPress(name))
	return m
}

func TestModelSelectsTheEntryNode(t *testing.T) {
	m := newModel(t, fixtureSequential(), 80, 24)
	if got := m.Selected(); got != "plan" {
		t.Errorf("selection = %q, want the entry node", got)
	}
	if m.Mode() != ModeView {
		t.Error("017 ships read-only; the mode is not ModeView")
	}
}

func TestModelEmptyDefinition(t *testing.T) {
	m := New()
	m.SetDefinition(nil)
	m.SetSize(80, 24)
	if !m.Empty() || m.Selected() != "" {
		t.Errorf("empty graph has selection %q", m.Selected())
	}
	// It must still render rather than panic: a workflow that did not parse
	// reaches the layer as an empty body.
	_ = m.View()
	if _, ok := m.SelectedNode(); ok {
		t.Error("SelectedNode reported a node in an empty graph")
	}
}

// Down follows the flow and up comes back, which is what makes a sequence
// navigable without thinking about coordinates.
func TestModelVerticalNavigation(t *testing.T) {
	m := newModel(t, fixtureSequential(), 80, 24)
	for _, want := range []string{"build", "ship", EndNodeID} {
		m = send(m, "down")
		if got := m.Selected(); got != want {
			t.Fatalf("down selected %q, want %q", got, want)
		}
	}
	m = send(m, "down")
	if got := m.Selected(); got != EndNodeID {
		t.Errorf("down past the end moved to %q, want to stay", got)
	}
	for _, want := range []string{"ship", "build", "plan"} {
		m = send(m, "up")
		if got := m.Selected(); got != want {
			t.Fatalf("up selected %q, want %q", got, want)
		}
	}
}

// A right move from one parallel member reaches the member beside it, which
// source-index navigation would get wrong the moment a group is involved.
func TestModelHorizontalNavigationAcrossAGroup(t *testing.T) {
	m := newModel(t, fixtureParallel(), 120, 40)
	m.Select("lint")
	for _, want := range []string{"unit", "e2e"} {
		m = send(m, "right")
		if got := m.Selected(); got != want {
			t.Fatalf("right selected %q, want %q", got, want)
		}
	}
	m = send(m, "right")
	if got := m.Selected(); got != "e2e" {
		t.Errorf("right past the last member moved to %q, want to stay", got)
	}
	m = send(m, "left")
	if got := m.Selected(); got != "unit" {
		t.Errorf("left selected %q, want unit", got)
	}
}

// Tab is the deterministic fallback for nodes geometry makes awkward to
// reach, and it wraps.
func TestModelTabWalksSourceOrder(t *testing.T) {
	m := newModel(t, fixtureSequential(), 80, 24)
	var seen []string
	for range len(m.diagram.Nodes) {
		m = send(m, "tab")
		seen = append(seen, m.Selected())
	}
	want := []string{"build", "ship", EndNodeID, "plan"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("tab order = %v, want %v", seen, want)
	}
	m = send(m, "shift+tab")
	if got := m.Selected(); got != EndNodeID {
		t.Errorf("shift+tab selected %q, want to walk backwards", got)
	}
}

// Reloading keeps the cursor where it was, which is the whole point of node
// ids not being coordinates (decision 19).
func TestModelSelectionSurvivesAReload(t *testing.T) {
	m := newModel(t, fixtureSequential(), 80, 24)
	m.Select("ship")

	// The same workflow with an extra step in front: everything moves, and
	// the selection does not.
	grown := fixtureSequential()
	grown.Steps = append([]apiclient.WorkflowStepDef{step("prep", "command")}, grown.Steps...)
	m.SetDefinition(grown)
	if got := m.Selected(); got != "ship" {
		t.Errorf("selection = %q, want it to survive the reload", got)
	}

	// A workflow that no longer has the node falls back to the entry.
	m.SetDefinition(fixtureCondition())
	if got := m.Selected(); got != "plan" {
		t.Errorf("selection = %q, want the entry node after the node vanished", got)
	}
}

// Arrows move the selection; the viewport follows rather than being driven
// directly, so a node below the fold scrolls itself into view.
func TestModelViewportFollowsTheSelection(t *testing.T) {
	m := newModel(t, fixtureNested(), 60, 8)
	if off := m.vp.YOffset(); off != 0 {
		t.Fatalf("fresh viewport is scrolled to %d", off)
	}
	for range 6 {
		m = send(m, "down")
	}
	if m.vp.YOffset() == 0 {
		t.Error("the viewport did not follow the selection down the canvas")
	}
	sel, ok := m.scene.Node(m.Selected())
	if !ok {
		t.Fatal("selection is not placed")
	}
	if sel.Y < m.vp.YOffset() || sel.Y >= m.vp.YOffset()+m.vp.Height() {
		t.Errorf("selected node at y=%d is outside the viewport [%d,%d)",
			sel.Y, m.vp.YOffset(), m.vp.YOffset()+m.vp.Height())
	}
}

// Panning is explicit and separate: the viewport's own arrow bindings are
// cleared, so an arrow can never scroll the canvas out from under the cursor.
func TestModelShiftArrowsPanWithoutMovingTheSelection(t *testing.T) {
	m := newModel(t, fixtureFanOut(), 30, 10)
	before := m.Selected()
	m = send(m, "shift+down")
	if m.Selected() != before {
		t.Errorf("shift+down moved the selection to %q", m.Selected())
	}
	if m.vp.YOffset() == 0 {
		t.Error("shift+down did not pan")
	}
	m = send(m, "shift+right")
	if m.vp.XOffset() == 0 {
		t.Error("shift+right did not pan horizontally")
	}
}

// Both axes scroll: a graph wider than the terminal is panned, never
// reflowed (decision 8).
func TestModelPansHorizontally(t *testing.T) {
	m := newModel(t, fixtureFanOut(), 30, 40)
	if m.scene.Width <= 30 {
		t.Fatalf("fixture is only %d columns; it cannot exercise panning", m.scene.Width)
	}
	m.Select(refNodeID("spread", "web"))
	if m.vp.XOffset() == 0 {
		t.Error("selecting a node off the right edge did not pan to it")
	}
}

func TestModelClickSelects(t *testing.T) {
	m := newModel(t, fixtureSequential(), 80, 40)
	target, ok := m.scene.Node("ship")
	if !ok {
		t.Fatal("ship was not placed")
	}
	m.ClickAt(target.X-m.vp.XOffset()+1, target.Y-m.vp.YOffset()+1)
	if got := m.Selected(); got != "ship" {
		t.Errorf("click selected %q, want ship", got)
	}
	// Empty canvas is not a deselect: there is nothing to deselect to. The
	// gap row below a node, at the far left of the column, is canvas.
	gap := Point{X: 0, Y: target.Y + target.H}
	if _, hit := m.scene.NodeAt(gap.X, gap.Y); hit {
		t.Fatalf("the test's empty cell %+v is inside a node", gap)
	}
	m.ClickAt(gap.X-m.vp.XOffset(), gap.Y-m.vp.YOffset())
	if got := m.Selected(); got != "ship" {
		t.Errorf("a click on empty canvas changed the selection to %q", got)
	}
}

func TestModelScrollWheel(t *testing.T) {
	m := newModel(t, fixtureNested(), 60, 6)
	m.Scroll(1)
	if m.vp.YOffset() == 0 {
		t.Error("the wheel did not scroll down")
	}
	m.Scroll(-1)
	if m.vp.YOffset() != 0 {
		t.Error("the wheel did not scroll back up")
	}
}

func TestModelTooNarrow(t *testing.T) {
	m := newModel(t, fixtureSequential(), DefaultOptions().MinWidth()-1, 24)
	if !m.TooNarrow() {
		t.Error("a terminal below the threshold did not report too narrow")
	}
	m.SetSize(DefaultOptions().MinWidth(), 24)
	if m.TooNarrow() {
		t.Error("a terminal at the threshold reported too narrow")
	}
	// A graph much wider than the terminal is still not "too narrow": it
	// pans.
	m = newModel(t, fixtureFanOut(), 40, 24)
	if m.TooNarrow() {
		t.Error("a wide graph in a usable terminal reported too narrow")
	}
}

// The inspector carries what the box had to reduce to a badge, and never the
// prompt or command body (decision 15).
func TestModelDetail(t *testing.T) {
	m := newModel(t, fixtureGuarded(), 80, 24)
	m.Select("maybe")
	var labels []string
	for _, f := range m.Detail() {
		labels = append(labels, f.Label)
		if f.Label == "prompt" || f.Label == "run" {
			t.Errorf("the inspector carries %q; `e` is the path to a body", f.Label)
		}
	}
	joined := strings.Join(labels, ",")
	for _, want := range []string{"name", "id", "type", "if"} {
		if !strings.Contains(joined, want) {
			t.Errorf("inspector rows = %v, want one labelled %q", labels, want)
		}
	}
}

// The rendered view is cropped to the viewport, never wrapped: a wrapped
// graph is a different picture.
func TestModelViewIsCroppedNotWrapped(t *testing.T) {
	m := newModel(t, fixtureFanOut(), 30, 10)
	rows := strings.Split(m.View(), "\n")
	if len(rows) > 10 {
		t.Errorf("view is %d rows, want at most the viewport's 10", len(rows))
	}
}
