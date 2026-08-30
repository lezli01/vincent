package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
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
	input   *textinput.Model
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

// rebuild recomputes the visible rows for the current breadcrumb.
func (e *wfEditorLayer) rebuild() {
	e.rows = nil
	if e.def == nil {
		return
	}
	if e.path == "" {
		e.buildTopLevel()
	} else {
		e.buildStep(e.path)
	}
	if e.cursor >= len(e.rows) {
		e.cursor = max(0, len(e.rows)-1)
	}
}

func (e *wfEditorLayer) buildTopLevel() {
	for _, f := range e.schema.TopLevel {
		row := wfEditRow{field: f, path: f.Name}
		switch f.Name {
		case "name":
			row.value = e.def.Name
		case "description":
			row.value = e.def.Description
		case "platforms":
			row.value = strings.Join(e.def.Platforms, ", ")
		case "fields":
			row.value = fmt.Sprintf("%d declared", len(e.def.Fields))
			row.path = ""
		case "defaults":
			row.value = defaultsSummary(e.def.Defaults)
			row.path = ""
		case "steps":
			row.path = ""
			row.value = fmt.Sprintf("%d steps", len(e.def.Steps))
		}
		e.rows = append(e.rows, row)
	}
	for i, st := range e.def.Steps {
		e.rows = append(e.rows, stepRow(fmt.Sprintf("steps[%d]", i), st))
	}
}

// stepRow is the summary line a step gets in a list: its id, its type, and
// the sub-list it descends into.
func stepRow(path string, st apiclient.WorkflowStepDef) wfEditRow {
	label := st.ID
	if label == "" {
		label = "(no id)"
	}
	return wfEditRow{
		field:   apiclient.WorkflowSchemaField{Name: path, Control: apiclient.WorkflowControlSteps},
		label:   label,
		value:   st.Type,
		descend: path,
	}
}

func defaultsSummary(d apiclient.WorkflowDefaults) string {
	var parts []string
	if d.Agent != "" {
		parts = append(parts, "agent "+d.Agent)
	}
	if d.Model != "" {
		parts = append(parts, "model "+d.Model)
	}
	if len(parts) == 0 {
		return "(unset)"
	}
	return strings.Join(parts, " · ")
}

// buildStep draws the form for one step: its type's schema fields plus the
// common fields that type accepts, and a descend row per nested body.
func (e *wfEditorLayer) buildStep(path string) {
	st, ok := e.stepAt(path)
	if !ok {
		e.err = "the step at " + path + " is no longer there"
		return
	}
	typ, known := e.stepType(st.Type)
	if !known {
		e.rows = append(e.rows, wfEditRow{
			field: apiclient.WorkflowSchemaField{Name: "type", Control: apiclient.WorkflowControlString},
			path:  path + ".type", value: st.Type,
		})
		return
	}
	accepts := map[string]bool{}
	for _, name := range typ.Common {
		accepts[name] = true
	}
	// `type` first: it is the discriminator, and changing it changes every
	// row below.
	e.rows = append(e.rows, wfEditRow{
		field: apiclient.WorkflowSchemaField{
			Name: "type", Control: apiclient.WorkflowControlEnum,
			Values: e.typesFor(e.contextOf(path)), Required: true, Help: typ.Help,
		},
		path: path + ".type", value: st.Type,
	})
	for _, f := range e.schema.Common {
		if !accepts[f.Name] {
			continue
		}
		e.rows = append(e.rows, wfEditRow{field: f, path: path + "." + f.Name, value: stepValue(st, f.Name)})
	}
	for _, f := range typ.Fields {
		row := wfEditRow{field: f, path: path + "." + f.Name, value: stepValue(st, f.Name)}
		switch f.Control {
		case apiclient.WorkflowControlSteps:
			row.path, row.descend = "", path+"."+f.Name
			row.value = fmt.Sprintf("%d steps", len(st.Steps))
		case apiclient.WorkflowControlLanes:
			row.path, row.descend = "", path+".lanes"
			row.value = fmt.Sprintf("%d lanes", len(st.Lanes))
		case apiclient.WorkflowControlMerge:
			row.path, row.descend = "", path+".merge"
			row.value = "(unset)"
			if st.Merge != nil {
				row.value = st.Merge.OnConflict
			}
		}
		e.rows = append(e.rows, row)
	}
	// The nested body's own steps, so descending twice is not needed to see
	// what is inside a group.
	if len(st.Steps) > 0 {
		for i, sub := range st.Steps {
			e.rows = append(e.rows, stepRow(fmt.Sprintf("%s.steps[%d]", path, i), sub))
		}
	}
}

// contextOf reports which §8.2 nesting context a path sits in, which is what
// decides the members of its `type` row.
func (e *wfEditorLayer) contextOf(path string) string {
	if i := strings.LastIndex(path, ".steps["); i >= 0 {
		if st, ok := e.stepAt(path[:i]); ok {
			switch st.Type {
			case "parallel":
				return apiclient.WorkflowContextParallel
			case "loop":
				return apiclient.WorkflowContextLoop
			}
		}
	}
	return apiclient.WorkflowContextBody
}

// typesFor is StepTypesFor, read off the served descriptor rather than
// re-derived: a type a context forbids is one the row does not offer.
func (e *wfEditorLayer) typesFor(context string) []string {
	var out []string
	for _, s := range e.schema.Steps {
		for _, c := range s.Contexts {
			if c == context {
				out = append(out, s.Type)
				break
			}
		}
	}
	return out
}

func (e *wfEditorLayer) stepType(typ string) (apiclient.WorkflowSchemaStepType, bool) {
	for _, s := range e.schema.Steps {
		if s.Type == typ {
			return s, true
		}
	}
	return apiclient.WorkflowSchemaStepType{}, false
}

// stepAt resolves a "steps[i].steps[j]" path against the fetched definition.
func (e *wfEditorLayer) stepAt(path string) (apiclient.WorkflowStepDef, bool) {
	if e.def == nil {
		return apiclient.WorkflowStepDef{}, false
	}
	steps := e.def.Steps
	var cur apiclient.WorkflowStepDef
	found := false
	for _, part := range strings.Split(path, ".") {
		if !strings.HasPrefix(part, "steps[") || !strings.HasSuffix(part, "]") {
			return apiclient.WorkflowStepDef{}, false
		}
		var idx int
		if _, err := fmt.Sscanf(part, "steps[%d]", &idx); err != nil {
			return apiclient.WorkflowStepDef{}, false
		}
		if idx < 0 || idx >= len(steps) {
			return apiclient.WorkflowStepDef{}, false
		}
		cur, found = steps[idx], true
		steps = cur.Steps
	}
	return cur, found
}

// stepValue reads one field of a step for display. It is a switch rather than
// reflection because the wire type is the client's own, and a field it does
// not carry is one the row shows as unset rather than one that panics.
func stepValue(st apiclient.WorkflowStepDef, name string) string {
	switch name {
	case "id":
		return st.ID
	case "name":
		return st.Name
	case "type":
		return st.Type
	case "if":
		return st.If
	case "prompt":
		return st.Prompt
	case "agent":
		return st.Agent
	case "model":
		return st.Model
	case "effort":
		return st.Effort
	case "permission_mode":
		return st.PermissionMode
	case "on_input":
		return st.OnInput
	case "check":
		return st.Check
	case "run":
		return st.Run
	case "shell":
		return st.Shell
	case "instructions":
		return st.Instructions
	case "workflow":
		return st.Workflow
	}
	return ""
}
