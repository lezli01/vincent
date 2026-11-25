package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/keymap"
)

// Searching a task's Output and Diff (#1074). `/` had no handler on either,
// and N next failure was the only jump. paneFind is the search both share: a
// find line while typing, then a layer of its own — n and N walk the
// matches, / searches again, esc ends it — so n keeps meaning new task and N
// next failure everywhere a search is not running.

type paneFind struct {
	typing bool
	buf    lineEdit
	query  string
	hits   []int // line indices of the content, in order
	cur    int
}

// active is a search on screen: being typed, or committed.
func (f *paneFind) active() bool { return f.typing || f.query != "" }

func (f *paneFind) open() {
	f.typing = true
	f.buf.max = 200
	f.buf.Set(f.query)
}

func (f *paneFind) end() { *f = paneFind{} }

// scan finds the query in lines, case-insensitively, keeping the current
// match where it still is.
func (f *paneFind) scan(lines []string) {
	f.hits = f.hits[:0]
	if f.query == "" {
		return
	}
	q := strings.ToLower(f.query)
	for i, l := range lines {
		if strings.Contains(strings.ToLower(ansi.Strip(l)), q) {
			f.hits = append(f.hits, i)
		}
	}
	f.cur = min(f.cur, max(len(f.hits)-1, 0))
}

// step moves to the next (+1) or previous (-1) match, wrapping.
func (f *paneFind) step(delta int) {
	if n := len(f.hits); n > 0 {
		f.cur = ((f.cur+delta)%n + n) % n
	}
}

// line is the current match's content line, false with none.
func (f *paneFind) line() (int, bool) {
	if f.query == "" || len(f.hits) == 0 {
		return 0, false
	}
	return f.hits[f.cur], true
}

// highlight is lines with the current match's line drawn reversed.
func (f *paneFind) highlight(lines []string) []string {
	at, ok := f.line()
	if !ok || at >= len(lines) {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	out[at] = chatMatchCurrentStyle.Render(ansi.Strip(lines[at]))
	return out
}

// bar is the line under the pane: the find line while typing, the count and
// the keys once committed.
func (f *paneFind) bar(width int, note string) string {
	if f.typing {
		return ansi.Truncate(styleKey.Render("/")+f.buf.View()+
			styleDim.Render("  enter search · esc cancel"), max(width, 1), "…")
	}
	count := "no match"
	if len(f.hits) > 0 {
		count = fmt.Sprintf("match %d/%d", f.cur+1, len(f.hits))
	}
	text := fmt.Sprintf("/%s  %s · n next · N previous · / again · esc ends the search", f.query, count)
	if note != "" {
		text += " · " + note
	}
	return ansi.Truncate(styleDim.Render(text), max(width, 1), "…")
}

// key runs one key on an active search and reports whether it was the
// search's. Neither pane's moved clears its findShown: the highlight key
// already changes with the match, and an empty findShown is how the render
// knows no highlight is drawn — clearing it on esc left the last match
// reversed after the search had ended (#1154). commit is told when a query is committed, step when the match
// moved, so the pane can scan and scroll.
func (f *paneFind) key(msg tea.KeyPressMsg, commit, moved func()) bool {
	if f.typing {
		switch msg.String() {
		case "esc":
			f.typing = false
			if f.query == "" {
				f.end()
			}
		case "enter":
			f.typing = false
			f.query, f.cur = strings.TrimSpace(f.buf.String()), 0
			if f.query == "" {
				f.end()
				return true
			}
			commit()
			moved()
		case "ctrl+c":
			return false
		default:
			f.buf.Key(msg)
		}
		return true
	}
	if f.query == "" {
		return false
	}
	switch msg.String() {
	case "n":
		f.step(1)
		moved()
	case "N":
		f.step(-1)
		moved()
	case "esc":
		f.end()
		moved()
	case opKey(keymap.Filter):
		f.open()
	default:
		return false
	}
	return true
}

// findKey runs a key on the Output pane's search; false when the search did
// not take it.
func (d *detail) findKey(msg tea.KeyPressMsg) bool {
	return d.find.key(msg,
		func() { d.find.scan(d.feedLines) },
		func() {
			if _, ok := d.find.line(); ok {
				// A reader jumping to a match is not following the tail.
				d.following, d.findJump = false, true
			}
		})
}

// diffReloadKey reads the task's diff again (#1074); a step ending does too.
const diffReloadKey = "ctrl+r"

// findKey runs a key on the Diff pane's search. Committing one opens every
// file first: a match inside a folded file is still a match.
func (p *diffPane) findKey(msg tea.KeyPressMsg) bool {
	return p.find.key(msg,
		func() {
			p.foldAll(true)
			p.rows = p.buildRows()
			lines := make([]string, len(p.rows))
			for i, r := range p.rows {
				lines[i] = r.text
			}
			p.vp.SetContent(strings.Join(lines, "\n"))
			p.built = true
			p.findLines, p.findShown = lines, ""
			p.find.scan(lines)
		},
		func() { p.findJump = true })
}
