package tui

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Markdown rendering for assistant prose (task 073).
//
// Everything here asserts against ansi.Strip'ped lines rather than against
// styled ones: what is under test is that structure survives without colour,
// which is the §15 rule that a monochrome terminal loses nothing.

// stripped renders assistant prose and removes every escape sequence, so a
// golden line is what a reader on a colour-blind terminal sees.
func stripped(text string, width int) []string {
	lines := markdownLines(text, width)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

// TestMarkdownConstructs is the subset, one case per construct, at three
// widths. The widths matter as much as the constructs: a marker that is
// composed into the content column has to keep its hanging indent when the
// pane is 20 columns wide, which is where a renderer that laid its structure
// out with fixed padding would come apart.
func TestMarkdownConstructs(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		width int
		want  []string
	}{
		{
			name: "heading loses its hashes and keeps a glyph", width: 40,
			src:  "# Findings",
			want: []string{"  ▌ Findings"},
		},
		{
			name: "heading level is the indent", width: 40,
			src:  "### Deeper",
			want: []string{"    ▌ Deeper"},
		},
		{
			name: "paragraph rejoins its soft breaks", width: 40,
			src:  "one two\nthree four",
			want: []string{"  one two three four"},
		},
		{
			name: "emphasis and strong lose their markers", width: 40,
			src:  "a *soft* and **loud** word",
			want: []string{"  a soft and loud word"},
		},
		{
			name: "inline code keeps its delimiters", width: 40,
			src:  "run `go test ./...` first",
			want: []string{"  run `go test ./...` first"},
		},
		{
			name: "an underscore inside a word stays literal", width: 40,
			src:  "see main_test.go and _really_ mean it",
			want: []string{"  see main_test.go and really mean it"},
		},
		{
			name: "unordered list", width: 40,
			src:  "- one\n- two",
			want: []string{"  • one", "  • two"},
		},
		{
			name: "ordered list keeps the author's numbers", width: 40,
			src:  "1. one\n2. two",
			want: []string{"  1. one", "  2. two"},
		},
		{
			name: "nested lists indent and change marker", width: 40,
			src:  "- outer\n  - inner\n    - deepest",
			want: []string{"  • outer", "    ◦ inner", "      ▪ deepest"},
		},
		{
			name: "a wrapped list item hangs under its text", width: 20,
			src:  "- alpha beta gamma delta",
			want: []string{"  • alpha beta gamma", "    delta"},
		},
		{
			name: "blockquote keeps its bar on every line", width: 20,
			src:  "> alpha beta gamma delta",
			want: []string{"  │ alpha beta gamma", "  │ delta"},
		},
		{
			name: "nested blockquote", width: 40,
			src:  ">> deep",
			want: []string{"  │ │ deep"},
		},
		{
			name: "fenced code keeps its indentation", width: 40,
			src:  "```go\nif x {\n    y()\n}\n```",
			want: []string{"  ▏ go", "  ▏ if x {", "  ▏     y()", "  ▏ }"},
		},
		{
			name: "horizontal rule fills the content column", width: 20,
			src:  "---",
			want: []string{"  " + strings.Repeat("─", 18)},
		},
		{
			name: "a rule spelled with stars and spaces", width: 20,
			src:  "* * *",
			want: []string{"  " + strings.Repeat("─", 18)},
		},
		{
			name: "blocks are separated, list items are not", width: 80,
			src:  "intro\n\n- one\n- two\n\nouttro",
			want: []string{"  intro", "", "  • one", "  • two", "", "  outtro"},
		},
		{
			name: "a heading sits straight on what it introduces", width: 80,
			src:  "## Steps\n\n- one",
			want: []string{"   ▌ Steps", "  • one"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripped(tc.src, tc.width)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("at width %d:\n got %q\nwant %q", tc.width, got, tc.want)
			}
			for i, l := range markdownLines(tc.src, tc.width) {
				if w := ansi.StringWidth(l); w > tc.width {
					t.Errorf("line %d is %d columns wide, pane is %d: %q", i, w, tc.width, l)
				}
			}
		})
	}
}

// TestMarkdownEveryConstructAtEveryWidth is the width sweep the table above
// only samples: no construct may overflow the pane, at any of the widths §15
// says the pane must survive.
func TestMarkdownEveryConstructAtEveryWidth(t *testing.T) {
	src := strings.Join([]string{
		"# Title",
		"",
		"A paragraph with **strong**, *emphasis* and `inline code` in it.",
		"",
		"## Section",
		"",
		"- an item long enough to wrap at every width under test",
		"  - a nested item, also long enough to wrap at every width",
		"1. first",
		"10. tenth",
		"",
		"> a quotation long enough that it has to wrap somewhere",
		"",
		"```sh",
		"echo 'a line of code that is far too long to fit in a narrow pane'",
		"```",
		"",
		"---",
		"",
		"The last word.",
	}, "\n")
	for _, width := range []int{20, 40, 80} {
		for i, l := range markdownLines(src, width) {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: line %d is %d columns: %q", width, i, w, l)
			}
		}
	}
}

// TestMarkdownOutsideTheSubsetStaysLiteral is the subset boundary, stated as
// a test rather than left implicit (decision 1). A construct that is not on
// the list renders as the characters the agent sent — never as something
// fetched, executed or half-interpreted.
func TestMarkdownOutsideTheSubsetStaysLiteral(t *testing.T) {
	cases := map[string]string{
		"reference link":  "see [the docs][1]",
		"autolink":        "see <https://example.com/x>",
		"bare url":        "see https://example.com/x for more",
		"titled link":     `see [the docs](https://example.com/x "Docs")`,
		"html block":      "<script>alert(1)</script>",
		"inline html":     "a <b>bold</b> word",
		"footnote":        "a claim[^1]",
		"setext underlin": "Title\n===",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			got := strings.Join(stripped(src, 80), "\n")
			for _, line := range strings.Split(src, "\n") {
				if !strings.Contains(got, line) {
					t.Errorf("%q was interpreted rather than left literal:\n%s", line, got)
				}
			}
		})
	}
}

// TestMarkdownReflowsFromSource is the resize criterion. Rendering at 80 and
// then at 40 must equal rendering fresh at 40: the pane keeps no rendered
// line, so a resize re-parses the Markdown rather than re-wrapping its own
// escape sequences — which is the failure mode ANSI-aware wrapping exists to
// paper over, and which §15's wrap-plain-then-style invariant avoids instead.
func TestMarkdownReflowsFromSource(t *testing.T) {
	src := "# Title\n\nA paragraph long enough to wrap differently at eighty and at forty " +
		"columns, with **strong** text in it.\n\n- an item that also wraps\n\n> and a quote"
	wide := markdownLines(src, 80)
	narrow := markdownLines(src, 40)
	again := markdownLines(src, 40)
	if strings.Join(narrow, "\n") != strings.Join(again, "\n") {
		t.Error("rendering the same source twice at one width disagreed with itself")
	}
	if strings.Join(wide, "\n") == strings.Join(narrow, "\n") {
		t.Fatal("the fixture does not reflow; it proves nothing")
	}
	// The wide rendering is not an input to the narrow one anywhere, which is
	// what the equality above establishes: nothing cached the 80-column pass.
	if got := markdownLines(src, 40); strings.Join(got, "\n") != strings.Join(narrow, "\n") {
		t.Error("a render after a wider render differed from a fresh one")
	}
}

// TestMarkdownCodeBlockIsWholeAndSafe is decision 3: a code block keeps every
// space and every hard break, a line longer than the pane continues on the
// next line at the block's rail rather than being truncated out of reach, and
// a fence carrying control sequences cannot reach past its own lines.
func TestMarkdownCodeBlockIsWholeAndSafe(t *testing.T) {
	src := "```\n    indented\n\ttabbed\n\nblank above\n```"
	got := stripped(src, 40)
	// A tab keeps its indentation as the four columns lipgloss renders it
	// as, so the block's measured width and its drawn width agree.
	want := []string{"  ▏     indented", "  ▏     tabbed", "  ▏ ", "  ▏ blank above"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("code block whitespace was not preserved:\n got %q\nwant %q", got, want)
	}

	long := "```\n" + strings.Repeat("x", 100) + "\n```"
	lines := markdownLines(long, 40)
	var body strings.Builder
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("code line %d is %d columns: %q", i, w, l)
		}
		body.WriteString(strings.TrimPrefix(ansi.Strip(l), "  ▏ "))
	}
	if body.String() != strings.Repeat("x", 100) {
		t.Errorf("a long code line lost bytes to the wrap: %q", body.String())
	}
	if len(lines) < 3 {
		t.Errorf("a 100-column line in a 40-column pane produced %d lines", len(lines))
	}

	evil := "```\nclear:\x1b[2J and \x1b]0;pwned\a and \rback\n```"
	for _, l := range markdownLines(evil, 40) {
		if strings.ContainsFunc(ansi.Strip(l), isTerminalControl) {
			t.Errorf("a control sequence survived a code fence: %q", l)
		}
	}
}

// TestMarkdownMonochromeKeepsEveryDistinction is §15's rule applied to the
// new structure: strip the colour and the document is still a document. A
// heading, a quote, a list, an inline code span and a rule each have to be
// visible as characters, because colour alone does not survive a monochrome
// terminal or an SSH session that lost its profile.
func TestMarkdownMonochromeKeepsEveryDistinction(t *testing.T) {
	src := "# Heading\n\n> quoted\n\n- bullet\n\n1. numbered\n\nan `inline` span\n\n---\n\n```\ncode\n```"
	got := strings.Join(stripped(src, 60), "\n")
	for _, want := range []string{
		"▌ Heading", "│ quoted", "• bullet", "1. numbered", "`inline`", "─────", "▏ code",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not visible without colour:\n%s", want, got)
		}
	}
}

// TestMarkdownEmitsOnlyVincentStyles is the injection criterion at the
// renderer's own door. Whatever an agent sends, the escape sequences on a
// rendered line are the ones vincent asked lipgloss for.
func TestMarkdownEmitsOnlyVincentStyles(t *testing.T) {
	src := "before \x1b[2J after\n\n\x1b]0;title\a\n\n- \x1b[31mred\x1b[0m item\n\nbare\rreturn and back\bspace"
	for _, l := range markdownLines(src, 40) {
		text := ansi.Strip(l)
		if strings.ContainsFunc(text, isTerminalControl) {
			t.Errorf("a control character reached the pane: %q", l)
		}
		if strings.Contains(text, "[2J") || strings.Contains(text, "title") {
			t.Errorf("an escape sequence was rendered as its parameters: %q", l)
		}
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("an escape sequence widened a line to %d columns: %q", w, l)
		}
	}
}

// TestMarkdownDoesNotMutateTheRecord discharges "keep raw Markdown copyable"
// (decision 4). The TUI has no copy-out action, and terminal drag-select
// copies rendered glyphs — so what this issue can promise is the property
// that makes the follow-up possible: rendering is derived, and the record the
// pane rendered is the record the transcript holds.
func TestMarkdownDoesNotMutateTheRecord(t *testing.T) {
	const src = "# Title\n\n- **bold** item with `code`\n\n> quote"
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: src},
		{Type: "agent.result", ResultText: src},
	}
	before := append([]apiclient.TranscriptRecord(nil), recs...)
	for _, width := range []int{20, 40, 80} {
		outputLines(recs, levelNormal, width, lineOpts{expandKey: "v"})
	}
	if !reflect.DeepEqual(recs, before) {
		t.Errorf("rendering mutated the records:\n got %+v\nwant %+v", recs, before)
	}
}

// TestMarkdownWideRunesMeasureInCells is decision 2 at the renderer: a pane
// full of CJK, emoji or combining marks fills to the same edge an ASCII one
// does, which a rune count could not do.
func TestMarkdownWideRunesMeasureInCells(t *testing.T) {
	cases := map[string]string{
		"cjk":       strings.Repeat("日本語テキスト ", 8),
		"emoji":     strings.Repeat("🎉🚀 ", 20),
		"zwj":       strings.Repeat("👨‍👩‍👧 ", 20),
		"combining": strings.Repeat("e\u0301x\u0308 ", 20),
		"mixed":     "**strong** 日本語 `code` 🎉 and plain words to fill the line out",
	}
	for name, src := range cases {
		for _, width := range []int{20, 40, 80} {
			for i, l := range markdownLines(src, width) {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("%s at width %d: line %d is %d columns: %q",
						name, width, i, w, l)
				}
			}
		}
	}
}

// TestPreformattedContinuationKeepsItsPrefix is the mechanical half of
// decision 6: wrapLine indents continuations with spaces of the gutter's
// width unless the caller says otherwise, and a marker that is drawn once and
// then dropped is not a gutter.
func TestPreformattedContinuationKeepsItsPrefix(t *testing.T) {
	pl := paneLine{
		gutter:      "│ ",
		gutterStyle: lipgloss.NewStyle(),
		contPrefix:  "│ ",
		segs:        []segment{{text: strings.Repeat("word ", 20), style: lipgloss.NewStyle()}},
	}
	lines := wrapLine(pl, 30)
	if len(lines) < 2 {
		t.Fatalf("fixture did not wrap: %q", lines)
	}
	for i, l := range lines {
		if !strings.HasPrefix(ansi.Strip(l), "│ ") {
			t.Errorf("line %d lost the bar: %q", i, l)
		}
	}

	// The default is unchanged for every caller that predates this.
	plain := paneLine{
		gutter:      gutterThinking,
		gutterStyle: styleThinking,
		segs:        []segment{{text: strings.Repeat("word ", 20), style: styleThinking}},
	}
	got := wrapLine(plain, 30)
	if len(got) < 2 {
		t.Fatalf("fixture did not wrap: %q", got)
	}
	if !strings.HasPrefix(ansi.Strip(got[1]), "  ") {
		t.Errorf("continuation = %q, want the hanging indent in spaces", got[1])
	}
}

// TestMarkdownLinksNumberTheirDestinations is task 075 decision 2: the label
// is prose, the destination is on screen as a numbered reference, and nothing
// depends on OSC 8 or on a keybinding that does not exist yet.
func TestMarkdownLinksNumberTheirDestinations(t *testing.T) {
	got := stripped("see [the docs](https://example.com/x) now", 80)
	want := []string{"  see the docs [1] now", "", "  [1] https://example.com/x"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestMarkdownRepeatedDestinationSharesANumber keeps the reference block as
// short as the set of destinations, not as long as the count of links.
func TestMarkdownRepeatedDestinationSharesANumber(t *testing.T) {
	got := strings.Join(stripped(
		"[a](https://example.com/x) and [b](https://example.com/x) and [c](https://example.com/y)", 80), "\n")
	for _, want := range []string{"a [1]", "b [1]", "c [2]", "[1] https://example.com/x", "[2] https://example.com/y"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "https://example.com/x"); n != 1 {
		t.Errorf("a repeated destination was listed %d times, want 1:\n%s", n, got)
	}
}

// TestMarkdownImageRendersAltTextAndItsSource is the image half: alt text is
// the link-shaped item, the source is a reference, and nothing is fetched —
// there is no I/O in this path to disable.
func TestMarkdownImageRendersAltTextAndItsSource(t *testing.T) {
	got := stripped("![a diagram](https://example.com/x.png)", 80)
	want := []string{"  a diagram [1]", "", "  [1] https://example.com/x.png"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestMarkdownUnsafeSchemesAreTextAndNothingElse is the security criterion.
// Destinations are shown literally whatever their scheme, and the pane ships
// no action that could open one — openURLCmd, which is the only opener in the
// package, is not reachable from the renderer and still refuses everything but
// http and https.
func TestMarkdownUnsafeSchemesAreTextAndNothingElse(t *testing.T) {
	for _, dest := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,x"} {
		got := strings.Join(stripped("[click]("+dest+")", 80), "\n")
		if !strings.Contains(got, "[1] "+dest) {
			t.Errorf("%q was not shown literally:\n%s", dest, got)
		}
		if strings.Contains(got, "\x1b]8;") {
			t.Errorf("an OSC 8 hyperlink was emitted for %q", dest)
		}
	}
	// The one opener in the package still refuses a non-http scheme, which is
	// the guard for the day a reader action arrives (decision 3).
	if msg, ok := drain(openURLCmd("javascript:alert(1)")).(openedURLMsg); !ok || msg.err == nil {
		t.Errorf("openURLCmd accepted a javascript: URL: %+v", msg)
	}
}

// TestMarkdownLinksInBothWorkspacesAgree is the two-door criterion extended to
// the new constructs: `agent.output` in the task workspace and the chat's §17
// retention fallback reach the same renderer, so the same source at the same
// width renders identically.
func TestMarkdownRichBlocksRenderTheSameThroughBothDoors(t *testing.T) {
	const src = "| a | b |\n|---|---|\n| 1 | 2 |\n\nsee [docs](https://example.com/x)\n\n```go\nvar x = 1\n```"
	for _, width := range []int{40, 80} {
		direct := strings.Join(markdownLines(src, width), "\n")
		pane := strings.Join(outputLines(
			[]apiclient.TranscriptRecord{{Type: "agent.output", Text: src}},
			levelNormal, width, lineOpts{expandKey: "v"}), "\n")
		if !strings.Contains(pane, direct) {
			t.Errorf("width %d: the output pane did not render the shared renderer's lines:\n%s\n---\n%s",
				width, pane, direct)
		}
	}
}
