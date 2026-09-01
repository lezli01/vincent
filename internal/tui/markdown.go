package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Markdown rendering for assistant prose (task 073, §15).
//
// Only assistant prose reaches this file: `agent.output` records in both
// workspaces, and the chat's §17 retention fallback. Everything else the pane
// renders — reasoning, tool calls, tool results, command output, errors,
// vincent's own records — stays literal, because command output and tool
// summaries routinely contain Markdown punctuation nobody meant as formatting
// (decision 5).
//
// The parser is hand-rolled over a written-down subset rather than delegated
// to goldmark or glamour (decision 1). glamour in particular emits a
// pre-styled ANSI block with its own wrapping and margins, which cannot be
// folded into the two-column activity gutter and would break the
// wrap-plain-then-style invariant §15 rests on. The subset is exactly:
//
//	headings, paragraphs, emphasis, strong, ordered and unordered lists,
//	nested lists, blockquotes, inline code, fenced code, horizontal rules.
//
// Everything outside it — tables, links, reference links, HTML blocks,
// footnotes, setext headings, backslash escapes — renders as literal text.
// That is the boundary, and it is a list rather than "whatever CommonMark
// says", so a construct either appears above or it is safe text.
//
// Markdown structure lives *inside* the assistant content column (decision 6).
// The activity gutter is untouched: prose still gets gutterNone, and a list
// marker, a blockquote bar or a code block's rail is composed after it, the
// way toolResultLine composes gutterResult + mark. wrapLine's hanging indent
// then falls out for free.

var (
	// styleMDHeading is the section marker and its text. It carries a glyph
	// as well as a colour because a monochrome terminal must still see the
	// structure — colour alone would not survive an SSH session, which is
	// the same reason §15's gutters are glyphs.
	styleMDHeading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	// styleMDMarker paints list markers and the code block's rail. Markers
	// are not prose, so they are dimmer than what they introduce.
	styleMDMarker = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	styleMDQuote  = lipgloss.NewStyle().Faint(true).Italic(true)
	styleMDRule   = lipgloss.NewStyle().Faint(true)
	styleMDCode   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// Content-column prefixes. Every one is a single cell wide plus its trailing
// space, so a nested structure's indent arithmetic is the count of levels.
const (
	mdHeadingMark = "▌ "
	mdQuoteBar    = "│ "
	mdCodeRail    = "▏ "
)

// mdBulletsByDepth cycles the unordered marker so a nested list is readable
// without counting columns. Past the third level the shape repeats; the
// indent is what carries depth by then.
var mdBulletsByDepth = []string{"• ", "◦ ", "▪ "}

// mdKind classifies a block, which is all the vertical-spacing rule needs to
// know: consecutive list items are one list and get no blank between them, a
// heading is followed straight by what it introduces, and everything else is
// separated by one line.
type mdKind int

const (
	mdParagraph mdKind = iota
	mdHeading
	mdItem
	mdQuote
	mdCode
	mdRule
)

// mdBlock is one parsed block. Its lines are laid out but not yet wrapped,
// except mdRule, which is drawn to the pane's width at render time and so
// carries no lines at all.
type mdBlock struct {
	kind  mdKind
	lines []paneLine
}

// markdownLines renders assistant prose to wrapped pane lines.
//
// Reflow is always from this text: nothing here caches a rendered line, so
// re-rendering at a new width re-parses the Markdown rather than re-wrapping
// its own escape sequences. The record it was handed is never mutated —
// rendering is derived, and the transcript on disk stays the agent's bytes
// (decision 4).
func markdownLines(text string, width int) []string {
	blocks := parseMarkdown(sanitizeText(text))
	out := make([]string, 0, len(blocks)*2)
	prev := mdParagraph
	for _, b := range blocks {
		if len(out) > 0 && mdNeedsBlank(prev, b.kind) {
			out = append(out, "")
		}
		out = append(out, renderMDBlock(b, width)...)
		prev = b.kind
	}
	return out
}

// mdNeedsBlank is the vertical-spacing rule, which is restrained on purpose:
// one blank line between blocks, none between the items of a list, and none
// between a heading and what it introduces — a heading floating a line above
// its own list reads as a heading about the blank.
func mdNeedsBlank(prev, next mdKind) bool {
	if prev == mdHeading {
		return false
	}
	return prev != mdItem || next != mdItem
}

// renderMDBlock wraps one block to the pane.
func renderMDBlock(b mdBlock, width int) []string {
	if b.kind == mdRule {
		// Width-bounded rather than a fixed run of dashes: a rule is the
		// separator the pane is wide, and one that stopped short would read
		// as a line of text.
		n := max(width-cols(gutterNone), 1)
		return []string{gutterNone + styleMDRule.Render(strings.Repeat("─", n))}
	}
	out := make([]string, 0, len(b.lines))
	for _, pl := range b.lines {
		out = append(out, wrapLine(pl, width)...)
	}
	return out
}

// mdPending is the block being accumulated. Paragraphs, list items and
// blockquotes all span several source lines, and a fenced block spans every
// line until its closing fence.
type mdPending struct {
	kind    mdKind
	open    bool
	text    []string
	marker  string
	depth   int
	fence   byte
	fenceLn int
}

// parseMarkdown scans the text a line at a time. It is a scanner rather than
// a grammar because the subset is a list of line shapes: a line either opens
// a fence, is a fence's content, is a rule, a heading, a quote or a list
// item, or it is prose that continues whatever came before it.
func parseMarkdown(text string) []mdBlock {
	src := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var (
		blocks []mdBlock
		pend   mdPending
		// stack holds the leading-space column of every open list level, so
		// nesting depth comes from the source's own indentation rather than
		// from a fixed step nobody agreed on.
		stack []int
	)
	flush := func() {
		if b, ok := pend.block(); ok {
			blocks = append(blocks, b)
		}
		pend = mdPending{}
	}
	// endList closes any open list: a heading, a rule, a fence, a quote or a
	// paragraph at the margin all end one.
	endList := func() { stack = stack[:0] }

	for _, line := range src {
		if pend.open {
			if isFenceClose(line, pend.fence, pend.fenceLn) {
				flush()
				continue
			}
			pend.text = append(pend.text, line)
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if ch, n, ok := fenceOpen(line); ok {
			flush()
			endList()
			pend = mdPending{kind: mdCode, open: true, fence: ch, fenceLn: n}
			continue
		}
		if isRule(trimmed) {
			flush()
			endList()
			blocks = append(blocks, mdBlock{kind: mdRule})
			continue
		}
		if level, htext, ok := headingOf(line); ok {
			flush()
			endList()
			blocks = append(blocks, mdBlock{
				kind:  mdHeading,
				lines: []paneLine{headingPaneLine(level, htext)},
			})
			continue
		}
		if depth, body, ok := quoteOf(line); ok {
			if pend.kind == mdQuote && pend.depth == depth && !pend.isEmpty() {
				pend.text = append(pend.text, body)
				continue
			}
			flush()
			endList()
			pend = mdPending{kind: mdQuote, depth: depth, text: []string{body}}
			continue
		}
		if indent, marker, body, ok := listItemOf(line); ok {
			flush()
			depth := listDepth(&stack, indent)
			pend = mdPending{kind: mdItem, depth: depth, marker: marker, text: []string{body}}
			continue
		}
		// Plain prose. It continues a paragraph or an item — a wrapped list
		// item's second source line belongs to the item, not to a new
		// paragraph at the margin — and otherwise opens a paragraph.
		if pend.kind == mdParagraph || pend.kind == mdItem {
			if !pend.isEmpty() {
				pend.text = append(pend.text, strings.TrimSpace(line))
				continue
			}
		}
		flush()
		endList()
		pend = mdPending{kind: mdParagraph, text: []string{strings.TrimSpace(line)}}
	}
	flush()
	return blocks
}

func (p mdPending) isEmpty() bool { return len(p.text) == 0 }

// block turns the accumulated lines into a block. Soft line breaks inside a
// paragraph, a list item or a quote are joined: they are the author's line
// length, not the reader's, and rejoining is what lets the pane reflow to its
// own width. A fenced block is the exception and keeps every line.
func (p mdPending) block() (mdBlock, bool) {
	switch p.kind {
	case mdCode:
		if !p.open && len(p.text) == 0 {
			return mdBlock{}, false
		}
		lines := make([]paneLine, 0, len(p.text))
		for _, l := range p.text {
			lines = append(lines, codePaneLine(l))
		}
		if len(lines) == 0 {
			// An empty fence still says a code block was there.
			lines = append(lines, codePaneLine(""))
		}
		return mdBlock{kind: mdCode, lines: lines}, true
	case mdItem:
		if p.isEmpty() {
			return mdBlock{}, false
		}
		return mdBlock{
			kind:  mdItem,
			lines: []paneLine{itemPaneLine(p.depth, p.marker, strings.Join(p.text, " "))},
		}, true
	case mdQuote:
		if p.isEmpty() {
			return mdBlock{}, false
		}
		return mdBlock{
			kind:  mdQuote,
			lines: []paneLine{quotePaneLine(p.depth, strings.Join(p.text, " "))},
		}, true
	default:
		if p.isEmpty() {
			return mdBlock{}, false
		}
		return mdBlock{
			kind:  mdParagraph,
			lines: []paneLine{paragraphPaneLine(strings.Join(p.text, " "))},
		}, true
	}
}

// listDepth resolves an item's indentation against the open list levels. A
// deeper indent than the innermost open level opens a level; a shallower one
// closes levels until it fits. The source's own columns decide, so a document
// indenting by two spaces and one indenting by four both nest once.
func listDepth(stack *[]int, indent int) int {
	s := *stack
	for len(s) > 0 && indent < s[len(s)-1] {
		s = s[:len(s)-1]
	}
	if len(s) == 0 || indent > s[len(s)-1] {
		s = append(s, indent)
	}
	*stack = s
	return len(s) - 1
}

// paragraphPaneLine is prose: no marker, flush against the gutter, exactly
// where an unformatted assistant message already sat.
func paragraphPaneLine(text string) paneLine {
	base := lipgloss.NewStyle()
	return paneLine{
		gutter:      gutterNone,
		gutterStyle: base,
		segs:        inlineSegments(text, base, 0),
	}
}

// headingPaneLine marks a section. The glyph is what carries the heading in
// monochrome; the level is the indent in front of it, so a subsection sits
// under its section without spending a second glyph on it.
func headingPaneLine(level int, text string) paneLine {
	return paneLine{
		gutter:      gutterNone + strings.Repeat(" ", level-1) + mdHeadingMark,
		gutterStyle: styleMDHeading,
		segs:        inlineSegments(text, styleMDHeading, 0),
	}
}

// itemPaneLine is one list item. The marker goes in the content column's own
// gutter, which is what gives a wrapped item its hanging indent for free.
func itemPaneLine(depth int, marker, text string) paneLine {
	if marker == "" {
		marker = mdBulletsByDepth[min(depth, len(mdBulletsByDepth)-1)]
	}
	return paneLine{
		gutter:      gutterNone + strings.Repeat("  ", depth) + marker,
		gutterStyle: styleMDMarker,
		segs:        inlineSegments(text, lipgloss.NewStyle(), 0),
	}
}

// quotePaneLine draws the bar on every line of the quote, not just the first:
// wrapLine indents continuations with spaces of the gutter's width by
// default, which would put the bar on the first line of a wrapped quote and
// drop it from the rest — a content gutter that is not a gutter (decision 6).
func quotePaneLine(depth int, text string) paneLine {
	bar := gutterNone + strings.Repeat(mdQuoteBar, depth)
	return paneLine{
		gutter:      bar,
		gutterStyle: styleMDQuote,
		contPrefix:  bar,
		segs:        inlineSegments(text, styleMDQuote, 0),
	}
}

// codePaneLine is one line of a fenced block, kept byte for byte. It is
// preformatted, so it hard-wraps at the cell boundary rather than going
// through splitWords, which collapses runs of whitespace: a code block that
// lost its indentation would not be the code the agent wrote (decision 3).
func codePaneLine(text string) paneLine {
	rail := gutterNone + mdCodeRail
	return paneLine{
		gutter:      rail,
		gutterStyle: styleMDMarker,
		contPrefix:  rail,
		pre:         true,
		segs:        []segment{{text: text, style: styleMDCode}},
	}
}

// fenceOpen reports an opening fence and its run, which the closing fence
// must match or exceed.
func fenceOpen(line string) (byte, int, bool) {
	t := strings.TrimLeft(line, " ")
	if t == "" {
		return 0, 0, false
	}
	ch := t[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	n := runLen(t, 0, ch)
	if n < 3 {
		return 0, 0, false
	}
	// An info string may not contain a backtick, which is what keeps
	// ``a`` in prose from opening a block.
	if ch == '`' && strings.ContainsRune(t[n:], '`') {
		return 0, 0, false
	}
	return ch, n, true
}

func isFenceClose(line string, ch byte, n int) bool {
	t := strings.TrimSpace(line)
	if t == "" || t[0] != ch {
		return false
	}
	return runLen(t, 0, ch) >= n && strings.Trim(t, string(ch)) == ""
}

// isRule matches the three spellings of a thematic break. It is checked
// before list items so `- - -` is a rule rather than an item whose text is a
// dash.
func isRule(trimmed string) bool {
	stripped := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, trimmed)
	if len(stripped) < 3 {
		return false
	}
	switch stripped[0] {
	case '-', '*', '_':
	default:
		return false
	}
	return strings.Trim(stripped, string(stripped[0])) == ""
}

// headingOf matches an ATX heading. Setext underlines are outside the subset:
// `---` under a line of prose is a rule here, which is what it looks like.
func headingOf(line string) (int, string, bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || !strings.HasPrefix(t, "#") {
		return 0, "", false
	}
	level := runLen(t, 0, '#')
	if level > 6 {
		return 0, "", false
	}
	rest := t[level:]
	if rest != "" && rest[0] != ' ' {
		// `#hashtag` is not a heading, which is the whole reason the space
		// is required.
		return 0, "", false
	}
	// A trailing run of hashes is the closing sequence, not text.
	return level, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), "#")), true
}

// quoteOf strips the quote markers and reports how deep they went.
func quoteOf(line string) (int, string, bool) {
	t := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(t, ">") {
		return 0, "", false
	}
	depth := 0
	for strings.HasPrefix(t, ">") {
		depth++
		t = strings.TrimPrefix(t[1:], " ")
		t = strings.TrimLeft(t, " ")
	}
	return depth, t, true
}

// listItemOf matches a bullet or a number. An empty marker means "use the
// bullet for this depth"; an ordered item keeps the number the author wrote,
// because a list that renumbers itself loses the reference the prose made
// to step 3.
func listItemOf(line string) (int, string, string, bool) {
	t := strings.TrimLeft(line, " \t")
	indent := indentCols(line[:len(line)-len(t)])
	if t == "" {
		return 0, "", "", false
	}
	switch t[0] {
	case '-', '*', '+':
		if len(t) > 1 && t[1] == ' ' {
			return indent, "", strings.TrimSpace(t[2:]), true
		}
		return 0, "", "", false
	}
	digits := 0
	for digits < len(t) && t[digits] >= '0' && t[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits > 9 || digits+1 >= len(t) {
		return 0, "", "", false
	}
	if t[digits] != '.' && t[digits] != ')' {
		return 0, "", "", false
	}
	if t[digits+1] != ' ' {
		return 0, "", "", false
	}
	return indent, t[:digits] + ". ", strings.TrimSpace(t[digits+2:]), true
}

// indentCols counts a prefix's columns, a tab being the four a terminal's
// default tab stop is closest to.
func indentCols(prefix string) int {
	n := 0
	for _, r := range prefix {
		if r == '\t' {
			n += 4
			continue
		}
		n++
	}
	return n
}

// inlineSegments splits one line of prose into styled runs. It emits the
// existing segment model directly, which is what keeps the wrap-plain-then-
// style invariant: wrapLine measures every token as plain text and styles the
// runs afterwards, so no break can land inside an escape sequence
// (decision 2).
//
// depth guards the recursion `**bold with `code`**` needs; nesting past it
// renders literally rather than growing a stack.
func inlineSegments(text string, base lipgloss.Style, depth int) []segment {
	segs := make([]segment, 0, 4)
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			segs = append(segs, segment{text: lit.String(), style: base})
			lit.Reset()
		}
	}
	for i := 0; i < len(text); {
		switch text[i] {
		case '`':
			// Inline code wins over emphasis, and keeps its delimiters: the
			// backticks are what a monochrome terminal has left once the
			// colour is gone.
			if body, next, ok := codeSpanAt(text, i); ok {
				flush()
				segs = append(segs, segment{text: body, style: mdCodeSpanStyle(base)})
				i = next
				continue
			}
		case '*', '_':
			if body, marks, next, ok := emphasisAt(text, i); ok && depth < 3 {
				flush()
				style := base.Italic(true)
				if marks >= 2 {
					style = base.Bold(true)
				}
				segs = append(segs, inlineSegments(body, style, depth+1)...)
				i = next
				continue
			}
		}
		lit.WriteByte(text[i])
		i++
	}
	flush()
	if len(segs) == 0 {
		return []segment{{text: text, style: base}}
	}
	return segs
}

// mdCodeSpanStyle tints an inline code span without losing whatever the
// surrounding context already set — a code span inside a heading stays bold.
func mdCodeSpanStyle(base lipgloss.Style) lipgloss.Style {
	return base.Foreground(lipgloss.Color("3"))
}

// codeSpanAt matches a backtick run and its partner, returning the span with
// its delimiters intact.
func codeSpanAt(text string, i int) (string, int, bool) {
	n := runLen(text, i, '`')
	closer := strings.Repeat("`", n)
	rest := text[i+n:]
	end := strings.Index(rest, closer)
	if end < 0 {
		return "", 0, false
	}
	return text[i : i+n+end+n], i + n + end + n, true
}

// emphasisAt matches `*x*`, `**x**` and their underscore spellings.
//
// The underscore spelling is only honored at a word boundary. Agents write
// file names, identifiers and flags constantly, and `main_test.go` turning
// half a sentence italic is worse than an underscore that stayed literal.
func emphasisAt(text string, i int) (string, int, int, bool) {
	ch := text[i]
	marks := min(runLen(text, i, ch), 2)
	delim := strings.Repeat(string(ch), marks)
	rest := text[i+marks:]
	if rest == "" || rest[0] == ' ' {
		return "", 0, 0, false
	}
	end := strings.Index(rest, delim)
	if end <= 0 {
		return "", 0, 0, false
	}
	body := rest[:end]
	if strings.HasSuffix(body, " ") {
		return "", 0, 0, false
	}
	next := i + marks + end + marks
	if ch == '_' && (!wordBoundary(text, i-1) || !wordBoundary(text, next)) {
		return "", 0, 0, false
	}
	return body, marks, next, true
}

// wordBoundary reports whether the byte at i is outside a word, the string's
// edges counting as outside.
func wordBoundary(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return true
	}
	c := text[i]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return false
	}
	return true
}

// runLen counts how many times the byte at i repeats.
func runLen(s string, i int, ch byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == ch {
		n++
	}
	return n
}
