package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Fenced code blocks: the language header and the highlighting (task 075).

// TestCodeFenceDrawsItsLanguage is the header: present when the fence declared
// a language, absent when it did not, and text rather than colour, so a
// monochrome terminal reads it too.
func TestCodeFenceDrawsItsLanguage(t *testing.T) {
	got := stripped("```go\nvar x = 1\n```", 40)
	want := []string{"  ▏ go", "  ▏ var x = 1"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := stripped("```\nvar x = 1\n```", 40); len(got) != 1 {
		t.Errorf("a fence with no info string drew a header: %q", got)
	}
	// Only the first word: the rest of an info string is attribute syntax
	// nobody agreed on, and what the header names is a language.
	if got := stripped("```go title=\"x\"\nvar x = 1\n```", 40); got[0] != "  ▏ go" {
		t.Errorf("the header rendered the whole info string: %q", got[0])
	}
}

// TestHighlightingEmitsStylesNeverCharacters is the property the whole scheme
// rests on: strip the escape sequences from a highlighted block and what is
// left is the fence's own content. Anything else would change what a reader
// copies out of the pane.
func TestHighlightingEmitsStylesNeverCharacters(t *testing.T) {
	body := []string{
		`package main`,
		``,
		`// a comment with "quotes" and 42`,
		`func main() {`,
		"\tconst greeting = \"hello, world\" // trailing",
		`	n := 0x1f + 3.5`,
		`	fmt.Println(greeting, n)`,
		`}`,
	}
	src := "```go\n" + strings.Join(body, "\n") + "\n```"
	var got []string
	for i, l := range markdownLines(src, 200) {
		if i == 0 {
			continue // the language header, which is vincent's, not the fence's
		}
		got = append(got, strings.TrimPrefix(ansi.Strip(l), "  ▏ "))
	}
	// Tabs are the four columns the pane draws them as, which predates this.
	want := make([]string, len(body))
	for i, l := range body {
		want[i] = strings.ReplaceAll(l, "\t", "    ")
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("highlighting changed the block's bytes:\n got %q\nwant %q", got, want)
	}
}

// TestHighlightSegmentsConcatenateToTheirSource is the same property at the
// scanner's own door, across every language on the list and a few it has
// never heard of.
func TestHighlightSegmentsConcatenateToTheirSource(t *testing.T) {
	lines := []string{
		`x := "a string with a # and a // in it"`,
		`# a comment`,
		`-- another one`,
		`SELECT * FROM t WHERE a = 'b' -- note`,
		`{"key": [1, 2.5, true, null]}`,
		`日本語 := "🎉 emoji" // 👨‍👩‍👧`,
		`if (a && b) { return c ?? d; }`,
		`unterminated "string`,
		``,
		`    `,
	}
	langs := []string{"go", "python", "js", "ts", "rust", "c", "java", "sh", "sql", "json", "yaml", "ruby", "brainfuck", ""}
	for _, lang := range langs {
		for _, line := range lines {
			segs := highlightSegments(lang, line)
			if got := segsPlain(segs); got != line {
				t.Errorf("%s: %q became %q", lang, line, got)
			}
		}
	}
}

// TestHighlightingFallsBackToPlain is the written-down list's other side: a
// language that is not on it renders exactly as an undeclared fence does,
// which is one plain run.
func TestHighlightingFallsBackToPlain(t *testing.T) {
	const line = `func main() { return "x" }`
	if segs := highlightSegments("brainfuck", line); len(segs) != 1 {
		t.Errorf("an unknown language produced %d runs, want one plain run", len(segs))
	}
	if segs := highlightSegments("go", line); len(segs) < 2 {
		t.Errorf("go produced %d runs; the fixture should tint something", len(segs))
	}
}

// TestHighlightedBlockKeepsItsStructureWithoutColour is §15's monochrome rule
// applied to the new tint: the rail, the header, the indentation and the hard
// wrap are all characters, so stripping the styling loses the colour and
// nothing else.
func TestHighlightedBlockKeepsItsStructureWithoutColour(t *testing.T) {
	src := "```python\ndef f():\n    return 1  # done\n```"
	got := stripped(src, 40)
	want := []string{"  ▏ python", "  ▏ def f():", "  ▏     return 1  # done"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestHighlightedLongLineStillWrapsAtTheRail is task 073 decision 3 under the
// new multi-segment lines: a line too long for the pane continues at the rail
// with every byte intact, whether or not it is highlighted.
func TestHighlightedLongLineStillWrapsAtTheRail(t *testing.T) {
	const line = `const message = "` + `abcdefghij abcdefghij abcdefghij abcdefghij abcdefghij` + `"`
	src := "```go\n" + line + "\n```"
	lines := markdownLines(src, 30)
	var body strings.Builder
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 30 {
			t.Errorf("line %d is %d columns: %q", i, w, l)
		}
		if !strings.HasPrefix(ansi.Strip(l), "  ▏ ") {
			t.Errorf("line %d lost the rail: %q", i, l)
		}
		if i == 0 {
			continue
		}
		body.WriteString(strings.TrimPrefix(ansi.Strip(l), "  ▏ "))
	}
	if body.String() != line {
		t.Errorf("a wrapped highlighted line lost bytes:\n got %q\nwant %q", body.String(), line)
	}
}

// TestHighlightingCannotSmuggleAnEscapeSequence keeps decision 7's chokepoint
// honest now that a code line arrives as several segments: whatever the fence
// contains, what reaches the pane is text plus vincent's own styles.
func TestHighlightingCannotSmuggleAnEscapeSequence(t *testing.T) {
	src := "```go\nx := \"\x1b[2J\" // \x1b]0;pwned\a\n```"
	for _, l := range markdownLines(src, 40) {
		text := ansi.Strip(l)
		if strings.ContainsFunc(text, isTerminalControl) {
			t.Errorf("a control character survived highlighting: %q", l)
		}
		if strings.Contains(text, "[2J") || strings.Contains(text, "pwned") {
			t.Errorf("an escape sequence was rendered as its parameters: %q", l)
		}
	}
}
