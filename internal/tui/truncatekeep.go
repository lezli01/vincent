package tui

import "github.com/charmbracelet/x/ansi"

// truncateKeep fits prefix+name+suffix into width display cells by shrinking
// name alone, with a trailing "…": the row-cell convention for identity
// lines (issue #800 W4), beside wrapText's one wrap for prose (task 135.1).
//
// An identity line is a label that has to tell rows apart — an attempt
// strip, a sidebar row, a diff file row — and what tells them apart is
// rarely the long part. A step name of forty cells sits between the attempt
// number and the state; a path sits beside its `+N -M`. Cutting the end of
// such a line keeps the one part every row shares and loses the part that
// differs, so the caller names the three parts and only the name gives way.
//
// Widths are display cells (ansi.StringWidth), so styled parts and wide runes
// count as they draw. prefix and suffix survive whole; when they alone exceed
// width the name is dropped, the suffix's start is kept over it, and the
// whole line is end-truncated as a last resort. width is clamped to 1.
func truncateKeep(prefix, name, suffix string, width int) string {
	return keepParts(prefix, name, suffix, width, func(s string, w int) string {
		return ansi.Truncate(s, w, "…")
	})
}

// truncateKeepPath is truncateKeep for a name that is a path: the middle of
// the name gives way instead of its end, so both the leading directory and
// the filename stay visible (`internal/…/diffpane.go`).
func truncateKeepPath(prefix, name, suffix string, width int) string {
	return keepParts(prefix, name, suffix, width, elideMiddle)
}

func keepParts(prefix, name, suffix string, width int, shrink func(string, int) string) string {
	width = max(width, 1)
	whole := prefix + name + suffix
	if cols(whole) <= width {
		return whole
	}
	room := width - cols(prefix) - cols(suffix)
	if room >= 1 {
		return prefix + shrink(name, room) + suffix
	}
	// No room for even one cell of the name: the suffix is what tells the
	// row apart, so it goes straight after the prefix and the cut, if any,
	// falls on its end.
	return ansi.Truncate(prefix+suffix, width, "…")
}

// elideMiddle cuts s to width cells by replacing its middle with "…",
// keeping a little more of the end — a path's filename — than of its start.
func elideMiddle(s string, width int) string {
	if cols(s) <= width {
		return s
	}
	if width <= 1 {
		return ansi.Truncate(s, width, "…")
	}
	keep := width - 1
	head := keep / 2
	tail := keep - head
	return ansi.Truncate(s, head, "") + "…" + ansi.TruncateLeft(s, cols(s)-tail, "")
}
