package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// wrapText is the one wrap of the TUI (task 135.1); these are the cases the
// five helpers it replaced each got a different subset of right.
func TestWrapText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		width int
		opts  wrapOpts
		want  []string
	}{
		{"fits", "one two", 20, wrapOpts{}, []string{"one two"}},
		{"words wrap", "one two three", 8, wrapOpts{}, []string{"one two", "three"}},
		{"empty is one line", "", 10, wrapOpts{}, []string{""}},
		{"paragraphs and blank lines", "a b\n\nc", 10, wrapOpts{}, []string{"a b", "", "c"}},
		{"long token hard-splits", "abcdefghij", 4, wrapOpts{}, []string{"abcd", "efgh", "ij"}},
		{"long token after a word starts its own line", "ab cdefgh", 4, wrapOpts{}, []string{"ab", "cdef", "gh"}},
		{"width 1", "abc de", 1, wrapOpts{}, []string{"a", "b", "c", "d", "e"}},
		{"width clamped to 1", "ab", -3, wrapOpts{}, []string{"a", "b"}},
		{"wide runes by cell", "日本語", 4, wrapOpts{}, []string{"日本", "語"}},
		{"emoji kept whole", "🎉🎉🎉", 4, wrapOpts{}, []string{"🎉🎉", "🎉"}},
		{"ZWJ emoji never split", "👨‍👩‍👧👨‍👩‍👧", 2, wrapOpts{}, []string{"👨‍👩‍👧", "👨‍👩‍👧"}},
		{
			"hanging indent in cells", "aa bb cc dd", 10,
			hanging("日本 "),
			[]string{"日本 aa bb", "     cc dd"},
		},
		{
			"indent never alone on a blank line", "a\n\nb", 10,
			hanging("  "),
			[]string{"  a", "", "  b"},
		},
		{"empty with a label keeps the label", "", 10, hanging("x: "), []string{"x:"}},
		{
			"maxLines ends on an ellipsis", "one two three four", 8,
			wrapOpts{maxLines: 2},
			[]string{"one two", "three…"},
		},
		{
			"maxLines on a full line", "abcdefgh ijkl", 8,
			wrapOpts{maxLines: 1},
			[]string{"abcdefg…"},
		},
		{"maxLines not reached", "one", 8, wrapOpts{maxLines: 2}, []string{"one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapText(tc.text, tc.width, tc.opts)
			if !slices.Equal(got, tc.want) {
				t.Errorf("wrapText(%q, %d) =\n%q\nwant\n%q", tc.text, tc.width, got, tc.want)
			}
		})
	}
}

// No width is too narrow to honour (decision 1): the floor at 8 that handed
// the overflow to frame() is gone. Wide text is held to 2, the narrowest a
// wide rune fits.
func TestWrapTextNeverExceedsTheWidth(t *testing.T) {
	texts := []string{
		"The quick brown fox jumps over the lazy dog, then a /very/long/path/with/no/spaces/at/all.",
		strings.Repeat("word ", 30) + "\n\n" + strings.Repeat("x", 40),
	}
	wide := []string{strings.Repeat("日本語 ", 10), strings.Repeat("🎉 done ", 8)}
	for w := 1; w <= 12; w++ {
		for _, opts := range []wrapOpts{{}, hanging(" "), {maxLines: 3}} {
			check := func(text string) {
				for i, line := range wrapText(text, w, opts) {
					if c := ansi.StringWidth(line); c > w {
						t.Errorf("width %d %+v: line %d is %d cells: %q", w, opts, i, c, line)
					}
					if strings.TrimRight(line, " ") != line {
						t.Errorf("width %d: line %d ends in whitespace: %q", w, i, line)
					}
				}
			}
			if w <= cols(opts.prefix) {
				continue // a prefix as wide as the line is the caller's overflow
			}
			for _, text := range texts {
				check(text)
			}
			if w >= 2+cols(opts.prefix) {
				for _, text := range wide {
					check(text)
				}
			}
		}
	}
}
