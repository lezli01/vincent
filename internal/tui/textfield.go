package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// textField is the TUI's single-value text row (issue #299).
//
// It is a textarea rather than a `textinput` because the wanted behaviour is a
// field that *wraps*. bubbles/v2 turns horizontal scrolling off entirely while
// `Width() <= 0` — `handleOverflow` returns early — so an unsized textinput
// draws its whole value on one row, which runs past the right edge of the pane
// and takes the cursor with it. Giving each field a width and letting it scroll
// horizontally instead is the smaller change and was rejected: §15 already says
// a value too long for its column wraps onto further lines rather than being
// truncated away (task 052's row height, and the code-block rule), and a field
// being typed into is the one surface where losing the tail also loses the
// cursor.
//
// It is a *field*, not an editor. The prompt gutter and the line numbers are
// off so a wrapped field reads as the single row it replaces, the newline key
// is disabled and a pasted newline becomes a space, so `Value` is always the
// one line the caller stored. Its height follows its content, which is why a
// host asks `rows` for the lines to lay out rather than assuming one.
type textField struct {
	ta textarea.Model
	// prompt is drawn by the field rather than by the textarea, which repeats
	// its own prompt on every row: a wrapped filter row must not grow a second
	// `/`. It is the row's prefix, so the wrapped rows indent past it.
	prompt string
	width  int
}

// newTextField is the constructor every text row in the TUI goes through.
func newTextField() textField {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.KeyMap.InsertNewline.SetEnabled(false)
	// The cursor line's background would draw a full-width bar across a form
	// row; a field is one row of a form, not a document being edited.
	s := ta.Styles()
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(s)
	ta.SetHeight(1)
	return textField{ta: ta, width: textareaDefaultWidth}
}

// textareaDefaultWidth is what bubbles gives a fresh textarea, kept so a field
// nobody sized still wraps somewhere rather than not at all.
const textareaDefaultWidth = 40

// SetPlaceholder sets the dim text shown while the field is empty.
func (f *textField) SetPlaceholder(s string) { f.ta.Placeholder = s }

// Placeholder is that text.
func (f textField) Placeholder() string { return f.ta.Placeholder }

// SetWidth sets how many columns the field's rows may use, prompt included.
// Callers pass the room left in the pane after whatever prefix the row carries,
// so the widest wrapped row plus that prefix is exactly the pane width.
func (f *textField) SetWidth(w int) {
	f.width = w
	f.applyWidth()
}

// SetPrompt sets the marker the first row carries — `/` for a filter, `: ` for
// a palette.
func (f *textField) SetPrompt(s string) {
	f.prompt = s
	f.applyWidth()
}

func (f *textField) applyWidth() {
	f.ta.SetWidth(max(f.width-ansi.StringWidth(f.prompt), 1))
}

// Width is the field's current column budget, prompt included.
func (f textField) Width() int { return f.width }

// CursorEnd puts the cursor after the last character.
func (f *textField) CursorEnd() { f.ta.CursorEnd() }

func (f textField) Value() string   { return f.ta.Value() }
func (f textField) Focused() bool   { return f.ta.Focused() }
func (f *textField) Blur()          { f.ta.Blur() }
func (f *textField) Focus() tea.Cmd { return f.ta.Focus() }

// SetValue stores a value. Newlines collapse to spaces: this is a field, and a
// value that grew a second logical line would stop round-tripping through
// Value.
func (f *textField) SetValue(s string) {
	f.ta.SetValue(fieldOneLine(s))
	f.ta.CursorEnd()
}

// Update runs the field's own key handling. A paste is flattened first, for the
// reason SetValue flattens.
func (f textField) Update(msg tea.Msg) (textField, tea.Cmd) {
	if p, ok := msg.(tea.PasteMsg); ok {
		msg = tea.PasteMsg{Content: fieldOneLine(p.Content)}
	}
	var cmd tea.Cmd
	f.ta, cmd = f.ta.Update(msg)
	return f, cmd
}

// View is the field's rows joined, which is what a host that has a whole line
// to itself wants. A host laying the field out beside a label wants rows.
func (f textField) View() string { return strings.Join(f.rows(), "\n") }

// rows is the field as it will be drawn: one entry per wrapped row, each no
// wider than the width it was given.
func (f textField) rows() []string {
	out := strings.Split(f.ta.View(), "\n")
	// The textarea pads every row out to its full width. That is invisible on
	// its own but it is not free to a host that puts something after the
	// field, or that is counting columns against the pane.
	for i, r := range out {
		out[i] = strings.TrimRight(r, " ")
	}
	return indentRows(f.prompt, out)
}

// fieldOneLine flattens a value to the single line a field holds.
func fieldOneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// fieldRows lays a field out under a fixed prefix: the first row carries the
// prefix, and every wrapped row after it is indented to the same column so the
// field still reads as the one row it grew out of.
func fieldRows(prefix string, f textField) []string {
	return indentRows(prefix, f.rows())
}

// indentRows is fieldRows for anything already split into rows.
func indentRows(prefix string, rows []string) []string {
	pad := strings.Repeat(" ", ansi.StringWidth(prefix))
	out := make([]string, 0, len(rows))
	for i, r := range rows {
		if i == 0 {
			out = append(out, prefix+r)
			continue
		}
		out = append(out, pad+r)
	}
	return out
}

// truncateRows cuts every row of a laid-out field to the pane width.
func truncateRows(rows []string, width int) []string {
	for i, r := range rows {
		rows[i] = ansi.Truncate(r, width, "…")
	}
	return rows
}
