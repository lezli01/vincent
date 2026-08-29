package workflowgraph

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Mode is the interaction the component is in. 017 ships ModeView alone; the
// enum exists because a builder will need the seam and adding one later would
// mean threading a mode through every handler that assumed there was none
// (task 017 decision 7).
type Mode int

const (
	// ModeView is read-only: focus, selection and panning, no mutation.
	ModeView Mode = iota
)

// Model is the reusable workflow-graph Bubble. It owns focus, selection and
// the viewport; it does not own the workflow, which arrives from the daemon
// and is replaced wholesale on a reload.
//
// Selection is keyed by node id and never by position, so a re-layout after a
// live registry reload keeps the cursor where it was as long as the node
// still exists (decision 19).
type Model struct {
	diagram Diagram
	scene   Scene
	opts    Options
	theme   Theme
	mode    Mode

	vp       viewport.Model
	selected string
	width    int
	height   int

	// body is the definition the diagram was built from, kept so an overlay
	// that discovers an off-snapshot attempt can rebuild without the host
	// having to refetch (task 051 decision 3).
	body *apiclient.WorkflowBody
	// run is the runtime overlay, empty for the definition viewer.
	run Overlay
	// off names the off-snapshot runs currently attached, so an overlay that
	// discovers none new is applied without touching the layout at all.
	off []OffGraphRun
	// sourceWalk is whether tab/shift+tab walk the nodes in source order.
	// Inside the task workspace they do not: `tab` is the workspace's tab
	// cycle there, and shadowing it would break the muscle memory task 049
	// built (task 051 decision 5).
	sourceWalk bool
}

// New returns an empty graph sized to nothing. The viewport's own arrow
// bindings are cleared: arrows move the *selection* here and the viewport
// follows it, so leaving them bound would scroll the canvas out from under
// the cursor (decision 14).
func New() Model {
	vp := viewport.New()
	vp.SoftWrap = false
	km := viewport.DefaultKeyMap()
	km.Down = key.NewBinding()
	km.Up = key.NewBinding()
	km.Left = key.NewBinding()
	km.Right = key.NewBinding()
	vp.KeyMap = km
	return Model{opts: DefaultOptions(), vp: vp, mode: ModeView, sourceWalk: true}
}

// SetSourceWalk decides whether tab/shift+tab walk the nodes in source order.
// A host that owns `tab` for something else turns it off rather than having
// the component shadow a key the surrounding screen already binds (task 051
// decision 5).
func (m *Model) SetSourceWalk(on bool) { m.sourceWalk = on }

// SetTheme sets the styles. Colour is decoration: the picture reads without
// it (decision 6).
func (m *Model) SetTheme(th Theme) {
	m.theme = th
	m.sync()
}

// Mode reports the interaction mode. Read-only in 017.
func (m *Model) Mode() Mode { return m.mode }

// SetDefinition points the graph at a workflow, keeping the selected node if
// that node still exists and falling back to the entry otherwise.
func (m *Model) SetDefinition(wf *apiclient.WorkflowBody) {
	m.body = wf
	m.rebuild()
}

// SetOverlay lands a task's run state on the graph it already has.
//
// It does not re-lay-out. Coordinates and selection survive, which is the
// whole point on a surface that is refreshed live: a reader watching a
// running task must not have the picture move under them (task 017 decisions
// 3 and 5). The one exception is an attempt no node answers for — a
// follow-up round, a repair rewrite — which is a *new node*, so the diagram
// is rebuilt exactly when the set of them changes and never otherwise.
func (m *Model) SetOverlay(o Overlay) {
	m.run = o
	if sameOffGraph(m.off, o.Off) {
		m.sync()
		return
	}
	m.rebuild()
}

// Overlay reports the applied run state, for a host rendering an inspector.
func (m *Model) Overlay() Overlay { return m.run }

func (m *Model) rebuild() {
	m.off = append([]OffGraphRun{}, m.run.Off...)
	m.diagram = AttachOffGraph(Build(m.body), m.off)
	m.scene = Layout(m.diagram, m.opts)
	if _, ok := m.scene.Node(m.selected); !ok {
		m.selected = m.firstNode()
	}
	m.sync()
	m.reveal()
}

func sameOffGraph(a, b []OffGraphRun) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sync repaints the canvas into the viewport. It runs eagerly rather than at
// the next View because the viewport cannot scroll content it does not have:
// every offset it computes clamps against the content's size, so a reveal or
// a wheel event arriving before the first paint would silently do nothing.
func (m *Model) sync() {
	lines := Render(m.diagram, m.scene, ViewState{Selected: m.selected, Run: m.run}, m.theme)
	m.vp.SetContent(strings.Join(lines, "\n"))
}

// Empty reports whether anything has been loaded.
func (m *Model) Empty() bool { return len(m.diagram.Nodes) == 0 }

func (m *Model) firstNode() string {
	if len(m.diagram.Root) > 0 {
		return m.diagram.Root[0]
	}
	if len(m.diagram.Nodes) > 0 {
		return m.diagram.Nodes[0].ID
	}
	return ""
}

// SetSize sizes the viewport. The scene is not re-laid-out: a narrow terminal
// crops and pans a graph rather than reflowing it into a different shape, so
// a resize cannot move a node out from under the selection (decision 8).
func (m *Model) SetSize(width, height int) {
	m.width, m.height = width, height
	m.vp.SetWidth(max(width, 1))
	m.vp.SetHeight(max(height, 1))
	m.sync()
	m.reveal()
}

// TooNarrow reports a terminal too small to draw a node readably. The screen
// says so rather than flattening the graph into something untrue
// (decision 8); the threshold comes from the node width (decision 17).
func (m *Model) TooNarrow() bool { return m.width > 0 && m.width < m.opts.MinWidth() }

// MinWidth is the width TooNarrow compares against, for a host that wants to
// say how much more room it needs.
func (m *Model) MinWidth() int { return m.opts.MinWidth() }

// Selected is the selected node's id, empty when nothing is loaded.
func (m *Model) Selected() string { return m.selected }

// SelectedNode returns the selected node, for the inspector.
func (m *Model) SelectedNode() (Node, bool) {
	for _, n := range m.diagram.Nodes {
		if n.ID == m.selected {
			return n, true
		}
	}
	return Node{}, false
}

// Select moves the selection to a node id, ignoring one that does not exist.
func (m *Model) Select(id string) {
	if _, ok := m.scene.Node(id); !ok {
		return
	}
	m.selected = id
	m.sync()
	m.reveal()
}

// Update handles keys. Mouse events arrive through ClickAt and Scroll
// instead: only the host knows where the pane sits on screen, and a component
// that guessed at that offset would select the wrong node whenever the
// surrounding chrome changed height.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch keyMsg.String() {
	case "up", "k":
		m.move(0, -1)
	case "down", "j":
		m.move(0, 1)
	case "left", "h":
		m.move(-1, 0)
	case "right", "l":
		m.move(1, 0)
	case "tab":
		if !m.sourceWalk {
			return m, nil
		}
		m.step(1)
	case "shift+tab":
		if !m.sourceWalk {
			return m, nil
		}
		m.step(-1)
	case "shift+up":
		m.vp.ScrollUp(1)
	case "shift+down":
		m.vp.ScrollDown(1)
	case "shift+left":
		m.vp.ScrollLeft(m.opts.NodeWidth / 2)
	case "shift+right":
		m.vp.ScrollRight(m.opts.NodeWidth / 2)
	default:
		// Everything else — the pager keys — is the viewport's.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

// ClickAt selects the node under a pane-relative cell, if any. A click on
// empty canvas changes nothing: there is no "deselect" in a viewer, and
// clearing the selection would empty the inspector for no reason the reader
// asked for.
func (m *Model) ClickAt(x, y int) {
	node, ok := m.scene.NodeAt(x+m.vp.XOffset(), y+m.vp.YOffset())
	if !ok {
		return
	}
	m.selected = node.ID
	m.sync()
}

// Scroll moves the canvas by lines — the mouse wheel's path.
func (m *Model) Scroll(delta int) {
	if delta > 0 {
		m.vp.ScrollDown(1)
		return
	}
	m.vp.ScrollUp(1)
}

// move walks the selection geometrically rather than by source index: a right
// move from one fan-out lane should reach the lane beside it, and a down move
// should follow the flow (decision 7).
func (m *Model) move(dx, dy int) {
	from, ok := m.scene.Node(m.selected)
	if !ok {
		m.selected = m.firstNode()
		m.sync()
		return
	}
	best, found := "", false
	var bestScore int
	for _, n := range m.scene.Nodes {
		if n.ID == from.ID {
			continue
		}
		primary, perp, ahead := offsets(from, n, dx, dy)
		if !ahead {
			continue
		}
		// Distance along the direction of travel dominates; the offset
		// across it only breaks ties, so a down move prefers the next rank
		// over a node far below in a neighbouring column.
		score := primary*1000 + perp
		if !found || score < bestScore {
			best, bestScore, found = n.ID, score, true
		}
	}
	if !found {
		return
	}
	m.selected = best
	m.sync()
	m.reveal()
}

// offsets measures one candidate against the direction of travel.
func offsets(from, to PlacedNode, dx, dy int) (primary, perp int, ahead bool) {
	fx, fy := from.X+from.W/2, from.Y+from.H/2
	tx, ty := to.X+to.W/2, to.Y+to.H/2
	switch {
	case dy > 0:
		return ty - fy, abs(tx - fx), to.Y >= from.Y+from.H
	case dy < 0:
		return fy - ty, abs(tx - fx), to.Y+to.H <= from.Y
	case dx > 0:
		return tx - fx, abs(ty - fy), to.X >= from.X+from.W
	default:
		return fx - tx, abs(ty - fy), to.X+to.W <= from.X
	}
}

// step is the deterministic fallback: source order, which is the order the
// diagram builder emitted its nodes in. It reaches nodes that geometry makes
// awkward — a lone lane, a node the flow only enters from the side.
func (m *Model) step(delta int) {
	if len(m.diagram.Nodes) == 0 {
		return
	}
	at := 0
	for i, n := range m.diagram.Nodes {
		if n.ID == m.selected {
			at = i
			break
		}
	}
	next := (at + delta + len(m.diagram.Nodes)) % len(m.diagram.Nodes)
	m.selected = m.diagram.Nodes[next].ID
	m.sync()
	m.reveal()
}

// reveal scrolls the selection into view on both axes.
func (m *Model) reveal() {
	n, ok := m.scene.Node(m.selected)
	if !ok {
		return
	}
	m.vp.EnsureVisible(n.Y, n.X, n.X+n.W)
}

// View renders the graph, cropped to the viewport. The canvas is already
// painted; this only crops it.
func (m *Model) View() string { return m.vp.View() }

// Detail is the selected node's inspector rows: the fields the box had to
// truncate or reduce to a badge. Prompts and command bodies are deliberately
// not among them (decision 15) — `e` opens the file.
func (m *Model) Detail() []DetailField {
	n, ok := m.SelectedNode()
	if !ok {
		return nil
	}
	return append([]DetailField{{Label: "name", Value: n.Label}}, n.Detail...)
}

// ScrollPercent is how far down the canvas the viewport sits, for a host that
// wants to show it.
func (m *Model) ScrollPercent() float64 { return m.vp.ScrollPercent() }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Nodes is the diagram's nodes in source order, for a host that has to join
// runtime rows onto them. It returns the slice the model holds: callers read
// it, and the package's own pipeline never mutates a node in place.
func (m *Model) Nodes() []Node { return m.diagram.Nodes }

// Lanes is every fan_out lane in the diagram, in source order. A lane is what
// a runtime overlay hangs a child task off, and its Key is the name to use —
// a lane id alone is unique only inside its own fan_out.
func (m *Model) Lanes() []Column {
	var out []Column
	for _, g := range m.diagram.Groups {
		if g.Kind != GroupFanOut {
			continue
		}
		out = append(out, g.Columns...)
	}
	return out
}
