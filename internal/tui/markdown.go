package tui

import (
	"strconv"
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
//	nested lists, blockquotes, inline code, fenced code, horizontal rules,
//	tables, inline links, inline images (task 075).
//
// Everything outside it — reference links, autolinks, bare URLs, titled
// links, HTML blocks, footnotes, setext headings, backslash escapes —
// renders as literal text. That is the boundary, and it is a list rather than
// "whatever CommonMark says", so a construct either appears above or it is
// safe text.
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
	// styleMDRef paints a link's `[n]` marker and the reference block that
	// resolves it (task 075 decision 2). Both are dim: the destination is
	// what a reader goes looking for, not what they read past.
	styleMDRef = lipgloss.NewStyle().Faint(true)
	// styleMDLang is the fenced block's info string, drawn at the rail.
	styleMDLang = lipgloss.NewStyle().Faint(true).Italic(true)
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
	mdTable
)

// mdBlock is one parsed block. Its lines are laid out but not yet wrapped,
// except mdRule, which is drawn to the pane's width at render time and so
// carries no lines at all, and mdTable, whose layout is two-dimensional and
// so cannot be a list of paneLines either.
type mdBlock struct {
	kind  mdKind
	lines []paneLine
	// info is a fenced block's info string, first word only. Empty for
	// every other kind and for a fence that declared no language.
	info string
	// table is set on mdTable and nil everywhere else.
	table *mdTableBlock
}

// markdownLines renders assistant prose to wrapped pane lines.
//
// Reflow is always from this text: nothing here caches a rendered line, so
// re-rendering at a new width re-parses the Markdown rather than re-wrapping
// its own escape sequences. The record it was handed is never mutated —
// rendering is derived, and the transcript on disk stays the agent's bytes
// (decision 4).
func markdownLines(text string, width int) []string {
	refs := &mdRefs{}
	blocks := parseMarkdown(sanitizeText(text), refs)
	out := make([]string, 0, len(blocks)*2)
	prev := mdParagraph
	for _, b := range blocks {
		if len(out) > 0 && mdNeedsBlank(prev, b.kind) {
			out = append(out, "")
		}
		out = append(out, renderMDBlock(b, width)...)
		prev = b.kind
	}
	if lines := renderMDRefs(refs, width); len(lines) > 0 {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	return out
}

// mdRefs numbers the destinations one rendered message links to (task 075
// decision 2). Numbering is per message and by first appearance, and two
// identical destinations share a number: what the reference block is for is
// putting the exact destination on screen without an OSC 8 hyperlink and
// without a keybinding, and repeating a URL under two numbers would only make
// that list longer.
type mdRefs struct {
	order []string
	index map[string]int
}

// add returns the 1-based number for a destination, assigning one on first
// sight.
func (r *mdRefs) add(dest string) int {
	if n, ok := r.index[dest]; ok {
		return n
	}
	if r.index == nil {
		r.index = make(map[string]int, 4)
	}
	r.order = append(r.order, dest)
	r.index[dest] = len(r.order)
	return len(r.order)
}

// renderMDRefs draws the reference block. Its lines are preformatted, so a
// destination is never folded at a space it happens to contain and a long one
// hard-wraps at the cell boundary exactly as a long code line does.
func renderMDRefs(r *mdRefs, width int) []string {
	if r == nil || len(r.order) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.order))
	for i, dest := range r.order {
		pl := paneLine{
			gutter:      gutterNone,
			gutterStyle: styleMDRef,
			pre:         true,
			segs: []segment{
				{text: "[" + strconv.Itoa(i+1) + "] ", style: styleMDRef},
				{text: dest, style: styleMDRef},
			},
		}
		out = append(out, wrapLine(pl, width)...)
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
	switch b.kind {
	case mdRule:
		// Width-bounded rather than a fixed run of dashes: a rule is the
		// separator the pane is wide, and one that stopped short would read
		// as a line of text.
		n := max(width-cols(gutterNone), 1)
		return []string{gutterNone + styleMDRule.Render(strings.Repeat("─", n))}
	case mdTable:
		return renderMDTable(b.table, width)
	}
	out := make([]string, 0, len(b.lines)+1)
	if b.kind == mdCode && b.info != "" {
		// The declared language, at the block's own rail. Dim and one word,
		// because it labels the block rather than being part of it — and it
		// is text, so a monochrome terminal reads it too.
		out = append(out, wrapLine(paneLine{
			gutter:      gutterNone + mdCodeRail,
			gutterStyle: styleMDMarker,
			contPrefix:  gutterNone + mdCodeRail,
			pre:         true,
			segs:        []segment{{text: b.info, style: styleMDLang}},
		}, width)...)
	}
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
	info    string
	refs    *mdRefs
}

// parseMarkdown scans the text a line at a time. It is a scanner rather than
// a grammar because the subset is a list of line shapes: a line either opens
// a fence, is a fence's content, is a rule, a heading, a quote or a list
// item, or it is prose that continues whatever came before it.
func parseMarkdown(text string, refs *mdRefs) []mdBlock {
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

	for i := 0; i < len(src); i++ {
		line := src[i]
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
		if ch, n, info, ok := fenceOpen(line); ok {
			flush()
			endList()
			pend = mdPending{kind: mdCode, open: true, fence: ch, fenceLn: n, info: info}
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
				lines: []paneLine{headingPaneLine(level, htext, refs)},
			})
			continue
		}
		// A table is recognized only with its delimiter row. Without it the
		// pipes stay a paragraph, which is what keeps prose containing `|`
		// from becoming a grid (task 075).
		if tbl, next, ok := tableAt(src, i, refs); ok {
			flush()
			endList()
			blocks = append(blocks, mdBlock{kind: mdTable, table: tbl})
			i = next - 1
			continue
		}
		if depth, body, ok := quoteOf(line); ok {
			if pend.kind == mdQuote && pend.depth == depth && !pend.isEmpty() {
				pend.text = append(pend.text, body)
				continue
			}
			flush()
			endList()
			pend = mdPending{kind: mdQuote, depth: depth, text: []string{body}, refs: refs}
			continue
		}
		if indent, marker, body, ok := listItemOf(line); ok {
			flush()
			depth := listDepth(&stack, indent)
			pend = mdPending{kind: mdItem, depth: depth, marker: marker, text: []string{body}, refs: refs}
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
		pend = mdPending{kind: mdParagraph, text: []string{strings.TrimSpace(line)}, refs: refs}
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
			lines = append(lines, codePaneLine(l, p.info))
		}
		if len(lines) == 0 {
			// An empty fence still says a code block was there.
			lines = append(lines, codePaneLine("", p.info))
		}
		return mdBlock{kind: mdCode, lines: lines, info: p.info}, true
	case mdItem:
		if p.isEmpty() {
			return mdBlock{}, false
		}
		return mdBlock{
			kind:  mdItem,
			lines: []paneLine{itemPaneLine(p.depth, p.marker, strings.Join(p.text, " "), p.refs)},
		}, true
	case mdQuote:
		if p.isEmpty() {
			return mdBlock{}, false
		}
		return mdBlock{
			kind:  mdQuote,
			lines: []paneLine{quotePaneLine(p.depth, strings.Join(p.text, " "), p.refs)},
		}, true
	default:
		if p.isEmpty() {
			return mdBlock{}, false
		}
		return mdBlock{
			kind:  mdParagraph,
			lines: []paneLine{paragraphPaneLine(strings.Join(p.text, " "), p.refs)},
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
func paragraphPaneLine(text string, refs *mdRefs) paneLine {
	base := lipgloss.NewStyle()
	return paneLine{
		gutter:      gutterNone,
		gutterStyle: base,
		segs:        inlineSegments(text, base, 0, refs),
	}
}

// headingPaneLine marks a section. The glyph is what carries the heading in
// monochrome; the level is the indent in front of it, so a subsection sits
// under its section without spending a second glyph on it.
func headingPaneLine(level int, text string, refs *mdRefs) paneLine {
	return paneLine{
		gutter:      gutterNone + strings.Repeat(" ", level-1) + mdHeadingMark,
		gutterStyle: styleMDHeading,
		segs:        inlineSegments(text, styleMDHeading, 0, refs),
	}
}

// itemPaneLine is one list item. The marker goes in the content column's own
// gutter, which is what gives a wrapped item its hanging indent for free.
func itemPaneLine(depth int, marker, text string, refs *mdRefs) paneLine {
	if marker == "" {
		marker = mdBulletsByDepth[min(depth, len(mdBulletsByDepth)-1)]
	}
	return paneLine{
		gutter:      gutterNone + strings.Repeat("  ", depth) + marker,
		gutterStyle: styleMDMarker,
		segs:        inlineSegments(text, lipgloss.NewStyle(), 0, refs),
	}
}

// quotePaneLine draws the bar on every line of the quote, not just the first:
// wrapLine indents continuations with spaces of the gutter's width by
// default, which would put the bar on the first line of a wrapped quote and
// drop it from the rest — a content gutter that is not a gutter (decision 6).
func quotePaneLine(depth int, text string, refs *mdRefs) paneLine {
	bar := gutterNone + strings.Repeat(mdQuoteBar, depth)
	return paneLine{
		gutter:      bar,
		gutterStyle: styleMDQuote,
		contPrefix:  bar,
		segs:        inlineSegments(text, styleMDQuote, 0, refs),
	}
}

// codePaneLine is one line of a fenced block, kept byte for byte. It is
// preformatted, so it hard-wraps at the cell boundary rather than going
// through splitWords, which collapses runs of whitespace: a code block that
// lost its indentation would not be the code the agent wrote (decision 3).
//
// The line is split into highlight runs (task 075), which is a styling of the
// same bytes and never a rewriting of them: concatenating the segments
// reproduces the argument exactly, so stripping the escape sequences from a
// rendered block gives back the fence's content.
func codePaneLine(text, lang string) paneLine {
	rail := gutterNone + mdCodeRail
	return paneLine{
		gutter:      rail,
		gutterStyle: styleMDMarker,
		contPrefix:  rail,
		pre:         true,
		segs:        highlightSegments(lang, text),
	}
}

// fenceOpen reports an opening fence, its run — which the closing fence must
// match or exceed — and the first word of its info string, which is the
// declared language (task 075). The rest of the info string is dropped: it is
// attribute syntax nobody agreed on, and what the header names is a language.
func fenceOpen(line string) (byte, int, string, bool) {
	t := strings.TrimLeft(line, " ")
	if t == "" {
		return 0, 0, "", false
	}
	ch := t[0]
	if ch != '`' && ch != '~' {
		return 0, 0, "", false
	}
	n := runLen(t, 0, ch)
	if n < 3 {
		return 0, 0, "", false
	}
	// An info string may not contain a backtick, which is what keeps
	// ``a`` in prose from opening a block.
	if ch == '`' && strings.ContainsRune(t[n:], '`') {
		return 0, 0, "", false
	}
	info := ""
	if fields := strings.Fields(t[n:]); len(fields) > 0 {
		info = fields[0]
	}
	return ch, n, info, true
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
//
// refs collects link and image destinations for the message's reference block
// (task 075 decision 2). It is never nil on the paths that reach here from
// markdownLines; a nil registry renders a link as its literal source, which is
// what a caller that cannot show a reference block should get.
func inlineSegments(text string, base lipgloss.Style, depth int, refs *mdRefs) []segment {
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
		case '[', '!':
			// An inline link or image. The label is prose and the
			// destination becomes a numbered reference; nothing here opens,
			// fetches or hyperlinks anything (decisions 2 and 3).
			if label, dest, next, ok := linkAt(text, i); ok && depth < 3 && refs != nil {
				flush()
				if label == "" {
					// An image with no alt text, or `[](x)`: the marker is
					// all that is left to hang the reference on.
					label = dest
				}
				segs = append(segs, inlineSegments(label, base, depth+1, refs)...)
				segs = append(segs, segment{
					text:  " [" + strconv.Itoa(refs.add(dest)) + "]",
					style: base.Faint(true),
				})
				i = next
				continue
			}
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
				segs = append(segs, inlineSegments(body, style, depth+1, refs)...)
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

// linkAt matches an inline link `[label](dest)` and an inline image
// `![alt](src)`, and nothing else (task 075 decision 4).
//
// The destination may not contain whitespace, which is what makes the titled
// form `[label](url "title")` fall out of the subset without a rule of its
// own, and reference links, autolinks and bare URLs never reach here at all.
// A construct that does not match is left to the caller as literal text.
func linkAt(text string, i int) (string, string, int, bool) {
	open := i
	if text[open] == '!' {
		open++
	}
	if open >= len(text) || text[open] != '[' {
		return "", "", 0, false
	}
	label := strings.IndexByte(text[open+1:], ']')
	if label < 0 {
		return "", "", 0, false
	}
	rest := text[open+1+label+1:]
	if !strings.HasPrefix(rest, "(") {
		return "", "", 0, false
	}
	// The closing paren is the balanced one, so a destination that contains
	// a pair of its own — `javascript:alert(1)` as much as a wikipedia URL —
	// is captured whole rather than cut at the first `)`.
	paren, depth := 0, 0
	for j := range len(rest) {
		switch rest[j] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 {
			paren = j
			break
		}
	}
	// paren < 2 is either no balanced closing paren or an empty destination.
	if paren < 2 {
		return "", "", 0, false
	}
	dest := rest[1:paren]
	if strings.ContainsAny(dest, " \t[]") {
		return "", "", 0, false
	}
	return text[open+1 : open+1+label], dest, open + label + paren + 3, true
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
