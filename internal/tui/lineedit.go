package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// lineEdit is a one-line field drawn by its owner: the chat's find line and
// shell prompt (#1078), which used to be append-only strings with a block
// cursor painted at the end. It moves and deletes the way the composer and
// every other text field do — by character, by word and to either end — so
// fixing a typo no longer means erasing back to it.
type lineEdit struct {
	r   []rune
	pos int
	max int // runes; 0 is unbounded
	// off is the first rune ViewIn shows (#1157): the field's horizontal
	// scroll, kept between draws so the window moves only when the cursor
	// would leave it rather than on every key.
	off int
}

func (e *lineEdit) String() string { return string(e.r) }

// Set replaces the text and puts the cursor at its end.
func (e *lineEdit) Set(s string) {
	e.r = []rune(s)
	e.pos = len(e.r)
}

// Insert types s at the cursor, as much of it as the bound allows.
func (e *lineEdit) Insert(s string) {
	in := []rune(s)
	if e.max > 0 {
		in = in[:max(min(len(in), e.max-len(e.r)), 0)]
	}
	e.r = append(e.r[:e.pos], append(in, e.r[e.pos:]...)...)
	e.pos += len(in)
}

// Key applies an editing key and reports whether it was one: a printable
// key types, and the movement and deletion keys edit.
func (e *lineEdit) Key(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "left", "ctrl+b":
		e.pos = max(e.pos-1, 0)
	case "right", "ctrl+f":
		e.pos = min(e.pos+1, len(e.r))
	case "home", "ctrl+a":
		e.pos = 0
	case "end", "ctrl+e":
		e.pos = len(e.r)
	case "alt+left", "alt+b", "ctrl+left":
		e.pos = e.wordStart()
	case "alt+right", "alt+f", "ctrl+right":
		e.pos = e.wordEnd()
	case "backspace", "ctrl+h":
		if e.pos > 0 {
			e.r = append(e.r[:e.pos-1], e.r[e.pos:]...)
			e.pos--
		}
	case "delete", "ctrl+d":
		if e.pos < len(e.r) {
			e.r = append(e.r[:e.pos], e.r[e.pos+1:]...)
		}
	case "ctrl+w", "alt+backspace":
		start := e.wordStart()
		e.r = append(e.r[:start], e.r[e.pos:]...)
		e.pos = start
	case "ctrl+u":
		e.r = e.r[e.pos:]
		e.pos = 0
	case "ctrl+k":
		e.r = e.r[:e.pos]
	default:
		if msg.Text == "" || msg.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			return false
		}
		e.Insert(msg.Text)
	}
	return true
}

func (e *lineEdit) wordStart() int {
	i := e.pos
	for i > 0 && unicode.IsSpace(e.r[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.r[i-1]) {
		i--
	}
	return i
}

func (e *lineEdit) wordEnd() int {
	i := e.pos
	for i < len(e.r) && unicode.IsSpace(e.r[i]) {
		i++
	}
	for i < len(e.r) && !unicode.IsSpace(e.r[i]) {
		i++
	}
	return i
}

// View is the text with the cursor drawn where it is: a block past the end,
// the character under it reversed otherwise.
func (e *lineEdit) View() string {
	if e.pos >= len(e.r) {
		return string(e.r) + "█"
	}
	return string(e.r[:e.pos]) + chatMatchStyle.Render(string(e.r[e.pos])) + string(e.r[e.pos+1:])
}

// ViewIn is View fitted to width cells (#1157). A line the field cannot hold
// had been drawn whole and cut by its owner, so typing past the edge typed
// out of sight and the cursor went with it. It scrolls instead: the window
// keeps the cursor in view, and a … at either edge says the line goes on.
func (e *lineEdit) ViewIn(width int) string {
	width = max(width, 3)
	cell := func(i int) int {
		if i >= len(e.r) {
			return 1 // the block past the end
		}
		return max(ansi.StringWidth(string(e.r[i])), 1)
	}
	total := 0
	for i := 0; i <= len(e.r); i++ {
		total += cell(i)
	}
	if total <= width {
		e.off = 0
		return e.View()
	}
	e.off = min(e.off, e.pos)
	span := func(from, to int) int { // cells of runes [from, to]
		n := 0
		for i := from; i <= to; i++ {
			n += cell(i)
		}
		return n
	}
	for e.off < e.pos {
		left, right := 0, 0
		if e.off > 0 {
			left = 1
		}
		if e.pos < len(e.r)-1 {
			right = 1
		}
		if left+span(e.off, e.pos)+right <= width {
			break
		}
		e.off++
	}
	var b strings.Builder
	used := 0
	if e.off > 0 {
		b.WriteString("…")
		used++
	}
	b.WriteString(string(e.r[e.off:min(e.pos, len(e.r))]))
	used += span(e.off, e.pos-1)
	if e.pos >= len(e.r) {
		b.WriteString("█")
		return b.String()
	}
	b.WriteString(chatMatchStyle.Render(string(e.r[e.pos])))
	used += cell(e.pos)
	end := e.pos + 1
	for end < len(e.r) && used+cell(end) <= width {
		used += cell(end)
		end++
	}
	if end < len(e.r) {
		// Room for the mark: give back what it needs.
		for used+1 > width && end > e.pos+1 {
			end--
			used -= cell(end)
		}
		b.WriteString(string(e.r[e.pos+1 : end]))
		b.WriteString("…")
		return b.String()
	}
	b.WriteString(string(e.r[e.pos+1:]))
	return b.String()
}

// lineField draws a one-line field after prefix with its keys' hint after
// it, in width cells: the field gets what the prefix and the hint leave, and
// the hint goes when that would be too little to edit in (#1157).
func lineField(prefix string, e *lineEdit, hint string, width int) string {
	width = max(width, 1)
	room := width - ansi.StringWidth(prefix) - ansi.StringWidth(hint)
	if room < 12 {
		hint = ""
		room = width - ansi.StringWidth(prefix)
	}
	return ansi.Truncate(prefix+e.ViewIn(room)+styleDim.Render(hint), width, "…")
}
