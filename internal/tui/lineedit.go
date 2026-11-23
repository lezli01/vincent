package tui

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
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
