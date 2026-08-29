package tui

import (
	"context"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/tui/workflowgraph"
)

// The graph is a sub-layer of the workflows takeover rather than a routed
// screen of its own (task 017 decision 13): `g` opens it over the list, the
// list's own `enter` expansion is untouched, and Escape closes one layer at a
// time — graph, then list, then home.
//
// It is a layer and not a replacement because the expansion carries findings,
// platform notes and §8.6 resolution the graph does not show, and because
// `e` and `R` act on the entry either way.

// workflowDefinitionMsg carries GET /v1/workflows/definition for one entry.
type workflowDefinitionMsg struct {
	key wfResolveKey
	def apiclient.WorkflowDefinition
	err error
}

// graphLayer is the open graph: which entry it belongs to, what the daemon
// said, and the component itself.
type graphLayer struct {
	key   wfResolveKey
	scope string
	file  string
	graph workflowgraph.Model
	// body is the definition as fetched, kept for the modal's workflow-level
	// header — the description, the declared fields and the platforms, which
	// are facts about the file the node sits in rather than about the node.
	body *apiclient.WorkflowBody
	// modal is the open step detail, nil while the graph has the keyboard.
	// It is a popup over the picture rather than a third in-place layer, so
	// "Escape closes one layer at a time" stays literally true: modal, then
	// graph, then the takeover (task 053).
	modal *stepModal
	// loading is the first fetch and every refetch; the layer keeps showing
	// the last good graph behind it rather than blanking.
	loading bool
	loaded  bool
	err     string
	// findings are a definition that came back null — the file broke between
	// the list load and this fetch, which is the race decision 11's envelope
	// exists to describe.
	findings []apiclient.WorkflowFinding
}

// stepModal is the open detail of one node. It keeps the node id rather than
// a copy of its detail: a refetch rebuilds the diagram, and the modal has to
// re-render from the new definition or close if the node went away — the same
// rule the selection follows (decision 19).
type stepModal struct {
	node  string
	title string
	vp    viewport.Model
}

func newGraphLayer(key wfResolveKey, entry *apiclient.WorkflowEntry) *graphLayer {
	g := workflowgraph.New()
	g.SetTheme(graphTheme())
	return &graphLayer{
		key:     key,
		scope:   entry.Scope,
		file:    entry.File,
		graph:   g,
		loading: true,
	}
}

// graphTheme dresses the picture. Every one of these is decoration: the
// topology reads with all of them stripped (decision 6), which
// TestStylingDoesNotChangeThePicture holds the renderer to.
func graphTheme() workflowgraph.Theme {
	return workflowgraph.Theme{
		Node:      lipgloss.NewStyle(),
		Selected:  styleFocus,
		Frame:     styleDim,
		Edge:      styleDim,
		EdgeLabel: styleWarn,
	}
}

// openGraph opens the layer on the entry under the cursor. A workflow already
// known not to parse has no graph to draw and its findings are already on
// screen in the expansion, so `g` says so instead of opening a layer that
// would repeat them (decision 5, round 4).
func (w *workflowsView) openGraph() tea.Cmd {
	line, ok := w.currentLine()
	if !ok {
		return nil
	}
	if !line.entry.Valid() {
		w.err = line.entry.Name + " does not parse — there is no graph to draw; see the errors above"
		return nil
	}
	key := wfResolveKey{name: line.entry.Name}
	if line.block != nil {
		key.projectID = line.block.projectID
	}
	w.err = ""
	w.graph = newGraphLayer(key, line.entry)
	return w.definitionCmd(key)
}

// definitionCmd fetches one workflow's definition. Nothing is cached: the
// rule a cache would need is "drop it whenever the registry changes", and the
// registry changing is exactly when someone is sitting in this view editing
// files, so it would be cold whenever it mattered (decision 19).
func (w *workflowsView) definitionCmd(key wfResolveKey) tea.Cmd {
	client := w.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		def, err := client.GetWorkflowDefinition(ctx, key.projectID, key.name)
		return workflowDefinitionMsg{key: key, def: def, err: err}
	}
}

// applyDefinition lands a fetch. A response for an entry the layer has since
// moved off is dropped rather than drawn.
func (w *workflowsView) applyDefinition(msg workflowDefinitionMsg) {
	g := w.graph
	if g == nil || g.key != msg.key {
		return
	}
	g.loading = false
	if msg.err != nil {
		g.err = errString(msg.err)
		return
	}
	g.err = ""
	g.scope, g.file = msg.def.Scope, msg.def.File
	g.findings = msg.def.Errors
	if msg.def.Definition == nil {
		return
	}
	g.findings = nil
	g.loaded = true
	g.body = msg.def.Definition
	// Selection is kept by node id across the reload, which is what node
	// identity being semantic rather than positional buys (decision 19).
	g.graph.SetDefinition(msg.def.Definition)
	// An open modal is re-rendered from the definition that just landed, and
	// closed back to the graph when the node it was reading no longer exists.
	// Select ignores an id the new diagram does not have, which is what makes
	// "did it survive?" one comparison.
	if g.modal != nil {
		g.graph.Select(g.modal.node)
		if g.graph.Selected() != g.modal.node {
			g.modal = nil
		}
	}
	w.sizeGraph()
}

// sizeGraph gives the component the room the layer's chrome leaves it.
func (w *workflowsView) sizeGraph() {
	if w.graph == nil {
		return
	}
	width, height := w.width, w.height
	if guidedTakeover(width, height) {
		_, width = guidedPaneWidths(width)
		width -= 2  // focused pane border
		height -= 2 // focused pane border
	}
	w.graph.graph.SetSize(max(width, 1), max(height-graphChromeRows, 1))
	w.syncModal()
}

// graphChromeRows is the header line plus the inspector strip and its rule.
// The strip is a fixed height so the viewport's arithmetic does not change
// when a node with more detail is selected (decision 7, round 2).
const graphChromeRows = 5

// updateGraphKey is the layer's keyboard. `e` and `R` carry in from the list:
// `e` opens the graphed workflow's own file, which with the live reload makes
// edit-save-watch the loop the feature is for, and `R` is the layer's only
// recovery from a failed fetch (decision 13, round 6).
func (w *workflowsView) updateGraphKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	g := w.graph
	switch msg.String() {
	case "esc":
		switch {
		case g.modal != nil:
			// The modal is the innermost layer: it closes first, and the node
			// it was opened on stays selected.
			g.modal = nil
		case g.err != "":
			g.err = ""
		default:
			w.graph = nil
		}
		return w, nil
	case "e":
		return w, w.editCmd()
	case "R":
		g.err = ""
		g.loading = true
		return w, w.definitionCmd(g.key)
	case "enter":
		if g.modal == nil {
			w.openStepModal()
			return w, nil
		}
	}
	// While the modal is open it owns the keyboard: scroll and pager keys move
	// it, and nothing reaches the selection underneath — a graph that moved
	// its cursor behind an open modal would close onto a different node.
	if g.modal != nil {
		var cmd tea.Cmd
		g.modal.vp, cmd = g.modal.vp.Update(msg)
		return w, cmd
	}
	updated, cmd := g.graph.Update(msg)
	g.graph = updated
	return w, cmd
}

// openStepModal opens the selected node in full. Every node opens something —
// `enter` is never inert — but there is nothing to open before the definition
// lands, and nothing selected in a terminal too narrow to draw a node, where
// the layer shows its hint instead (decision 8).
func (w *workflowsView) openStepModal() {
	g := w.graph
	if g == nil || !g.loaded || g.graph.TooNarrow() {
		return
	}
	node, ok := g.graph.SelectedNode()
	if !ok || len(node.Full) == 0 {
		return
	}
	// A fresh modal each time, so reopening starts at the top: the offset a
	// previous reading ended on is not where the next one begins (decision 19).
	vp := viewport.New()
	vp.SoftWrap = false
	g.modal = &stepModal{node: node.ID, title: node.Label, vp: vp}
	w.syncModal()
}

// bindingContext names the layer that has the keyboard, so the footer and the
// ? overlay describe the keys that actually work.
func (w *workflowsView) bindingContext() bindingContext {
	switch {
	case w.graph != nil && w.graph.modal != nil:
		return ctxWorkflowStep
	case w.graph != nil:
		return ctxWorkflowGraph
	default:
		return ctxWorkflows
	}
}

// The graph pane's origin inside a takeover. The root has already taken the
// header line off a mouse event's Y; the takeover's frame costs one more row
// and one column, and the layer's own header line one more row.
const (
	graphPaneX = 1
	graphPaneY = 2
)

// updateGraphMouse is click-to-select and the wheel — the two the viewport
// and the hit test already support. Drag-to-pan is a stateful gesture that
// belongs to an editor (decision 14).
func (w *workflowsView) updateGraphMouse(msg tea.Msg) {
	if w.graph == nil || !w.graph.loaded {
		return
	}
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return
		}
		x, y := w.graphPaneOrigin()
		if msg.X < x || msg.Y < y {
			return
		}
		w.graph.graph.ClickAt(msg.X-x, msg.Y-y)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelDown:
			w.graph.graph.Scroll(1)
		case tea.MouseWheelUp:
			w.graph.graph.Scroll(-1)
		}
	}
}

func (w *workflowsView) graphPaneOrigin() (x, y int) {
	if !guidedTakeover(w.width, w.height) {
		return graphPaneX, graphPaneY
	}
	railWidth, _ := guidedPaneWidths(w.width)
	return graphPaneX + railWidth + 1, graphPaneY + 1
}
