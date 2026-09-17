package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The link picker (task 110): one key lists the numbered destinations of the
// assistant prose on screen — the `[n]` a label carries and the reference
// block resolves (task 075) — and a pick opens one in the browser or copies
// it.
//
// It is a popup of its own rather than more rows in the copy picker. That
// popup is titled "copy" and its enter copies; a link needs a second action,
// and the search line takes every letter, so the second one has to be a ctrl
// key. A "copy" popup whose enter sometimes opened a browser would give enter
// two meanings depending on the row (decision 1).
//
// Its scope and its rows follow the copy picker exactly (decision 2): every
// assistant document in the loaded records or turns, newest first, under the
// same "message n" ordinals, and each row a reference to a document resolved
// at pick time. What the renderer draws is unchanged — the pane still emits
// no hyperlink and never reaches the opener (task 075 decision 3). Only an
// explicit pick here does.

// linkItem is one row: one numbered destination of one document.
type linkItem struct {
	// group is the document's "message n", numbered the way the copy picker
	// numbers it, so both popups call one document by one name.
	group string
	link  mdLink
	// ref names the document; link.n and link.dest are what a pick checks
	// the document still says (decision 2).
	ref copyDoc
	// refused is why enter will not open this destination, and nil when it
	// will. It is openableURL's own answer, never a second copy of the
	// scheme rule (decision 3).
	refused error
}

// linkItemsFrom lists one document's rows: one per `[n]`, image sources
// included, so the numbers stay contiguous with the reference block on
// screen.
func linkItemsFrom(group string, doc copyDoc) []linkItem {
	links := markdownLinks(doc.text)
	out := make([]linkItem, 0, len(links))
	for _, l := range links {
		out = append(out, linkItem{group: group, link: l, ref: doc, refused: openableURL(l.dest)})
	}
	return out
}

// linkDocs collects the rows from documents handed over newest first. The
// ordinal advances for every document the copy picker would list, including
// one with no links: that document contributes no group, but "message 3" here
// is still "message 3" there.
func linkDocs(docs []copyDoc) []linkItem {
	var out []linkItem
	n := 0
	for _, doc := range docs {
		if strings.TrimSpace(sanitizeText(doc.text)) == "" {
			continue
		}
		n++
		out = append(out, linkItemsFrom(fmt.Sprintf("message %d", n), doc)...)
	}
	return out
}

// pickLink is the destination a chosen row delivers. The document is re-read
// by its seq, and its `[n]` is used while it still names the destination the
// row showed; a document that is gone, or whose numbering changed under the
// popup — a prune took its first records — delivers the captured destination
// instead. A row delivers what it said it would, which is copyPayload's rule.
func pickLink(it linkItem, resolve func(seq int64) (string, bool)) string {
	if resolve == nil || !it.ref.ok {
		return it.link.dest
	}
	text, ok := resolve(it.ref.seq)
	if !ok {
		return it.link.dest
	}
	for _, l := range markdownLinks(text) {
		if l.n == it.link.n && l.dest == it.link.dest {
			return l.dest
		}
	}
	return it.link.dest
}

// openLinkPickerMsg asks the root to raise the picker, as openCopyPickerMsg
// does for the copy picker and for the same reasons.
type openLinkPickerMsg struct {
	items   []linkItem
	resolve func(seq int64) (string, bool)
}

// openLinkPicker turns a view's collected documents into that message.
func openLinkPicker(docs []copyDoc, resolve func(seq int64) (string, bool)) tea.Cmd {
	items := linkDocs(docs)
	return func() tea.Msg { return openLinkPickerMsg{items: items, resolve: resolve} }
}

// linkOpenedMsg is what came of opening a link. It is not openedURLMsg: every
// view sees that one, and the task workspace writes it into the pull-request
// note, so a link opened from the output pane would report on the wrong
// surface (decision 5). The root delivers this to the active view alone.
type linkOpenedMsg struct {
	url string
	err error
}

// notice renders the result as the line a human reads, and whether it is bad.
// A success says so too: a browser that opened on another desktop is
// otherwise indistinguishable from nothing.
func (msg linkOpenedMsg) notice() (string, bool) {
	if msg.err != nil {
		return openFailure(openedURLMsg(msg)), true
	}
	return "opened " + msg.url, false
}

// openLinkCmd hands a destination to openURLCmd — which refuses every scheme
// but http and https and passes the URL as one argv element — and rewraps the
// outcome for the link picker's own notice.
func openLinkCmd(dest string) tea.Cmd {
	open := openURLCmd(dest)
	return func() tea.Msg {
		res, _ := open().(openedURLMsg)
		return linkOpenedMsg(res)
	}
}

// linkAction is what a key in the picker asked for.
type linkAction int

const (
	linkNone linkAction = iota
	linkOpen
	linkCopy
)

// linkLabel names a row in the copy notice ("link [2] copied").
func linkLabel(it linkItem) string {
	return "link [" + strconv.Itoa(it.link.n) + "]"
}

// linkPicker is the popup itself: a search line over the captured rows.
type linkPicker struct {
	input  textField
	items  []linkItem
	cursor int
}

func newLinkPicker(items []linkItem) *linkPicker {
	in := newTextField()
	in.SetPlaceholder("type to search the links")
	in.SetPrompt(": ")
	in.Focus()
	return &linkPicker{input: in, items: items}
}

// matches filters rows against the typed query, over the label, the group and
// the destination.
func (p *linkPicker) matches() []linkItem {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if q == "" {
		return p.items
	}
	out := make([]linkItem, 0, len(p.items))
	for _, e := range p.items {
		hay := strings.ToLower(e.link.label + " " + e.group + " " + e.link.dest)
		if strings.Contains(hay, q) {
			out = append(out, e)
		}
	}
	return out
}

// current is the row under the cursor, if any row matches.
func (p *linkPicker) current() (linkItem, bool) {
	m := p.matches()
	if len(m) == 0 {
		return linkItem{}, false
	}
	return m[min(p.cursor, len(m)-1)], true
}

// update handles one key. done reports the popup should close; run is the row
// an action was taken on and act which action.
func (p *linkPicker) update(msg tea.KeyPressMsg) (run *linkItem, act linkAction, done bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, linkNone, true, nil
	case "up":
		p.cursor = max(p.cursor-1, 0)
		return nil, linkNone, false, nil
	case "down":
		p.cursor = min(p.cursor+1, max(len(p.matches())-1, 0))
		return nil, linkNone, false, nil
	case "enter", copyPickKey:
		// ctrl+y means copy in here exactly as it does outside (decision 1).
		e, ok := p.current()
		if !ok {
			return nil, linkNone, true, nil
		}
		act = linkOpen
		if msg.String() == copyPickKey {
			act = linkCopy
		}
		return &e, act, true, nil
	}
	var c tea.Cmd
	p.input, c = p.input.Update(msg)
	if n := len(p.matches()); p.cursor >= n {
		p.cursor = max(n-1, 0)
	}
	return nil, linkNone, false, c
}

// paste types into the search line, as the copy picker's does.
func (p *linkPicker) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(tea.PasteMsg{Content: text})
	if n := len(p.matches()); p.cursor >= n {
		p.cursor = max(n-1, 0)
	}
	return cmd
}

// linkPickerHint is the popup's last line: the two actions have no registry
// row of their own, so the popup says what its keys do.
const linkPickerHint = " enter open · " + copyPickKey + " copy · esc close"

// linkCopyOnly marks a row enter will not open.
const linkCopyOnly = "copy only"

// render draws the popup for overlaying: the search line, the rows windowed
// around the cursor and grouped by document, the cursor row's whole
// destination, and the key hint.
func (p *linkPicker) render(w, h int) string {
	inner := max(w-2, 10)
	body := max(h-2, 1)
	lines := make([]string, 0, body)
	p.input.SetWidth(max(inner-1, 1))
	lines = append(lines, fieldRows(" ", p.input)...)

	m := p.matches()
	if len(m) == 0 {
		text := "  no links — esc closes"
		if len(p.items) > 0 {
			text = "  nothing matches — esc closes"
		}
		lines = append(lines, styleDim.Render(text))
		return frame("links", strings.Join(lines, "\n"), w, h, true)
	}
	cursor := min(p.cursor, len(m)-1)

	rows := make([]string, 0, len(m)*2)
	cursorRow := 0
	group := ""
	for i, e := range m {
		if e.group != group {
			group = e.group
			if len(rows) > 0 {
				rows = append(rows, "")
			}
			label := " " + strings.ToUpper(group) + " "
			fill := max(inner-ansi.StringWidth(label)-1, 0)
			rows = append(rows, styleTitle.Render(label)+
				styleDim.Render(strings.Repeat("─", fill)))
		}
		mark, style := "  ", styleDim
		if i == cursor {
			mark, style = styleFocus.Render("› "), styleTitle
			cursorRow = len(rows)
		}
		rows = append(rows, mark+linkRow(e, style, inner-2))
	}

	// The whole destination of the cursor row, wrapped rather than cut: the
	// row truncates, and a reader must be able to see the exact bytes enter
	// would hand to the opener (decision 4). It is drawn the way the pane
	// draws a reference line, hard-wrapped at the cell boundary.
	cur := m[cursor]
	dest := wrapLine(paneLine{
		gutter:      gutterNone,
		gutterStyle: styleMDRef,
		pre:         true,
		segs:        []segment{{text: cur.link.dest, style: styleMDRef}},
	}, inner)
	// The rows keep at least a few lines of their own; a destination longer
	// than what is left is cut with a marker rather than pushing the hint
	// out of the box. Two lines are the rule above it and the hint below.
	room := max(body-len(lines)-2-min(len(rows), 3), 1)
	if len(dest) > room {
		dest = dest[:room]
		dest[room-1] = ansi.Truncate(dest[room-1], max(inner-1, 1), "") + "…"
	}
	lines = append(lines, window(rows, cursorRow, max(body-len(lines)-len(dest)-2, 1))...)
	// A rule, so the whole destination does not read as one more row.
	lines = append(lines, styleDim.Render(strings.Repeat("─", inner)))
	lines = append(lines, dest...)
	lines = append(lines, styleDim.Render(linkPickerHint))
	return frame("links", strings.Join(lines, "\n"), w, h, true)
}

// linkRow is one row's text after the cursor mark: `[n] label`, then the
// destination dim and truncated to what is left, and a row enter will not
// open says so before its destination.
func linkRow(e linkItem, style lipgloss.Style, width int) string {
	head := "[" + strconv.Itoa(e.link.n) + "] " + e.link.label
	head = ansi.Truncate(head, max(width/2, 8), "…")
	tail := e.link.dest
	if e.refused != nil {
		tail = linkCopyOnly + " · " + tail
	}
	room := max(width-ansi.StringWidth(head)-2, 1)
	tail = ansi.Truncate(tail, room, "…")
	pad := max(width-ansi.StringWidth(head)-ansi.StringWidth(tail), 1)
	return style.Render(head) + strings.Repeat(" ", pad) + styleDim.Render(tail)
}
