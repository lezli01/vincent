package tui

import (
	"fmt"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// scrollPane is the TUI's one scrolling region (task 135.1): content taller
// than its pane lives in one, with a cue saying where the window is, rather
// than being clipped to its first rows — a static first-N clip is a bug under
// the §15 wrap-or-truncate convention.
//
// viewport.Model is the scroll engine — offsets and their clamping — and
// nothing more. The pane owns its keys and never forwards one to the
// viewport's keymap, whose j/k/f/b/u/d would swallow keys the surfaces
// already bind; and its content is lines the caller has already wrapped
// (wrapText), never left to the viewport's SoftWrap, which wraps styled
// text by cutting through it.
type scrollPane struct {
	vp    viewport.Model
	lines []string
	// cue reserves the pane's last row, while the content overflows, for the
	// position line the help sheet introduced (task 129.17).
	cue bool
	// h is the pane's height, cue row included.
	h int
	// keys name the lines for the anchor SetContentKeyed holds, nil when the
	// content came without them.
	keys []string
}

// newScrollPane is a pane of width×height cells, cue row included.
func newScrollPane(width, height int, cue bool) scrollPane {
	p := scrollPane{vp: viewport.New(), cue: cue}
	p.SetSize(width, height)
	return p
}

// SetSize resizes the pane, keeping the window's top line where it can.
func (p *scrollPane) SetSize(width, height int) {
	p.vp.SetWidth(max(width, 1))
	p.h = max(height, 1)
	p.fit()
}

// fit sizes the viewport to the pane, the cue row taken out of it while the
// content overflows, and clamps the offset to what is now reachable.
func (p *scrollPane) fit() {
	off := p.vp.YOffset()
	visible := p.h
	if p.cue && len(p.lines) > p.h && p.h > 1 {
		visible--
	}
	p.vp.SetHeight(visible)
	p.vp.SetYOffset(off)
}

// SetContentLines replaces the content. The lines must already fit the
// pane's width; the window keeps its offset, clamped to the new content.
func (p *scrollPane) SetContentLines(lines []string) {
	p.lines = lines
	p.keys = nil
	p.vp.SetContentLines(lines)
	p.fit()
}

// SetContentKeyed replaces the content the way SetContentLines does, and
// holds the anchor: when the line whose key was at the window's top is still
// in the content, the window moves with it. keys[i] names lines[i]; an empty
// key never anchors.
func (p *scrollPane) SetContentKeyed(lines, keys []string) {
	anchor := p.topKey()
	p.SetContentLines(lines)
	p.keys = keys
	if anchor == "" {
		return
	}
	for i, k := range keys {
		if k == anchor {
			p.vp.SetYOffset(i)
			return
		}
	}
}

// topKey is the key of the line at the window's top, if it has one.
func (p *scrollPane) topKey() string {
	if off := p.vp.YOffset(); off < len(p.keys) {
		return p.keys[off]
	}
	return ""
}

// clipped reports whether the content is taller than the window.
func (p *scrollPane) clipped() bool { return len(p.lines) > p.vp.Height() }

// Offset is the content line at the window's top.
func (p *scrollPane) Offset() int { return p.vp.YOffset() }

// GotoTop puts the window back at the first line.
func (p *scrollPane) GotoTop() { p.vp.SetYOffset(0) }

// EnsureVisible scrolls the least it takes to show lines from..to — a
// cursor's row and the rows that belong to it — favouring from when they do
// not all fit.
func (p *scrollPane) EnsureVisible(from, to int) {
	h := p.vp.Height()
	off := p.vp.YOffset()
	if to >= off+h {
		off = to - h + 1
	}
	if from < off {
		off = from
	}
	p.vp.SetYOffset(off)
}

// Update scrolls on the pane's own keys — ↑/↓, pgup/pgdn, home/end — and the
// mouse wheel, and reports whether it consumed msg. Everything else is the
// caller's: j, k, f, b, u and d in particular are never taken.
func (p *scrollPane) Update(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		page := max(p.vp.Height()-1, 1)
		switch msg.String() {
		case "up":
			p.vp.SetYOffset(p.vp.YOffset() - 1)
		case "down":
			p.vp.SetYOffset(p.vp.YOffset() + 1)
		case "pgup":
			p.vp.SetYOffset(p.vp.YOffset() - page)
		case "pgdown":
			p.vp.SetYOffset(p.vp.YOffset() + page)
		case "home":
			p.vp.SetYOffset(0)
		case "end":
			p.vp.SetYOffset(len(p.lines))
		default:
			return false
		}
		return true
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			p.vp.SetYOffset(p.vp.YOffset() - wheelStep)
		case tea.MouseWheelDown:
			p.vp.SetYOffset(p.vp.YOffset() + wheelStep)
		default:
			return false
		}
		return true
	}
	return false
}

// wheelStep is how many lines one notch of the wheel scrolls a pane.
const wheelStep = 3

// View is the window's lines, the cue last when the content overflows and
// the pane has one. It is not padded: the frame around it does that.
func (p *scrollPane) View() []string {
	off := p.vp.YOffset()
	h := p.vp.Height()
	shown := p.lines[min(off, len(p.lines)):min(off+h, len(p.lines))]
	out := make([]string, len(shown), len(shown)+1)
	copy(out, shown)
	if p.cue && p.clipped() {
		out = append(out, scrollCue(off, h, len(p.lines)))
	}
	return out
}

// scrollCue is a pane's last line when its content does not fit: where the
// window is, and which way there is more. The wording is the help sheet's
// (task 129.17), now every pane's.
func scrollCue(from, visible, total int) string {
	to := min(from+visible, total)
	arrows := ""
	if from > 0 {
		arrows += "↑"
	}
	if to < total {
		arrows += "↓"
	}
	return styleDim.Render(fmt.Sprintf(" %s more · lines %d–%d of %d", arrows, from+1, to, total))
}
