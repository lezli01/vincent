package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// textPane is the TUI's multi-line editing surface, and the deliberate
// opposite of [textField] (issue #320).
//
// The two share a base — both are a bubbles textarea, because that is the one
// widget that wraps — and invert every setting textField chose, because
// textField is a *field* and this is an *editor*. A workflow's `prompt:`,
// `run:` and `instructions:` are YAML block scalars whose newlines, blank
// lines and indentation are the value; opening one in a textField flattened it
// to a single line and the daemon then rewrote the block scalar as one long
// line. textField's flattening is right for every other caller and stays; the
// remedy is this pane beside it, not a mode on it.
//
// So, against textField, point for point:
//
//   - The newline key is enabled. Enter is how a paragraph is made here, not a
//     commit; a host that wants a save key binds one of its own and intercepts
//     it before delegating, since what "save" means belongs to the host. This
//     pane interprets no `esc` and no save key.
//   - The height is fixed and told, not derived. DynamicHeight would grow a
//     60-line prompt to 60 rows and push the rest of the view off the screen;
//     a pane is given the room the host has and scrolls its content inside it.
//   - MaxHeight is cleared. It defaults to 99 and it is not only a display
//     cap: [textarea.Model] refuses to insert a newline once the value has
//     that many logical lines, so a long prompt would silently stop accepting
//     enter. A pane must never refuse a newline.
//   - Line numbers stay off, but for a new reason. The gutter is sized from
//     MaxHeight, which is now zero, so a numbered pane would reserve one
//     column and then draw "60" into it.
//   - The cursor line keeps its highlight. textField dropped it because a
//     full-width bar across one row of a form reads as an error; across the
//     row being edited in a document it reads as the cursor.
//   - The value keeps its newlines verbatim, which is the whole point.
type textPane struct {
	ta textarea.Model
	// seed is the value SetValue was handed, byte for byte, and is what Value
	// returns until a key edits the pane. Round-tripping through the textarea
	// is not byte-exact: its rune sanitizer expands a tab to four spaces and
	// folds a lone CR into a newline. Nothing the pane was merely *shown* may
	// come back altered, so a clean pane answers from what it was given. Once
	// the user has edited, the widget holds the truth and Value asks it.
	seed string
	// dirty latches on the first keypress that changes the value. It is the
	// data-loss fix: a host writes only a dirty pane, so opening a prompt and
	// closing it again cannot rewrite the file. Moving the cursor is not an
	// edit, which is why this is a before/after comparison of the value rather
	// than "a key arrived".
	dirty bool
}

// newTextPane is the constructor for every multi-line editing surface in the
// TUI.
func newTextPane() textPane {
	ta := textarea.New()
	// The pane owns its whole width; a per-row prompt gutter would only be a
	// two-column indent repeated down the left edge.
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.DynamicHeight = false
	// See the type's doc: zero is "no cap", and the cap is on inserting
	// newlines, not just on drawing them.
	ta.MaxHeight = 0
	ta.MaxContentHeight = 0
	// Default-enabled, set anyway: it is the single setting that most
	// distinguishes this from textField, and a reader comparing the two
	// constructors should find it stated on both sides.
	ta.KeyMap.InsertNewline.SetEnabled(true)
	return textPane{ta: ta}
}

// SetValue seeds the body. Newlines are kept verbatim — a block scalar's
// shape is its meaning — and the pane is clean afterwards, however many lines
// arrived.
func (p *textPane) SetValue(v string) {
	p.seed = v
	p.dirty = false
	p.ta.SetValue(v)
	// A reader opens a prompt at its first line, not past its last one, which
	// is the inversion of textField's CursorEnd.
	p.ta.MoveToBegin()
}

// Value is the body. It round-trips SetValue byte for byte while the pane is
// clean; after an edit it is what the widget holds.
func (p textPane) Value() string {
	if !p.dirty {
		return p.seed
	}
	return p.ta.Value()
}

// SetSize sets the columns the body wraps at and the rows it may draw. The
// content scrolls within that height rather than growing past it.
func (p *textPane) SetSize(width, height int) {
	p.ta.SetWidth(max(width, 1))
	p.ta.SetHeight(max(height, 1))
}

// Focus gives the pane the keyboard. A host uses Focused to decide that
// single-key global bindings must not fire.
func (p *textPane) Focus() tea.Cmd { return p.ta.Focus() }

// Blur takes the keyboard back.
func (p *textPane) Blur() { p.ta.Blur() }

// Focused reports whether the pane holds the keyboard.
func (p textPane) Focused() bool { return p.ta.Focused() }

// Update runs the pane's own key handling, including enter, and marks the pane
// dirty when the key changed the value. Keys the host reserves — esc, and
// whatever it means by save — never reach here: it intercepts them first.
func (p textPane) Update(msg tea.KeyPressMsg) (textPane, tea.Cmd) {
	before := p.ta.Value()
	var cmd tea.Cmd
	p.ta, cmd = p.ta.Update(msg)
	if !p.dirty && p.ta.Value() != before {
		p.dirty = true
	}
	return p, cmd
}

// View is the pane as drawn: exactly the height it was given, scrolled to
// follow the cursor.
func (p textPane) View() string { return p.ta.View() }

// Dirty reports whether a key has changed the value since SetValue.
func (p textPane) Dirty() bool { return p.dirty }

// LineCount is the number of lines in the current value. It is counted from
// Value rather than asked of the widget so that the two can never disagree
// about a body the pane is holding verbatim.
func (p textPane) LineCount() int {
	return strings.Count(p.Value(), "\n") + 1
}
