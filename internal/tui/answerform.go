package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// formRowKind is what one line of the form does when the cursor is on it.
type formRowKind int

const (
	rowHeader formRowKind = iota // a question's text; never selectable
	rowOption                    // one suggested option
	rowFree                      // "type an answer" for this question
	rowAllow                     // permission: allow
	rowDeny                      // permission: deny
)

type formRow struct {
	kind     formRowKind
	question int
	option   int
	text     string
}

// answerForm renders a pending §7.4 request and submits the answer. Options
// are suggestions, never an enum, so every question also offers free text.
type answerForm struct {
	req     apiclient.InputRequest
	rows    []formRow
	cursor  int
	answers map[string][]string
	allow   *bool

	input   textinput.Model
	editing bool

	err        string
	submitting bool
}

func newAnswerForm(req apiclient.InputRequest) *answerForm {
	in := textinput.New()
	in.Placeholder = "type an answer"
	f := &answerForm{
		req:     req,
		answers: map[string][]string{},
		input:   in,
	}
	f.buildRows()
	f.cursor = f.nextSelectable(-1, 1)
	return f
}

func (f *answerForm) buildRows() {
	if f.req.Kind == apiclient.InputKindPermission {
		summary := "permission requested"
		if f.req.Permission != nil {
			summary = fmt.Sprintf("%s wants to run: %s", f.req.Permission.Tool, f.req.Permission.Summary)
		}
		f.rows = []formRow{
			{kind: rowHeader, text: summary},
			{kind: rowAllow, text: "allow"},
			{kind: rowDeny, text: "deny"},
		}
		return
	}
	for qi, q := range f.req.Questions {
		header := q.Text
		if q.Header != "" {
			header = q.Header + ": " + q.Text
		}
		if q.MultiSelect {
			header += " (choose one or more)"
		}
		f.rows = append(f.rows, formRow{kind: rowHeader, question: qi, text: header})
		for oi, opt := range q.Options {
			f.rows = append(f.rows, formRow{kind: rowOption, question: qi, option: oi, text: opt})
		}
		f.rows = append(f.rows, formRow{kind: rowFree, question: qi, text: "type your own answer"})
	}
}

// sameRequest reports whether a refetched request is the one on screen, so a
// refresh does not throw away half-typed answers.
func (f *answerForm) sameRequest(req apiclient.InputRequest) bool {
	if f.req.Kind != req.Kind || len(f.req.Questions) != len(req.Questions) {
		return false
	}
	for i := range req.Questions {
		if f.req.Questions[i].Text != req.Questions[i].Text {
			return false
		}
	}
	return true
}

// capturing reports that the free-text field owns the keyboard.
func (f *answerForm) capturing() bool { return f.editing }

// paste types into the free-text answer. Option rows are keyboard-only —
// pasting onto one would be a paste with no field under it.
func (f *answerForm) paste(text string) tea.Cmd {
	if !f.editing {
		return nil
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(tea.PasteMsg{Content: text})
	return cmd
}

// update handles one key. exit=true asks the caller to leave the form.
func (f *answerForm) update(msg tea.KeyPressMsg, client *apiclient.Client, taskID int64) (cmd tea.Cmd, exit bool) {
	if f.editing {
		switch msg.String() {
		case "enter":
			f.commitFreeText()
			return nil, false
		case "esc":
			f.editing = false
			f.input.Blur()
			return nil, false
		}
		var c tea.Cmd
		f.input, c = f.input.Update(msg)
		return c, false
	}

	switch msg.String() {
	case "up", "k":
		f.cursor = f.nextSelectable(f.cursor, -1)
	case "down", "j":
		f.cursor = f.nextSelectable(f.cursor, 1)
	case " ", "space":
		f.pick()
	case "e":
		f.startFreeText()
	case "enter":
		return f.submit(client, taskID), false
	case "esc":
		return nil, true
	}
	return nil, false
}

// nextSelectable walks past header lines in the given direction, staying put
// at the ends rather than wrapping.
func (f *answerForm) nextSelectable(from, delta int) int {
	i := from + delta
	for i >= 0 && i < len(f.rows) {
		if f.rows[i].kind != rowHeader {
			return i
		}
		i += delta
	}
	if from >= 0 && from < len(f.rows) && f.rows[from].kind != rowHeader {
		return from
	}
	// Nothing selectable in that direction: find the first selectable row.
	for j, r := range f.rows {
		if r.kind != rowHeader {
			return j
		}
	}
	return 0
}

func (f *answerForm) currentRow() (formRow, bool) {
	if f.cursor < 0 || f.cursor >= len(f.rows) {
		return formRow{}, false
	}
	return f.rows[f.cursor], true
}

// pick selects the row under the cursor: a single-select replaces its answer,
// a multi-select toggles, a permission decides.
func (f *answerForm) pick() {
	row, ok := f.currentRow()
	if !ok {
		return
	}
	f.err = ""
	switch row.kind {
	case rowAllow:
		yes := true
		f.allow = &yes
	case rowDeny:
		no := false
		f.allow = &no
	case rowOption:
		q := f.req.Questions[row.question]
		f.toggle(q, q.Options[row.option])
	case rowFree:
		f.startFreeText()
	case rowHeader:
	}
}

func (f *answerForm) toggle(q apiclient.InputQuestion, value string) {
	if !q.MultiSelect {
		f.answers[q.Text] = []string{value}
		return
	}
	current := f.answers[q.Text]
	for i, v := range current {
		if v == value {
			f.answers[q.Text] = append(current[:i:i], current[i+1:]...)
			return
		}
	}
	f.answers[q.Text] = append(current, value)
}

func (f *answerForm) startFreeText() {
	row, ok := f.currentRow()
	if !ok || f.req.Kind == apiclient.InputKindPermission {
		return
	}
	f.editing = true
	f.input.SetValue("")
	f.input.Focus()
	f.cursor = f.freeRowFor(row.question)
}

// freeRowFor is the "type your own answer" row of a question, so opening the
// editor from an option row moves the cursor somewhere that explains itself.
func (f *answerForm) freeRowFor(question int) int {
	for i, r := range f.rows {
		if r.kind == rowFree && r.question == question {
			return i
		}
	}
	return f.cursor
}

func (f *answerForm) commitFreeText() {
	f.editing = false
	f.input.Blur()
	value := strings.TrimSpace(f.input.Value())
	f.input.SetValue("")
	row, ok := f.currentRow()
	if !ok || value == "" {
		return
	}
	q := f.req.Questions[row.question]
	if q.MultiSelect {
		f.answers[q.Text] = append(f.answers[q.Text], value)
		return
	}
	f.answers[q.Text] = []string{value}
}

// response is the answer as it would be submitted.
func (f *answerForm) response() apiclient.InputResponse {
	return apiclient.InputResponse{Answers: f.answers, Allow: f.allow}
}

// submit validates locally first — the daemon validates too and stays the
// authority, but a form that can say "answer the second question" without a
// round trip should.
func (f *answerForm) submit(client *apiclient.Client, taskID int64) tea.Cmd {
	resp := f.response()
	if err := f.req.Validate(resp); err != nil {
		f.err = errString(err)
		return nil
	}
	if client == nil {
		f.err = "not connected"
		return nil
	}
	f.err = ""
	f.submitting = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		task, err := client.Answer(ctx, taskID, resp)
		return actionResultMsg{taskID: taskID, action: apiclient.ActionAnswer, task: task, err: err}
	}
}

// height is how many lines the form wants at the width it will be drawn at,
// capped by the caller. Rows wrap, so this cannot be counted from the number
// of rows alone.
func (f *answerForm) height(width int) int {
	lines, _, _ := f.layout(width)
	return len(lines) + 2
}

// render draws the form, windowed around the cursor when it does not fit —
// a question that scrolls out of reach is worse than one that scrolls.
func (f *answerForm) render(width, height int) string {
	lines, from, to := f.layout(width)
	if f.editing {
		lines = append(lines, "  "+f.input.View())
		from, to = len(lines)-1, len(lines)
	}
	switch {
	case f.err != "":
		lines = append(lines, styleBad.Render("  ⚠ "+f.err))
	case f.submitting:
		lines = append(lines, styleDim.Render("  submitting…"))
	default:
		lines = append(lines, styleDim.Render(
			"  space select · e type an answer · enter submit · esc leave the form"))
	}
	return strings.Join(windowRange(lines, from, to, height), "\n")
}

// layout wraps every row to width and reports the line range the focused row
// occupies, so windowing can keep the whole of it on screen. Rows stay the
// unit the cursor moves in; only their mapping to lines is many-to-one.
func (f *answerForm) layout(width int) (lines []string, from, to int) {
	lines = make([]string, 0, len(f.rows)+2)
	for i, row := range f.rows {
		rendered := f.renderRow(i, row, width)
		if i == f.cursor && row.kind != rowHeader {
			from, to = len(lines), len(lines)+len(rendered)
		}
		lines = append(lines, rendered...)
	}
	return lines, from, to
}

// headerPrefix is the question marker; its width is also the hanging indent
// a wrapped question continues under.
const headerPrefix = "  ? "

// renderRow lays one row out across width, wrapping rather than letting the
// tail run into frame's truncation (§15 wraps the output pane for the same
// reason). Continuation lines are indented to the row's marker column, so a
// wrapped option still reads as one option.
func (f *answerForm) renderRow(i int, row formRow, width int) []string {
	if row.kind == rowHeader {
		out := wrapPlain(row.text, width-cols(headerPrefix))
		for j := range out {
			prefix := headerPrefix
			if j > 0 {
				prefix = strings.Repeat(" ", cols(headerPrefix))
			}
			out[j] = styleAsk.Render(prefix + out[j])
		}
		return out
	}
	cursor := "  "
	if i == f.cursor && !f.editing {
		cursor = styleFocus.Render("› ")
	}
	prefix := cursor + "  " + f.marker(row) + " "
	// The prefix is already styled, so its width is measured, not counted.
	indent := ansi.StringWidth(prefix)
	out := wrapPlain(row.text, width-indent)
	for j := range out {
		if j == 0 {
			out[j] = prefix + out[j]
			continue
		}
		out[j] = strings.Repeat(" ", indent) + out[j]
	}
	return out
}

// wrapPlain lays plain text out in lines of at most width columns, splitting
// words the way the §15 output pane does so an over-long one is hard-split
// rather than left to overflow. The text is plain and the caller styles the
// lines it gets back: a break can then never land inside an escape sequence.
func wrapPlain(text string, width int) []string {
	if width < 8 {
		// Too narrow to lay anything out; frame's truncation is what is left
		// at this width, and it is still a better line than one column.
		width = 8
	}
	var out []string
	var cur strings.Builder
	col := 0
	pendingSpace := false
	for _, tok := range splitWords(text, width) {
		if tok == " " {
			// Held rather than written: a separator emitted before a word that
			// turns out not to fit leaves the line ending in whitespace.
			pendingSpace = col > 0
			continue
		}
		need := cols(tok)
		if pendingSpace {
			need++
		}
		if col+need > width && col > 0 {
			out = append(out, cur.String())
			cur.Reset()
			col = 0
			pendingSpace = false
			need = cols(tok)
		}
		if pendingSpace {
			cur.WriteString(" ")
			pendingSpace = false
		}
		cur.WriteString(tok)
		col += need
	}
	if col > 0 || len(out) == 0 {
		out = append(out, cur.String())
	}
	return out
}

// marker shows what is selected: a checkbox for multi-select, a radio for
// single-select and for the permission decision.
func (f *answerForm) marker(row formRow) string {
	switch row.kind {
	case rowAllow:
		return radio(f.allow != nil && *f.allow)
	case rowDeny:
		return radio(f.allow != nil && !*f.allow)
	case rowOption:
		q := f.req.Questions[row.question]
		on := contains(f.answers[q.Text], q.Options[row.option])
		if q.MultiSelect {
			return checkbox(on)
		}
		return radio(on)
	case rowFree:
		q := f.req.Questions[row.question]
		if typed := f.typedAnswers(q); typed != "" {
			return styleOK.Render("✎ " + typed)
		}
		return styleDim.Render("✎")
	case rowHeader:
	}
	return " "
}

// typedAnswers renders the answers to this question that are not one of its
// options — the free text a human entered, echoed so it is visibly held.
func (f *answerForm) typedAnswers(q apiclient.InputQuestion) string {
	var typed []string
	for _, v := range f.answers[q.Text] {
		if !contains(q.Options, v) {
			typed = append(typed, v)
		}
	}
	return strings.Join(typed, ", ")
}

func radio(on bool) string {
	if on {
		return styleOK.Render("(•)")
	}
	return "( )"
}

func checkbox(on bool) string {
	if on {
		return styleOK.Render("[x]")
	}
	return "[ ]"
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
