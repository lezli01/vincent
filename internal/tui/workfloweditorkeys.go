package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The editor's keys and its one write path. Every committed row becomes one
// operation and one PATCH, carrying the version the last read handed back —
// so a file that moved underneath answers 409 and the pending edit stays on
// screen with the offer to reload (task 065 decision 4).

// unsetMarker is the row value that means "the file sets nothing here". It is
// the `(unset)` stop task 060's config form uses, and committing it removes
// the key rather than writing an empty string: an absent `model:` inherits
// the workflow default, and `model: ""` does not.
const unsetMarker = "(unset)"

func (w *workflowsView) updateEditorKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	e := w.editor
	if e.input != nil {
		return w.updateEditorInput(msg)
	}
	switch msg.String() {
	case "up", "k":
		e.cursor = max(0, e.cursor-1)
		return w, nil
	case "down", "j":
		e.cursor = min(len(e.rows)-1, e.cursor+1)
		return w, nil
	case "enter":
		return w, w.editorActivate()
	case "R":
		// The reload a 409 offers, and the manual one. Both re-read the file
		// and rebuild the rows from what it now says.
		e.err, e.stale = "", false
		e.loading = true
		return w, w.editorLoadCmd(e.key)
	case "esc":
		switch {
		case e.err != "":
			e.err, e.stale = "", false
		case e.path != "":
			// One layer per press (§15): out of the step, then out of the
			// editor, then out of the takeover.
			e.path = parentPath(e.path)
			e.cursor = 0
			e.rebuild()
		default:
			w.editor = nil
		}
		return w, nil
	}
	return w, nil
}

// parentPath drops the last "steps[i]" segment of a breadcrumb.
func parentPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	return ""
}

// editorActivate opens the row under the cursor: descend into a nested body,
// cycle an enum, or focus a text field.
func (w *workflowsView) editorActivate() tea.Cmd {
	e := w.editor
	if e.cursor < 0 || e.cursor >= len(e.rows) {
		return nil
	}
	row := e.rows[e.cursor]
	if row.descend != "" {
		e.path = row.descend
		e.cursor = 0
		e.rebuild()
		return nil
	}
	if row.path == "" {
		return nil
	}
	switch row.field.Control {
	case apiclient.WorkflowControlEnum:
		// An enum row cycles rather than opening a text field (task 058's
		// enum rows): the members are known, so there is nothing to type and
		// nothing to get wrong.
		return w.commitRow(row, cycleEnum(row.field, row.value))
	case apiclient.WorkflowControlBool:
		next := "true"
		if row.value == "true" {
			next = "false"
		}
		return w.commitRow(row, next)
	}
	in := newTextField()
	in.SetValue(row.value)
	in.Focus()
	e.input = &in
	e.editing = e.cursor
	return nil
}

// cycleEnum steps through the members and then past the end to (unset),
// which an optional row needs and a required one does not get.
func cycleEnum(f apiclient.WorkflowSchemaField, value string) string {
	values := f.Values
	if !f.Required {
		values = append(append([]string{}, values...), unsetMarker)
	}
	for i, v := range values {
		if v == value || (value == "" && v == unsetMarker) {
			return values[(i+1)%len(values)]
		}
	}
	if len(values) == 0 {
		return value
	}
	return values[0]
}

func (w *workflowsView) updateEditorInput(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	e := w.editor
	switch msg.String() {
	case "esc":
		e.input = nil
		return w, nil
	case "enter":
		value := e.input.Value()
		e.input = nil
		if e.editing < 0 || e.editing >= len(e.rows) {
			return w, nil
		}
		return w, w.commitRow(e.rows[e.editing], value)
	}
	in, cmd := e.input.Update(msg)
	e.input = &in
	return w, cmd
}

// commitRow turns one row into one operation. A row set to the unset marker
// or cleared becomes a remove; everything else is a set, as a block scalar
// where the control says the value is multi-line.
func (w *workflowsView) commitRow(row wfEditRow, value string) tea.Cmd {
	e := w.editor
	if value == row.value {
		return nil
	}
	op := apiclient.WorkflowOp{Op: apiclient.WorkflowOpSet, Path: row.path}
	switch {
	case value == unsetMarker || (value == "" && !row.field.Required):
		op.Op = apiclient.WorkflowOpRemove
	case row.field.Control == apiclient.WorkflowControlText:
		op.Value, op.Block = value, true
	case row.field.Control == apiclient.WorkflowControlList:
		op.Value = renderFlowList(value)
	default:
		op.Value = renderYAMLScalar(value)
	}
	// The row shows what was typed until the daemon answers. A rejected value
	// stays visible beside its error, which is what makes the error
	// actionable (§15).
	e.rows[e.cursor].value = value
	e.saving = true
	return w.editorPatchCmd([]apiclient.WorkflowOp{op})
}

// renderFlowList splits a comma-separated row into a YAML flow sequence.
func renderFlowList(value string) string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, renderYAMLScalar(p))
		}
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// renderYAMLScalar quotes a value YAML would otherwise read back as something
// other than the string that was typed. The daemon re-validates whatever
// arrives, so this is about fidelity rather than about safety.
func renderYAMLScalar(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, `:#{}[],&*?|<>=!%@\"'`+"\n\t") || strings.HasPrefix(value, " ") ||
		strings.HasSuffix(value, " ") {
		return fmt.Sprintf("%q", value)
	}
	switch strings.ToLower(value) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return fmt.Sprintf("%q", value)
	}
	return value
}

func (w *workflowsView) editorPatchCmd(ops []apiclient.WorkflowOp) tea.Cmd {
	client, e := w.client, w.editor
	if client == nil || e == nil {
		return nil
	}
	key, version := e.key, e.version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		res, err := client.PatchWorkflow(ctx, key.name, key.projectID, version, ops)
		return wfEditorSavedMsg{result: res, err: err}
	}
}

// updateEditorMsg handles the editor's own messages.
func (w *workflowsView) updateEditorMsg(msg tea.Msg) (panel, tea.Cmd, bool) {
	e := w.editor
	if e == nil {
		return w, nil, false
	}
	switch msg := msg.(type) {
	case wfEditorLoadedMsg:
		if msg.key != e.key {
			return w, nil, true
		}
		e.loading = false
		if msg.err != nil {
			e.err = msg.err.Error()
			return w, nil, true
		}
		e.schema, e.def, e.version = msg.schema, msg.def.Definition, msg.def.Version
		if e.def == nil {
			// The file broke between the list load and this fetch. $EDITOR is
			// the escape hatch for a file the forms cannot load, which is why
			// `e` is still on the list behind this layer.
			e.err = "this file does not parse; press esc and use e to open it in $EDITOR"
			return w, nil, true
		}
		e.rebuild()
		return w, nil, true
	case wfEditorSavedMsg:
		e.saving = false
		if msg.err != nil {
			var apiErr *apiclient.Error
			if errors.As(msg.err, &apiErr) && apiErr.Status == 409 {
				e.stale = true
				e.version = apiErr.Details["version"]
				e.err = msg.err.Error() + " — press R to re-read it"
				return w, nil, true
			}
			e.err = msg.err.Error()
			return w, nil, true
		}
		e.err, e.stale = "", false
		e.version = msg.result.Version
		// The registry reload the write triggered brings the new definition
		// back through the same path an external edit takes.
		return w, w.editorLoadCmd(e.key), true
	}
	return w, nil, false
}
