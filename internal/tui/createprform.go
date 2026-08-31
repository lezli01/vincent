package tui

import (
	"net/url"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// The pull-request form (task 052.6 decision 4, task 069).
//
// `compare_url` arrives from the daemon with a title and a body already
// encoded as query parameters — a guess of the same kind task 035 decision 2
// already sanctions for task creation. 052 decision 4 requires that guess be
// visible and editable *before* it is used, so it lands in the form's own
// rows and the URL is re-encoded here on the way out.
//
// Handing GitHub's own page the unedited URL was considered and rejected: it
// satisfies the letter of "editable before submit" only by relying on a
// surface vincent does not own.
//
// **The form's primary action is now a daemon call** (task 069). ctrl+s posts
// the edited title, body and draft flag to
// POST /v1/tasks/{id}/github/pull/create, which pushes the branch and opens
// the pull request; ctrl+o is the browser hand-off, and it is also what the
// form falls back to when the daemon answers that it could not write.
//
// What has not changed is that **this client never talks to GitHub**. It
// parses a URL, edits three values and either hands them to the daemon or
// re-encodes them into a URL for a browser. Every request to GitHub is the
// daemon's, per the ownership invariant.

// cprRow is one line of the form.
type cprRow int

const (
	cprTitle cprRow = iota
	cprBody
	cprDraft
	cprRowCount
)

// createPREditMsg carries the text an $EDITOR session left behind.
type createPREditMsg struct {
	taskID int64
	text   string
	err    error
}

type createPRForm struct {
	taskID int64
	// base is the compare URL as served, kept whole: the path carries the
	// base and head refs, and re-deriving it from parts would be a second
	// implementation of a URL the daemon already built.
	base    *url.URL
	branch  string
	title   string
	body    string
	draft   bool
	cursor  cprRow
	editor  textarea.Model
	editing bool
	// sending is set from the moment ctrl+s posts until the daemon answers.
	// It is what refuses a double submission at the client (task 069
	// decision 7); the daemon refuses it too — a second call sees a live link
	// and is 409'd — and GitHub refuses a third for the same head and base.
	sending bool
	// submit posts the edited values to the daemon. Injected by the task
	// view, which owns the API client, for the reason openEditor is.
	submit func(title, body string, draft bool) tea.Cmd

	// openEditor hands the focused row's text to $EDITOR. Injected by the
	// task view, which owns the exec path, so the form needs no terminal.
	openEditor func(text string) tea.Cmd

	err string
}

// newCreatePRForm decodes the daemon's prefill into editable rows. A compare
// URL that will not parse is refused here rather than opened: a browser given
// a malformed URL fails in a place vincent cannot explain.
func newCreatePRForm(taskID int64, compareURL, branch string) (*createPRForm, error) {
	u, err := url.Parse(strings.TrimSpace(compareURL))
	if err != nil {
		return nil, err
	}
	ed := textarea.New()
	ed.Prompt = ""
	ed.ShowLineNumbers = false
	ed.DynamicHeight = true
	ed.MinHeight = 1
	ed.MaxHeight = editorLines
	ed.SetHeight(3)
	ed.SetWidth(40)
	q := u.Query()
	return &createPRForm{
		taskID: taskID,
		base:   u,
		branch: branch,
		title:  q.Get("title"),
		body:   q.Get("body"),
		editor: ed,
	}, nil
}

// url rebuilds the compare URL from the edited rows. Every other query
// parameter the daemon set — `expand=1`, which is what makes GitHub open the
// form rather than the diff — is preserved.
func (f *createPRForm) url() string {
	out := *f.base
	q := out.Query()
	setOrDelete(q, "title", f.title)
	setOrDelete(q, "body", f.body)
	out.RawQuery = q.Encode()
	return out.String()
}

func setOrDelete(q url.Values, key, value string) {
	if strings.TrimSpace(value) == "" {
		q.Del(key)
		return
	}
	q.Set(key, value)
}

func (f *createPRForm) paste(text string) tea.Cmd {
	if !f.editing {
		return nil
	}
	var cmd tea.Cmd
	f.editor, cmd = f.editor.Update(tea.PasteMsg{Content: text})
	return cmd
}

// applyEdit installs what an $EDITOR session produced.
func (f *createPRForm) applyEdit(msg createPREditMsg) {
	if msg.taskID != f.taskID {
		return
	}
	if msg.err != nil {
		f.err = errString(msg.err)
		return
	}
	f.setRow(f.cursor, strings.TrimRight(msg.text, "\n"))
	f.err = ""
}

// update handles one key. exit asks the caller to close the form; cmd is the
// browser hand-off when ctrl+s was pressed.
func (f *createPRForm) update(msg tea.KeyPressMsg) (cmd tea.Cmd, exit bool) {
	if f.editing {
		switch msg.String() {
		case "esc":
			// esc discards, enter is a newline: a pull-request body is prose
			// and wants more than one line, so the field cannot spend enter
			// on committing. ctrl+s is what leaves it.
			f.editing = false
			f.editor.Blur()
			return nil, false
		case "ctrl+s":
			f.commit()
			return nil, false
		}
		var c tea.Cmd
		f.editor, c = f.editor.Update(msg)
		return c, false
	}

	switch msg.String() {
	case "up", "k":
		f.cursor = max(f.cursor-1, 0)
	case "down", "j":
		f.cursor = min(f.cursor+1, cprRowCount-1)
	case "enter":
		if f.cursor == cprDraft {
			f.draft = !f.draft
			f.err = ""
			return nil, false
		}
		f.startEdit(f.cursor)
	case "e":
		if f.cursor == cprDraft {
			return nil, false
		}
		if f.openEditor != nil {
			return f.openEditor(f.rowValue(f.cursor)), false
		}
		f.startEdit(f.cursor)
	case " ", "space":
		// The draft toggle is space on its own row rather than a key of its
		// own: it is one of the three values this form carries, and a
		// separate global key would be a fourth thing to remember for a
		// field that is right there under the cursor.
		if f.cursor == cprDraft {
			f.draft = !f.draft
			f.err = ""
		}
	case "ctrl+s":
		return f.send(), false
	case "ctrl+o":
		return f.open(), true
	case "esc":
		return nil, true
	}
	return nil, false
}

// send hands the edited values to the daemon: the branch is pushed and the
// pull request opened without leaving vincent (task 069). It does not close
// the form — the answer comes back as a message, and a form that vanished
// before it arrived would have nowhere to report a failure.
func (f *createPRForm) send() tea.Cmd {
	if f.sending {
		return nil
	}
	if strings.TrimSpace(f.title) == "" {
		f.err = "give the pull request a title first"
		return nil
	}
	if f.submit == nil {
		// No client wired. The browser hand-off is still there and is
		// exactly what this form did before task 069.
		return f.open()
	}
	f.sending, f.err = true, ""
	return f.submit(f.title, f.body, f.draft)
}

// failed reports what the daemon said and re-arms the form, so a human can
// fix a title GitHub refused and press ctrl+s again.
func (f *createPRForm) failed(msg string) {
	f.sending = false
	f.err = msg
}

// open hands the re-encoded URL to a browser — ctrl+o, and the fallback the
// daemon's own answer sends a client to when it could not write. The title is
// required because GitHub's form is unusable without one and finding that out
// in a browser tab is a round trip wasted.
func (f *createPRForm) open() tea.Cmd {
	if strings.TrimSpace(f.title) == "" {
		f.err = "give the pull request a title first"
		return nil
	}
	return openURLCmd(f.url())
}

func (f *createPRForm) startEdit(row cprRow) {
	f.editing = true
	f.cursor = row
	f.editor.SetValue(f.rowValue(row))
	f.editor.Focus()
}

func (f *createPRForm) commit() {
	f.editing = false
	f.editor.Blur()
	f.setRow(f.cursor, f.editor.Value())
	f.err = ""
}

func (f *createPRForm) rowValue(row cprRow) string {
	switch row {
	case cprBody:
		return f.body
	case cprDraft:
		if f.draft {
			return "draft"
		}
		return "ready for review"
	default:
		return f.title
	}
}

func (f *createPRForm) setRow(row cprRow, value string) {
	if row == cprDraft {
		// The draft row is a toggle, not text: an $EDITOR session or a paste
		// landing on it changes nothing rather than storing a word.
		return
	}
	if row == cprBody {
		f.body = strings.TrimRight(value, "\n")
		return
	}
	// A title is one line by construction; a pasted paragraph collapses
	// rather than encoding newlines GitHub would render as spaces anyway.
	f.title = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
}

func (f *createPRForm) height(width int) int { return len(f.lines(width)) }

func (f *createPRForm) render(width, height int) string {
	lines := f.lines(width)
	return strings.Join(windowRange(lines, 0, len(lines), height), "\n")
}

// lines is the whole form: what it is about, the two rows, and the key line.
func (f *createPRForm) lines(width int) []string {
	out := make([]string, 0, 16)
	out = append(out, styleDim.Render("  "+f.subject()))
	out = append(out, "")
	for _, row := range []cprRow{cprTitle, cprBody, cprDraft} {
		out = append(out, f.rowLines(row, width)...)
	}
	out = append(out, "")
	// The URL is shown because it is what ctrl+o opens and what the daemon's
	// fallback sends a client to, and because a prefill nobody can see the
	// effect of is the thing decision 4 exists to prevent.
	out = append(out, styleDim.Render("  ctrl+o opens"))
	for _, line := range wrapPlain(f.url(), max(width-4, 20)) {
		out = append(out, styleDim.Render("    "+line))
	}
	switch {
	case f.err != "":
		out = append(out, styleBad.Render("  ⚠ "+f.err))
	case f.sending:
		out = append(out, styleDim.Render("  pushing the branch and opening the pull request…"))
	case f.editing:
		out = append(out, styleDim.Render("  ctrl+s keeps this text · esc discards it"))
	default:
		out = append(out, styleDim.Render(
			"  enter edit · e $EDITOR · space toggle draft · ctrl+s push and create · ctrl+o browser · esc leave"))
	}
	return out
}

// subject is the one line above the rows, and it states the consequence a
// human cannot otherwise see: a push sends commits, so uncommitted work in
// the task's worktree is not in the pull request (task 069 decision 4). It is
// said here, before the confirmation, rather than discovered afterwards.
func (f *createPRForm) subject() string {
	out := "a pull request for this task's branch"
	if f.branch != "" {
		out = "a pull request for " + f.branch
	}
	return out + " — ctrl+s pushes the committed work on it to origin and opens the pull request"
}

func (f *createPRForm) rowLines(row cprRow, width int) []string {
	cursor := "  "
	if f.cursor == row && !f.editing {
		cursor = styleFocus.Render("› ")
	}
	label := "title"
	switch row {
	case cprBody:
		label = "body "
	case cprDraft:
		label = "draft"
	}
	out := []string{cursor + label}
	if f.cursor == row && f.editing {
		return append(out, f.editorLines(width)...)
	}
	value := f.rowValue(row)
	if value == "" {
		return append(out, styleDim.Render(strings.Repeat(" ", editorIndent)+"(empty)"))
	}
	for _, line := range wrapPlain(value, max(width-editorIndent, 20)) {
		out = append(out, strings.Repeat(" ", editorIndent)+styleOK.Render(line))
	}
	return out
}

func (f *createPRForm) editorLines(width int) []string {
	f.editor.SetWidth(max(width-editorIndent-2, 20))
	indent := strings.Repeat(" ", editorIndent)
	out := make([]string, 0, 6)
	for _, line := range strings.Split(f.editor.View(), "\n") {
		out = append(out, indent+line)
	}
	return out
}
