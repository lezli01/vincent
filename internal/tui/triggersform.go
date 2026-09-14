package tui

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The triggers form (§15 view 11, task 096.6): task 065's form machinery on
// the trigger descriptor. The rows are wfEditRows drawn by renderFormRows;
// a committed row becomes one operation through rowOp; a block scalar, a
// mapping and a closed set open the same three overlays the workflow editor
// uses. What is the trigger's own is only what the workflow descriptor does
// not have: the source and action variants chosen by the document's `type`,
// the project picker, a number, the match map, and the values the served
// schema marks dangerous, which ask before they are written (decision 19).

// Form messages.
type (
	trigFormLoadedMsg struct {
		id     string
		schema apiclient.TriggerSchema
		detail apiclient.TriggerDetail
		err    error
	}
	trigFormSavedMsg struct {
		id     string
		result apiclient.TriggerWriteResult
		err    error
	}
)

// trigRow is one form row: the shared row plus what the trigger descriptor
// adds to a field.
type trigRow struct {
	wfEditRow
	dangerous []apiclient.TriggerDangerousValue
	// fallback is the descriptor's Default: what an absent key means.
	fallback string
	// readOnly, when set, is why enter does not edit this row.
	readOnly string
}

// trigValueConfirm is a dangerous value waiting for its `y`.
type trigValueConfirm struct {
	row     trigRow
	value   string
	op      apiclient.WorkflowOp
	warning []string
}

// trigFormLayer is the open form.
type trigFormLayer struct {
	id      string
	file    string
	version string
	schema  apiclient.TriggerSchema
	def     map[string]any
	// path is the block on screen: "" for the top level, then "source",
	// "action", "limits" or "source.signature".
	path   string
	rows   []trigRow
	cursor int

	input   *textField
	overlay wfEditorOverlay
	editing int
	confirm *trigValueConfirm

	loading bool
	saving  bool
	// pending is the path of the row whose write is in flight, which is where
	// a refusal naming a path the screen has no row for is shown.
	pending string
	err     string
	note    string
	// rowErr is the daemon's refusal per row path, rendered on the field.
	rowErr map[string]string
}

func (f *trigFormLayer) capturing() bool {
	return f.input != nil || f.overlay != nil || f.confirm != nil
}

// openForm opens the form on the selected trigger.
func (v *triggersView) openForm() tea.Cmd {
	s, ok := v.current()
	if !ok {
		return nil
	}
	return v.openFormOn(s.ID, s.File, s.Version)
}

func (v *triggersView) openFormOn(id, file, version string) tea.Cmd {
	v.err, v.note = "", ""
	v.form = &trigFormLayer{
		id: id, file: file, version: version,
		loading: true, editing: -1, rowErr: map[string]string{},
	}
	return v.formLoadCmd(id)
}

func (v *triggersView) formLoadCmd(id string) tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		schema, err := client.TriggerSchema(ctx)
		if err != nil {
			return trigFormLoadedMsg{id: id, err: err}
		}
		detail, err := client.Trigger(ctx, id)
		return trigFormLoadedMsg{id: id, schema: schema, detail: detail, err: err}
	}
}

// updateLayerMsg lands the answers the view's layers asked for: the form's
// reads and writes, a create, and the two dry runs.
func (v *triggersView) updateLayerMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case trigFormLoadedMsg:
		v.applyFormLoaded(msg)
		return nil, true
	case trigFormSavedMsg:
		return v.applyFormSaved(msg), true
	case trigCreatedMsg:
		return v.applyCreated(msg), true
	case triggerTestedMsg:
		if d := v.dry; d != nil && !d.poll && d.id == msg.id {
			d.running = false
			if msg.err != nil {
				d.err, d.judgement = errString(msg.err), nil
			} else {
				d.err, d.judgement = "", &msg.judgement
			}
		}
		return nil, true
	case triggerPolledMsg:
		if d := v.dry; d != nil && d.poll && d.id == msg.id {
			d.running = false
			if msg.err != nil {
				d.err, d.result = errString(msg.err), nil
			} else {
				d.err, d.result = "", &msg.poll
			}
		}
		return nil, true
	}
	return nil, false
}

func (v *triggersView) applyFormLoaded(msg trigFormLoadedMsg) {
	f := v.form
	if f == nil || f.id != msg.id {
		return
	}
	f.loading = false
	if msg.err != nil {
		f.err = errString(msg.err)
		return
	}
	f.schema = msg.schema
	f.version, f.file = msg.detail.Version, firstNonEmpty(msg.detail.File, f.file)
	f.def = msg.detail.Definition
	if f.def == nil {
		f.rows = nil
		f.err = "this file does not validate, so the form cannot load it — esc, then e opens it in $EDITOR"
		if len(msg.detail.Errors) > 0 {
			f.err += " (" + findingText(msg.detail.Errors[0]) + ")"
		}
		return
	}
	f.rebuild()
}

// applyFormSaved lands one PATCH. A stale version says so and re-reads the
// file; a refusal puts each finding on the row it names and leaves the typed
// value where it was; a success re-reads the file the daemon just wrote.
func (v *triggersView) applyFormSaved(msg trigFormSavedMsg) tea.Cmd {
	f := v.form
	if f == nil || f.id != msg.id {
		return nil
	}
	f.saving = false
	if msg.err == nil {
		f.err, f.note = "", "saved"
		f.version = msg.result.Version
		f.loading = true
		return tea.Batch(v.formLoadCmd(f.id), v.loadCmd())
	}
	var apiErr *apiclient.Error
	switch {
	case isStale(msg.err):
		f.err = "the file changed on disk since the form read it — re-read it; make the change again"
		f.rowErr = map[string]string{}
		f.loading = true
		return v.formLoadCmd(f.id)
	case errors.As(msg.err, &apiErr) && apiErr.Status == http.StatusBadRequest && apiErr.Details["errors"] != "":
		var findings []apiclient.WorkflowFinding
		if json.Unmarshal([]byte(apiErr.Details["errors"]), &findings) == nil && len(findings) > 0 {
			f.placeRefusals(findings)
			f.err = "refused — nothing was written; the value that caused it is still on its row"
			return nil
		}
	}
	f.err = errString(msg.err)
	return nil
}

// placeRefusals puts each finding on the row whose path it names. A finding
// for a key this block has no row for goes on the row that was written, with
// its own path, so nothing the daemon said is lost off screen.
func (f *trigFormLayer) placeRefusals(findings []apiclient.WorkflowFinding) {
	f.rowErr = map[string]string{}
	for _, fd := range findings {
		target, text := f.pending, fd.Path+": "+fd.Message
		if f.indexOf(fd.Path) >= 0 {
			target, text = fd.Path, fd.Message
		}
		if prev := f.rowErr[target]; prev != "" {
			text = prev + "; " + text
		}
		f.rowErr[target] = text
	}
}

func (f *trigFormLayer) indexOf(path string) int {
	if path == "" {
		return -1
	}
	for i, r := range f.rows {
		if r.path == path {
			return i
		}
	}
	return -1
}

// rebuild draws the rows of the block on screen from the served descriptor
// and the document's values.
func (f *trigFormLayer) rebuild() {
	f.rows = nil
	if f.def == nil {
		return
	}
	switch f.path {
	case "":
		f.buildTop()
	case "source":
		f.buildVariant("source", f.schema.Sources)
	case "action":
		f.buildVariant("action", f.schema.Actions)
	case "limits":
		f.buildFields("limits", f.schema.Limits)
	case "source.signature":
		f.buildFields("source.signature", f.schema.Signature)
	}
	f.cursor = min(max(f.cursor, 0), max(len(f.rows)-1, 0))
}

func trigField(sf apiclient.TriggerSchemaField) apiclient.WorkflowSchemaField {
	return apiclient.WorkflowSchemaField{
		Name: sf.Name, Control: sf.Control, Values: sf.Values, Required: sf.Required, Help: sf.Help,
	}
}

func (f *trigFormLayer) leaf(block string, sf apiclient.TriggerSchemaField) trigRow {
	path := sf.Name
	if block != "" {
		path = block + "." + sf.Name
	}
	row := trigRow{
		wfEditRow: wfEditRow{field: trigField(sf), path: path, value: trigValue(f.def, path)},
		dangerous: sf.Dangerous,
		fallback:  sf.Default,
	}
	if sf.Control == apiclient.TriggerControlMatch {
		row.field.Help += " — one key=value per entry; a|b means any of"
	}
	return row
}

func (f *trigFormLayer) buildTop() {
	for _, sf := range f.schema.TopLevel {
		row := f.leaf("", sf)
		switch sf.Control {
		case apiclient.TriggerControlSource:
			row.path, row.descend = "", "source"
			row.value = strings.Join(nonEmpty(trigValue(f.def, "source.type"),
				projectLabel(trigValue(f.def, "source.project"))), " · ")
		case apiclient.TriggerControlAction:
			row.path, row.descend = "", "action"
			row.value = trigValue(f.def, "action.type")
		case apiclient.TriggerControlLimits:
			row.path, row.descend = "", "limits"
			row.value = trigValue(f.def, "limits")
		}
		if sf.Name == "id" {
			row.readOnly = "the id is the file's name — create a new trigger to rename one"
		}
		f.rows = append(f.rows, row)
	}
}

// buildVariant draws a source or an action: the fields of the variant its
// `type` names. An unknown type draws only the type row, offering every one.
func (f *trigFormLayer) buildVariant(block string, variants []apiclient.TriggerSchemaVariant) {
	typ := trigValue(f.def, block+".type")
	for _, variant := range variants {
		if variant.Type != typ {
			continue
		}
		for _, sf := range variant.Fields {
			row := f.leaf(block, sf)
			switch {
			case sf.Name == "type":
				row.field.Help = variantHelp(variant)
			case sf.Control == apiclient.TriggerControlSignature:
				row.path, row.descend = "", "source.signature"
				row.value = trigValue(f.def, "source.signature.scheme")
			}
			f.rows = append(f.rows, row)
		}
		return
	}
	values := make([]string, 0, len(variants))
	for _, variant := range variants {
		values = append(values, variant.Type)
	}
	f.rows = append(f.rows, trigRow{wfEditRow: wfEditRow{
		field: apiclient.WorkflowSchemaField{Name: "type", Control: apiclient.WorkflowControlEnum, Values: values, Required: true},
		path:  block + ".type", value: typ,
	}})
}

// variantHelp is a type row's help: what the variant does and, for a GitHub
// source, which events it synthesizes and which of them are trusted.
func variantHelp(variant apiclient.TriggerSchemaVariant) string {
	parts := nonEmpty(variant.Help)
	if len(variant.Events) > 0 {
		parts = append(parts, "events: "+strings.Join(variant.Events, ", "))
	}
	if len(variant.Trusted) > 0 {
		parts = append(parts, "trusted without allowed_actors: "+strings.Join(variant.Trusted, ", "))
	}
	return strings.Join(parts, " · ")
}

func (f *trigFormLayer) buildFields(block string, fields []apiclient.TriggerSchemaField) {
	for _, sf := range fields {
		f.rows = append(f.rows, f.leaf(block, sf))
	}
}

// updateFormKey routes a key to whichever part of the form has it.
func (v *triggersView) updateFormKey(msg tea.KeyPressMsg) tea.Cmd {
	f := v.form
	switch {
	case f.confirm != nil:
		switch msg.String() {
		case "y", "Y":
			c := f.confirm
			f.confirm = nil
			return v.formSend(c.row, c.value, c.op)
		case "n", "N", "esc":
			f.confirm = nil
		}
		return nil
	case f.input != nil:
		switch msg.String() {
		case "esc":
			f.input, f.editing = nil, -1
			return nil
		case "enter":
			value, i := f.input.Value(), f.editing
			f.input, f.editing = nil, -1
			if i < 0 || i >= len(f.rows) {
				return nil
			}
			return v.formCommit(f.rows[i], value)
		}
		in, cmd := f.input.Update(msg)
		f.input = &in
		return cmd
	case f.overlay != nil:
		overlay, cmd := f.overlay.Update(msg)
		f.overlay = overlay
		if overlay == nil {
			f.editing = -1
		}
		return cmd
	}
	switch msg.String() {
	case "up", "k":
		f.cursor = max(0, f.cursor-1)
	case "down", "j":
		f.cursor = min(max(len(f.rows)-1, 0), f.cursor+1)
	case "enter":
		return v.formActivate()
	case "R":
		f.err, f.note = "", ""
		f.rowErr = map[string]string{}
		f.loading = true
		return v.formLoadCmd(f.id)
	case "esc":
		switch {
		case f.err != "" || f.note != "":
			f.err, f.note = "", ""
		case f.path != "":
			f.path = parentPath(f.path)
			f.cursor = 0
			f.rebuild()
		default:
			v.form = nil
			return v.loadCmd()
		}
	}
	return nil
}

// formActivate opens the row under the cursor: descend, cycle, toggle, or
// hand it to the field or overlay its control needs.
func (v *triggersView) formActivate() tea.Cmd {
	f := v.form
	if f.cursor < 0 || f.cursor >= len(f.rows) {
		return nil
	}
	row := f.rows[f.cursor]
	switch {
	case row.descend != "":
		f.path, f.cursor = row.descend, 0
		f.rebuild()
		return nil
	case row.readOnly != "":
		f.err = row.readOnly
		return nil
	case row.path == "":
		return nil
	}
	commit := func(value string) tea.Cmd { return v.formCommit(row, value) }
	switch row.field.Control {
	case apiclient.WorkflowControlEnum:
		return v.formCommit(row, cycleEnum(row.field, row.value))
	case apiclient.WorkflowControlBool:
		next := "true"
		if row.value == "true" {
			next = "false"
		}
		return v.formCommit(row, next)
	case apiclient.WorkflowControlText:
		f.editing = f.cursor
		pane, cmd := newWFEditorPane(row.value, commit)
		f.overlay = pane
		return cmd
	case apiclient.WorkflowControlMap, apiclient.TriggerControlMatch:
		f.editing = f.cursor
		f.overlay = newWFEditorMap(row.value, commit)
		return nil
	case apiclient.TriggerControlProject:
		// Host state, as the agent pickers are: the projects come from
		// GET /v1/projects, never from the descriptor.
		f.editing = f.cursor
		f.overlay = newWFEditorPicker("project", row.field.Control, row.value, v.projectOptions(), false, commit)
		return nil
	}
	in := newTextField()
	in.SetValue(row.value)
	in.Focus()
	f.input, f.editing = &in, f.cursor
	return nil
}

func (v *triggersView) projectOptions() []pickerOption {
	out := make([]pickerOption, 0, len(v.projects))
	for _, p := range v.projects {
		out = append(out, pickerOption{value: strconv.FormatInt(p.ID, 10), label: p.Name, note: p.Path})
	}
	return out
}

// formCommit turns a row's new value into its operation. A value this side
// can already refuse is refused on its row; a value the served schema marks
// dangerous waits for `y`; anything else is written.
func (v *triggersView) formCommit(row trigRow, value string) tea.Cmd {
	f := v.form
	if f == nil || value == row.value {
		return nil
	}
	op, bad := trigRowOp(row, value)
	if bad != "" {
		if i := f.indexOf(row.path); i >= 0 {
			f.rows[i].value = value
		}
		f.rowErr[row.path] = bad
		return nil
	}
	for _, d := range row.dangerous {
		if d.Value != value {
			continue
		}
		warning := []string{d.Warning}
		if row.path == "enabled" {
			warning = append(warning, v.enableConsequence(trigValue(f.def, "on_fire"))...)
		}
		f.confirm = &trigValueConfirm{row: row, value: value, op: op, warning: warning}
		return nil
	}
	return v.formSend(row, value, op)
}

// enableConsequence is what enabling means for a trigger with this on_fire.
func (v *triggersView) enableConsequence(onFire string) []string {
	out := []string{"on_fire is propose: each task is created paused, for you to resume"}
	if onFire == "create" {
		out = []string{"on_fire is create: each task starts running as soon as a slot is free"}
	}
	if v.loaded && !v.list.Enabled {
		out = append(out, "triggers.enabled is off, so nothing polls until it is turned on")
	}
	return out
}

func (v *triggersView) formSend(row trigRow, value string, op apiclient.WorkflowOp) tea.Cmd {
	f := v.form
	if f == nil {
		return nil
	}
	if i := f.indexOf(row.path); i >= 0 {
		// The row shows what was written until the daemon answers, and keeps
		// showing it beside a refusal.
		f.rows[i].value = value
	}
	delete(f.rowErr, row.path)
	f.err, f.note = "", ""
	client := v.client
	if client == nil {
		f.err = "not connected"
		return nil
	}
	f.saving, f.pending = true, row.path
	id, version := f.id, f.version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		res, err := client.PatchTrigger(ctx, id, version, []apiclient.WorkflowOp{op})
		return trigFormSavedMsg{id: id, result: res, err: err}
	}
}

// trigRowOp is rowOp for the controls a trigger adds. A boolean, a number and
// a project id are YAML's own scalars: rowOp's quoting is right for text and
// would turn `true` into a string a boolean field refuses.
func trigRowOp(row trigRow, value string) (apiclient.WorkflowOp, string) {
	set := apiclient.WorkflowOp{Op: apiclient.WorkflowOpSet, Path: row.path}
	if value == unsetMarker || (value == "" && !row.field.Required) {
		return rowOp(row.wfEditRow, value)
	}
	trimmed := strings.TrimSpace(value)
	switch row.field.Control {
	case apiclient.WorkflowControlBool:
		if trimmed != "true" && trimmed != "false" {
			return set, row.field.Name + ": must be true or false"
		}
		set.Value = trimmed
		return set, ""
	case apiclient.TriggerControlNumber:
		n, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) {
			return set, row.field.Name + ": must be a non-negative number such as 2.50, got " + strconv.Quote(value)
		}
		set.Value = trimmed
		return set, ""
	case apiclient.TriggerControlProject:
		if n, err := strconv.ParseInt(trimmed, 10, 64); err != nil || n < 1 {
			return set, row.field.Name + ": must be a project id, got " + strconv.Quote(value)
		}
		set.Value = trimmed
		return set, ""
	case apiclient.TriggerControlMatch:
		set.Value = renderMatchMap(value)
		return set, ""
	}
	return rowOp(row.wfEditRow, value)
}

// renderMatchMap writes the match row's `key=value` entries as a YAML flow
// mapping, where `a|b` is a list meaning any of.
func renderMatchMap(value string) string {
	pairs := parseMapRow(value)
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if p.key == "" {
			continue
		}
		val := renderYAMLScalar(p.value)
		if strings.Contains(p.value, "|") {
			items := strings.Split(p.value, "|")
			for i := range items {
				items[i] = renderYAMLScalar(strings.TrimSpace(items[i]))
			}
			val = "[" + strings.Join(items, ", ") + "]"
		}
		out = append(out, renderYAMLScalar(p.key)+": "+val)
	}
	return "{" + strings.Join(out, ", ") + "}"
}

// trigValue reads one dotted path out of the parsed definition and renders it
// the way its row shows it — and the way the row's commit reads it back.
func trigValue(def map[string]any, path string) string {
	var cur any = def
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[seg]
	}
	return trigRender(cur, ", ")
}

func trigRender(v any, listSep string) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return wfBoolText(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, trigRender(item, listSep))
		}
		return strings.Join(parts, listSep)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+trigRender(x[k], "|"))
		}
		return strings.Join(parts, ", ")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, s := range values {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func projectLabel(id string) string {
	if id == "" {
		return ""
	}
	return "project " + id
}

// rowNote is the form's trailing annotation: the refusal on its field, the
// project a project id names, or what an absent key means.
func (v *triggersView) rowNote(f *trigFormLayer) func(int) string {
	return func(i int) string {
		row := f.rows[i]
		if msg := f.rowErr[row.path]; msg != "" && row.path != "" {
			return styleBad.Render("⚠ " + msg)
		}
		switch {
		case row.field.Control == apiclient.TriggerControlProject && row.value != "":
			if id, err := strconv.ParseInt(row.value, 10, 64); err == nil {
				return styleDim.Render(v.projectName(id))
			}
		case row.value == "" && row.fallback != "":
			return styleDim.Render("absent means " + row.fallback)
		}
		return ""
	}
}

func (f *trigFormLayer) plainRows() []wfEditRow {
	out := make([]wfEditRow, len(f.rows))
	for i := range f.rows {
		out[i] = f.rows[i].wfEditRow
	}
	return out
}
