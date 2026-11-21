package tui

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// Vim's modes over the chat composer (task 170, `tui.chat.vim`).
//
// A layer, not an editor: the composer stays the bubbles textarea it is, and
// this translates a normal-mode key into a new value and cursor for it,
// written back through the component's public API. So what the textarea holds
// after `dw` is exactly what it would hold had the same characters been
// deleted with backspace — which matters because a chat's draft is the
// textarea's value, persisted as is.
//
// The set is vim's core and nothing past it: the motions h j k l w b e 0 $ gg
// G; i a I A o O; x; d, c and y over those motions and doubled; p; u. There
// are no counts, no visual mode, no registers beyond one unnamed yank, no ex
// commands and no redo.
//
// Departures from vim, each because the component's API offers no clean way:
//
//   - Undo is the layer's own stack of value-and-cursor snapshots, one per
//     normal-mode change and one per insert session, not vim's undo tree. A
//     change made around the layer — a paste, a rebindable ctrl operation —
//     is not a step of its own; `u` returns to the snapshot before the last
//     change the layer made or watched.
//   - j and k move by logical line, as vim does, never by the wrapped row the
//     textarea's own arrow keys move by.
//   - The cursor is the textarea's bar in both modes; the mode is said by
//     label, not by a block cursor.

// vimMode is which of the two modes the composer is in.
type vimMode int

const (
	// vimInsert types: every key but esc is the caller's, exactly as with the
	// layer off.
	vimInsert vimMode = iota
	// vimNormal moves and edits; it never types.
	vimNormal
)

// vimUndoDepth bounds the snapshot stack. A composer holds a message, not a
// file; a hundred steps back is further than anyone presses u.
const vimUndoDepth = 100

// vimPos is a cursor: a logical line and a rune column in it.
type vimPos struct{ row, col int }

// vimSnap is the composer at one moment: what undo restores.
type vimSnap struct {
	text string
	at   vimPos
}

// chatVim is the layer's state between keys.
type chatVim struct {
	state vimMode
	// op is the pending operator — 'd', 'c' or 'y' — or 0.
	op rune
	// g is a pending `g`, waiting for the second of `gg`.
	g bool
	// yank is the one unnamed buffer; linewise is how p puts it back.
	yank     string
	linewise bool
	// wantCol is the column j and k aim for while they follow one another,
	// so a short line in between does not pull the cursor left for good.
	wantCol   int
	wantValid bool
	// base is the composer as the current insert session found it, pushed as
	// one undo step when the session ends having changed something.
	base *vimSnap
	undo []vimSnap
}

// newChatVim starts in insert mode, so a hand without the vim habit still
// types into a composer that has the layer on.
func newChatVim() *chatVim { return &chatVim{} }

func (m *chatVim) mode() vimMode { return m.state }

// label names the mode for the composer's edge.
func (m *chatVim) label() string {
	if m.state == vimNormal {
		return "NORMAL"
	}
	return "INSERT"
}

// reset returns to insert mode with nothing pending, nothing yanked and
// nothing to undo: the composer was emptied or refilled from outside — a send,
// a draft, another chat — and none of the old state describes the new text.
func (m *chatVim) reset() { *m = chatVim{} }

// update offers one key to the layer. handled false means the key is the
// caller's to route exactly as it would with the layer off.
//
// Insert mode handles esc alone. Normal mode handles its key set, handles and
// ignores every other printable key, and leaves alone: enter, which sends in
// both modes; any key with ctrl or alt, so rebindable operations keep working;
// esc with nothing pending, so it peels whatever the caller peels next; and
// the non-printing keys — arrows, backspace — which stay the textarea's.
//
// The command is always nil today. It is in the signature because the caller
// is written against it, and a mode that needs the program — a cursor shape —
// will need it.
//
//nolint:unparam // cmd is part of the contract the chat view is wired to
func (m *chatVim) update(ta *textarea.Model, msg tea.KeyPressMsg) (handled bool, cmd tea.Cmd) {
	esc := msg.Code == tea.KeyEscape && msg.Mod == 0
	if m.state == vimInsert {
		if !esc {
			// Seen before the caller applies it: this is the composer the
			// insert session will have changed.
			if m.base == nil {
				m.base = &vimSnap{text: ta.Value(), at: vimPos{ta.Line(), ta.Column()}}
			}
			return false, nil
		}
		m.leaveInsert(ta)
		return true, nil
	}

	pending := m.op != 0 || m.g
	if esc {
		m.clearPending()
		return pending, nil
	}
	r, printable := vimRune(msg)
	if !printable {
		m.clearPending()
		m.wantValid = false
		return false, nil
	}
	m.normal(ta, r)
	return true, nil
}

// vimRune is the character a press types, when it types exactly one and
// carries no ctrl or alt. Shift is not a modifier here: `G` arrives as its
// own text.
func vimRune(msg tea.KeyPressMsg) (rune, bool) {
	if msg.Mod&^tea.ModShift != 0 {
		return 0, false
	}
	rs := []rune(msg.Text)
	if len(rs) != 1 || !unicode.IsPrint(rs[0]) {
		return 0, false
	}
	return rs[0], true
}

func (m *chatVim) clearPending() { m.op, m.g = 0, false }

// leaveInsert is esc in insert mode: the session becomes one undo step, and
// the cursor steps back onto the last character typed, as vim's does.
func (m *chatVim) leaveInsert(ta *textarea.Model) {
	m.state = vimNormal
	m.clearPending()
	m.wantValid = false
	if m.base != nil && m.base.text != ta.Value() {
		m.push(*m.base)
	}
	m.base = nil
	if col := ta.Column(); col > 0 {
		ta.SetCursorColumn(col - 1)
	}
}

func (m *chatVim) push(s vimSnap) {
	if len(m.undo) == vimUndoDepth {
		m.undo = append(m.undo[:0], m.undo[1:]...)
	}
	m.undo = append(m.undo, s)
}

// vimBuf is the composer's text as lines of runes, with the cursor.
type vimBuf struct {
	lines [][]rune
	at    vimPos
}

func readVimBuf(ta *textarea.Model) vimBuf {
	parts := strings.Split(ta.Value(), "\n")
	b := vimBuf{lines: make([][]rune, len(parts)), at: vimPos{ta.Line(), ta.Column()}}
	for i, p := range parts {
		b.lines[i] = []rune(p)
	}
	b.at.row = min(max(b.at.row, 0), len(b.lines)-1)
	// Normal mode's cursor is on a character; the textarea's may be past the
	// last one, after an arrow key or the end key.
	b.at.col = b.clampCol(b.at.row, b.at.col)
	return b
}

func (b vimBuf) text() string {
	parts := make([]string, len(b.lines))
	for i, l := range b.lines {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

func (b vimBuf) last() int { return len(b.lines) - 1 }

// clampCol is col held onto a character of row, or 0 on an empty line.
func (b vimBuf) clampCol(row, col int) int {
	return min(max(col, 0), max(len(b.lines[row])-1, 0))
}

// firstNonBlank is where I, and a linewise landing, put the cursor.
func (b vimBuf) firstNonBlank(row int) int {
	for i, r := range b.lines[row] {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return b.clampCol(row, len(b.lines[row]))
}

// offset is p as an index into the text with its line breaks; fromOffset is
// its inverse. Charwise ranges that cross lines are cut in these terms.
func (b vimBuf) offset(p vimPos) int {
	n := 0
	for i := range p.row {
		n += len(b.lines[i]) + 1
	}
	return n + p.col
}

func (b vimBuf) runes() []rune { return []rune(b.text()) }

func vimBufOf(text []rune, off int) vimBuf {
	parts := strings.Split(string(text), "\n")
	b := vimBuf{lines: make([][]rune, len(parts))}
	for i, p := range parts {
		b.lines[i] = []rune(p)
	}
	for row, l := range b.lines {
		if off <= len(l) || row == b.last() {
			b.at = vimPos{row, min(off, len(l))}
			break
		}
		off -= len(l) + 1
	}
	return b
}

// vimClass sorts a rune the way vim's word motions do: blank, word character,
// or punctuation. A run of one class is a word.
func vimClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 1
	}
	return 2
}

// wordForward is w: the start of the next word, an empty line counting as
// one. At the end of the text it is the position past the last character,
// which a motion clamps and an operator cuts to.
func (b vimBuf) wordForward(p vimPos) vimPos {
	line, i := b.lines[p.row], p.col
	if i < len(line) {
		if c := vimClass(line[i]); c != 0 {
			for i < len(line) && vimClass(line[i]) == c {
				i++
			}
		}
	}
	for {
		for i < len(line) && vimClass(line[i]) == 0 {
			i++
		}
		if i < len(line) || p.row == b.last() {
			return vimPos{p.row, i}
		}
		p.row++
		line, i = b.lines[p.row], 0
		if len(line) == 0 {
			return vimPos{p.row, 0}
		}
	}
}

// wordEnd is e: the last character of the word the cursor is in or, from its
// end, of the next one. Empty lines are passed over.
func (b vimBuf) wordEnd(p vimPos) vimPos {
	row, i := p.row, p.col+1
	for {
		line := b.lines[row]
		for i < len(line) && vimClass(line[i]) == 0 {
			i++
		}
		if i < len(line) {
			for c := vimClass(line[i]); i+1 < len(line) && vimClass(line[i+1]) == c; {
				i++
			}
			return vimPos{row, i}
		}
		if row == b.last() {
			return vimPos{row, b.clampCol(row, len(line))}
		}
		row, i = row+1, 0
	}
}

// wordBack is b: the start of the word the cursor is in or, from its start,
// of the one before. An empty line is a word here too.
func (b vimBuf) wordBack(p vimPos) vimPos {
	row, i := p.row, p.col-1
	for {
		line := b.lines[row]
		for i >= 0 && vimClass(line[i]) == 0 {
			i--
		}
		if i >= 0 {
			for c := vimClass(line[i]); i > 0 && vimClass(line[i-1]) == c; {
				i--
			}
			return vimPos{row, i}
		}
		if row == 0 {
			return vimPos{0, 0}
		}
		row--
		if len(b.lines[row]) == 0 {
			return vimPos{row, 0}
		}
		i = len(b.lines[row]) - 1
	}
}

// vimMotion is where a motion key goes from the cursor, and how an operator
// reads the span: linewise takes whole lines; inclusive takes the character
// landed on as well.
type vimMotion struct {
	to        vimPos
	linewise  bool
	inclusive bool
	// vertical is j or k, which keep the column they were aiming for.
	vertical bool
}

// motion resolves a motion key. ok is false for a key that is no motion, and
// for one with nowhere to go — j on the last line — which cancels an operator
// rather than acting on the line alone, as in vim.
func (m *chatVim) motion(b vimBuf, r rune) (mv vimMotion, ok bool) {
	at := b.at
	switch r {
	case 'h':
		return vimMotion{to: vimPos{at.row, max(at.col-1, 0)}}, true
	case 'l':
		// One past the cursor even at the end of the line: `dl` there is x.
		// A plain move is clamped back onto the last character.
		return vimMotion{to: vimPos{at.row, min(at.col+1, len(b.lines[at.row]))}}, true
	case 'j', 'k':
		row := at.row + 1
		if r == 'k' {
			row = at.row - 1
		}
		if row < 0 || row > b.last() {
			return vimMotion{}, false
		}
		if !m.wantValid {
			m.wantCol = at.col
		}
		return vimMotion{to: vimPos{row, b.clampCol(row, m.wantCol)}, linewise: true, vertical: true}, true
	case 'w':
		return vimMotion{to: b.wordForward(at)}, true
	case 'b':
		return vimMotion{to: b.wordBack(at)}, true
	case 'e':
		return vimMotion{to: b.wordEnd(at), inclusive: true}, true
	case '0':
		return vimMotion{to: vimPos{at.row, 0}}, true
	case '$':
		return vimMotion{to: vimPos{at.row, b.clampCol(at.row, len(b.lines[at.row]))}, inclusive: true}, true
	case 'G':
		return vimMotion{to: vimPos{b.last(), b.firstNonBlank(b.last())}, linewise: true}, true
	}
	return vimMotion{}, false
}

// normal applies one printable key in normal mode. Every key lands here
// handled; one outside the set only clears what was pending.
func (m *chatVim) normal(ta *textarea.Model, r rune) {
	b := readVimBuf(ta)
	keepWant := false
	defer func() {
		if !keepWant {
			m.wantValid = false
		}
	}()

	// The second key of gg, alone or under an operator.
	if m.g {
		m.g = false
		op := m.op
		m.op = 0
		if r != 'g' {
			return
		}
		mv := vimMotion{to: vimPos{0, b.firstNonBlank(0)}, linewise: true}
		if op != 0 {
			m.operate(ta, b, op, mv)
			return
		}
		vimPlace(ta, mv.to)
		return
	}
	if r == 'g' {
		m.g = true
		return
	}

	if op := m.op; op != 0 {
		m.op = 0
		if r == op { // dd, cc, yy
			m.operate(ta, b, op, vimMotion{to: b.at, linewise: true})
			return
		}
		mv, ok := m.motion(b, r)
		if !ok {
			return
		}
		if r == 'w' {
			mv = b.wordOperand(op, mv)
		}
		m.operate(ta, b, op, mv)
		return
	}

	if mv, ok := m.motion(b, r); ok {
		mv.to.col = b.clampCol(mv.to.row, mv.to.col)
		vimPlace(ta, mv.to)
		if mv.vertical {
			m.wantValid, keepWant = true, true
		}
		return
	}

	switch r {
	case 'd', 'c', 'y':
		m.op = r
	case 'x':
		if len(b.lines[b.at.row]) > 0 {
			m.operate(ta, b, 'd', vimMotion{to: vimPos{b.at.row, b.at.col + 1}})
		}
	case 'p':
		m.put(ta, b)
	case 'u':
		m.undoLast(ta)
	case 'i':
		m.insertAt(ta, b, b.at)
	case 'a':
		m.insertAt(ta, b, vimPos{b.at.row, min(b.at.col+1, len(b.lines[b.at.row]))})
	case 'I':
		col := b.firstNonBlank(b.at.row)
		if len(b.lines[b.at.row]) > 0 && unicode.IsSpace(b.lines[b.at.row][col]) {
			col = len(b.lines[b.at.row]) // a line of blanks: after them
		}
		m.insertAt(ta, b, vimPos{b.at.row, col})
	case 'A':
		m.insertAt(ta, b, vimPos{b.at.row, len(b.lines[b.at.row])})
	case 'o', 'O':
		m.openLine(ta, b, r == 'o')
	}
}

// wordOperand is vim's two special cases for an operator over w. `cw` on a
// word changes the word and leaves the blank after it, as `ce` does. And a w
// that would run onto a later line stops at the end of this one, so `dw` on a
// line's last word does not also join the next line to it.
func (b vimBuf) wordOperand(op rune, mv vimMotion) vimMotion {
	line := b.lines[b.at.row]
	if op == 'c' && b.at.col < len(line) && vimClass(line[b.at.col]) != 0 {
		// To the end of this word: from its last character e would go on to
		// the next one, so the run is walked here.
		i := b.at.col
		for c := vimClass(line[i]); i+1 < len(line) && vimClass(line[i+1]) == c; {
			i++
		}
		return vimMotion{to: vimPos{b.at.row, i}, inclusive: true}
	}
	if mv.to.row > b.at.row {
		mv.to = vimPos{b.at.row, len(line)}
	}
	return mv
}

// snap is the composer as b describes it, for the undo stack.
func (b vimBuf) snap() vimSnap { return vimSnap{text: b.text(), at: b.at} }

// operate runs d, c or y over the span from the cursor to a motion's target.
func (m *chatVim) operate(ta *textarea.Model, b vimBuf, op rune, mv vimMotion) {
	if mv.linewise {
		m.operateLines(ta, b, op, min(b.at.row, mv.to.row), max(b.at.row, mv.to.row))
		return
	}
	from, to := b.offset(b.at), b.offset(mv.to)
	if from > to {
		from, to = to, from
	} else if mv.inclusive && mv.to.col < len(b.lines[mv.to.row]) {
		// Only onto a character: on an empty line there is none to take,
		// and one more would be the line break.
		to++
	}
	text := b.runes()
	to = min(to, len(text))
	if from < to {
		m.yank, m.linewise = string(text[from:to]), false
	}
	if op == 'y' {
		vimPlace(ta, vimBufOf(text, from).at)
		return
	}
	before := b.snap()
	after := vimBufOf(append(text[:from:from], text[to:]...), from)
	if op == 'c' {
		// One step with the typing that follows: esc pushes it.
		m.enterInsert(before)
		vimSet(ta, after.text(), after.at)
		return
	}
	if from == to {
		return
	}
	m.push(before)
	after.at.col = after.clampCol(after.at.row, after.at.col)
	vimSet(ta, after.text(), after.at)
}

// operateLines is the linewise half: whole lines first through last.
func (m *chatVim) operateLines(ta *textarea.Model, b vimBuf, op rune, first, last int) {
	parts := make([]string, 0, last-first+1)
	for _, l := range b.lines[first : last+1] {
		parts = append(parts, string(l))
	}
	m.yank, m.linewise = strings.Join(parts, "\n"), true
	before := b.snap()
	switch op {
	case 'y':
		vimPlace(ta, vimPos{first, b.clampCol(first, b.at.col)})
	case 'c':
		// The lines become one empty line to type on.
		b.lines = append(append(b.lines[:first:first], []rune{}), b.lines[last+1:]...)
		m.enterInsert(before)
		vimSet(ta, b.text(), vimPos{first, 0})
	default:
		b.lines = append(b.lines[:first:first], b.lines[last+1:]...)
		if len(b.lines) == 0 {
			b.lines = [][]rune{{}}
		}
		row := min(first, b.last())
		m.push(before)
		vimSet(ta, b.text(), vimPos{row, b.firstNonBlank(row)})
	}
}

// put is p: the yank after the cursor, or under the line when it was taken
// linewise.
func (m *chatVim) put(ta *textarea.Model, b vimBuf) {
	if m.yank == "" && !m.linewise {
		return
	}
	m.push(b.snap())
	if m.linewise {
		added := strings.Split(m.yank, "\n")
		lines := make([][]rune, 0, len(b.lines)+len(added))
		lines = append(lines, b.lines[:b.at.row+1]...)
		for _, l := range added {
			lines = append(lines, []rune(l))
		}
		b.lines = append(lines, b.lines[b.at.row+1:]...)
		vimSet(ta, b.text(), vimPos{b.at.row + 1, b.firstNonBlank(b.at.row + 1)})
		return
	}
	text, off := b.runes(), b.offset(b.at)
	if len(b.lines[b.at.row]) > 0 {
		off++
	}
	added := []rune(m.yank)
	out := make([]rune, 0, len(text)+len(added))
	out = append(append(append(out, text[:off]...), added...), text[off:]...)
	// On the last character put, or on the first when the yank spans lines.
	land := off + len(added) - 1
	if strings.Contains(m.yank, "\n") {
		land = off
	}
	after := vimBufOf(out, land)
	after.at.col = after.clampCol(after.at.row, after.at.col)
	vimSet(ta, after.text(), after.at)
}

// undoLast is u: the composer as it was before the last change.
func (m *chatVim) undoLast(ta *textarea.Model) {
	if len(m.undo) == 0 {
		return
	}
	s := m.undo[len(m.undo)-1]
	m.undo = m.undo[:len(m.undo)-1]
	b := vimBufOf([]rune(s.text), 0)
	row := min(max(s.at.row, 0), b.last())
	vimSet(ta, s.text, vimPos{row, b.clampCol(row, s.at.col)})
}

// enterInsert switches to insert mode. before is the composer the change
// started from — for i and a, the composer as it is; for o and c, as it was
// before they cut or opened anything — so that the command and what is typed
// after it undo as one step.
func (m *chatVim) enterInsert(before vimSnap) {
	m.state = vimInsert
	m.clearPending()
	m.base = &before
}

func (m *chatVim) insertAt(ta *textarea.Model, b vimBuf, at vimPos) {
	m.enterInsert(b.snap())
	vimPlace(ta, at)
}

// openLine is o (below) and O (above): a new empty line, in insert mode.
func (m *chatVim) openLine(ta *textarea.Model, b vimBuf, below bool) {
	before := b.snap()
	row := b.at.row
	if below {
		row++
	}
	lines := make([][]rune, 0, len(b.lines)+1)
	lines = append(append(append(lines, b.lines[:row]...), []rune{}), b.lines[row:]...)
	b.lines = lines
	m.enterInsert(before)
	vimSet(ta, b.text(), vimPos{row, 0})
}

// vimSet writes a new value and cursor to the textarea. SetValue is the same
// call a draft is loaded with, so the component's own sanitizing and limits
// apply to the result as they would to that text arriving any other way.
func vimSet(ta *textarea.Model, text string, at vimPos) {
	ta.SetValue(text)
	vimPlace(ta, at)
}

// vimPlace moves the textarea's cursor to a logical line and column. The
// component has no "go to line": its vertical moves walk wrapped rows, so
// the line is reached by walking down from the top until the logical row is
// the one wanted. A walk that stops making progress is abandoned rather than
// spun on — the column is then set on whatever row was reached.
func vimPlace(ta *textarea.Model, at vimPos) {
	if ta.Line() != at.row {
		ta.MoveToBegin()
		for ta.Line() < at.row {
			row, col := ta.Line(), ta.Column()
			ta.CursorDown()
			if ta.Line() == row && ta.Column() == col {
				break
			}
		}
	}
	ta.SetCursorColumn(at.col)
}
