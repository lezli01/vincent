package tui

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

var taskFieldDecimalNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// pickerOption is one selectable value. note is the dim suffix — a
// provenance tag, a scope badge, an availability warning — and disabled
// marks a row that is shown for information only.
type pickerOption struct {
	value    string
	label    string
	note     string
	disabled bool
	// selected marks a member of a multi-select list. Only a multi picker
	// draws it, because in a single-select list the cursor already says which
	// value would be chosen.
	selected bool
}

// picker is the list a focused row expands into. allowFree adds a free-text
// row, because an adapter's catalog is a suggestion: a model shipped this
// morning is not in it, and the daemon accepts unknown values with a warning
// rather than rejecting them.
//
// row is an opaque identifier the owning form gives back to itself when a
// value is chosen — an ntRow in the new-task flow, a pfRow in the project
// form — so the picker itself stays ignorant of either row set.
// pickerWindow bounds how many option rows are drawn at once.
//
// Through v1 every catalog fit on a screen — claude ships 3 models, codex
// none — so the list was drawn whole. Cursor's catalog is ~180 models (§9.7),
// which would push the rest of the form, the hints and the error line off the
// terminal. The window is fixed rather than derived from the terminal height
// because the picker is embedded in a form with rows above and below it: a
// full-height list would be wrong even on a tall terminal.
const pickerWindow = 10

type picker struct {
	row       int
	heading   string
	options   []pickerOption
	cursor    int // index into matches, not into options
	top       int // first visible match; the window scrolls to follow cursor
	allowFree bool
	input     textinput.Model
	editing   bool
	// filter narrows the list incrementally. matches holds the indices of
	// options passing it, so the full catalog is never mutated and clearing
	// the filter is free.
	filter    textinput.Model
	filtering bool
	matches   []int
	err       string
	// multi turns enter into a toggle instead of a commit: a `multiple: true`
	// enum field is chosen a member at a time and the list stays open, so
	// picking three reviewers is one visit rather than three (§8.1.2, task
	// 058).
	multi bool
}

func newPicker(row int, heading string, options []pickerOption, allowFree bool, current string) *picker {
	in := textinput.New()
	in.Placeholder = "type a value"
	fl := textinput.New()
	fl.Placeholder = "filter"
	p := &picker{row: row, heading: heading, options: options, allowFree: allowFree, input: in, filter: fl}
	p.applyFilter()
	for i, idx := range p.matches {
		if options[idx].value == current {
			p.cursor = i
			break
		}
	}
	p.scrollToCursor()
	return p
}

// applyFilter recomputes matches from the filter text, case-insensitively
// over both the label and the note — a user hunting "sonnet" and one hunting
// "curated" are both searching what they can see.
func (p *picker) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	p.matches = p.matches[:0]
	for i, o := range p.options {
		if q == "" || strings.Contains(strings.ToLower(o.label), q) ||
			strings.Contains(strings.ToLower(o.note), q) {
			p.matches = append(p.matches, i)
		}
	}
	p.cursor = min(p.cursor, max(p.rowCount()-1, 0))
	p.scrollToCursor()
}

// scrollToCursor keeps the cursor inside the window, moving it the least
// distance needed.
func (p *picker) scrollToCursor() {
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+pickerWindow {
		p.top = p.cursor - pickerWindow + 1
	}
	p.top = max(p.top, 0)
}

// rowCount includes the free-text row when there is one.
func (p *picker) rowCount() int {
	if p.allowFree {
		return len(p.matches) + 1
	}
	return len(p.matches)
}

func (p *picker) onFreeRow() bool { return p.allowFree && p.cursor == len(p.matches) }

// current is the option under the cursor, if the cursor is on one.
func (p *picker) current() (pickerOption, bool) {
	if p.cursor < 0 || p.cursor >= len(p.matches) {
		return pickerOption{}, false
	}
	return p.options[p.matches[p.cursor]], true
}

// pickerResult reports what a keystroke did to the picker.
type pickerResult struct {
	value  string
	chosen bool
	closed bool
	cmd    tea.Cmd
}

// paste types into the free-text entry. The option list is keyboard-only.
func (p *picker) paste(text string) tea.Cmd {
	if !p.editing {
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(tea.PasteMsg{Content: text})
	return cmd
}

func (p *picker) update(msg tea.KeyPressMsg) pickerResult {
	if p.editing {
		switch msg.String() {
		case "enter":
			v := strings.TrimSpace(p.input.Value())
			p.editing = false
			p.input.Blur()
			if v == "" {
				return pickerResult{}
			}
			return pickerResult{value: v, chosen: true, closed: true}
		case "esc":
			p.editing = false
			p.input.Blur()
			return pickerResult{}
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return pickerResult{cmd: cmd}
	}
	if p.filtering {
		switch msg.String() {
		case "enter":
			// Accept the narrowing and go back to navigating it; the text
			// stays visible so it is obvious the list is a subset.
			p.filtering = false
			p.filter.Blur()
			return pickerResult{}
		case "esc":
			p.filtering = false
			p.filter.Blur()
			p.filter.SetValue("")
			p.applyFilter()
			return pickerResult{}
		}
		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(msg)
		p.applyFilter()
		return pickerResult{cmd: cmd}
	}
	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
			p.scrollToCursor()
		}
	case "down", "j":
		if p.cursor < p.rowCount()-1 {
			p.cursor++
			p.scrollToCursor()
		}
	case "/":
		p.filtering = true
		p.filter.Focus()
	case "e":
		if p.allowFree {
			p.startFree()
		}
	case "enter", " ", "space":
		if p.onFreeRow() {
			p.startFree()
			return pickerResult{}
		}
		opt, ok := p.current()
		if !ok {
			return pickerResult{}
		}
		if opt.disabled {
			p.err = firstNonEmpty(opt.note, "this option cannot be selected")
			return pickerResult{}
		}
		return pickerResult{value: opt.value, chosen: true, closed: !p.multi}
	case "esc":
		// A narrowed list clears back to the whole catalog first; only an
		// already-whole list closes. Otherwise escaping a typo'd filter costs
		// the whole selection.
		if p.filter.Value() != "" {
			p.filter.SetValue("")
			p.applyFilter()
			return pickerResult{}
		}
		return pickerResult{closed: true}
	}
	return pickerResult{}
}

// renderBody draws the option list, the free-text row and the picker's own
// error. Whatever explanatory lines a particular row deserves are the owning
// form's business, appended after this.
func (p *picker) renderBody() []string {
	heading := "    " + p.heading + ":"
	if q := p.filter.Value(); q != "" {
		heading += fmt.Sprintf("  (%d of %d match %q)", len(p.matches), len(p.options), q)
	}
	out := []string{styleDim.Render(heading)}
	if p.filtering {
		out = append(out, "      "+p.filter.View())
	}
	end := min(p.top+pickerWindow, len(p.matches))
	if p.top > 0 {
		out = append(out, styleDim.Render(fmt.Sprintf("    ▲ %d more", p.top)))
	}
	for i := p.top; i < end; i++ {
		opt := p.options[p.matches[i]]
		marker := "  "
		if i == p.cursor && !p.editing && !p.filtering {
			marker = styleFocus.Render("▸ ")
		}
		label := opt.label
		if label == "" {
			label = "(none)"
		}
		if p.multi {
			box := "[ ] "
			if opt.selected {
				box = "[x] "
			}
			label = box + label
		}
		if opt.disabled {
			label = styleBad.Render(label)
		}
		line := "    " + marker + label
		if opt.note != "" {
			line += "  " + styleDim.Render(opt.note)
		}
		out = append(out, line)
	}
	if rest := len(p.matches) - end; rest > 0 {
		out = append(out, styleDim.Render(fmt.Sprintf("    ▼ %d more", rest)))
	}
	if len(p.matches) == 0 && p.filter.Value() != "" {
		out = append(out, styleDim.Render("    nothing matches — esc clears the filter"))
	}
	// The free-text row survives filtering on purpose: the likeliest reason a
	// catalog has no match is that the value is newer than the catalog (§9.6).
	if p.allowFree {
		marker := "  "
		if p.onFreeRow() && !p.editing {
			marker = styleFocus.Render("▸ ")
		}
		out = append(out, "    "+marker+styleDim.Render("type a value not listed…"))
	}
	if p.editing {
		out = append(out, "      "+p.input.View())
	}
	if p.err != "" {
		out = append(out, styleBad.Render("    ⚠ "+p.err))
	}
	return out
}

func (p *picker) startFree() {
	p.editing = true
	p.filtering = false
	p.filter.Blur()
	p.cursor = len(p.matches)
	p.input.SetValue("")
	p.input.Focus()
}

// openPicker builds the list for a row and opens it.
func (n *newTask) openPicker(row ntRow) {
	switch row {
	case ntProject:
		n.pick = newPicker(int(row), "project", n.projectOptions(), false, strconv.FormatInt(n.projectID, 10))
	case ntWorkflow:
		n.pick = newPicker(int(row), "workflow", n.workflowOptions(), false, n.workflow)
	case ntIssue:
		n.pick = newPicker(int(row), "github issue", n.issueOptions(), false, n.issueValue())
	case ntAgent:
		n.pick = newPicker(int(row), "agent override", n.agentOptions(), false, n.agent)
	case ntModel:
		n.pick = newPicker(int(row), "model override", n.optionRows(n.effectiveAgentModels()), true, n.model)
	case ntEffort:
		n.pick = newPicker(int(row), "effort override", n.optionRows(n.effectiveAgentEfforts()), true, n.effort)
	case ntTitle, ntDescription, ntFields, ntBranch, ntBranchName, ntPriority, ntCreate, ntRowCount:
		return
	}
	n.mode = ntPicking
}

// openFieldPicker opens the value list for the focused declared enum row. It
// is a second entry point into the same picker the §8.6 override rows use —
// the list, the window, the filter and the scrolling are one implementation,
// so a 40-member enum scrolls exactly like cursor's model catalog does.
func (n *newTask) openFieldPicker() {
	f := n.fieldsEd
	if f == nil || f.cursor < 0 || f.cursor >= len(f.rows) {
		return
	}
	row := f.rows[f.cursor]
	if !row.declared || row.definition.Type != apiclient.WorkflowFieldEnum {
		return
	}
	current := row.value
	if row.definition.Multiple {
		current = ""
	}
	n.pick = newPicker(f.cursor, row.definition.DisplayLabel(),
		n.enumOptions(row), false, current)
	n.pick.multi = row.definition.Multiple
	n.mode = ntFieldPicking
}

// enumOptions is one row per declared member, in declared order. An optional
// single-choice field gets a leading "(unset)" row, because clearing a
// workflow-owned row is otherwise impossible: the row cannot be deleted.
func (n *newTask) enumOptions(row kv) []pickerOption {
	out := make([]pickerOption, 0, len(row.definition.Values)+1)
	if !row.definition.Required && !row.definition.Multiple {
		out = append(out, pickerOption{value: "", label: "(unset)"})
	}
	picked := enumSelection(row.value)
	for _, member := range row.definition.Values {
		opt := pickerOption{value: member, label: member, selected: slices.Contains(picked, member)}
		if member == row.definition.Default {
			opt.note = "default"
		}
		out = append(out, opt)
	}
	return out
}

// updateFieldPicking runs the enum list and hands the form back to the fields
// editor when it closes — the editor stays open underneath the whole time, so
// esc from the list returns to the row rather than to the form.
func (n *newTask) updateFieldPicking(msg tea.KeyPressMsg) tea.Cmd {
	if n.pick == nil || n.fieldsEd == nil {
		n.mode = ntFieldsOpen
		return nil
	}
	res := n.pick.update(msg)
	if res.chosen {
		n.applyFieldPick(res.value)
	}
	if res.closed {
		n.pick = nil
		n.mode = ntFieldsOpen
	}
	return res.cmd
}

// applyFieldPick writes the chosen member back onto the editor row. A
// `multiple` field toggles membership and is rewritten in **declared** order
// every time, so the row always shows the canonical string the daemon would
// store rather than click order (§8.1.2, task 058).
func (n *newTask) applyFieldPick(value string) {
	f := n.fieldsEd
	at := n.pick.row
	if at < 0 || at >= len(f.rows) {
		return
	}
	row := &f.rows[at]
	if !row.definition.Multiple {
		row.value = value
	} else {
		picked := enumSelection(row.value)
		if i := slices.Index(picked, value); i >= 0 {
			picked = append(picked[:i], picked[i+1:]...)
		} else {
			picked = append(picked, value)
		}
		row.value = enumValue(row.definition, picked)
		for i := range n.pick.options {
			n.pick.options[i].selected = slices.Contains(picked, n.pick.options[i].value)
		}
	}
	f.err = fieldValidationMessage(*row)
	n.touched = true
}

func (n *newTask) updatePicking(msg tea.KeyPressMsg) tea.Cmd {
	if n.pick == nil {
		n.mode = ntNavigating
		return nil
	}
	res := n.pick.update(msg)
	cmds := []tea.Cmd{res.cmd}
	if res.chosen {
		cmds = append(cmds, n.applyPick(ntRow(n.pick.row), res.value))
	}
	if res.closed {
		n.pick = nil
		n.mode = ntNavigating
	}
	return tea.Batch(cmds...)
}

// applyPick commits a chosen value and repairs whatever it invalidated. It
// returns a command when the choice invalidated data the form is holding.
func (n *newTask) applyPick(row ntRow, value string) tea.Cmd {
	n.touched = true
	delete(n.rowErr, row)
	switch row {
	case ntProject:
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id == n.projectID {
			return nil
		}
		for _, p := range n.projects {
			if p.ID == id {
				n.setProject(p)
			}
		}
		// The registry is project-scoped (§5.2), so the workflow list and
		// the selection made from it are both stale now. So is everything
		// GitHub: a different repository, a different answer to "is this on
		// GitHub", and an issue from the old project has no meaning here.
		n.workflows = nil
		n.setWorkflow("")
		n.github, n.githubProject = apiclient.GitHubStatus{}, 0
		n.issues, n.issuesFor, n.issuesErr, n.issue = nil, issuesKey{}, "", nil
		return tea.Batch(n.workflowsCmd(id), n.githubCmd(id))
	case ntWorkflow:
		n.setWorkflow(value)
		// Each listed issue carries the prefill computed for the workflow's
		// declared fields, so a workflow change makes those prefills stale.
		return tea.Batch(n.issuesCmd(), n.resolveCmd())
	case ntIssue:
		n.applyIssuePick(value)
		// The title and fields the prefill just wrote are §8.6 and branch
		// inputs, so the draft's resolution has changed with them.
		return n.resolveCmd()
	case ntAgent:
		if value == n.agent {
			return nil
		}
		n.agent = value
		// §8.6 agent-scoped inheritance: model and effort only inherit from a
		// level whose agent matches. Carrying a claude alias onto codex is
		// exactly the leak that rule exists to prevent, so switching agent
		// resets both rather than sending a triple the resolver would reject.
		n.model, n.effort = "", ""
	case ntModel:
		n.model = value
	case ntEffort:
		n.effort = value
	case ntTitle, ntDescription, ntFields, ntBranch, ntBranchName, ntPriority, ntCreate, ntRowCount:
		return nil
	}
	// Every row that falls through here is a §8.6 input, so what the draft
	// resolves to has just changed.
	return n.resolveCmd()
}

func (n *newTask) projectOptions() []pickerOption {
	out := make([]pickerOption, 0, len(n.projects))
	for _, p := range n.projects {
		out = append(out, pickerOption{
			value: strconv.FormatInt(p.ID, 10),
			label: p.Name,
			note:  p.Path,
		})
	}
	return out
}

func (n *newTask) workflowOptions() []pickerOption {
	out := make([]pickerOption, 0, len(n.workflows))
	for _, e := range n.workflows {
		opt := pickerOption{value: e.Name, label: e.Name, note: e.Scope}
		if !e.Valid() {
			opt.disabled = true
			opt.note = "invalid: " + e.FirstError()
		} else if !e.RunsHere() {
			// Offered but not selectable: the workflow exists and the human
			// may well be looking for it, so the row says why it is out of
			// reach rather than vanishing (§8.1.1, task 010).
			opt.disabled = true
			opt.note = "not on this platform · " + e.PlatformNote()
		} else if bad := n.unavailableSteps(e); len(bad) > 0 {
			opt.note = fmt.Sprintf("%s · ⚠ %d step(s) need an unavailable agent", e.Scope, len(bad))
		}
		out = append(out, opt)
	}
	return out
}

// agentOptions lists every registered adapter plus "leave it to the
// workflow". An unavailable adapter stays selectable — it may be installed
// before the task is admitted — but says so.
func (n *newTask) agentOptions() []pickerOption {
	out := []pickerOption{{
		value: "",
		label: "(workflow default)",
		note:  strings.TrimPrefix(n.resolvedAgents(), " → "),
	}}
	needsInput := n.workflowNeedsInputAgent()
	for _, a := range n.agents {
		opt := pickerOption{value: a.Name, label: a.Name}
		switch {
		case needsInput && a.CannotTakeInput():
			// Offered but not selectable, the way a foreign-platform workflow
			// is (task 010): the adapter exists and the human may be reaching
			// for it, so the row says why it is out of reach for *this*
			// workflow rather than vanishing (§7.4, task 013).
			opt.disabled = true
			opt.note = "cannot answer questions · this workflow requires it"
		case !a.Available:
			opt.note = "⚠ unavailable: " + firstNonEmpty(a.Error, "not found")
		case a.NotAuthenticated():
			// Installed and probing clean, but every run would fail at the
			// API — a distinct sentence from "not found" because the fix is
			// different (§9.5).
			opt.note = "⚠ installed but not logged in"
		case a.Version != "":
			opt.note = a.Version
		}
		out = append(out, opt)
	}
	return out
}

// effectiveAgent is the adapter whose catalog the model and effort pickers
// should show: the override when one is set, else the agent the selected
// workflow's first agent step resolves to. It is empty when neither says.
func (n *newTask) effectiveAgent() string {
	if n.agent != "" {
		return n.agent
	}
	e := n.workflowEntry(n.workflow)
	if e == nil {
		return ""
	}
	for _, s := range e.Steps {
		if s.Agent != "" {
			return s.Agent
		}
	}
	return ""
}

func (n *newTask) effectiveAgentModels() []apiclient.AgentOption {
	a, ok := n.agents.Find(n.effectiveAgent())
	if !ok {
		return nil
	}
	return a.Models
}

func (n *newTask) effectiveAgentEfforts() []apiclient.AgentOption {
	a, ok := n.agents.Find(n.effectiveAgent())
	if !ok {
		return nil
	}
	return a.Efforts
}

// optionRows renders a catalog with its provenance tags. A "cli" value came
// from the adapter itself and cannot be stale; a "curated" one is vincent's
// own list and may be.
func (n *newTask) optionRows(opts []apiclient.AgentOption) []pickerOption {
	out := []pickerOption{{value: "", label: "(workflow default)"}}
	for _, o := range opts {
		out = append(out, pickerOption{value: o.Value, label: o.Value, note: o.Source})
	}
	return out
}

// fieldsEditor edits the free-form key/value map (§5.3). It is a row list
// rather than a raw "k=v per line" buffer because that buffer defers every
// parse error to submit time and cannot tell an empty value from a missing
// key.
type fieldsEditor struct {
	rows   []kv
	cursor int
	// editing is 0 for none, 1 for the key, 2 for the value.
	editing int
	input   textinput.Model
	err     string
}

func newFieldsEditor(rows []kv) *fieldsEditor {
	in := textinput.New()
	return &fieldsEditor{rows: append([]kv(nil), rows...), input: in}
}

func (n *newTask) updateFields(msg tea.KeyPressMsg) tea.Cmd {
	f := n.fieldsEd
	if f == nil {
		n.mode = ntNavigating
		return nil
	}
	if f.editing != 0 {
		return f.updateEditing(msg)
	}
	switch msg.String() {
	case "up", "k":
		if f.cursor > 0 {
			f.cursor--
			f.err = ""
		}
	case "down", "j":
		if f.cursor < len(f.rows)-1 {
			f.cursor++
			f.err = ""
		}
	case "a":
		f.rows = append(f.rows, kv{})
		f.cursor = len(f.rows) - 1
		f.startEdit(1)
	case "enter", " ", "left", "right":
		if len(f.rows) == 0 {
			return nil
		}
		row := f.rows[f.cursor]
		switch {
		case row.declared && row.definition.Type == apiclient.WorkflowFieldBoolean:
			f.toggleBoolean()
		case row.declared && row.definition.Type == apiclient.WorkflowFieldEnum:
			// left/right step through the members in place the way a boolean
			// cycles, so a two- or three-value field stays a single keypress;
			// enter opens the list, which is the only workable control for a
			// long one and the only one that can toggle a `multiple` set.
			switch msg.String() {
			case "left":
				f.stepEnum(-1)
			case "right":
				f.stepEnum(1)
			default:
				n.openFieldPicker()
			}
		case msg.String() == "enter":
			f.startEdit(1)
		}
	case "d":
		if len(f.rows) > 0 {
			if f.rows[f.cursor].declared {
				f.err = "workflow-declared fields cannot be deleted"
				return nil
			}
			f.rows = append(f.rows[:f.cursor], f.rows[f.cursor+1:]...)
			if f.cursor >= len(f.rows) {
				f.cursor = max(0, len(f.rows)-1)
			}
		}
	case "esc":
		n.commitFields()
		return n.resolveCmd()
	}
	return nil
}

func (f *fieldsEditor) startEdit(which int) {
	if f.cursor < 0 || f.cursor >= len(f.rows) {
		return
	}
	if f.rows[f.cursor].declared {
		if f.rows[f.cursor].definition.Type == apiclient.WorkflowFieldBoolean {
			f.toggleBoolean()
			return
		}
		which = 2 // the workflow owns a declared field's key
	}
	f.editing = which
	f.err = ""
	if which == 1 {
		f.input.SetValue(f.rows[f.cursor].key)
	} else {
		f.input.SetValue(f.rows[f.cursor].value)
	}
	f.input.Focus()
}

// updateEditing runs the key field then the value field, so adding a pair is
// one uninterrupted "a, key, enter, value, enter".
// paste types into whichever half of the row is being edited.
func (f *fieldsEditor) paste(text string) tea.Cmd {
	if f.editing == 0 {
		return nil
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(tea.PasteMsg{Content: text})
	return cmd
}

func (f *fieldsEditor) updateEditing(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v := strings.TrimSpace(f.input.Value())
		if f.editing == 1 {
			f.rows[f.cursor].key = v
			f.startEdit(2)
			return nil
		}
		f.rows[f.cursor].value = v
		f.editing = 0
		f.input.Blur()
		f.dedupe()
		if f.err == "" && f.cursor < len(f.rows) {
			f.err = fieldValidationMessage(f.rows[f.cursor])
		}
		return nil
	case "esc":
		f.editing = 0
		f.input.Blur()
		return nil
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

// stepEnum moves a single-choice enum one member along, wrapping. An optional
// field gets an extra stop at the empty value, which is the only way back to
// "unset" without deleting a row the workflow owns; a required one has no such
// stop, because it has no legal empty value.
//
// A `multiple` field is not stepped: "the next set" has no meaning, so
// left/right open nothing and the picker is the way to change it.
func (f *fieldsEditor) stepEnum(delta int) {
	if f.cursor < 0 || f.cursor >= len(f.rows) {
		return
	}
	row := &f.rows[f.cursor]
	if row.definition.Multiple {
		return
	}
	stops := append([]string(nil), row.definition.Values...)
	if !row.definition.Required {
		stops = append(stops, "")
	}
	if len(stops) == 0 {
		return
	}
	at := slices.Index(stops, row.value)
	if at < 0 {
		// An unknown value — seeded from an issue prefill, or left behind by
		// a workflow edit — steps to the first member rather than nowhere.
		at = -1
		if delta < 0 {
			at = 1
		}
	}
	row.value = stops[((at+delta)%len(stops)+len(stops))%len(stops)]
	f.err = fieldValidationMessage(*row)
}

// enumSelection splits a stored enum value into the members it names.
func enumSelection(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, apiclient.WorkflowFieldSeparator) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// enumValue joins a selection in the workflow's **declared** order, which is
// the canonical string the daemon stores (§8.1.2, task 058). The TUI writes
// the canonical form itself rather than leaning on the daemon to rewrite it,
// so what the row shows is what the task will carry.
func enumValue(def apiclient.WorkflowField, picked []string) string {
	out := make([]string, 0, len(picked))
	for _, member := range def.Values {
		if slices.Contains(picked, member) {
			out = append(out, member)
		}
	}
	return strings.Join(out, apiclient.WorkflowFieldSeparator)
}

func (f *fieldsEditor) toggleBoolean() {
	if f.cursor < 0 || f.cursor >= len(f.rows) {
		return
	}
	switch f.rows[f.cursor].value {
	case "true":
		f.rows[f.cursor].value = "false"
	case "false":
		if f.rows[f.cursor].definition.Required {
			f.rows[f.cursor].value = "true"
		} else {
			f.rows[f.cursor].value = ""
		}
	default:
		f.rows[f.cursor].value = "true"
	}
	f.err = ""
}

// fieldValidationMessage mirrors the daemon's pure value checks so the TUI
// can attach immediate feedback to the Fields row. POST /v1/tasks remains the
// authoritative gate for every client and for workflow reload races.
func fieldValidationMessage(field kv) string {
	if !field.declared {
		return ""
	}
	if field.value == "" {
		if field.definition.Required {
			return fmt.Sprintf("%s is required", field.definition.DisplayLabel())
		}
		return ""
	}
	switch field.definition.Type {
	case apiclient.WorkflowFieldInteger:
		if _, err := strconv.ParseInt(field.value, 10, 64); err != nil {
			return fmt.Sprintf("%s must be a base-10 integer", field.definition.DisplayLabel())
		}
	case apiclient.WorkflowFieldNumber:
		value, err := strconv.ParseFloat(field.value, 64)
		if !taskFieldDecimalNumber.MatchString(field.value) || err != nil ||
			math.IsInf(value, 0) || math.IsNaN(value) {
			return fmt.Sprintf("%s must be a finite decimal number", field.definition.DisplayLabel())
		}
	case apiclient.WorkflowFieldBoolean:
		if field.value != "true" && field.value != "false" {
			return fmt.Sprintf("%s must be true or false", field.definition.DisplayLabel())
		}
	case apiclient.WorkflowFieldEnum:
		for _, member := range enumSelection(field.value) {
			if !slices.Contains(field.definition.Values, member) {
				return fmt.Sprintf("%s must be one of %s; got %q",
					field.definition.DisplayLabel(),
					strings.Join(field.definition.Values, ", "), member)
			}
		}
	case apiclient.WorkflowFieldString:
		if field.definition.Pattern != "" {
			pattern, err := regexp.Compile(field.definition.Pattern)
			if err != nil || !pattern.MatchString(field.value) {
				return fmt.Sprintf("%s must match %s",
					field.definition.DisplayLabel(), field.definition.Pattern)
			}
		}
	}
	return ""
}

func (n *newTask) firstFieldError() string {
	for _, field := range n.fields {
		if message := fieldValidationMessage(field); message != "" {
			return message
		}
	}
	return ""
}

// dedupe collapses a repeated key onto the row that already holds it, and
// says so. The wire format is a map, so two rows with one key would silently
// drop one of them at submit time.
func (f *fieldsEditor) dedupe() {
	seen := map[string]int{}
	out := make([]kv, 0, len(f.rows))
	for _, r := range f.rows {
		if r.key == "" {
			out = append(out, r)
			continue
		}
		if at, dup := seen[r.key]; dup {
			out[at].value = r.value
			f.err = fmt.Sprintf("%q was already set; the later value wins", r.key)
			continue
		}
		seen[r.key] = len(out)
		out = append(out, r)
	}
	f.rows = out
	if f.cursor >= len(f.rows) {
		f.cursor = max(0, len(f.rows)-1)
	}
}

// commitFields copies the editor's rows back onto the draft, dropping the
// blank ones an abandoned "a" left behind.
func (n *newTask) commitFields() {
	f := n.fieldsEd
	if f == nil {
		return
	}
	out := make([]kv, 0, len(f.rows))
	for _, r := range f.rows {
		if r.key != "" {
			out = append(out, r)
		}
	}
	n.fields = out
	n.fieldsEd = nil
	n.mode = ntNavigating
	n.touched = true
}
