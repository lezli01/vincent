package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
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

	editor  textarea.Model
	editing bool

	err        string
	submitting bool
	// dirty is any answer chosen or typed since the form opened (task 132.6).
	dirty bool
	// noDetails drops `ctrl+t task details` from the key line: only the
	// task workspace answers it, and in a chat there is no task to read
	// (issue #801 F7).
	noDetails bool
}

func newAnswerForm(req apiclient.InputRequest) *answerForm {
	f := &answerForm{
		req:     req,
		answers: map[string][]string{},
		editor:  newAnswerEditor(),
	}
	f.buildRows()
	f.cursor = f.nextSelectable(-1, 1)
	return f
}

// editorLines is how tall the free-text field may grow before it scrolls. An
// answer longer than that is rare enough that a scrolling field is the right
// trade against a popup that eats the tail underneath it.
const editorLines = 6

// newAnswerEditor is the free-text field: a wrapping field that grows with
// what is typed, not a single line. A single line can only slide its window
// along with the cursor, so an answer wider than the popup could never be read
// back in full before submitting it (§7.4 free-text entry).
func newAnswerEditor() textarea.Model {
	ed := textarea.New()
	ed.Placeholder = "type an answer"
	// No prompt, no line numbers: this is one answer, not a source file, and
	// both would eat columns the answer itself needs.
	ed.Prompt = ""
	ed.ShowLineNumbers = false
	ed.DynamicHeight = true
	ed.MinHeight = 1
	ed.MaxHeight = editorLines
	ed.MaxContentHeight = editorContentCap
	ed.SetHeight(1)
	// A width until the first render sizes it; keys can arrive before then.
	ed.SetWidth(40)
	return ed
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
	defer f.watch()()
	if !f.editing {
		return nil
	}
	var cmd tea.Cmd
	f.editor, cmd = f.editor.Update(tea.PasteMsg{Content: text})
	return cmd
}

// update handles one key. exit=true asks the caller to leave the form.
func (f *answerForm) update(msg tea.KeyPressMsg, client *apiclient.Client, taskID int64) (cmd tea.Cmd, exit bool) {
	return f.updateWith(msg, func(resp apiclient.InputResponse) tea.Cmd {
		return f.submitTask(resp, client, taskID)
	})
}

// updateWith is the form's keyboard over an arbitrary submitter. It exists so
// a chat's answer popup is *this* popup rather than a fork of it: structured
// options, multi-select and the permission allow/deny distinction are the same
// because the request is the same request, and only where the answer is POSTed
// differs (task 063 decision 8, task 067).
func (f *answerForm) updateWith(msg tea.KeyPressMsg, submit func(apiclient.InputResponse) tea.Cmd) (cmd tea.Cmd, exit bool) {
	defer f.watch()()
	if f.editing {
		switch msg.String() {
		case "enter":
			f.commitFreeText()
			return nil, false
		case "esc":
			f.editing = false
			f.editor.Blur()
			return nil, false
		}
		var c tea.Cmd
		f.editor, c = f.editor.Update(msg)
		return c, false
	}

	switch msg.String() {
	case "up", "k":
		f.cursor = f.nextSelectable(f.cursor, -1)
	case "down", "j":
		f.cursor = f.nextSelectable(f.cursor, 1)
	case " ", "space":
		f.pick()
	case opKey(keymap.FreeText):
		f.startFreeText()
	case "enter":
		return f.submitTo(submit), false
	case "esc":
		return nil, true
	}
	return nil, false
}

// submitTo validates locally, then hands the response to the caller's poster.
// Local validation is a convenience and never the authority — the daemon
// checks the same rules — but a form that can say "answer the second
// question" without a round trip should.
func (f *answerForm) submitTo(post func(apiclient.InputResponse) tea.Cmd) tea.Cmd {
	if f.submitting {
		// One answer in flight at a time: its reply clears this either way.
		return nil
	}
	resp := f.response()
	if err := f.req.Validate(resp); err != nil {
		f.err = errString(err)
		return nil
	}
	f.err = ""
	f.submitting = true
	return post(resp)
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
	f.editor.SetValue("")
	f.editor.Focus()
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
	f.editor.Blur()
	value := strings.TrimSpace(f.editor.Value())
	f.editor.SetValue("")
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

// submitTask posts the answer to a task. It is the submitter the task path
// hands updateWith.
func (f *answerForm) submitTask(resp apiclient.InputResponse, client *apiclient.Client, taskID int64) tea.Cmd {
	if client == nil {
		f.err = "not connected"
		f.submitting = false
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		task, err := client.Answer(ctx, taskID, resp)
		return actionResultMsg{taskID: taskID, action: apiclient.ActionAnswer, task: task, err: err}
	}
}

// height is how many lines the form wants at the width it will be drawn at,
// capped by the caller. Rows wrap and the free-text field grows with what is
// typed, so this cannot be counted from the number of rows alone.
func (f *answerForm) height(width int) int {
	body, _, _ := f.layout(width)
	return len(body) + len(f.statusLines(width))
}

// render draws the form, windowed around the cursor when it does not fit —
// a question that scrolls out of reach is worse than one that scrolls.
//
// The status line is pinned under the window rather than windowed with the
// rows (issue #800 W6): it is the line that says how to submit or leave, and
// as the last content line it was only reachable by scrolling to the bottom
// of a long question. The chat composer pins it the same way (#801 F7).
func (f *answerForm) render(width, height int) string {
	body, from, to := f.layout(width)
	status := f.statusLines(width)
	if height <= 0 || len(body)+len(status) <= height {
		return strings.Join(append(body, status...), "\n")
	}
	room := height - len(status)
	if room < 1 {
		// Too short for both: the focused row is the one that cannot go.
		return strings.Join(windowRange(body, from, to, height), "\n")
	}
	from = f.anchorFrom(width, from, to, room)
	return strings.Join(append(windowWithCues(body, from, to, room), status...), "\n")
}

// anchorFrom widens the focused range up to the first line of the focused
// row's question while the whole of that still fits in room (issue #800 W6).
// A long option list otherwise scrolled the question itself off the top,
// leaving a column of radios with nothing saying what they answer.
func (f *answerForm) anchorFrom(width, from, to, room int) int {
	row, ok := f.currentRow()
	if !ok || f.req.Kind == apiclient.InputKindPermission {
		return from
	}
	line := 0
	for i, r := range f.rows {
		if r.kind == rowHeader && r.question == row.question {
			// The window gives a row to "↓ more" — it is being windowed, so
			// something is cut — and another to "↑ more" when the header is
			// not the form's first line (review F2). Anchoring on the room
			// before those cues pushed the focused row off the bottom.
			cues := 1
			if line > 0 {
				cues++
			}
			if to-line <= room-cues {
				return min(line, from)
			}
			return from
		}
		line += len(f.renderRow(i, r, width))
	}
	return from
}

// windowWithCues is windowRange giving up a row at each cut edge to a dim
// "↑ more" / "↓ more", so a window over a longer form says there is more of
// it rather than reading as the whole question.
func windowWithCues(lines []string, from, to, height int) []string {
	n := len(lines)
	if height <= 0 || n <= height {
		return lines
	}
	for h := height - 1; h >= 1; h-- {
		start := windowStart(n, from, h)
		if to > start+h {
			start = min(to-h, from)
		}
		start = max(min(start, n-h), 0)
		up, down := start > 0, start+h < n
		cues := 0
		if up {
			cues++
		}
		if down {
			cues++
		}
		if h+cues > height {
			continue
		}
		out := make([]string, 0, height)
		if up {
			out = append(out, styleDim.Render("  ↑ more"))
		}
		out = append(out, lines[start:start+h]...)
		if down {
			out = append(out, styleDim.Render("  ↓ more"))
		}
		return out
	}
	return windowRange(lines, from, to, height)
}

// lines is the whole form at width — the rows, the open free-text field, and
// the status line — with the range the focused element occupies. The status
// is the last element and exactly one line: the chat composer pins that line
// under its own window (#801 F7) and truncates rather than wraps it.
func (f *answerForm) lines(width int) (lines []string, from, to int) {
	lines, from, to = f.layout(width)
	style, text := f.status()
	return append(lines, style.Render("  "+text)), from, to
}

// statusLines is the status line wrapped to width, for the task popup, which
// has the room to say all of it (issue #800 W7).
func (f *answerForm) statusLines(width int) []string {
	style, text := f.status()
	return proseLines(style, text, width)
}

// status is the form's one status sentence and its style: an error, what the
// keys mean while the field is open, or the form's keys.
func (f *answerForm) status() (lipgloss.Style, string) {
	switch {
	case f.err != "":
		return styleBad, "⚠ " + f.err
	case f.editing:
		// enter and esc mean something else while the field is open, and a
		// hint that says "enter submit" over an open field is a lost answer.
		return styleDim, "enter keeps this answer · esc discards it"
	case f.submitting:
		return styleDim, "submitting…"
	}
	details := " · ctrl+t task details"
	if f.noDetails {
		details = ""
	}
	return styleDim, "space select · " + opKey(keymap.FreeText) + " type an answer · enter submit" + details + " · esc leave the form"
}

// proseLines wraps one sentence of popup chrome — a subject, a hint, an error
// — at width under the forms' two-column indent, instead of handing frame a
// line it truncates (issue #800 W7). The sentences these popups carry end in
// the clause that matters ("the task returns to done afterwards"), which is
// exactly the part a cut loses.
func proseLines(style lipgloss.Style, text string, width int) []string {
	out := wrapText(text, width, wrapOpts{prefix: "  ", indent: 2})
	for i := range out {
		out[i] = style.Render(out[i])
	}
	return out
}

// editorContentCap is every popup editor's MaxContentHeight, so that its
// MaxHeight is only a display cap. bubbles' textarea refuses to insert a
// newline once the value has MaxHeight logical lines unless MaxContentHeight
// is set, which then is the limit instead (see [textPane]'s doc); the seventh
// enter in a six-row field, which these forms document as a newline, was
// silently dropped (issue #800 W3). A limit nobody types to is no limit,
// while MaxHeight still keeps the popup at editorLines rows — the field
// scrolls inside them.
const editorContentCap = 1 << 20

// editorView sizes the free-text field to the space the popup actually has
// and returns its lines, indented clear of the cursor gutter. Sizing it here
// rather than on resize is what keeps the width the field wraps at and the
// width the popup is drawn at the same number.
func (f *answerForm) editorView(width int) []string {
	f.editor.SetWidth(max(width-editorIndent, 8))
	out := strings.Split(f.editor.View(), "\n")
	pad := strings.Repeat(" ", editorIndent)
	for i := range out {
		out[i] = pad + out[i]
	}
	return out
}

// editorIndent clears the cursor gutter the rows are drawn in, so the field
// reads as part of the question above it rather than as a new column.
const editorIndent = 4

// layout wraps every row to width and reports the line range the focused row
// occupies, so windowing can keep the whole of it on screen. Rows stay the
// unit the cursor moves in; only their mapping to lines is many-to-one.
//
// An open free-text field is drawn under the row it belongs to, not at the
// bottom of the form: which question is being answered is the one thing the
// field itself cannot say.
func (f *answerForm) layout(width int) (lines []string, from, to int) {
	lines = make([]string, 0, len(f.rows)+2)
	for i, row := range f.rows {
		focused := i == f.cursor && row.kind != rowHeader
		if focused {
			from = len(lines)
		}
		lines = append(lines, f.renderRow(i, row, width)...)
		if focused && f.editing {
			lines = append(lines, f.editorView(width)...)
		}
		if focused {
			to = len(lines)
		}
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
		out := wrapText(row.text, width-cols(headerPrefix), wrapOpts{})
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
	text, style := f.rowBody(row)
	out := wrapText(text, width-indent, wrapOpts{})
	for j := range out {
		if j == 0 {
			out[j] = prefix + style.Render(out[j])
			continue
		}
		out[j] = strings.Repeat(" ", indent) + style.Render(out[j])
	}
	return out
}

// rowBody is what a row says, and how. A free-text row that holds an answer
// says the answer: it is text of a length nobody chose, so it belongs in the
// part of the row that wraps rather than in the fixed-width marker, where it
// would push the label off the popup and lose its own tail to truncation.
func (f *answerForm) rowBody(row formRow) (string, lipgloss.Style) {
	if row.kind == rowFree {
		if typed := f.typedAnswers(f.req.Questions[row.question]); typed != "" {
			return typed, styleOK
		}
	}
	return row.text, stylePlain
}

// stylePlain leaves a line exactly as it was wrapped.
var stylePlain = lipgloss.NewStyle()

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
		if f.typedAnswers(q) != "" {
			// The answer itself is the row's body (see rowBody), so the marker
			// stays as wide as a radio however long the answer is — which is
			// also what keeps the labels of one question in a single column.
			return styleOK.Render(" ✎ ")
		}
		return styleDim.Render(" ✎ ")
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
