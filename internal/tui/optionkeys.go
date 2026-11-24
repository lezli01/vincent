package tui

import (
	"runtime"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/keymap"
)

// The TUI's alt chords (a chat's diff, rewind, shell and model, the chat
// switch, the next task or issue, a chat's export, …) reach it only when the
// terminal sends Option as Meta. On a stock macOS Terminal.app or iTerm2 it
// does not: Option+d types `∂` and nothing says why (#1061). The limit is the
// terminal's, and is recorded at chatswitch.go; the silence was the defect.
// When a key arrives as the character a US-layout Option press produces for a
// chord the surface in front of the user binds, the TUI says once, per run,
// what to set and how to reach the command meanwhile.
//
// Requesting keyboard enhancements would not help: Bubble Tea already
// negotiates the kitty protocol where the terminal speaks it, and whether
// Option is a modifier or a character is the terminal's own setting either
// way — Terminal.app does not speak the protocol at all.
//
// #1140 widened it in three ways. The glyph table covers what #1061 left out:
// Option+digit, Option+Shift+letter, and the dead keys (below). Detection
// runs on every surface, in the root, against the bindings of whatever is in
// front of the user, rather than in the composer alone. And inside a text
// field a glyph is no longer taken for a chord on sight, because there it may
// well be meant — `ß`, `å`, `ç` and `π` are ordinary characters to someone
// typing German, Swedish, French or maths, and on the US layout typing one on
// purpose *is* pressing Option. The two cannot be told apart from the
// character, only from what the hand does next: someone who meant it goes on
// typing, someone who wanted the command sees a stray character appear and
// either deletes it or presses the chord again. So in a text field the note
// waits for the next key and is said only when that key is backspace or
// another bound Option glyph; anything else — a letter, a space, enter —
// leaves the character as the text it was meant to be. Off a text field a key
// is never text, so the first glyph is enough.

// optionPress is one Option chord a glyph may have come from: press is how the
// note names the keys pressed, chord the key Bubble Tea would have reported
// had the terminal sent Option as Meta.
type optionPress struct {
	press, chord string
}

// optionGlyphs maps what the macOS US layout types for an Option chord to the
// chords it may have come from, most likely first. A glyph two chords type —
// `˜` is Option+Shift+n, and Option+n twice — lists both, and the first that
// is bound wins. Built by init from the tables below.
var optionGlyphs = map[rune][]optionPress{}

func init() {
	add := func(glyph rune, press, chord string) {
		optionGlyphs[glyph] = append(optionGlyphs[glyph], optionPress{press: press, chord: chord})
	}
	// Option+letter. Dead keys (e, i, n, u, `) compose with the next key and
	// type nothing alone, so they are not here but below.
	for glyph, letter := range map[rune]rune{
		'å': 'a', '∫': 'b', 'ç': 'c', '∂': 'd', 'ƒ': 'f', '©': 'g', '˙': 'h',
		'∆': 'j', '˚': 'k', '¬': 'l', 'µ': 'm', 'ø': 'o', 'π': 'p', 'œ': 'q',
		'®': 'r', 'ß': 's', '†': 't', '√': 'v', '∑': 'w', '≈': 'x', '¥': 'y',
		'Ω': 'z',
	} {
		add(glyph, "Option+"+string(letter), "alt+"+string(letter))
	}
	// Option+digit (#1140): the chat switch's alt+1 … alt+9.
	for glyph, digit := range map[rune]rune{
		'º': '0', '¡': '1', '™': '2', '£': '3', '¢': '4', '∞': '5', '§': '6',
		'¶': '7', '•': '8', 'ª': '9',
	} {
		add(glyph, "Option+"+string(digit), "alt+"+string(digit))
	}
	// Option+Shift+letter (#1140): alt+shift+n, alt+shift+g. The four whose
	// glyph is a spacing accent are with the dead keys below, behind the
	// unshifted dead key that types the same accent.
	for glyph, letter := range map[rune]rune{
		'Å': 'a', 'ı': 'b', 'Ç': 'c', 'Î': 'd', 'Ï': 'f', '˝': 'g', 'Ó': 'h',
		'Ô': 'j', '': 'k', 'Ò': 'l', 'Â': 'm', 'Ø': 'o', '∏': 'p',
		'Œ': 'q', '‰': 'r', 'Í': 's', 'ˇ': 't', '◊': 'v', '„': 'w', '˛': 'x',
		'Á': 'y', '¸': 'z',
	} {
		add(glyph, "Option+Shift+"+string(letter), "alt+shift+"+string(letter))
	}
	// The dead keys (#1140). Option+e, i, n and u type nothing when pressed:
	// the terminal holds the accent until the next key, and what arrives
	// then is either the accent alone (the dead key pressed again, or space)
	// or the accent composed onto a vowel. A dead key pressed once and left
	// sends the TUI nothing at all, so that press cannot be detected — only
	// the next one can. Option+` is a dead key too, but its accent is a
	// plain backtick, which is never taken for anything.
	for _, dk := range []struct {
		letter   string
		accent   rune
		composed string
	}{
		{"e", '´', "áéíóú"},
		{"i", 'ˆ', "âêîôû"},
		{"n", '˜', "ãñõ"},
		{"u", '¨', "äëïöüÿ"},
	} {
		add(dk.accent, "Option+"+dk.letter, "alt+"+dk.letter)
		add(dk.accent, "Option+Shift+"+dk.letter, "alt+shift+"+dk.letter)
		// Lower case only: an upper-case composed vowel is also what an
		// Option+Shift letter types directly (Î is Option+Shift+d), and
		// that is listed above.
		for _, v := range dk.composed {
			add(v, "Option+"+dk.letter+", then a letter,", "alt+"+dk.letter)
		}
	}
}

// optionGOOS is the platform detection runs on; a test sets it.
var optionGOOS = runtime.GOOS

// optionNoted is whether this run has said it already.
var optionNoted atomic.Bool

// optionHit is a key that arrived as an Option glyph for a bound chord.
type optionHit struct {
	glyph rune
	optionPress
}

// optionHitOf is the bound chord msg's character came from, if it is the
// glyph of one: bound answers whether the surface binds a chord.
func optionHitOf(msg tea.KeyPressMsg, bound func(string) bool) (optionHit, bool) {
	r := []rune(msg.Text)
	if len(r) != 1 {
		return optionHit{}, false
	}
	for _, p := range optionGlyphs[r[0]] {
		if bound(p.chord) {
			return optionHit{glyph: r[0], optionPress: p}, true
		}
	}
	return optionHit{}, false
}

// note is what the TUI says for a hit.
func (h optionHit) note() string {
	palette := opKey(keymap.PaletteAlt)
	if palette == "" {
		palette = "ctrl+p"
	}
	return h.press + " typed " + string(h.glyph) + " — the terminal sends Option as a character, so " +
		h.chord + " never arrives. Set Terminal's “Use Option as Meta key” or iTerm2's Option key to “Esc+”; meanwhile " +
		palette + " reaches every command"
}

// optionWatch is the root's half of the detection: the glyph a text field is
// still deciding about (#1140), and the surface it was typed on.
type optionWatch struct {
	pending *optionHit
	ctx     bindingContext
}

// observe judges one key press on surface ctx, where typing says a text field
// has the keyboard, and returns the note to say, if any. It does not check the
// platform or whether the note was said: the caller does.
func (w *optionWatch) observe(msg tea.KeyPressMsg, ctx bindingContext, typing bool) (string, bool) {
	hit, isHit := optionHitOf(msg, func(chord string) bool { return contextBindsKey(ctx, chord) })
	pending := w.pending
	w.pending = nil
	if !typing {
		if isHit {
			return hit.note(), true
		}
		return "", false
	}
	// The glyph before this key is a chord if this key takes it back or
	// presses an Option chord again; anything else is the user typing on.
	if pending != nil && w.ctx == ctx && (msg.String() == "backspace" || isHit) {
		return pending.note(), true
	}
	if isHit {
		w.pending, w.ctx = &hit, ctx
	}
	return "", false
}

// contextBindsKey is whether key does something on the surface ctx: one of
// its own rows, a global row, or — on the board and the task workspace — a
// task action's row.
func contextBindsKey(ctx bindingContext, key string) bool {
	for _, b := range registry() {
		if b.key != key || b.unbound {
			continue
		}
		switch {
		case b.scope == scopePanel && b.context == ctx,
			b.scope == scopeGlobal,
			b.scope == scopeTaskAction && isHomeContext(ctx):
			return true
		}
	}
	return false
}

// optionNoter is a view that says the Option note on a line of its own — a
// chat, on its note line, where #1061 first said it. Any other surface gets
// the root's status line.
type optionNoter interface {
	noteOption(note string)
}

// watchOption runs the detection for one key press: surface and typing are
// read before the key is routed, since the key may change them, and the note
// is placed after, so the routing's own clearing of notes does not take it.
func (m *root) watchOption(msg tea.KeyPressMsg, route func() (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	if optionGOOS != "darwin" || optionNoted.Load() || m.notice.active || m.help ||
		m.pending != nil || m.leave != nil || m.globalPause.asking || m.connScreen() {
		return route()
	}
	ctx, popup := m.popupContext()
	if !popup {
		ctx = m.activeContext()
	}
	// Every root popup but the draft question takes text; treating them all
	// as text fields only ever waits for one more key.
	typing := popup || m.activeCapturesInput()
	note, ok := m.option.observe(msg, ctx, typing)
	model, cmd := route()
	if !ok {
		return model, cmd
	}
	optionNoted.Store(true)
	if v, isNoter := m.views[m.active].(optionNoter); isNoter && !popup && !m.popupOpen() {
		v.noteOption(note)
	} else {
		m.selNotice, m.selNoticeWarn = note, false
	}
	return model, cmd
}
