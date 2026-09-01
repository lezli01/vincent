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
// Each row **captures its payload when the popup is built** rather than
// holding an index into the records. That is what makes a target stable
// across resize, transcript reload and incremental rendering: there is no
// index left to drift when a chunk arrives, a re-render happens, or the
// chat's maxRecords cap prunes the front of the slice. When #291 lands a
// semantic document model the rows become references to it, and none of the
// payloads below change.

// copyItem is one offer: what it is called, and the exact text a pick puts on
// the clipboard.
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
func copyItemsFrom(group, text string) []copyItem {
	clean := sanitizeText(text)
	if strings.TrimSpace(clean) == "" {
		return nil
	}
	snippet := copySnippet(clean)
	out := []copyItem{
		{group: group, label: copyLabelMarkdown, snippet: snippet, text: clean},
		{group: group, label: copyLabelPlain, snippet: snippet, text: markdownPlainText(clean)},
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
		})
	}
	return out
}

// copyDocs collects the picker's rows from assistant documents already
// handed to it newest-first. It is the one place the "message N" ordinals are
// assigned, so the task pane's records and the chat's turns number the same
// way.
func copyDocs(docs []string) []copyItem {
	out := make([]copyItem, 0, len(docs)*2)
	n := 0
	for _, text := range docs {
		group := fmt.Sprintf("message %d", n+1)
		items := copyItemsFrom(group, text)
		if len(items) == 0 {
			continue
		}
		n++
		out = append(out, items...)
	}
	return out
}

// copyDocsFromRecords is the task workspace's source: the assistant prose in
// the loaded records, newest first. Only agent.output is offered, because it
// is the only record type that was ever read as Markdown (task 073
// decision 5).
func copyDocsFromRecords(records []apiclient.TranscriptRecord) []string {
	docs := make([]string, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Type == "agent.output" {
			docs = append(docs, records[i].Text)
		}
	}
	return docs
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
type openCopyPickerMsg struct{ items []copyItem }

// openCopyPicker turns a view's collected documents into that message.
func openCopyPicker(docs []string) tea.Cmd {
	items := copyDocs(docs)
	return func() tea.Msg { return openCopyPickerMsg{items: items} }
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
