package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The copy picker (task 076 decision 6): one key lists what can be copied
// out of the assistant prose currently on screen, and a pick puts it on the
// clipboard.
//
// It is a popup rather than a cursor in the pane. The output pane is a
// viewport over rendered []string — it has no cursor, no selection and no
// block identity, and apiclient.TranscriptRecord carries no id — so a "copy
// the focused block" key would first have to invent a navigation model. The
// issue's own alternatives reject that ("make every code block permanently
// focused … adds heavy navigation state to ordinary reading"), so the picker
// stays dormant until asked for, follows palette.go's shape, and owns the
// keyboard while it is up, at the top of the §15 esc stack.
//
// Each row is a **reference to a document**, resolved when it is picked, with
// the text captured at build time as its fallback (#291, which landed the
// identity model task 076 decision 6 deliberately shipped without). A
// reference rather than an index: an index would drift the moment a chunk
// arrived or the chat's maxRecords cap pruned the front of the slice, and a
// document's seq does neither.
//
// One behaviour change falls out of resolving late, and it is deliberate:
// picking a document that has grown since the popup opened copies it as it is
// now, because "copy this message" should yield the whole message. A finished
// document — the common case — is unaffected. A reference that no longer
// resolves, because the prune took its records, copies the text captured when
// the popup was built, so a pick can never fail.

// copyKind is which payload of a document a row offers (decision 5). It is
// kept beside the captured text so a pick can re-derive the payload from the
// document as it stands now.
type copyKind int

const (
	copyKindMarkdown copyKind = iota
	copyKindPlain
	copyKindCode
)

// copyDoc is one assistant document offered to the picker: its text as the
// pane drew it, and the identity a pick resolves against. A document with no
// identity — a chat turn whose transcript went to retention and contributes
// its ResultText instead (§17) — has ref.ok false and is always copied from
// the captured text.
type copyDoc struct {
	seq  int64
	text string
	ok   bool
}

// copyItem is one offer: what it is called, the exact text a pick puts on the
// clipboard if the reference cannot be resolved, and the reference itself.
type copyItem struct {
	// group is the document the row belongs to — "message 1" is the newest.
	// Ordinals rather than the opening words alone, because two documents
	// that start the same way have to stay distinguishable.
	group string
	label string
	// snippet is a one-line preview, which is what makes the right document
	// findable when a conversation has twenty of them.
	snippet string
	text    string
	// ref names the document this row was built from, and kind/index say
	// which of its payloads to re-derive at pick time.
	ref   copyDoc
	kind  copyKind
	index int
}

// copyItemLabels are the three payloads (decision 5).
const (
	copyLabelMarkdown = "markdown"
	copyLabelPlain    = "plain text"
	copyLabelCode     = "code block"
)

// copyItemsFrom lists what can be copied out of one assistant document, in
// the order a reader would look for them: the whole thing as its source, the
// whole thing without the punctuation, then each fenced block.
func copyItemsFrom(group string, doc copyDoc) []copyItem {
	clean := sanitizeText(doc.text)
	if strings.TrimSpace(clean) == "" {
		return nil
	}
	snippet := copySnippet(clean)
	out := []copyItem{
		{
			group: group, label: copyLabelMarkdown, snippet: snippet,
			text: clean, ref: doc, kind: copyKindMarkdown,
		},
		{
			group: group, label: copyLabelPlain, snippet: snippet,
			text: markdownPlainText(clean), ref: doc, kind: copyKindPlain,
		},
	}
	blocks := codeBlocks(clean)
	for i, code := range blocks {
		label := copyLabelCode
		if len(blocks) > 1 {
			// Numbered only when there is a choice to make: "code block 1"
			// on a document with one of them is noise.
			label = fmt.Sprintf("%s %d", copyLabelCode, i+1)
		}
		out = append(out, copyItem{
			group: group, label: label, snippet: copySnippet(code), text: code,
			ref: doc, kind: copyKindCode, index: i,
		})
	}
	return out
}

// copyPayload re-derives one row's payload from a document's current source.
// It is copyItemsFrom's body, minus the labelling, and the two must agree:
// what a pick copies is what the row said it would, read from the document as
// it stands now.
func copyPayload(text string, kind copyKind, index int) (string, bool) {
	clean := sanitizeText(text)
	if strings.TrimSpace(clean) == "" {
		return "", false
	}
	switch kind {
	case copyKindMarkdown:
		return clean, true
	case copyKindPlain:
		return markdownPlainText(clean), true
	case copyKindCode:
		blocks := codeBlocks(clean)
		if index < 0 || index >= len(blocks) {
			// The document changed shape under the popup and the block the
			// row named is gone; the captured text is what it promised.
			return "", false
		}
		return blocks[index], true
	}
	return "", false
}

// copyDocs collects the picker's rows from assistant documents already
// handed to it newest-first. It is the one place the "message N" ordinals are
// assigned, so the task pane's records and the chat's turns number the same
// way.
func copyDocs(docs []copyDoc) []copyItem {
	out := make([]copyItem, 0, len(docs)*2)
	n := 0
	for _, doc := range docs {
		group := fmt.Sprintf("message %d", n+1)
		items := copyItemsFrom(group, doc)
		if len(items) == 0 {
			continue
		}
		n++
		out = append(out, items...)
	}
	return out
}

// copyDocsFromRecords is the task workspace's source: the assistant documents
// in the loaded records, newest first. One row per *document* rather than per
// record (#291), so "message 1" is the newest document and a message an
// adapter split across records is offered once rather than in pieces.
func copyDocsFromRecords(records []apiclient.TranscriptRecord, seqs []int64) []copyDoc {
	found := assistantDocs(records, seqs)
	out := make([]copyDoc, 0, len(found))
	for i := len(found) - 1; i >= 0; i-- {
		out = append(out, copyDoc{seq: found[i].seq, text: found[i].text, ok: true})
	}
	return out
}

// resolveDocs answers a pick from a record window: the current source of the
// document a row references, if it is still there.
func resolveDocs(records []apiclient.TranscriptRecord, seqs []int64, seq int64) (string, bool) {
	for _, doc := range assistantDocs(records, seqs) {
		if doc.seq == seq {
			return doc.text, true
		}
	}
	return "", false
}

// copySnippet is a row's preview: the first line with anything on it,
// whitespace collapsed, so a preview never spans two rows.
func copySnippet(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return strings.Join(strings.Fields(t), " ")
		}
	}
	return ""
}

// openCopyPickerMsg asks the root to raise the picker. A view sends it rather
// than owning a popup of its own: the picker overlays the whole screen and
// takes the keyboard, which is the root's job for the palette already — and
// building the items in the view is what captures them at pick time.
// resolve re-reads a document by its seq from the view that offered it, and
// is what makes a row a reference rather than a snapshot. It closes over the
// view, so it sees the records as they are at pick time.
type openCopyPickerMsg struct {
	items   []copyItem
	resolve func(seq int64) (string, bool)
}

// openCopyPicker turns a view's collected documents into that message.
func openCopyPicker(docs []copyDoc, resolve func(seq int64) (string, bool)) tea.Cmd {
	items := copyDocs(docs)
	return func() tea.Msg { return openCopyPickerMsg{items: items, resolve: resolve} }
}

// pickText is what a chosen row puts on the clipboard: the document as it
// stands now, or the text captured when the popup was built.
func pickText(it copyItem, resolve func(seq int64) (string, bool)) string {
	if resolve == nil || !it.ref.ok {
		return it.text
	}
	text, ok := resolve(it.ref.seq)
	if !ok {
		return it.text
	}
	payload, ok := copyPayload(text, it.kind, it.index)
	if !ok {
		return it.text
	}
	return payload
}

// readerPicker is the popup itself: a search line over the captured rows.
type readerPicker struct {
	input  textinput.Model
	items  []copyItem
	cursor int
}

func newReaderPicker(items []copyItem) *readerPicker {
	in := textinput.New()
	in.Placeholder = "type to search what can be copied"
	in.Prompt = ": "
	in.Focus()
	return &readerPicker{input: in, items: items}
}

// matches filters rows against the typed query, over label, group and
// preview — the same three fields the row shows.
func (p *readerPicker) matches() []copyItem {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if q == "" {
		return p.items
	}
	out := make([]copyItem, 0, len(p.items))
	for _, e := range p.items {
		hay := strings.ToLower(e.label + " " + e.group + " " + e.snippet)
		if strings.Contains(hay, q) {
			out = append(out, e)
		}
	}
	return out
}

// update handles one key. done reports the popup should close; run is the row
// that was chosen.
func (p *readerPicker) update(msg tea.KeyPressMsg) (run *copyItem, done bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, true, nil
	case "up":
		p.cursor = max(p.cursor-1, 0)
		return nil, false, nil
	case "down":
		p.cursor = min(p.cursor+1, max(len(p.matches())-1, 0))
		return nil, false, nil
	case "enter":
		m := p.matches()
		if len(m) == 0 {
			return nil, true, nil
		}
		e := m[min(p.cursor, len(m)-1)]
		return &e, true, nil
	}
	var c tea.Cmd
	p.input, c = p.input.Update(msg)
	if n := len(p.matches()); p.cursor >= n {
		p.cursor = max(n-1, 0)
	}
	return nil, false, c
}

// paste types into the search line — the picker is a text field like the
// palette, and the root routes paste to whichever popup has the keyboard.
func (p *readerPicker) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(tea.PasteMsg{Content: text})
	if n := len(p.matches()); p.cursor >= n {
		p.cursor = max(n-1, 0)
	}
	return cmd
}

// render draws the popup for overlaying: the search line, then the rows
// windowed around the cursor and grouped by document.
func (p *readerPicker) render(w, h int) string {
	inner := max(w-2, 10)
	lines := make([]string, 0, h)
	lines = append(lines, " "+p.input.View())

	m := p.matches()
	if len(m) == 0 {
		body := "  nothing to copy — esc closes"
		if len(p.items) > 0 {
			body = "  nothing matches — esc closes"
		}
		lines = append(lines, styleDim.Render(body))
		return frame("copy", strings.Join(lines, "\n"), w, h, true)
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
		// The preview is dim and to the right of the payload's name: what a
		// row *is* is what a reader picks by, and the words are how they
		// tell two of them apart.
		label := e.label
		pad := max(inner-2-ansi.StringWidth(label)-1, 1)
		snippet := ansi.Truncate(e.snippet, max(pad-1, 1), "…")
		rows = append(rows, mark+style.Render(label)+
			strings.Repeat(" ", max(pad-ansi.StringWidth(snippet), 1))+styleDim.Render(snippet))
	}
	lines = append(lines, window(rows, cursorRow, h-3)...)
	return frame("copy", strings.Join(lines, "\n"), w, h, true)
}
