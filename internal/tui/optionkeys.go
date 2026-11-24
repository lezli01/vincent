package tui

import (
	"runtime"
	"sync/atomic"

	"github.com/lezli01/vincent/internal/keymap"
)

// The chat's alt chords (diff, rewind, shell, model, the chat switch, …)
// reach it only when the terminal sends Option as Meta. On a stock macOS
// Terminal.app or iTerm2 it does not: Option+d types `∂` into the composer
// and nothing says why (#1061). The limit is the terminal's, and is recorded
// at chatswitch.go; the silence was the defect. When the composer receives
// the character a US-layout Option press produces for a letter whose alt
// chord the chat binds, it says once, per run, what to set and how to reach
// the command meanwhile.
//
// Requesting keyboard enhancements would not help: Bubble Tea already
// negotiates the kitty protocol where the terminal speaks it, and whether
// Option is a modifier or a character is the terminal's own setting either
// way — Terminal.app does not speak the protocol at all.

// optionGlyphs is what Option+letter types on the macOS US layout. Dead
// keys (e, i, n, u, `) compose with the next key and type nothing alone,
// so they are not here.
var optionGlyphs = map[rune]rune{
	'å': 'a', '∫': 'b', 'ç': 'c', '∂': 'd', 'ƒ': 'f', '©': 'g', '˙': 'h',
	'∆': 'j', '˚': 'k', '¬': 'l', 'µ': 'm', 'ø': 'o', 'π': 'p', 'œ': 'q',
	'®': 'r', 'ß': 's', '†': 't', '√': 'v', '∑': 'w', '≈': 'x', '¥': 'y',
	'Ω': 'z',
}

// optionNoted is whether this run has said it already.
var optionNoted atomic.Bool

// optionKeyNote is the note for text typed into the composer, and false when
// there is nothing to say: not macOS, not a lone Option glyph, a letter the
// chat binds no alt chord to, or said already.
func optionKeyNote(text string) (string, bool) {
	return optionKeyNoteOn(runtime.GOOS, text)
}

func optionKeyNoteOn(goos, text string) (string, bool) {
	if goos != "darwin" || optionNoted.Load() {
		return "", false
	}
	r := []rune(text)
	if len(r) != 1 {
		return "", false
	}
	letter, ok := optionGlyphs[r[0]]
	if !ok {
		return "", false
	}
	chord := "alt+" + string(letter)
	if !chatBindsKey(chord) {
		return "", false
	}
	optionNoted.Store(true)
	palette := opKey(keymap.PaletteAlt)
	if palette == "" {
		palette = "ctrl+p"
	}
	return "Option+" + string(letter) + " typed " + string(r[0]) + " — the terminal sends Option as a character, so " +
		chord + " never arrives. Set Terminal's “Use Option as Meta key” or iTerm2's Option key to “Esc+”; meanwhile " +
		palette + " reaches every command", true
}

// chatBindsKey is whether any chat surface's row answers key.
func chatBindsKey(key string) bool {
	for _, ctx := range []bindingContext{ctxChat, ctxChatConversation} {
		for _, b := range bindingsFor(ctx) {
			if b.key == key {
				return true
			}
		}
	}
	return false
}
