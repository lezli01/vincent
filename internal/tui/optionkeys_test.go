package tui

import (
	"strings"
	"testing"
)

// #1061: a macOS Option glyph for a letter the chat binds an alt chord to
// says once what to set, and nothing else does.
func TestOptionGlyphNotesOnce(t *testing.T) {
	optionNoted.Store(false)
	t.Cleanup(func() { optionNoted.Store(false) })

	if _, ok := optionKeyNoteOn("linux", "∂"); ok {
		t.Fatal("a non-macOS run noted an Option glyph")
	}
	for _, text := range []string{"d", "é", "∂∂", ""} {
		if _, ok := optionKeyNoteOn("darwin", text); ok {
			t.Fatalf("%q was taken for an Option glyph", text)
		}
	}
	note, ok := optionKeyNoteOn("darwin", "∂")
	if !ok {
		t.Fatal("Option+d's ∂ said nothing")
	}
	for _, want := range []string{"Option+d typed ∂", "alt+d", "Use Option as Meta key", "Esc+", "ctrl+p"} {
		if !strings.Contains(note, want) {
			t.Errorf("note misses %q: %s", want, note)
		}
	}
	if _, ok := optionKeyNoteOn("darwin", "ß"); ok {
		t.Error("the note was said twice in one run")
	}
}

// Only a letter the chat binds is worth a note: a glyph whose alt chord does
// nothing in the chat is just a character.
func TestOptionGlyphNeedsABoundChord(t *testing.T) {
	optionNoted.Store(false)
	t.Cleanup(func() { optionNoted.Store(false) })
	for glyph, letter := range optionGlyphs {
		_, ok := optionKeyNoteOn("darwin", string(glyph))
		optionNoted.Store(false)
		if ok != chatBindsKey("alt+"+string(letter)) {
			t.Errorf("%c: noted %v, but alt+%c bound %v", glyph, ok, letter, !ok)
		}
	}
}
