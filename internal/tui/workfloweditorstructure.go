package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The editor's structural half (issue #320, claim 5). internal/workflow's
// editor has implemented insert, remove and move since task 065 and the API
// has passed all four operations through since; the form emitted `set` and a
// leaf `remove` and nothing else, so a step could be retyped and never added,
// dropped or reordered.
//
// Two rules shape what is here. The first is that the file must be valid
// *between* PATCHes, because the daemon re-parses the whole document on each
// one and refuses it entire: an insert therefore carries the new entry's
// required fields, and where a type's shape needs a child entry — a parallel
// group with no sub-step, a fan_out with no lane — the same PATCH carries the
// ops that make one. The second is that `d` asks first. §15 view 5's "there is
// no delete: the view gains no destructive action" is narrowed by this issue
// to mean no *unconfirmed* destructive action, and the confirmation overlay
// below is what makes that reading true.

// wfEditorConfirm is the yes/no in front of a destructive key. It is a
// [wfEditorOverlay] like the value editors are, which is what stops `q` from
// quitting the TUI while it is up and what gets it drawn on the row it is
// about.
type wfEditorConfirm struct {
	prompt string
	act    func() tea.Cmd
}

func newWFEditorConfirm(prompt string, act func() tea.Cmd) *wfEditorConfirm {
	return &wfEditorConfirm{prompt: prompt, act: act}
}

func (o *wfEditorConfirm) Update(msg tea.KeyPressMsg) (wfEditorOverlay, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		return nil, o.act()
	default:
		// Anything else is "no". A confirmation whose only refusal was one
		// particular key would be a trap for the key that missed.
		return nil, nil
	}
}

func (o *wfEditorConfirm) View(_, _ int) string {
	return styleBad.Render(o.prompt) + styleDim.Render("  y remove · any other key cancels")
}

func (o *wfEditorConfirm) FullPane() bool { return false }

// Value and Dirty are the interface's, and mean nothing here: a confirmation
// commits no value. Dirty is false so a host that treats "clean" as "write
// nothing" is right about this overlay too.
func (o *wfEditorConfirm) Value() string { return "" }
func (o *wfEditorConfirm) Dirty() bool   { return false }

// wfListTarget is the sequence the structural keys act on.
type wfListTarget struct {
	// list is the dotted path of the sequence — "steps", "steps[3].steps",
	// "steps[7].lanes", "fields" — and kind its last segment, which is what
	// decides whether an insert builds a step, a lane or a declared field.
	list  string
	kind  string
	index int
	count int
	// onItem is false when the cursor is on the list itself rather than on a
	// member of it, which is how `a` appends to an empty list.
	onItem bool
}

// listTarget reports the sequence the cursor addresses. A row that is a member
// of one carries the list and its index (task 065's wfEditRow fields); a row
// that *holds* a list — the top-level `steps`, a group's `steps`, a fan_out's
// `lanes`, the declared `fields` — addresses the end of it; and a form that is
// itself a list with no members left addresses its own breadcrumb, which is
// what keeps an emptied list from becoming unrefillable.
func (e *wfEditorLayer) listTarget() (wfListTarget, bool) {
	if e.cursor >= 0 && e.cursor < len(e.rows) {
		row := e.rows[e.cursor]
		if row.list != "" {
			return wfListTarget{
				list: row.list, kind: wfListKind(row.list), index: row.index,
				count: e.listCount(row.list), onItem: true,
			}, true
		}
		if list, ok := rowHoldsList(row); ok {
			count := e.listCount(list)
			return wfListTarget{list: list, kind: wfListKind(list), index: count, count: count}, true
		}
	}
	// An emptied list: no rows, so the breadcrumb is the only address left.
	if node, ok := e.resolve(e.path); ok {
		switch node.kind {
		case wfNodeSteps, wfNodeLanes, wfNodeFields:
			return wfListTarget{list: e.path, kind: wfListKind(e.path)}, true
		}
	}
	return wfListTarget{}, false
}

// rowHoldsList reports the sequence a row is the header of rather than a
// member of.
func rowHoldsList(row wfEditRow) (string, bool) {
	switch row.field.Control {
	case apiclient.WorkflowControlSteps, apiclient.WorkflowControlLanes,
		apiclient.WorkflowControlFields:
	default:
		return "", false
	}
	if row.descend != "" {
		return row.descend, true
	}
	if row.field.Name == "steps" {
		// The top-level `steps` row is a count, not a descent: its members are
		// the rows below it.
		return "steps", true
	}
	return "", false
}

// listCount is how many members a sequence has, counted from the rows that
// claim it — the same source the indices come from, so the two cannot disagree.
func (e *wfEditorLayer) listCount(list string) int {
	n := 0
	for _, row := range e.rows {
		if row.list == list {
			n++
		}
	}
	return n
}

// wfListKind is the last segment of a sequence path: "steps", "lanes" or
// "fields".
func wfListKind(list string) string {
	if i := strings.LastIndex(list, "."); i >= 0 {
		list = list[i+1:]
	}
	return list
}

// insertOps builds the PATCH an `a` sends. typ is the chosen step type, and is
// ignored for a lanes or fields list, which have no type to choose.
func (e *wfEditorLayer) insertOps(t wfListTarget, typ string) []apiclient.WorkflowOp {
	at := t.index
	if t.onItem {
		// Add *after* the row under the cursor: `a` on the third step means
		// "another one here", and appending to the end of a long list would
		// scroll the answer off the screen.
		at++
	}
	used := e.usedIDs()
	switch t.kind {
	case "steps":
		return e.stepInsertOps(t.list, at, typ, used)
	case "lanes":
		return e.laneInsertOps(t.list, at, used)
	case "fields":
		return []apiclient.WorkflowOp{e.fieldInsertOp(t.list, at)}
	}
	return nil
}

// stepInsertOps is one new step, plus whatever its type's shape needs to be
// valid on arrival.
func (e *wfEditorLayer) stepInsertOps(list string, at int, typ string, used map[string]bool) []apiclient.WorkflowOp {
	path := fmt.Sprintf("%s[%d]", list, at)
	item := []apiclient.WorkflowOpField{
		{Key: "id", Value: wfUniqueID(typ, used)},
		{Key: "type", Value: renderYAMLScalar(typ)},
	}
	st, known := e.stepType(typ)
	for _, f := range st.Fields {
		// A structural required field — a group's `steps:` — is not a value
		// to seed but a child entry to insert, which is the loop below.
		if !f.Required || wfStructuralControls[f.Control] != "" {
			continue
		}
		item = append(item, wfSkeletonField(f))
	}
	ops := []apiclient.WorkflowOp{{Op: apiclient.WorkflowOpInsert, Path: path, Item: item}}
	if !known {
		return ops
	}
	// A group with no sub-step and a fan_out with no lane are both refused by
	// §8.2, so the ops that give them one ride along in the same PATCH: the
	// daemon applies them in order against the evolving document and validates
	// once, at the end.
	for _, f := range st.Fields {
		if f.Control == apiclient.WorkflowControlSteps && f.Required {
			ops = append(ops, e.stepInsertOps(path+"."+f.Name, 0, wfPlaceholderStep, used)...)
		}
	}
	if typ == wfStepFanOut {
		// `lanes:` is not marked required by the descriptor — a fan_out may
		// carry a `lane:` template instead — but §7.6 refuses one that has
		// neither, so the skeleton picks the shape a person can see and edit.
		ops = append(ops, e.laneInsertOps(path+".lanes", 0, used)...)
	}
	return ops
}

// laneInsertOps is one new lane: an id, and the inline step §7.6 requires a
// lane to have when it does not name a workflow.
func (e *wfEditorLayer) laneInsertOps(list string, at int, used map[string]bool) []apiclient.WorkflowOp {
	path := fmt.Sprintf("%s[%d]", list, at)
	ops := []apiclient.WorkflowOp{{
		Op: apiclient.WorkflowOpInsert, Path: path,
		Item: []apiclient.WorkflowOpField{{Key: "id", Value: wfUniqueID("lane", used)}},
	}}
	return append(ops, e.stepInsertOps(path+".steps", 0, wfPlaceholderStep, used)...)
}

// fieldInsertOp is one new §8.1.2 declared field: a name and a type, which are
// the two the descriptor marks required.
func (e *wfEditorLayer) fieldInsertOp(list string, at int) apiclient.WorkflowOp {
	used := map[string]bool{}
	if e.def != nil {
		for _, f := range e.def.Fields {
			used[f.Name] = true
		}
	}
	item := []apiclient.WorkflowOpField{{Key: "name", Value: wfUniqueID("field", used)}}
	for _, f := range e.schema.Field {
		if f.Required && f.Name != "name" {
			item = append(item, wfSkeletonField(f))
		}
	}
	return apiclient.WorkflowOp{
		Op: apiclient.WorkflowOpInsert, Path: fmt.Sprintf("%s[%d]", list, at), Item: item,
	}
}

// wfStepFanOut and wfPlaceholderStep are wire strings rather than constants
// from internal/workflow: the TUI talks to the daemon over apiclient, which
// publishes neither, and the step types are the descriptor's own vocabulary.
const (
	wfStepFanOut = "fan_out"
	// wfPlaceholderStep is the type a generated child gets. `command` is legal
	// in every context a generated child lands in — a group, a loop body, a
	// lane — and needs nothing but a `run:` to be valid.
	wfPlaceholderStep = "command"
)

// wfSkeletonPlaceholders is what a required field is seeded with, by field
// name. The values are deliberately obvious rather than plausible: a skeleton
// is something to edit, and a `run:` that looked real would be one somebody
// shipped.
var wfSkeletonPlaceholders = map[string]string{
	"prompt":       "TODO: what the agent should do",
	"run":          "echo TODO",
	"instructions": "TODO: what a person must do here",
	// A guard is a template, and one that renders "true" runs the step it
	// guards, which is the harmless default for a skeleton.
	"if":       "true",
	"workflow": "todo",
}

// wfSkeletonField renders one required field of a new entry.
func wfSkeletonField(f apiclient.WorkflowSchemaField) apiclient.WorkflowOpField {
	if f.Control == apiclient.WorkflowControlText {
		// A block scalar, because that is the shape the value will grow into
		// and because it is what the multi-line pane will open on.
		return apiclient.WorkflowOpField{
			Key: f.Name, Value: firstNonEmpty(wfSkeletonPlaceholders[f.Name], "TODO"), Block: true,
		}
	}
	value := wfSkeletonPlaceholders[f.Name]
	if value == "" {
		switch f.Control {
		case apiclient.WorkflowControlEnum:
			if len(f.Values) > 0 {
				value = f.Values[0]
			}
		case apiclient.WorkflowControlInt:
			value = "1"
		case apiclient.WorkflowControlDuration:
			value = "1m"
		case apiclient.WorkflowControlBool:
			value = "false"
		default:
			value = "todo"
		}
	}
	return apiclient.WorkflowOpField{Key: f.Name, Value: renderYAMLScalar(value)}
}

// wfUniqueID picks the first free "base-N", and records it: a PATCH that
// inserts a group and its sub-step in one request must not name both the same
// thing, and the daemon would refuse the pair rather than the second half.
func wfUniqueID(base string, used map[string]bool) string {
	for n := 1; ; n++ {
		id := base + "-" + strconv.Itoa(n)
		if !used[id] {
			used[id] = true
			return id
		}
	}
}

// usedIDs collects every step and lane id the document already has. Ids are
// unique per workflow rather than per list (§8.2), so the walk is the whole
// definition rather than the block on screen.
func (e *wfEditorLayer) usedIDs() map[string]bool {
	used := map[string]bool{}
	if e.def == nil {
		return used
	}
	var walk func(steps []apiclient.WorkflowStepDef)
	lanes := func(list []apiclient.WorkflowLaneDef) {
		for _, lane := range list {
			used[lane.ID] = true
			walk(lane.Steps)
		}
	}
	walk = func(steps []apiclient.WorkflowStepDef) {
		for _, st := range steps {
			used[st.ID] = true
			walk(st.Steps)
			lanes(st.Lanes)
			if st.Lane != nil {
				lanes([]apiclient.WorkflowLaneDef{*st.Lane})
			}
			if st.Merge != nil && st.Merge.Agent != nil {
				walk([]apiclient.WorkflowStepDef{*st.Merge.Agent})
			}
		}
	}
	walk(e.def.Steps)
	return used
}

// wfItemLabel names the entry a confirmation is about, so the prompt says what
// is being removed rather than only where it sits.
func wfItemLabel(row wfEditRow) string {
	label := row.label
	if label == "" {
		label = row.field.Name
	}
	return fmt.Sprintf("%s[%d] (%s)", row.list, row.index, label)
}

// editorAdd is `a`. On a steps list it asks which type first — the answer
// decides which required fields the skeleton carries — and on a lanes or a
// declared-fields list there is nothing to ask, so it inserts straight away.
func (w *workflowsView) editorAdd() tea.Cmd {
	e := w.editor
	t, ok := e.listTarget()
	if !ok {
		e.err = "a adds to a list: move to a step, a lane or a declared field first"
		return nil
	}
	if t.kind != "steps" {
		return w.editorInsert(t, "")
	}
	at := t.index
	if t.onItem {
		at++
	}
	types := e.typesFor(e.contextOf(fmt.Sprintf("%s[%d]", t.list, at)))
	if len(types) == 0 {
		e.err = "no step type is legal here"
		return nil
	}
	options := make([]pickerOption, 0, len(types))
	for _, typ := range types {
		note := ""
		if st, known := e.stepType(typ); known {
			note = st.Help
		}
		options = append(options, pickerOption{value: typ, label: typ, note: note})
	}
	e.editing = e.cursor
	// No free-text row: the descriptor says which types this context accepts,
	// and a type it does not accept is a 400 the form must never offer
	// (task 065 decision 3).
	e.overlay = newWFEditorPicker("new step", apiclient.WorkflowControlSteps, "",
		options, false, func(typ string) tea.Cmd { return w.editorInsert(t, typ) })
	return nil
}

// editorInsert sends the skeleton.
func (w *workflowsView) editorInsert(t wfListTarget, typ string) tea.Cmd {
	e := w.editor
	ops := e.insertOps(t, typ)
	if len(ops) == 0 {
		e.err = "nothing to add at " + t.list
		return nil
	}
	e.saving = true
	return w.editorPatchCmd(ops)
}

// editorRemove is `d`: the confirmation, and the removal it guards.
func (w *workflowsView) editorRemove() {
	e := w.editor
	if e.cursor < 0 || e.cursor >= len(e.rows) {
		return
	}
	row := e.rows[e.cursor]
	if row.list == "" {
		e.err = "d removes a list item: move to a step, a lane or a declared field first"
		return
	}
	path := fmt.Sprintf("%s[%d]", row.list, row.index)
	e.editing = e.cursor
	e.overlay = newWFEditorConfirm("remove "+wfItemLabel(row)+"?", func() tea.Cmd {
		e.saving = true
		return w.editorPatchCmd([]apiclient.WorkflowOp{{
			Op: apiclient.WorkflowOpRemove, Path: path,
		}})
	})
}

// editorMove is `K` and `J`. They are capitalised on purpose: `k` and `j` move
// the cursor, and a key that moved the *step* by one letter's difference is a
// file rewritten by a typo.
func (w *workflowsView) editorMove(delta int) tea.Cmd {
	e := w.editor
	if e.cursor < 0 || e.cursor >= len(e.rows) {
		return nil
	}
	row := e.rows[e.cursor]
	if row.list == "" {
		e.err = "K and J reorder a list item: move to a step, a lane or a declared field first"
		return nil
	}
	to := row.index + delta
	if to < 0 || to >= e.listCount(row.list) {
		// Already at an end. Silence rather than an error: the row cannot go
		// further and nothing has gone wrong.
		return nil
	}
	// The cursor follows the item it moved. The rows are rebuilt from the file
	// once the write lands, and a list's rows are contiguous and in index
	// order, so the neighbouring row is where the item will be.
	e.cursor = max(0, min(len(e.rows)-1, e.cursor+delta))
	e.saving = true
	return w.editorPatchCmd([]apiclient.WorkflowOp{{
		Op: apiclient.WorkflowOpMove, Path: fmt.Sprintf("%s[%d]", row.list, row.index), To: to,
	}})
}
