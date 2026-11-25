package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// clickRow is one row of an open list as its last render drew it (#1144):
// its words, ANSI stripped and without the indent or the cursor mark, and the
// index pointAt takes for it.
type clickRow struct {
	text  string
	index int
}

// clickList is an open list a click chooses a row of (#1144): the pickers a
// form or a chat draws inline. The palette and the root's popups know where
// their rows are, since the root places them (#1065); these are drawn
// wherever their owner's layout puts them — a form row's expansion, a popup
// over the task workspace, the lines over a chat's composer — so the root
// finds a clicked row by its words on the line that was clicked instead of
// asking a dozen layouts for their offsets.
type clickList interface {
	clickRows() []clickRow
	// pointAt puts the cursor on the row, as ↑/↓ would; the root then
	// presses enter. false is a row that cannot be pointed at now.
	pointAt(index int) bool
}

// selectOnClick is a list whose click only moves the cursor (#1144): enter
// on it does what a stray click must not, such as rewinding a chat.
type selectOnClick interface {
	selectsOnClick()
}

// clickListOwner is a view that may have such a list open. openList is nil
// while none is, and while a confirmation owns the keyboard.
type clickListOwner interface {
	openList() clickList
}

// drawnRow is a clickRow for a line exactly as it was drawn.
func drawnRow(line string, index int) clickRow {
	s := strings.TrimLeft(ansi.Strip(line), " ")
	for _, mark := range []string{"▸ ", "› "} {
		s = strings.TrimPrefix(s, mark)
	}
	return clickRow{text: strings.TrimSpace(s), index: index}
}

// rowAt is the row a click at cell x of the screen line lands on: the row
// whose words are drawn there as a whole — not inside a longer word — or the
// two cells of cursor mark before them.
func rowAt(line string, x int, rows []clickRow) (int, bool) {
	for _, r := range rows {
		if r.text == "" {
			continue
		}
		for from := 0; from < len(line); {
			at := strings.Index(line[from:], r.text)
			if at < 0 {
				break
			}
			at += from
			end := at + len(r.text)
			x0 := ansi.StringWidth(line[:at])
			if x >= x0-2 && x < x0+ansi.StringWidth(r.text) && rowBounded(line, at, end) {
				return r.index, true
			}
			_, size := utf8.DecodeRuneInString(line[at:])
			from = at + max(size, 1)
		}
	}
	return 0, false
}

// rowBounded reports that line[at:end] stands alone: a space or the line's
// edge before it, and a space, the edge, a cut or a border after it.
func rowBounded(line string, at, end int) bool {
	if at > 0 {
		r, _ := utf8.DecodeLastRuneInString(line[:at])
		if r != ' ' {
			return false
		}
	}
	if end < len(line) {
		r, _ := utf8.DecodeRuneInString(line[end:])
		if r != ' ' && r != '…' && r != '│' && r != '┃' {
			return false
		}
	}
	return true
}

// The screens' open lists (#1144). Each is the list its screen hands keys to
// first, and none while that screen's own question owns the keyboard.

func (n *newTask) openList() clickList {
	if n.pick == nil || n.submitting || n.mode != ntPicking && n.mode != ntFieldPicking {
		return nil
	}
	return n.pick
}

func (t *taskView) openList() clickList {
	if t.detail == nil || t.popupTab == popupTabDetails {
		return nil
	}
	switch d := t.detail; {
	case d.followUp != nil:
		if d.followUp.picker == nil || d.followUp.confirming {
			return nil
		}
		return d.followUp.picker
	case d.repair != nil:
		if d.repair.picker == nil || d.repair.confirming {
			return nil
		}
		return d.repair.picker
	}
	return nil
}

func (w *issueWrites) openList() clickList {
	switch {
	case w.form != nil:
		if w.form.pick == nil || w.form.confirming {
			return nil
		}
		return w.form.pick
	case w.act != nil:
		if w.act.pick == nil || w.act.stage == iaConfirm || w.act.stage == iaBusy {
			return nil
		}
		return w.act.pick
	}
	return nil
}

func (v *issuesView) openList() clickList { return v.w.openList() }

func (v *issueView) openList() clickList { return v.w.openList() }

func (v *chatsView) openList() clickList {
	if v.create == nil || v.create.pick == nil || v.create.confirming {
		return nil
	}
	return v.create.pick
}

func (p *projectsView) openList() clickList {
	if p.form == nil || p.form.pick == nil || p.form.confirming {
		return nil
	}
	return p.form.pick
}

func (v *pullRequestsView) openList() clickList {
	if v.picker == nil {
		return nil
	}
	return v.picker
}

func (w *workflowsView) openList() clickList {
	if w.editor == nil {
		return nil
	}
	if o, ok := w.editor.overlay.(*wfEditorPicker); ok && o.picker != nil {
		return o.picker
	}
	return nil
}

func (v *chatView) openList() clickList {
	if v.form != nil || v.closing || v.compacting || v.raising || v.clearingQueue {
		return nil
	}
	switch {
	case v.picker != nil:
		return v.picker
	case v.rewind != nil:
		if v.rewind.busy {
			return nil
		}
		return v.rewind
	case v.history.open:
		return &v.history
	}
	return nil
}
