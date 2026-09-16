package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// The comment popup (task 068.4, issue #387).
//
// It is its own confirmation, with no second y/n. That is the create-PR
// form's shape and task 069 decision 2's reasoning: the keypress and the
// editable body in front of it are the consent, and a question asked after
// ctrl+s would be asked of someone who has just answered it. esc discards and
// sends nothing; a blank body is refused here, before the daemon sees it.
//
// It opens with the body focused, because writing is what `i` was pressed
// for. esc leaves the text field first — which is what makes `e` reachable —
// and closes the popup on the second press.

// pullCommentEditMsg carries the text an $EDITOR session left behind.
type pullCommentEditMsg struct {
	taskID int64
	text   string
	err    error
}

type pullCommentForm struct {
	taskID  int64
	subject string
	body    string
	editor  textarea.Model
	editing bool
	// sending refuses a second ctrl+s while the first is in flight: comments
	// carry no idempotency key, so a double press would post twice (task 069
	// decision 7 is the precedent).
	sending bool
	err     string

	// submit posts the body. openEditor hands it to $EDITOR. Both are
	// injected by the task view, which owns the client and the exec path.
	submit     func(body string) tea.Cmd
	openEditor func(text string) tea.Cmd
}

func newPullCommentForm(taskID int64, subject string) *pullCommentForm {
	ed := textarea.New()
	ed.Prompt = ""
	ed.ShowLineNumbers = false
	ed.DynamicHeight = true
	ed.MinHeight = 3
	ed.MaxHeight = editorLines
	ed.SetHeight(3)
	ed.SetWidth(40)
	f := &pullCommentForm{taskID: taskID, subject: subject, editor: ed}
	f.startEdit()
	return f
}

func (f *pullCommentForm) startEdit() {
	f.editing = true
	f.editor.SetValue(f.body)
	f.editor.Focus()
}

// stopEdit keeps what was typed: leaving the field is not discarding it.
func (f *pullCommentForm) stopEdit() {
	f.editing = false
	f.body = f.editor.Value()
	f.editor.Blur()
}

// paste lands in the body, opening the field if it was not open.
func (f *pullCommentForm) paste(text string) tea.Cmd {
	if !f.editing {
		f.startEdit()
	}
	var cmd tea.Cmd
	f.editor, cmd = f.editor.Update(tea.PasteMsg{Content: text})
	return cmd
}

// applyEdit installs what an $EDITOR session produced.
func (f *pullCommentForm) applyEdit(msg pullCommentEditMsg) {
	if msg.taskID != f.taskID {
		return
	}
	if msg.err != nil {
		f.err = errString(msg.err)
		return
	}
	f.body = strings.TrimRight(msg.text, "\n")
	f.editor.SetValue(f.body)
	f.err = ""
}

// update handles one key. exit asks the task view to close the popup.
func (f *pullCommentForm) update(msg tea.KeyPressMsg) (cmd tea.Cmd, exit bool) {
	if f.editing {
		switch msg.String() {
		case "esc":
			f.stopEdit()
			return nil, false
		case "ctrl+s":
			f.stopEdit()
			return f.send(), false
		}
		var c tea.Cmd
		f.editor, c = f.editor.Update(msg)
		return c, false
	}
	switch msg.String() {
	case "enter":
		f.startEdit()
	case "e":
		if f.openEditor != nil {
			return f.openEditor(f.body), false
		}
		f.startEdit()
	case "ctrl+s":
		return f.send(), false
	case "esc":
		return nil, true
	}
	return nil, false
}

// send hands the body to the daemon, verbatim. The popup stays up until the
// answer lands, so a refusal has somewhere to be shown next to the text it
// refused.
func (f *pullCommentForm) send() tea.Cmd {
	if f.sending {
		return nil
	}
	if strings.TrimSpace(f.body) == "" {
		f.err = "write the comment first — a blank one is not sent"
		return nil
	}
	if f.submit == nil {
		return nil
	}
	cmd := f.submit(f.body)
	if cmd == nil {
		return nil
	}
	f.sending, f.err = true, ""
	return cmd
}

// failed reports what the daemon said and re-arms the popup.
func (f *pullCommentForm) failed(msg string) {
	f.sending = false
	f.err = msg
}

func (f *pullCommentForm) lines(width int) []string {
	out := []string{styleDim.Render("  posted on " + f.subject + " as the daemon's GitHub credential — ctrl+s is the confirmation"), ""}
	indent := strings.Repeat(" ", editorIndent)
	switch {
	case f.editing:
		f.editor.SetWidth(max(width-editorIndent-2, 20))
		for _, line := range strings.Split(f.editor.View(), "\n") {
			out = append(out, indent+line)
		}
	case f.body == "":
		out = append(out, styleDim.Render(indent+"(empty)"))
	default:
		for _, line := range wrapPlain(f.body, max(width-editorIndent, 20)) {
			out = append(out, indent+styleOK.Render(line))
		}
	}
	out = append(out, "")
	switch {
	case f.err != "":
		out = append(out, styleBad.Render("  ⚠ "+f.err))
	case f.sending:
		out = append(out, styleDim.Render("  posting…"))
	case f.editing:
		out = append(out, styleDim.Render("  ctrl+s post · esc stop typing"))
	default:
		out = append(out, styleDim.Render("  enter type · e $EDITOR · ctrl+s post · esc discard"))
	}
	return out
}

func (f *pullCommentForm) height(width int) int { return len(f.lines(width)) }

func (f *pullCommentForm) render(width, height int) string {
	lines := f.lines(width)
	return strings.Join(windowRange(lines, 0, len(lines), height), "\n")
}
