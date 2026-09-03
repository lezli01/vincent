package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The editor's typed-value overlays: the multi-line pane a block scalar is
// edited in, and the key/value sub-form a mapping is (issue #320, the write
// half). Both implement [wfEditorOverlay], both own every key while they are
// open, and both hand a committed value back to the row through a closure
// rather than reaching into the layer — the host decides what a commit means,
// which is the same split [textPane]'s doc states.

// wfEditorPane is a `prompt:`, a `run:` or an `instructions:` being edited.
//
// It exists because opening one of those in the one-line [textField] was a
// silent data-loss bug: SetValue flattens newlines by design, editorActivate
// seeded the field with the row's stored value, so the flattened text differed
// from the row's and commitRow's "nothing changed" guard missed — one
// keystroke, no typing, and a 60-line block scalar went back as one line. The
// remedy is this pane plus the Dirty gate below: a pane nobody typed into
// writes nothing at all.
type wfEditorPane struct {
	pane textPane
	// commit is what ctrl+s does with the body. The overlay never patches: it
	// does not know whether the row is a set, a remove or a block scalar, and
	// commitRow already does.
	commit func(value string) tea.Cmd
}

// newWFEditorPane opens the pane on a value, focused. The returned command is
// the cursor blink the textarea starts.
func newWFEditorPane(value string, commit func(string) tea.Cmd) (*wfEditorPane, tea.Cmd) {
	p := newTextPane()
	p.SetValue(value)
	cmd := p.Focus()
	return &wfEditorPane{pane: p, commit: commit}, cmd
}

// Update runs the pane's keys. esc and ctrl+s are intercepted here rather than
// delegated: enter inserts a newline in a pane, so a save key has to be one the
// body can never mean, and the pane itself interprets neither (its doc says so).
func (o *wfEditorPane) Update(msg tea.KeyPressMsg) (wfEditorOverlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "ctrl+s":
		if !o.pane.Dirty() {
			// The whole point of the Dirty latch: opening a prompt and
			// closing it again must be a no-op on the file, whatever the
			// pane's round trip through the widget would have produced.
			return nil, nil
		}
		return nil, o.commit(o.pane.Value())
	}
	pane, cmd := o.pane.Update(msg)
	o.pane = pane
	return o, cmd
}

// wfPaneChrome is the rows View spends on the form's own header, its footer
// and the pane's key hint. The pane is given the rest, because a pane that
// asked for the whole panel height would push the footer off the screen.
const wfPaneChrome = 8

// wfPaneMinHeight keeps a pane usable on a short terminal: a single visible
// row still scrolls, and refusing to draw at all would be worse.
const wfPaneMinHeight = 3

func (o *wfEditorPane) View(width, height int) string {
	o.pane.SetSize(width, max(height-wfPaneChrome, wfPaneMinHeight))
	hint := "  enter newline · ctrl+s save · esc cancel"
	if !o.pane.Dirty() {
		hint += " (unchanged: saving writes nothing)"
	}
	return o.pane.View() + "\n" + styleDim.Render(hint)
}

// FullPane is true: a block scalar has no room to share with the rows behind
// it, and the rows are what it would have to share with.
func (o *wfEditorPane) FullPane() bool { return true }

func (o *wfEditorPane) Value() string { return o.pane.Value() }
func (o *wfEditorPane) Dirty() bool   { return o.pane.Dirty() }

// wfEditorMap is the sub-form a `map` row opens: a step's `env:`, a lane's
// `fields:`. Before it those rows fell through to renderYAMLScalar and
// committed a quoted scalar over a mapping, which is a file the daemon then
// refused.
//
// It edits the row's own `k=v, k=v` text — the form [wfMap] reads and
// [renderFlowMap] writes — so what the row shows, what the sub-form holds and
// what the commit sends are one representation rather than three.
type wfEditorMap struct {
	pairs  []wfMapPair
	seed   string
	cursor int
	// input is the pair being typed, as one `key=value` line. Editing a pair
	// as a line rather than as two fields keeps the sub-form one column wide,
	// which is all the value column it is drawn in has.
	input  *textField
	commit func(value string) tea.Cmd
}

type wfMapPair struct{ key, value string }

// newWFEditorMap opens the sub-form on a row's rendered value.
func newWFEditorMap(value string, commit func(string) tea.Cmd) *wfEditorMap {
	return &wfEditorMap{pairs: parseMapRow(value), seed: value, commit: commit}
}

// parseMapRow reads the `k=v, k=v` form back. A pair with no `=` is kept as a
// key with an empty value rather than dropped: dropping it would delete a key
// the file has because the row could not parse it.
func parseMapRow(value string) []wfMapPair {
	var out []wfMapPair
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		out = append(out, wfMapPair{key: strings.TrimSpace(k), value: strings.TrimSpace(v)})
	}
	return out
}

// renderMapRow is parseMapRow's inverse, with the keys sorted the way wfMap
// sorts them so a round trip through the sub-form does not reorder the row.
func renderMapRow(pairs []wfMapPair) string {
	sorted := append([]wfMapPair(nil), pairs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].key < sorted[j].key })
	parts := make([]string, 0, len(sorted))
	for _, p := range sorted {
		if p.key == "" {
			continue
		}
		parts = append(parts, p.key+"="+p.value)
	}
	return strings.Join(parts, ", ")
}

func (o *wfEditorMap) Update(msg tea.KeyPressMsg) (wfEditorOverlay, tea.Cmd) {
	if o.input != nil {
		return o.updateInput(msg)
	}
	switch msg.String() {
	case "esc":
		return nil, nil
	case "ctrl+s":
		return nil, o.commit(o.Value())
	case "up", "k":
		o.cursor = max(0, o.cursor-1)
	case "down", "j":
		o.cursor = min(len(o.pairs), o.cursor+1)
	case "enter":
		in := newTextField()
		if o.cursor < len(o.pairs) {
			in.SetValue(o.pairs[o.cursor].key + "=" + o.pairs[o.cursor].value)
		}
		in.Focus()
		o.input = &in
	case "d":
		// No confirmation here, deliberately: nothing has reached the file
		// yet, and esc still abandons the whole sub-form. The confirmation
		// §15 view 5 asks for guards the key that writes a removal.
		if o.cursor < len(o.pairs) {
			o.pairs = append(o.pairs[:o.cursor], o.pairs[o.cursor+1:]...)
			o.cursor = min(o.cursor, len(o.pairs))
		}
	}
	return o, nil
}

func (o *wfEditorMap) updateInput(msg tea.KeyPressMsg) (wfEditorOverlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		o.input = nil
		return o, nil
	case "enter":
		text := strings.TrimSpace(o.input.Value())
		o.input = nil
		k, v, _ := strings.Cut(text, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case k == "":
		case o.cursor < len(o.pairs):
			o.pairs[o.cursor] = wfMapPair{key: k, value: v}
		default:
			o.pairs = append(o.pairs, wfMapPair{key: k, value: v})
		}
		return o, nil
	}
	in, cmd := o.input.Update(msg)
	o.input = &in
	return o, cmd
}

func (o *wfEditorMap) View(width, _ int) string {
	rows := make([]string, 0, len(o.pairs)+2)
	for i, p := range o.pairs {
		line := "  " + p.key + " = " + p.value
		if i == o.cursor {
			if o.input != nil {
				o.input.SetWidth(max(width-2, 10))
				line = "› " + strings.Join(o.input.rows(), "\n")
			} else {
				line = styleFocus.Render("› ") + p.key + " = " + p.value
			}
		}
		rows = append(rows, line)
	}
	add := "  + add a key"
	if o.cursor == len(o.pairs) {
		if o.input != nil {
			o.input.SetWidth(max(width-2, 10))
			add = "› " + strings.Join(o.input.rows(), "\n")
		} else {
			add = styleFocus.Render("› ") + "add a key"
		}
	}
	rows = append(rows, add, styleDim.Render("  enter edit · d drop · ctrl+s save · esc cancel"))
	return strings.Join(rows, "\n")
}

// FullPane is false: a mapping is short, and keeping the other rows on screen
// is what says which block the keys belong to.
func (o *wfEditorMap) FullPane() bool { return false }

func (o *wfEditorMap) Value() string { return renderMapRow(o.pairs) }
func (o *wfEditorMap) Dirty() bool   { return o.Value() != o.seed }
