package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTruncateKeepShrinksOnlyTheName(t *testing.T) {
	cases := []struct {
		name                string
		prefix, mid, suffix string
		width               int
		want                string
	}{
		{"fits whole", "1 ", "build", " #2", 20, "1 build #2"},
		{"name gives way", "1 ", "step-20-run-the-entire-suite", " #2", 14, "1 step-20-… #2"},
		{"one cell of name", "1 ", "abcdef", " #2", 6, "1 … #2"},
		{"suffix over the name", "Attempt ", "name", " · paused", 12, "Attempt  · …"},
		{"wide runes count as two", "", "日本語の名前", " #3", 8, "日本… #3"},
		{"width clamps to one", "a", "b", "c", 0, "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateKeep(c.prefix, c.mid, c.suffix, c.width)
			if got != c.want {
				t.Errorf("truncateKeep(%q, %q, %q, %d) = %q, want %q", c.prefix, c.mid, c.suffix, c.width, got, c.want)
			}
			if w := ansi.StringWidth(got); w > max(c.width, 1) {
				t.Errorf("result is %d cells, over %d", w, c.width)
			}
		})
	}
}

// Styling is counted by what it draws, not by its bytes, and a cut through a
// styled name keeps its escapes balanced enough to strip clean.
func TestTruncateKeepCountsStyledPartsByCells(t *testing.T) {
	got := truncateKeep(styleDim.Render("1 "), styleSelected.Render("a-very-long-step-name"), styleWarn.Render(" #2"), 12)
	if w := ansi.StringWidth(got); w != 12 {
		t.Errorf("styled result is %d cells, want 12: %q", w, got)
	}
	if plain := ansi.Strip(got); plain != "1 a-very… #2" {
		t.Errorf("styled result reads %q", plain)
	}
}

func TestTruncateKeepPathKeepsBothEnds(t *testing.T) {
	path := "internal/tui/some/deeply/nested/directory/diffpane.go"
	got := truncateKeepPath("▸ ", path, "  +40 -12", 40)
	if w := ansi.StringWidth(got); w != 40 {
		t.Fatalf("result is %d cells, want 40: %q", w, got)
	}
	for _, want := range []string{"▸ internal/", "diffpane.go  +40 -12", "…"} {
		if !strings.Contains(got, want) {
			t.Errorf("path row %q misses %q", got, want)
		}
	}
	if got := truncateKeepPath("", "a/b.go", " +1 -1", 20); got != "a/b.go +1 -1" {
		t.Errorf("a path that fits is changed: %q", got)
	}
}
