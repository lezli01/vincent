package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Markdown tables in the output pane (task 075).
//
// Everything here asserts against ansi.Strip'ped lines, for the same reason
// the rest of the Markdown tests do: what is under test is that the structure
// survives without colour.

const tableSrc = "| Name | Count |\n|------|------:|\n| alpha | 1 |\n| beta | 22 |"

// wideTableSrc has a column whose longest token is wide enough that the table
// runs out of room at a width the pane actually reaches, which is what makes
// the degradation and the reflow observable at all.
const wideTableSrc = "| Name | Description |\n|------|-------------|\n" +
	"| alpha | supercalifragilistic expialidocious |\n" +
	"| beta | short |"

// TestTableRendersAlignedWhenItFits is the aligned form: a rule under the
// header, two spaces between columns, no borders, and the right-aligned column
// flush to its own edge.
func TestTableRendersAlignedWhenItFits(t *testing.T) {
	got := stripped(tableSrc, 40)
	want := []string{
		"  Name   Count",
		"  ─────  ─────",
		"  alpha      1",
		"  beta      22",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestTableKeepsEveryCellAtEveryWidth is the acceptance criterion stated as a
// sweep: no width loses a cell, and no produced line overflows the pane.
func TestTableKeepsEveryCellAtEveryWidth(t *testing.T) {
	for _, width := range []int{20, 30, 40, 60, 80} {
		lines := markdownLines(tableSrc, width)
		joined := strings.Join(stripped(tableSrc, width), "\n")
		for _, cell := range []string{"Name", "Count", "alpha", "beta", "22"} {
			if !strings.Contains(joined, cell) {
				t.Errorf("width %d lost %q:\n%s", width, cell, joined)
			}
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: line %d is %d columns: %q", width, i, w, l)
			}
		}
	}
}

// TestTableDegradesToStackedAtItsStatedWidth is the deterministic switch. The
// threshold is computed from the same rule the renderer uses rather than read
// off an observed rendering, so the test says *why* the form changed.
func TestTableDegradesToStackedAtItsStatedWidth(t *testing.T) {
	refs := &mdRefs{}
	blocks := parseMarkdown(wideTableSrc, refs)
	if len(blocks) != 1 || blocks[0].kind != mdTable {
		t.Fatalf("fixture did not parse as one table: %+v", blocks)
	}
	tbl := blocks[0].table

	// The narrowest available width that still lays out as a table.
	narrowest := 0
	for avail := 1; avail <= 80; avail++ {
		if _, ok := mdTableWidths(tbl, avail); ok {
			narrowest = avail
			break
		}
	}
	if narrowest == 0 {
		t.Fatal("the fixture never fits, at any width")
	}
	fits := stripped(wideTableSrc, narrowest+cols(gutterNone))
	if !strings.Contains(strings.Join(fits, "\n"), "─") {
		t.Errorf("at the stated minimum the table did not draw its rule:\n%s", strings.Join(fits, "\n"))
	}
	stacked := stripped(wideTableSrc, narrowest-1+cols(gutterNone))
	joined := strings.Join(stacked, "\n")
	if strings.Contains(joined, "─") {
		t.Errorf("one column below the minimum the table still drew a rule:\n%s", joined)
	}
	for _, want := range []string{
		"▪ Name: alpha", "Description:", "supercalifragilistic",
		"▪ Name: beta", "Description: short",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the stacked form lost %q:\n%s", want, joined)
		}
	}
	// Records are separated by a blank line, so the row boundary is not a
	// colour and holds when a cell is empty.
	if !strings.Contains(joined, "\n\n") {
		t.Errorf("the stacked form did not separate its records:\n%s", joined)
	}
	// Neither fallback is a clipped pipe table.
	if strings.Contains(joined, "|") {
		t.Errorf("the stacked form fell back to pipe syntax:\n%s", joined)
	}
}

// TestTableSurplusGoesWhereTheDemandIs asserts the allocation directly: at a
// width between the minimum and the natural sum, both columns grow and neither
// absorbs the whole surplus.
func TestTableSurplusGoesWhereTheDemandIs(t *testing.T) {
	src := "| a | b |\n|---|---|\n| " +
		strings.Repeat("xx ", 6) + "| " + strings.Repeat("yyyy ", 3) + "|"
	blocks := parseMarkdown(src, &mdRefs{})
	tbl := blocks[0].table

	full, ok := mdTableWidths(tbl, 200)
	if !ok {
		t.Fatal("the fixture does not fit even at 200 columns")
	}
	sum := full[0] + full[1] + mdTableGap
	avail := sum - 10
	got, ok := mdTableWidths(tbl, avail)
	if !ok {
		t.Fatalf("the fixture stacked at %d columns; it should still lay out", avail)
	}
	if got[0]+got[1]+mdTableGap != avail {
		t.Errorf("widths %v do not spend the %d columns available", got, avail)
	}
	minWidths := []int{cellMinWidth(tbl.rows[0][0]), cellMinWidth(tbl.rows[0][1])}
	for c := range got {
		if got[c] <= minWidths[c] {
			t.Errorf("column %d got %d, its minimum is %d: the surplus skipped it",
				c, got[c], minWidths[c])
		}
		if got[c] >= full[c] {
			t.Errorf("column %d got %d of a %d natural width: it absorbed the whole surplus",
				c, got[c], full[c])
		}
	}
}

// TestTableCellsSurviveInlineMarkdownAndWideRunes is the corruption criterion:
// the same fixtures task 073 used for prose, now inside cells. Every produced
// line has to measure within the pane and every column has to stay aligned.
func TestTableCellsSurviveInlineMarkdownAndWideRunes(t *testing.T) {
	src := strings.Join([]string{
		"| Item | Note |",
		"|------|------|",
		"| `go test ./...` | **strong** and *soft* text that is long enough to wrap |",
		"| 日本語テキスト | 🎉🚀 emoji and 👨‍👩‍👧 a ZWJ sequence |",
		"| e\u0301x\u0308 | a combining mark |",
	}, "\n")
	for _, width := range []int{30, 40, 60, 80} {
		lines := markdownLines(src, width)
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: line %d is %d columns: %q", width, i, w, l)
			}
		}
		plain := stripped(src, width)
		if !strings.Contains(strings.Join(plain, "\n"), "`go test ./...`") {
			t.Errorf("width %d broke the code cell, which is unbreakable:\n%s",
				width, strings.Join(plain, "\n"))
		}
		// Every body line of an aligned table starts its second column at the
		// same cell, which is the hanging alignment a wrapped cell needs.
		if !strings.Contains(strings.Join(plain, "\n"), "─") {
			continue // stacked at this width; alignment is not what it promises
		}
		want := -1
		for _, l := range plain {
			if !strings.Contains(l, "─") {
				continue
			}
			if want = strings.Index(l, "  ─"); want >= 0 {
				break
			}
		}
		if want < 0 {
			t.Errorf("width %d drew a rule with only one column: %q", width, plain)
		}
	}
}

// TestTableNeedsItsDelimiterRow is what keeps prose containing a pipe from
// becoming a grid.
func TestTableNeedsItsDelimiterRow(t *testing.T) {
	for name, src := range map[string]string{
		"a lone row":        "| a | b |",
		"a lone rule":       "|---|---|",
		"prose with pipes":  "run a | b | c to see it",
		"a mismatched rule": "| a | b |\n|---|",
	} {
		t.Run(name, func(t *testing.T) {
			got := strings.Join(stripped(src, 80), "\n")
			for _, line := range strings.Split(src, "\n") {
				if !strings.Contains(got, line) {
					t.Errorf("%q became a table rather than staying a paragraph:\n%s", line, got)
				}
			}
		})
	}
}

// TestTableCellsMayLinkAndTheReferenceIsNumberedInOrder proves the two
// features compose: a link inside a cell is resolved at parse time, so its
// number follows document order like any other.
func TestTableCellsMayLinkAndTheReferenceIsNumberedInOrder(t *testing.T) {
	src := "see [first](https://example.com/a)\n\n| Doc | Where |\n|-----|-------|\n" +
		"| api | [second](https://example.com/b) |"
	got := strings.Join(stripped(src, 80), "\n")
	for _, want := range []string{
		"first [1]", "second [2]",
		"[1] https://example.com/a", "[2] https://example.com/b",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
}

// TestTableReflowsFromSource is the resize criterion for the new block kind:
// a table re-lays out from its Markdown rather than from a drawn grid.
func TestTableReflowsFromSource(t *testing.T) {
	wide := strings.Join(markdownLines(wideTableSrc, 80), "\n")
	narrow := strings.Join(markdownLines(wideTableSrc, 40), "\n")
	if wide == narrow {
		t.Fatal("the fixture does not reflow; it proves nothing")
	}
	if again := strings.Join(markdownLines(wideTableSrc, 40), "\n"); again != narrow {
		t.Error("a render after a wider render differed from a fresh one")
	}
}

// TestTableMonochromeKeepsHeaderAndRowBoundaries is §15's rule applied to the
// grid: strip the colour and the header, the rule and every row boundary are
// still characters.
func TestTableMonochromeKeepsHeaderAndRowBoundaries(t *testing.T) {
	got := strings.Join(stripped(tableSrc, 40), "\n")
	if !strings.Contains(got, "Name") || !strings.Contains(got, "─") {
		t.Errorf("the header lost its meaning without colour:\n%s", got)
	}
	if lines := stripped(tableSrc, 40); len(lines) != 4 {
		t.Errorf("a two-row table drew %d lines, want a header, a rule and two rows", len(lines))
	}
}
