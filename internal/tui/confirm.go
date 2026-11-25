package tui

// confirmAnswer is the one reading of a key pressed at a y/n question
// (#1162): `y` or `Y` confirms; `n`, `N` or esc declines; and any other key
// is neither, so the question stays up until it is answered. Every
// confirmation in the TUI asks it rather than spelling its own switch —
// before it, some took `Y` and some did not, and some cancelled on any key
// while others waited, so the same keystroke meant different things on
// different screens.
func confirmAnswer(key string) (yes, no bool) {
	switch key {
	case "y", "Y":
		return true, false
	case "n", "N", "esc":
		return false, true
	}
	return false, false
}
