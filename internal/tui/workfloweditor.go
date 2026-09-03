package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The structured workflow editor (§15 view 5, task 065). It is a sub-layer of
// the workflows takeover, the shape the graph already uses: a nullable
// sub-model that takes the keys and renders in place.
//
// It composes no YAML. Every committed row becomes one edit operation on the
// wire, and the daemon writes the file — which is what preserves the comments
// an author put there (decisions 1 and 2). It draws no field of its own
// invention either: the rows come from GET /v1/workflows/schema, so a field
// the daemon would refuse is one the form never offers (decision 3).
//
// `e` is untouched and still means $EDITOR, in this view and in the six other
// contexts bindings.go gives it (decision 6). The editor takes `i`, create
// takes `a` — the projects view's "add" — and fork takes `f`.

// Editor messages.
type (
	// wfEditorLoadedMsg carries the schema and the definition the form is
	// built from. Both are needed before a single row can be drawn.
	wfEditorLoadedMsg struct {
		key    wfResolveKey
		schema apiclient.WorkflowSchema
		def    apiclient.WorkflowDefinition
		err    error
	}
	// wfEditorSavedMsg is one PATCH landing. A 409 arrives here too, as err:
	// the file moved underneath and the pending edit is still on screen.
	wfEditorSavedMsg struct {
		result apiclient.WorkflowWriteResult
		err    error
	}
	// wfCreatedMsg is a create or a fork landing.
	wfCreatedMsg struct {
		result    apiclient.WorkflowWriteResult
		projectID int64
		err       error
	}
)

// wfEditRow is one editable line of the form: a schema field bound to a path
// in the document, plus the value the file currently holds.
type wfEditRow struct {
	field apiclient.WorkflowSchemaField
	// path is the dotted path with list indices an op addresses — the same
	// string the daemon resolves. Empty on a row that only descends.
	path  string
	value string
	// descend is the sub-list this row opens: a step body, a lane list, a
	// merge block. Empty on a leaf row.
	descend string
	// label overrides the field name, which is how a step row reads as
	// "plan (agent)" rather than as "steps[0]".
	label string
	// list is the dotted path of the sequence this row is an item of —
	// "steps", "steps[3].steps", "steps[7].lanes", "fields" — and index
	// its position in it. Both are zero on a row that is a field rather
	// than a list item. They are what the insert/remove/move keys address.
	list  string
	index int
}

// wfEditorOverlay is a value editor that takes the keyboard from the row
// list: the multi-line pane a `prompt:` needs, a picker for a closed set.
// Nothing implements it on this branch and e.overlay is always nil here —
// the seam is deliberate, so the write half of issue #320 can add the pane
// and its keys without touching the read path this file owns.
type wfEditorOverlay interface {
	// Update takes a key the layer did not claim and returns the overlay to keep.
	Update(msg tea.KeyPressMsg) (wfEditorOverlay, tea.Cmd)
	// View draws the overlay in the space it was given.
	View(width, height int) string
	// FullPane reports whether View replaces the whole form body rather than
	// drawing in the value column of the row being edited.
	FullPane() bool
	// Value is the text a commit would send.
	Value() string
	// Dirty is false while the value is still the one the overlay opened on.
	Dirty() bool
}

// wfEditorLayer is the open editor.
type wfEditorLayer struct {
	key    wfResolveKey
	scope  string
	file   string
	schema apiclient.WorkflowSchema
	def    *apiclient.WorkflowBody
	// version is the token the next PATCH carries; a save replaces it with
	// the one the daemon hands back (decision 4).
	version string
	// path is the breadcrumb into the document: "" at the top level,
	// "steps[1]" inside a step, "steps[1].steps[0]" inside its sub-step.
	path   string
	rows   []wfEditRow
	cursor int

	// input is the focused text row, nil when the list has the keyboard.
	input *textField
	// overlay is the value editor that has the keyboard instead, nil when
	// the row list does. The row it belongs to is editing, the same as
	// input's.
	overlay wfEditorOverlay
	editing int

	loading bool
	saving  bool
	err     string
	// stale is a 409: the file changed on disk. The pending edit stays on
	// screen and `R` re-reads it, which is the offer the brief asks for.
	stale bool
}

// openEditor opens the structured editor on the entry under the cursor.
func (w *workflowsView) openEditor() tea.Cmd {
	line, ok := w.currentLine()
	if !ok {
		return nil
	}
	if line.entry.File == "" {
		w.err = line.entry.Name + " is built in — fork it with f to edit a copy"
		return nil
	}
	key := wfResolveKey{name: line.entry.Name}
	if line.block != nil {
		key.projectID = line.block.projectID
	}
	w.editor = &wfEditorLayer{
		key:     key,
		scope:   line.entry.Scope,
		file:    line.entry.File,
		version: line.entry.Version,
		loading: true,
	}
	return w.editorLoadCmd(key)
}

func (w *workflowsView) editorLoadCmd(key wfResolveKey) tea.Cmd {
	client := w.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		schema, err := client.WorkflowSchema(ctx)
		if err != nil {
			return wfEditorLoadedMsg{key: key, err: err}
		}
		def, err := client.GetWorkflowDefinition(ctx, key.projectID, key.name)
		return wfEditorLoadedMsg{key: key, schema: schema, def: def, err: err}
	}
}

// rebuild recomputes the visible rows for the current breadcrumb. The
// breadcrumb decides which form is drawn, so every block the resolver can
// reach is a block the editor can render: steps and their nested bodies,
// a fan-out's lanes and its merge, the declared fields, and defaults.
func (e *wfEditorLayer) rebuild() {
	e.rows = nil
	if e.def == nil {
		return
	}
	node, ok := e.resolve(e.path)
	if !ok {
		// The file moved underneath: an index that resolved when the row was
		// drawn no longer does.
		e.err = "the block at " + e.path + " is no longer there"
		return
	}
	switch node.kind {
	case wfNodeRoot:
		e.buildTopLevel()
	case wfNodeStep:
		e.buildStep(e.path, node.step)
	case wfNodeSteps:
		e.buildSteps(e.path, node.steps)
	case wfNodeLanes:
		e.buildLanes(e.path, node.lanes)
	case wfNodeLane:
		e.buildLane(e.path, node.lane)
	case wfNodeMerge:
		e.buildMerge(e.path, node.merge)
	case wfNodeFields:
		e.buildFields()
	case wfNodeField:
		e.buildField(e.path, node.field)
	case wfNodeDefaults:
		e.buildDefaults()
	case wfNodeContainer:
		e.buildContainer()
	}
	if e.cursor >= len(e.rows) {
		e.cursor = max(0, len(e.rows)-1)
	}
}

// capturing reports whether the layer has the keyboard for a value: a focused
// one-line field, or an overlay. The view above asks this rather than the
// fields themselves, so a new overlay does not have to be wired into the
// global key path a second time.
func (e *wfEditorLayer) capturing() bool { return e.input != nil || e.overlay != nil }
