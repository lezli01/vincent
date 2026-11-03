package render

import (
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLToMarkdown converts a Jira-rendered HTML body to Markdown in the TUI's
// subset (see the package doc).
//
// base is the binding's site (for example https://acme.atlassian.net/):
// every relative href and src is resolved against it, because Jira renders
// attachments and profile links site-relative. A nil base leaves relative
// URLs as they are. Only http, https and mailto destinations survive as
// links or images; any other scheme (javascript:, data:, …) degrades to the
// element's text.
//
// It never fails: unknown elements degrade to their text, malformed markup
// is repaired by the HTML5 tree builder, and the output is deterministic —
// the same input and base always give the same bytes. The result is trimmed
// and carries no trailing newline.
func HTMLToMarkdown(src string, base *url.URL) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	context := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(src), context)
	if err != nil {
		// A strings.Reader never fails, so this is unreachable; degrade to
		// the text rather than invent an error the contract does not have.
		return oneLine(src)
	}
	c := &converter{base: base, memo: map[*html.Node]bool{}}
	out := strings.Join(c.blocks(nodes), "\n\n")
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// converter carries the per-call state. memo caches containsBlock so a deep
// chain of inline wrappers is not rescanned at every level; it is only ever
// looked up, never iterated, so it cannot reorder output.
type converter struct {
	base *url.URL
	memo map[*html.Node]bool
}

// blockTags are the elements that start a block of their own. Anything not
// here and not in droppedTags is inline and contributes its text to the
// surrounding paragraph.
var blockTags = map[atom.Atom]bool{
	atom.Address: true, atom.Article: true, atom.Aside: true, atom.Blockquote: true,
	atom.Body: true, atom.Caption: true, atom.Center: true, atom.Dd: true,
	atom.Details: true, atom.Dialog: true, atom.Dir: true, atom.Div: true,
	atom.Dl: true, atom.Dt: true, atom.Fieldset: true, atom.Figcaption: true,
	atom.Figure: true, atom.Footer: true, atom.Form: true, atom.H1: true,
	atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Header: true, atom.Hgroup: true, atom.Hr: true, atom.Html: true,
	atom.Li: true, atom.Main: true, atom.Menu: true, atom.Nav: true,
	atom.Ol: true, atom.P: true, atom.Pre: true, atom.Section: true,
	atom.Summary: true, atom.Table: true, atom.Tbody: true, atom.Td: true,
	atom.Tfoot: true, atom.Th: true, atom.Thead: true, atom.Tr: true,
	atom.Ul: true,
}

// droppedTags never contribute anything, text included.
var droppedTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true,
	atom.Noscript: true, atom.Template: true, atom.Iframe: true,
	atom.Object: true, atom.Embed: true, atom.Svg: true, atom.Colgroup: true,
}

func dropped(n *html.Node) bool {
	return n.Type == html.ElementNode && droppedTags[n.DataAtom]
}

// isBlock reports whether n is laid out as a block: a block tag, or an inline
// element wrapping one (`<span><p>a</p><p>b</p></span>`), whose paragraphs
// would otherwise run together.
func (c *converter) isBlock(n *html.Node) bool {
	if n.Type != html.ElementNode || dropped(n) {
		return false
	}
	return blockTags[n.DataAtom] || c.containsBlock(n)
}

func (c *converter) containsBlock(n *html.Node) bool {
	if v, ok := c.memo[n]; ok {
		return v
	}
	found := false
	for k := n.FirstChild; k != nil && !found; k = k.NextSibling {
		if k.Type == html.ElementNode && !dropped(k) {
			found = blockTags[k.DataAtom] || c.containsBlock(k)
		}
	}
	c.memo[n] = found
	return found
}

func kids(n *html.Node) []*html.Node {
	var out []*html.Node
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		out = append(out, k)
	}
	return out
}

// blocks lays out a run of sibling nodes as Markdown blocks. Inline siblings
// accumulate into one paragraph, which a block sibling flushes.
func (c *converter) blocks(nodes []*html.Node) []string {
	var out []string
	var buf strings.Builder
	flush := func() {
		if p := paragraph(buf.String()); p != "" {
			out = append(out, p)
		}
		buf.Reset()
	}
	for _, n := range nodes {
		switch {
		case n.Type == html.TextNode:
			writeText(&buf, n.Data)
		case n.Type != html.ElementNode, dropped(n):
		case c.isBlock(n):
			flush()
			out = append(out, c.block(n)...)
		default:
			c.inline(&buf, n)
		}
	}
	flush()
	return out
}

func (c *converter) block(n *html.Node) []string {
	if isMedia(n) {
		return []string{c.media(n)}
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		text := oneLine(strings.Join(c.blocks(kids(n)), " "))
		if text == "" {
			return nil
		}
		return []string{strings.Repeat("#", int(n.Data[1]-'0')) + " " + text}
	case atom.Hr:
		return []string{"---"}
	case atom.Blockquote:
		inner := c.blocks(kids(n))
		if len(inner) == 0 {
			return nil
		}
		lines := strings.Split(strings.Join(inner, "\n\n"), "\n")
		for i, l := range lines {
			if l == "" {
				lines[i] = ">"
			} else {
				lines[i] = "> " + l
			}
		}
		return []string{strings.Join(lines, "\n")}
	case atom.Pre:
		if p := c.pre(n); p != "" {
			return []string{p}
		}
		return nil
	case atom.Ul, atom.Ol:
		if l := c.list(n); l != "" {
			return []string{l}
		}
		return nil
	case atom.Table:
		return c.table(n)
	}
	// p, div, section and every other container, and an inline element
	// wrapping blocks: its children, laid out.
	return c.blocks(kids(n))
}

// pre is a fenced code block. Its text is kept byte for byte apart from line
// endings and trailing whitespace; the fence is longer than any backtick run
// inside, so the content can never close it.
func (c *converter) pre(n *html.Node) string {
	var b strings.Builder
	rawText(&b, n)
	content := strings.ReplaceAll(b.String(), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = strings.ReplaceAll(content, " ", " ")
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	content = strings.Trim(strings.Join(lines, "\n"), "\n")
	if strings.TrimSpace(content) == "" {
		return ""
	}
	fence := strings.Repeat("`", max(3, longestRun(content, '`')+1))
	return fence + codeLang(n) + "\n" + content + "\n" + fence
}

// codeLang is the language a code block declares: a `language-x` or Jira's
// own `code-x` class on the pre, or on a lone code element inside it.
func codeLang(n *html.Node) string {
	cands := []*html.Node{n}
	if k := onlyElementChild(n); k != nil && k.DataAtom == atom.Code {
		cands = append(cands, k)
	}
	for _, e := range cands {
		for _, cls := range classes(e) {
			for _, prefix := range []string{"language-", "lang-", "code-"} {
				if lang, ok := strings.CutPrefix(cls, prefix); ok {
					if lang = sanitizeLang(lang); lang != "" && lang != "none" {
						return lang
					}
				}
			}
		}
	}
	return ""
}

func sanitizeLang(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && (r == '-' || r == '_' || r == '+' || r == '#' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func onlyElementChild(n *html.Node) *html.Node {
	var only *html.Node
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		switch k.Type {
		case html.ElementNode:
			if only != nil {
				return nil
			}
			only = k
		case html.TextNode:
			if strings.TrimSpace(k.Data) != "" {
				return nil
			}
		}
	}
	return only
}

// list lays out ul/ol. A nested list is indented by its parent item's marker
// width, which is what the TUI's parser reads as one level deeper (it nests
// on any deeper indent). Lines inside an item never leave a blank between
// them, so the TUI keeps the items one list.
func (c *converter) list(n *html.Node) string {
	ordered := n.DataAtom == atom.Ol
	num := 1
	if ordered {
		if s, err := strconv.Atoi(strings.TrimSpace(attr(n, "start"))); err == nil && s >= 0 {
			num = s
		}
	}
	var lines []string
	pad := ""
	for _, k := range kids(n) {
		var item []string
		switch {
		case k.Type == html.TextNode:
			if p := paragraph(collapseSpace(k.Data)); p != "" {
				item = []string{p}
			}
		case k.Type != html.ElementNode, dropped(k):
			continue
		case k.DataAtom == atom.Ul || k.DataAtom == atom.Ol:
			// A list directly inside a list — malformed, but common in
			// hand-edited HTML — nests under the item before it.
			if sub := c.list(k); sub != "" {
				for _, l := range strings.Split(sub, "\n") {
					lines = append(lines, pad+l)
				}
			}
			continue
		case k.DataAtom == atom.Li:
			item = c.blocks(kids(k))
		default:
			item = c.blocks([]*html.Node{k})
		}
		if len(item) == 0 {
			continue
		}
		marker := "- "
		if ordered {
			marker = strconv.Itoa(num) + ". "
			num++
		}
		pad = strings.Repeat(" ", len(marker))
		first := true
		for _, blk := range item {
			for _, l := range strings.Split(blk, "\n") {
				if strings.TrimSpace(l) == "" {
					continue
				}
				if first {
					lines = append(lines, marker+l)
					first = false
				} else {
					lines = append(lines, pad+l)
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// table lays out a pipe table. The first row is the header — a th row when
// the source has one, since that is where it is written — and short rows are
// padded so every row has the same number of cells. A caption becomes a
// paragraph before the table.
func (c *converter) table(n *html.Node) []string {
	var out []string
	var rows [][]string
	var walk func(*html.Node)
	walk = func(p *html.Node) {
		for k := p.FirstChild; k != nil; k = k.NextSibling {
			if k.Type != html.ElementNode {
				continue
			}
			switch k.DataAtom {
			case atom.Caption:
				if t := oneLine(strings.Join(c.blocks(kids(k)), " ")); t != "" {
					out = append(out, t)
				}
			case atom.Thead, atom.Tbody, atom.Tfoot:
				walk(k)
			case atom.Tr:
				rows = append(rows, c.row(k))
			}
		}
	}
	walk(n)
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	if width == 0 {
		return out
	}
	line := func(cells []string) string {
		full := make([]string, width)
		copy(full, cells)
		return "| " + strings.Join(full, " | ") + " |"
	}
	lines := []string{line(rows[0])}
	sep := make([]string, width)
	for i := range sep {
		sep[i] = "---"
	}
	lines = append(lines, line(sep))
	for _, r := range rows[1:] {
		lines = append(lines, line(r))
	}
	return append(out, strings.Join(lines, "\n"))
}

// row is a table row's cells, each on one line and with any pipe replaced so
// it cannot split the cell.
func (c *converter) row(tr *html.Node) []string {
	var cells []string
	for k := tr.FirstChild; k != nil; k = k.NextSibling {
		if k.Type == html.ElementNode && (k.DataAtom == atom.Td || k.DataAtom == atom.Th) {
			text := oneLine(strings.Join(c.blocks(kids(k)), " "))
			cells = append(cells, strings.ReplaceAll(text, "|", "¦"))
		}
	}
	return cells
}

// inline writes an inline element's Markdown into b.
func (c *converter) inline(b *strings.Builder, n *html.Node) {
	if isMedia(n) {
		b.WriteString(c.media(n))
		return
	}
	switch n.DataAtom {
	case atom.Br:
		b.WriteString("\n")
	case atom.Strong, atom.B:
		c.emphasis(b, n, "**")
	case atom.Em, atom.I:
		c.emphasis(b, n, "*")
	case atom.Code, atom.Tt, atom.Kbd, atom.Samp:
		codeSpan(b, n)
	case atom.A:
		c.link(b, n)
	case atom.Img:
		c.image(b, n)
	default:
		// del, s, ins, u, sup, sub, span, font and every unknown element:
		// their text, with any formatting inside them kept.
		c.inlineKids(b, n)
	}
}

func (c *converter) inlineKids(b *strings.Builder, n *html.Node) {
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		switch {
		case k.Type == html.TextNode:
			writeText(b, k.Data)
		case k.Type != html.ElementNode, dropped(k):
		default:
			c.inline(b, k)
		}
	}
}

func (c *converter) inlineString(n *html.Node) string {
	var b strings.Builder
	c.inlineKids(&b, n)
	return b.String()
}

// emphasis wraps n's content in mark. Whitespace at the content's edges is
// moved outside the markers — `** bold**` is not emphasis in Markdown.
func (c *converter) emphasis(b *strings.Builder, n *html.Node, mark string) {
	inner := c.inlineString(n)
	core := strings.TrimSpace(inner)
	if core == "" {
		b.WriteString(edgeSpace(inner))
		return
	}
	if inner != strings.TrimLeft(inner, " \n") {
		b.WriteString(" ")
	}
	if strings.HasPrefix(core, mark) && strings.HasSuffix(core, mark) && len(core) > 2*len(mark) {
		// <b><b>x</b></b>: already wrapped once.
		b.WriteString(core)
	} else {
		b.WriteString(mark + core + mark)
	}
	if inner != strings.TrimRight(inner, " \n") {
		b.WriteString(" ")
	}
}

// codeSpan is inline code. The backtick run delimiting it is longer than any
// run inside, and padded with a space when the content starts or ends with a
// backtick, which is how Markdown spells literal backticks without escapes.
func codeSpan(b *strings.Builder, n *html.Node) {
	var raw strings.Builder
	rawText(&raw, n)
	inner := strings.ReplaceAll(raw.String(), " ", " ")
	core := oneLine(inner)
	if core == "" {
		b.WriteString(edgeSpace(inner))
		return
	}
	if inner != strings.TrimLeft(inner, " \t\r\n") {
		b.WriteString(" ")
	}
	fence := strings.Repeat("`", longestRun(core, '`')+1)
	if strings.HasPrefix(core, "`") || strings.HasSuffix(core, "`") {
		core = " " + core + " "
	}
	b.WriteString(fence + core + fence)
	if inner != strings.TrimRight(inner, " \t\r\n") {
		b.WriteString(" ")
	}
}

// link writes an inline link, or only its text when it is a mention or has
// no usable destination.
func (c *converter) link(b *strings.Builder, n *html.Node) {
	if isMention(n) {
		// The display name, never the profile link: that link is where the
		// accountId lives.
		c.inlineKids(b, n)
		return
	}
	href := c.resolve(attr(n, "href"))
	if img := loneImage(n); img != nil && href != "" {
		// An image wrapped in a link (Jira's thumbnail → full size) is the
		// image; a link around an image is not in the TUI's subset.
		src := c.resolve(attr(img, "src"))
		if src == "" || attr(n, "file-preview-type") == "image" {
			src = href
		}
		writeImage(b, oneLine(attr(img, "alt")), src)
		return
	}
	inner := c.inlineString(n)
	if href == "" {
		b.WriteString(inner)
		return
	}
	core := oneLine(inner)
	if core == "" {
		core = href
	}
	if inner != strings.TrimLeft(inner, " \n") {
		b.WriteString(" ")
	}
	b.WriteString("[" + core + "](" + href + ")")
	if inner != strings.TrimRight(inner, " \n") {
		b.WriteString(" ")
	}
}

func (c *converter) image(b *strings.Builder, n *html.Node) {
	if hasClass(n, "rendericon") {
		// Jira's link-type decoration (the paperclip after an attachment
		// link): an icon, not content.
		return
	}
	alt := oneLine(attr(n, "alt"))
	if hasClass(n, "emoticon") {
		b.WriteString(alt)
		return
	}
	src := c.resolve(attr(n, "src"))
	if src == "" {
		b.WriteString(alt)
		return
	}
	writeImage(b, alt, src)
}

func writeImage(b *strings.Builder, alt, src string) {
	b.WriteString("![" + alt + "](" + src + ")")
}

// loneImage is the single non-decorative img inside a, when that is all a
// holds apart from whitespace.
func loneImage(a *html.Node) *html.Node {
	var img *html.Node
	ok := true
	var walk func(*html.Node)
	walk = func(p *html.Node) {
		for k := p.FirstChild; k != nil && ok; k = k.NextSibling {
			switch {
			case k.Type == html.TextNode:
				if strings.TrimSpace(strings.ReplaceAll(k.Data, " ", " ")) != "" {
					ok = false
				}
			case k.Type != html.ElementNode, dropped(k):
			case k.DataAtom == atom.Img:
				switch {
				case hasClass(k, "rendericon"), hasClass(k, "emoticon"), img != nil:
					ok = false
				default:
					img = k
				}
			default:
				walk(k)
			}
		}
	}
	walk(a)
	if !ok {
		return nil
	}
	return img
}

// isMention recognises a Jira user mention in rendered HTML. Cloud renders
// one as a profile link carrying the account id; the shapes below are the
// documented ones, to be pinned against captured bodies (#899).
func isMention(n *html.Node) bool {
	if hasAttr(n, "data-account-id") || hasAttr(n, "accountid") || hasAttr(n, "data-mention-id") ||
		hasClass(n, "user-hover") || hasClass(n, "mention") {
		return true
	}
	href := attr(n, "href")
	for _, p := range []string{"/secure/ViewProfile.jspa", "/jira/people/", "/wiki/people/"} {
		if strings.Contains(href, p) {
			return true
		}
	}
	return false
}

// isMedia recognises Jira's attachment and media wrappers: the image-wrap
// span around an embedded image, and the data-attachment-* / media-services
// containers an ADF media node renders to.
func isMedia(n *html.Node) bool {
	return n.Type == html.ElementNode && n.DataAtom != atom.Img && n.DataAtom != atom.A &&
		(hasClass(n, "image-wrap") || hasAttr(n, "data-attachment-name") ||
			hasAttr(n, "data-attachment-type") || hasAttr(n, "data-media-services-id"))
}

// media renders an attachment wrapper as the image or link it holds, and as
// a `[attachment: name]` placeholder when no URL is recoverable — the body
// still says something was attached.
func (c *converter) media(n *html.Node) string {
	name := oneLine(attr(n, "data-attachment-name"))
	// The first usable link or image, in document order. A link is not
	// descended into: link() already knows an image inside one is Jira's
	// thumbnail and the link's target the attachment itself.
	var found *html.Node
	var walk func(*html.Node)
	walk = func(p *html.Node) {
		for k := p.FirstChild; k != nil && found == nil; k = k.NextSibling {
			if k.Type != html.ElementNode || dropped(k) {
				continue
			}
			switch {
			case k.DataAtom == atom.A && c.resolve(attr(k, "href")) != "":
				found = k
			case k.DataAtom == atom.Img && !hasClass(k, "rendericon") && c.resolve(attr(k, "src")) != "":
				found = k
			default:
				walk(k)
			}
		}
	}
	walk(n)
	var b strings.Builder
	switch {
	case found != nil && found.DataAtom == atom.Img:
		alt := oneLine(attr(found, "alt"))
		if alt == "" {
			alt = name
		}
		writeImage(&b, alt, c.resolve(attr(found, "src")))
	case found != nil:
		if loneImage(found) == nil && oneLine(c.inlineString(found)) == "" && name != "" {
			b.WriteString("[" + name + "](" + c.resolve(attr(found, "href")) + ")")
		} else {
			c.link(&b, found)
		}
	default:
		if name == "" {
			name = oneLine(c.inlineString(n))
		}
		if name == "" {
			return "[attachment]"
		}
		b.WriteString("[attachment: " + name + "]")
	}
	return strings.TrimSpace(b.String())
}

// resolve absolutizes a URL against the base and returns "" when the result
// is not a destination we emit. Characters that would end a Markdown link
// destination early are percent-encoded rather than escaped.
func (c *converter) resolve(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if c.base != nil {
		u = c.base.ResolveReference(u)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
	case "":
		if c.base != nil || u.Opaque != "" {
			return ""
		}
	default:
		return ""
	}
	s := u.String()
	if s == "" {
		return ""
	}
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E").Replace(s)
}

// rawText is n's text, verbatim, with br as a newline — the content of a pre
// or a code span.
func rawText(b *strings.Builder, n *html.Node) {
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		switch {
		case k.Type == html.TextNode:
			b.WriteString(k.Data)
		case k.Type != html.ElementNode, dropped(k):
		case k.DataAtom == atom.Br:
			b.WriteString("\n")
		default:
			rawText(b, k)
		}
	}
}

// writeText appends HTML text with whitespace collapsed the way a browser
// collapses it outside pre. A non-breaking space counts as a space: Jira's
// editor scatters them, and keeping them would make the stored body differ
// from what anyone sees.
func writeText(b *strings.Builder, s string) {
	b.WriteString(collapseSpace(s))
}

func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\f', ' ':
			if !space {
				b.WriteByte(' ')
			}
			space = true
		default:
			b.WriteRune(r)
			space = false
		}
	}
	return b.String()
}

// paragraph normalises an inline run: each line (a br ends one) collapsed
// and trimmed, no leading or trailing blank line, and never two blank lines
// in a row.
func paragraph(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = oneLine(l)
		if l == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, l)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// oneLine collapses every run of whitespace, newlines included, to a space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, " ", " ")), " ")
}

// edgeSpace is a single space when s has any whitespace, so dropping empty
// formatting never glues its neighbours together.
func edgeSpace(s string) string {
	if s != "" {
		return " "
	}
	return ""
}

func longestRun(s string, ch byte) int {
	best, run := 0, 0
	for i := range len(s) {
		if s[i] == ch {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return true
		}
	}
	return false
}

func classes(n *html.Node) []string { return strings.Fields(attr(n, "class")) }

func hasClass(n *html.Node, cls string) bool {
	for _, c := range classes(n) {
		if c == cls {
			return true
		}
	}
	return false
}
