package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// onMacOS runs a test as on macOS with the Option note not yet said.
func onMacOS(t *testing.T) {
	t.Helper()
	was := optionGOOS
	optionGOOS = "darwin"
	optionNoted.Store(false)
	t.Cleanup(func() {
		optionGOOS = was
		optionNoted.Store(false)
	})
}

func backspace() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyBackspace} }

// pressText presses each rune of s as a typed key.
func pressText(m *root, s string) {
	for _, r := range s {
		m.Update(key(string(r)))
	}
}

// #1061: a macOS Option glyph for a chord the chat binds says once what to
// set. #1140: in the composer it waits for the next key, and backspace —
// taking the stray character back — is what says it was a chord.
func TestOptionGlyphNotesOnceInTheComposer(t *testing.T) {
	onMacOS(t)
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)

	m.Update(key("∂"))
	if v.note != "" {
		t.Fatalf("∂ alone was taken for a chord before the next key: %s", v.note)
	}
	if got := v.composer.Value(); got != "∂" {
		t.Fatalf("the glyph was not typed: %q", got)
	}
	m.Update(backspace())
	for _, want := range []string{"Option+d typed ∂", "alt+d", "Use Option as Meta key", "Esc+", "ctrl+p"} {
		if !strings.Contains(v.note, want) {
			t.Errorf("note misses %q: %s", want, v.note)
		}
	}
	v.note = ""
	m.Update(key("ß"))
	m.Update(backspace())
	if v.note != "" {
		t.Errorf("the note was said twice in one run: %s", v.note)
	}
}

// #1140: typing ß, å, ç or π on purpose — mid-word, starting a word, or as a
// word of its own — never raises the note, even though each is the glyph of
// a chord the chat binds.
func TestOptionGlyphTypedOnPurposeSaysNothing(t *testing.T) {
	onMacOS(t)
	for _, text := range []string{"Straße", "ß", "år", "Håkan", "ça va", "garçon", "π r", "π\n", "2π"} {
		m := connectedChatRoot(t)
		v := m.views[viewChat].(*chatView)
		pressText(m, text)
		if v.note != "" {
			t.Errorf("typing %q raised: %s", text, v.note)
		}
	}
}

// #1140: pressing the chord again — the second stray glyph — says it too.
func TestOptionGlyphTwiceNotes(t *testing.T) {
	onMacOS(t)
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)
	pressText(m, "ßß")
	if !strings.Contains(v.note, "Option+s typed ß") || !strings.Contains(v.note, "alt+s") {
		t.Fatalf("a repeated ß said: %q", v.note)
	}
}

// #1140: the chat switch's alt+1 … alt+9 and the shifted chords. #1157: the
// keys that moved off the dead keys — alt+. and alt+, (≥ and ≤), the
// conversation's alt+o (ø) and the export's alt+w (∑) — each type their
// character on the first press, so the first press is what is detected.
func TestOptionGlyphsBeyondLetters(t *testing.T) {
	for _, c := range []struct{ glyph, want string }{
		{"¡", "Option+1 typed ¡ — the terminal sends Option as a character, so alt+1"},
		{"ª", "alt+9 never arrives"},
		{"˝", "Option+Shift+g typed ˝"},
		{"≥", "Option+. typed ≥ — the terminal sends Option as a character, so alt+. never arrives"},
		{"≤", "Option+, typed ≤ — the terminal sends Option as a character, so alt+, never arrives"},
		{"ø", "Option+o typed ø — the terminal sends Option as a character, so alt+o never arrives"},
		{"∑", "Option+w typed ∑ — the terminal sends Option as a character, so alt+w never arrives"},
	} {
		onMacOS(t)
		m := connectedChatRoot(t)
		v := m.views[viewChat].(*chatView)
		m.Update(key(c.glyph))
		m.Update(backspace())
		if !strings.Contains(v.note, c.want) {
			t.Errorf("%s: note %q, want %q", c.glyph, v.note, c.want)
		}
	}
}

// #1157: no default is bound to a dead key any more, so what the dead keys
// deliver is only a chord when tui.keys puts an operation back on one.
func TestOptionDeadKeyGlyphs(t *testing.T) {
	for _, c := range []struct {
		overrides   map[string]string
		glyph, want string
	}{
		{nil, "´", ""},
		{nil, "˜", ""},
		{nil, "ˆ", ""},
		{map[string]string{"export_chat": "alt+e"}, "´", "Option+e typed ´"}, // pressed again
		{map[string]string{"export_chat": "alt+e"}, "é", "alt+e never arrives"},
		{map[string]string{"next_chat": "alt+n"}, "˜", "Option+n typed ˜"},
		{map[string]string{"conversation": "alt+i"}, "ˆ", "Option+i typed ˆ"},
	} {
		t.Run(fmt.Sprintf("%s %v", c.glyph, c.overrides), func(t *testing.T) {
			onMacOS(t)
			if c.overrides != nil {
				withKeymap(t, c.overrides)
			}
			m := connectedChatRoot(t)
			v := m.views[viewChat].(*chatView)
			m.Update(key(c.glyph))
			m.Update(backspace())
			if c.want == "" && v.note != "" {
				t.Errorf("an unbound dead key raised %q", v.note)
			}
			if !strings.Contains(v.note, c.want) {
				t.Errorf("note %q, want %q", v.note, c.want)
			}
		})
	}
}

// A glyph whose chord the surface does not bind is just a character: ¨ is
// Option+u's, and nothing binds alt+u.
func TestOptionGlyphNeedsABoundChord(t *testing.T) {
	onMacOS(t)
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)
	for _, g := range []string{"¨", "ü"} {
		m.Update(key(g))
		m.Update(backspace())
	}
	if v.note != "" {
		t.Fatalf("an unbound chord's glyph raised: %s", v.note)
	}
}

// Off a text field a key is never text, so the first glyph is enough —
// #1140's "every surface with an alt binding": here the conversation focus,
// whose alt+shift+g follows the live end again.
func TestOptionGlyphOffATextField(t *testing.T) {
	onMacOS(t)
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)
	v.convFocus = true
	if m.activeCapturesInput() {
		t.Fatal("fixture: the conversation focus captures input")
	}
	m.Update(key("˝"))
	if !strings.Contains(v.note, "Option+Shift+g typed ˝") {
		t.Fatalf("˝ in the conversation said %q", v.note)
	}
}

// And on a surface that is not a chat at all the root's status line says it:
// the board binds alt+t, chat with….
func TestOptionGlyphOnTheBoard(t *testing.T) {
	onMacOS(t)
	m := connectedChatRoot(t)
	m.switchTo(viewHome)
	if m.activeCapturesInput() || !contextBindsKey(m.activeContext(), "alt+t") {
		t.Fatalf("fixture: board context %q captures input or does not bind alt+t", m.activeContext())
	}
	m.Update(key("†"))
	if !strings.Contains(m.selNotice, "Option+t typed †") {
		t.Fatalf("† on the board said %q", m.selNotice)
	}
}

// Not macOS, nothing to say.
func TestOptionGlyphOnlyOnMacOS(t *testing.T) {
	onMacOS(t)
	optionGOOS = "linux"
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)
	pressText(m, "∂∂")
	if v.note != "" {
		t.Fatalf("linux noted: %s", v.note)
	}
}

// #1157: alt+. and alt+, arrive as those strings whichever way the terminal
// encodes Meta — the legacy ESC prefix, or the kitty protocol's CSI u with
// the alt modifier (3) — so the defaults are keys a press produces.
func TestPunctuationAltChordsDecode(t *testing.T) {
	for raw, want := range map[string]string{
		"\x1b.":      "alt+.",
		"\x1b,":      "alt+,",
		"\x1b[46;3u": "alt+.",
		"\x1b[44;3u": "alt+,",
		"\x1bo":      "alt+o",
		"\x1bw":      "alt+w",
	} {
		if got := decodePress(t, raw).String(); got != want {
			t.Errorf("%q decodes as %q, want %q", raw, got, want)
		}
	}
}
