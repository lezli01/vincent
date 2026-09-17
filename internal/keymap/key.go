package keymap

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// namedKeys are the non-rune key names Bubble Tea v2 prints, which is the form
// `tui.keys` is written in (decision 7). Only the ones a keymap could sensibly
// use are accepted; a name outside this set is refused rather than stored as a
// key no press can produce.
var namedKeys = map[string]bool{
	"enter": true, "tab": true, "esc": true, "space": true, "backspace": true,
	"delete": true, "insert": true, "home": true, "end": true, "pgup": true,
	"pgdown": true, "up": true, "down": true, "left": true, "right": true,
}

func init() {
	for i := 1; i <= 20; i++ {
		namedKeys[fmt.Sprintf("f%d", i)] = true
	}
}

// modifiers are accepted in the order Bubble Tea prints them.
var modifiers = []string{"ctrl", "alt", "shift"}

// ParseKey validates one key string and returns it as Bubble Tea prints the
// press: `R`, `ctrl+r`, `f5`, `shift+tab`.
func ParseKey(s string) (string, error) {
	if _, err := printableKey(s); err != nil {
		return "", err
	}
	return s, nil
}

// printableKey reports whether key is one a focused text field would type —
// a single character or space with no ctrl or alt — and refuses a string no
// press produces.
func printableKey(key string) (bool, error) {
	if key == "" {
		return false, fmt.Errorf("an empty key")
	}
	rest := key
	mods := map[string]bool{}
	next := 0
	for {
		matched := false
		for i := next; i < len(modifiers); i++ {
			prefix := modifiers[i] + "+"
			if strings.HasPrefix(rest, prefix) && len(rest) > len(prefix) {
				mods[modifiers[i]] = true
				rest = rest[len(prefix):]
				next = i + 1
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	if namedKeys[rest] {
		return rest == "space" && !mods["ctrl"] && !mods["alt"], nil
	}
	r, size := utf8.DecodeRuneInString(rest)
	if size != len(rest) || r == utf8.RuneError || unicode.IsSpace(r) || unicode.IsControl(r) {
		return false, fmt.Errorf("%q is not a key; want one character, or a name such as enter, f5 or pgdown, optionally after ctrl+, alt+ or shift+", key)
	}
	if mods["shift"] {
		return false, fmt.Errorf("%q: write a shifted character as itself (%q), the way the terminal reports it", key, strings.ToUpper(rest))
	}
	return !mods["ctrl"] && !mods["alt"], nil
}
