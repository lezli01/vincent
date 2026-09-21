package tui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/agent"
)

// The chat composer's inline picker core (issue #554, task 126.9): the rows,
// the filter buffer, the highlight, the window arithmetic and the renderer
// that every list drawn between the note line and the composer shares. It is
// the shape task 124 decision 94 already described in prose — "the rows, the
// ranking, the hostile-row guard, the window arithmetic and the renderer are
// shared" — made explicit in a type instead of in a mode enum.
//
// It knows nothing about what a row *is*: chatSkillList (chatskills.go) embeds
// it and supplies the skills, the verdicts and the words. Every decision
// comment here was moved from there rather than rewritten.
//
// Two rules hold the seam in place:
//
//   - The core carries data, not callbacks (issue #554 decision 1). The owner
//     fills title, tail and the three state lines before it draws, and the core
//     never calls back — no rebuild func, no owner back-pointer. chatview.go
//     assigns a bare chatSkillList{} and hideSkills re-zeroes the filter, so
//     anything a constructor wired would be silently dropped at run time with
//     nothing failing at compile time.
//   - Embedding shadows, it does not dispatch (issue #554 decision 6). Go has
//     no virtual dispatch through an embedded struct: if render called
//     l.titleLine() expecting the owner's, it would get the core's. So the core
//     reads fields, and the owner's render, height, window and titleLine shadow
//     the promoted ones rather than being dispatched back into.

// chatInlineList is an inline picker's whole state, with nothing in it that
// knows what the rows are. The zero value is a closed list holding nothing.
type chatInlineList struct {
	// open is whether the list is on screen.
	open bool
	// loading is a request in flight with nothing to draw yet.
	loading bool
	// err is a failed fetch, shown on the note line.
	err string

	// filter is the list's own buffer, drawn on the title line and never in
	// the composer.
	filter string
	// cursor indexes rows; -1 is "nothing highlighted", which is what keeps
	// `enter` meaning "send the message as typed" until the human moves into
	// the list.
	cursor int
	rows   []chatInlineRow
	// cap is the most rows the owner's build keeps, out of however many
	// matched. Zero is no cap, which is what the skills list uses: a catalog
	// is dozens of rows and a cap there would be theatre (issue #553
	// decision 1). It is a field rather than a package constant because
	// the lists that share this renderer want different numbers — the
	// zero value stays a closed list that has asked for nothing.
	cap int
	// matched is how many rows matched the filter *before* cap truncated
	// them. It is the half of `3 of 20` that tells a reader their row
	// may exist and simply not be listed (issue #553 decision 2), and it is
	// free: the owner's ranking already returns every matching index.
	matched int
	// rowLine draws one row, chatSkillRowLine unless a test replaced it.
	// It is a seam because "render styles only the visible rows" is a
	// property about *how many* calls happen, and counting them is
	// deterministic where a wall clock is not (issue #553 decision 3).
	rowLine func(r chatInlineRow, selected bool, width int) string

	// suppressed is the draft token an inline `esc` closed the list on. It
	// stays shut until the token changes, which is also what gives `↑`/`↓`
	// back to editing the draft (task 124 decision 90).
	suppressed string

	// title is the word the title line starts with, and tail the dim
	// dot-joined cells after it. Both are the owner's, set before every
	// draw: the core renders the line's shape and never its contents
	// (issue #554 decision 2).
	title string
	tail  []string
	// loadingText, emptyText and noMatchText are what stands in for the rows
	// while a request is in flight, when the answer held none, and when the
	// filter matched none. The core indents each one; the sentence is the
	// owner's.
	loadingText string
	emptyText   string
	noMatchText string
	// reserve is how many wrapped lines the highlighted row's description
	// gets under the list. They are *reserved* whenever the list is open
	// rather than added when a row is highlighted (task 124 decision 75), so
	// arrowing through the rows does not change the body's budget and scroll
	// the conversation under the reader.
	//
	// The owner sets it on every call rather than at construction, because
	// nothing constructs one of these lists and a zero reserve draws nothing
	// while claiming nothing (issue #554 decision 5).
	reserve int
}

// chatInlineRow is one drawn row: the flattened, sanitized text and whether it
// can be accepted.
type chatInlineRow struct {
	// insert is the exact text written into the draft — for a skills list the
	// adapter's own invocation, byte for byte, never rebuilt here.
	insert string
	// display is insert flattened and sanitized for drawing. They differ
	// only for a hostile row, which is disabled anyway.
	display     string
	hint        string
	description string
	note        string
	// disabled marks a row that cannot be accepted, with why in reason.
	disabled bool
	reason   string
}

// chatSkillFlatten makes agent-supplied text safe to measure and to draw.
//
// sanitizeText alone is not enough: it deliberately keeps `\n` and `\t`
// (outputlines.go), and a multi-line string reaching a row would trip the
// #299 hazard chatrender.go documents — render's per-line ansi.Truncate
// measures a joined string as the sum of its rows. So every field is
// flattened to one line first, the way task 124 decision 67 flattens an
// invocation's arguments, and sanitized after.
func chatSkillFlatten(s string) string {
	return sanitizeText(agent.OneLine(s, 0))
}

// chatSkillHostile reports text a row must not be able to insert into the
// draft: a C0 or C1 control — a newline among them — or an ANSI escape,
// whose introducer is itself a control.
//
// Whitespace is allowed on purpose (task 124 decision 71). Banning it would
// disable codex's linked form for a duplicated name,
// `[$review](/path with spaces/.agents/skills/review/SKILL.md)`, which is
// exactly what task 124 decision 30 requires — and on macOS a chat
// worktree's path always has a space in it, so the ban would have fired on
// the common case rather than on a hostile one.
func chatSkillHostile(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
	}) >= 0
}

// reset clears the filter and the highlight, which is the state an opener
// raises the list in: every row, nothing selected.
func (l *chatInlineList) reset() {
	l.filter = ""
	l.cursor = -1
}

// exact reports a row whose insert text is tok byte for byte — the test
// behind the inline opener's "the invocation is typed out and a space
// follows it" hide rule.
func (l *chatInlineList) exact(tok string) bool {
	for _, r := range l.rows {
		if r.insert == tok {
			return true
		}
	}
	return false
}

// move walks the highlight. From "nothing highlighted", `↓` lands on the
// first row and `↑` on the last.
func (l *chatInlineList) move(delta int) {
	if len(l.rows) == 0 {
		l.cursor = -1
		return
	}
	switch {
	case l.cursor < 0 && delta > 0:
		l.cursor = 0
	case l.cursor < 0:
		l.cursor = len(l.rows) - 1
	default:
		l.cursor = min(max(l.cursor+delta, 0), len(l.rows)-1)
	}
}

// pick is the row `enter` or `tab` would accept: the highlighted one, or the
// top match when nothing is highlighted.
func (l *chatInlineList) pick() (chatInlineRow, bool) {
	if len(l.rows) == 0 {
		return chatInlineRow{}, false
	}
	return l.rows[max(l.cursor, 0)], true
}

// typeText extends the filter with the text one key produced, and reports
// that the filter moved — the owner is what re-ranks the rows, since the core
// has no idea what they are (issue #554 decision 1).
//
// The press's own Text is what is asked, not its spelling: that is what a
// terminal says the key typed, and it is empty for every ctrl, alt and named
// key, so the list's buffer can never take a control by accident.
func (l *chatInlineList) typeText(text string) bool {
	if text == "" || chatSkillHostile(text) {
		return false
	}
	l.filter += text
	l.cursor = -1
	return true
}

// backspace shortens the filter, and reports the press that found it already
// empty: that closes the list, which is the way out the human walked in by.
// A press that shortened it leaves the rows to the owner to rebuild.
func (l *chatInlineList) backspace() (closed bool) {
	if l.filter == "" {
		return true
	}
	runes := []rune(l.filter)
	l.filter = string(runes[:len(runes)-1])
	l.cursor = -1
	return false
}

// height is how many lines the list occupies, which render must spend out of
// the body's budget before it draws anything (#299). It does not depend on
// the highlight: the reserved lines are reserved, not added.
func (l *chatInlineList) height(paneHeight int) int {
	if !l.open {
		return 0
	}
	return 1 + l.window(paneHeight) + l.reserve
}

// window is how many row lines are drawn: the picker's fixed window, what the
// pane has room for, and at most a third of the pane — below the §7.4
// popup's half, because this list is an aid to typing and that popup is the
// turn itself.
func (l *chatInlineList) window(paneHeight int) int {
	rows := max(len(l.rows), 1)
	return max(min(rows, pickerWindow, paneHeight/3-1-l.reserve), 1)
}

// render draws the list: a title line, the windowed rows, and the highlighted
// row's description wrapped into the fixed reserve. One slice element per
// rendered line (#299).
//
// Everything it says in words — the title, its tail and the three lines that
// stand in for rows — the owner has already put in the fields it reads.
func (l *chatInlineList) render(width, paneHeight int) []string {
	if !l.open {
		return nil
	}
	win := l.window(paneHeight)
	out := make([]string, 0, 1+win+l.reserve)
	out = append(out, " "+l.titleLine())

	switch {
	case l.loading:
		out = append(out, styleDim.Render("   "+l.loadingText))
	case len(l.rows) == 0 && l.filter != "":
		out = append(out, styleDim.Render("   "+l.noMatchText))
	case len(l.rows) == 0:
		out = append(out, styleDim.Render("   "+l.emptyText))
	default:
		out = append(out, l.visibleRows(width, win)...)
	}
	// The reserve, whether or not a row is highlighted.
	body := make([]string, l.reserve)
	if r, ok := l.pick(); ok && l.cursor >= 0 {
		text := r.description
		if r.disabled && r.reason != "" {
			text = strings.TrimSpace(r.reason + " · " + text)
		}
		wrapped := wrapPlain(text, max(width-5, 8))
		for i := range min(len(wrapped), l.reserve) {
			body[i] = styleDim.Render("    " + wrapped[i])
		}
	}
	out = append(out, body...)
	// Pad to the promised height: a window narrower than the rows still owes
	// render exactly what height() said it would draw.
	for len(out) < 1+win+l.reserve {
		out = append(out, "")
	}
	return out[:1+win+l.reserve]
}

// visibleRows styles the rows the window shows, and only those.
//
// The list used to style every row it held and window the result afterwards,
// which made one frame cost the whole row set — 2.0 ms at this repo's 1,615
// tracked files and 129 ms at 100,000 (issue #553). Computing the range
// first makes the cost flat in the row count.
//
// Equivalence with the old path is by construction rather than by
// inspection: `window` (detailrender.go) returns every line when there are
// at most `height` of them and `lines[windowStart(…) : +height]` otherwise,
// so these two indices reproduce both of its arms. Its `height <= 0` arm is
// unreachable here — chatInlineList.window never returns less than 1. The
// scroll behaviour a reader sees is `windowStart`'s, and is unchanged.
func (l *chatInlineList) visibleRows(width, win int) []string {
	start := windowStart(len(l.rows), max(l.cursor, 0), win)
	end := min(start+win, len(l.rows))
	draw := l.rowLine
	if draw == nil {
		draw = chatSkillRowLine
	}
	out := make([]string, 0, max(end-start, 0))
	for i := start; i < end; i++ {
		out = append(out, draw(l.rows[i], i == l.cursor, width))
	}
	return out
}

// titleLine draws the shape of the title line: the owner's title, then the
// owner's cells dot-joined and dimmed after it.
func (l *chatInlineList) titleLine() string {
	title := styleTitle.Render(l.title)
	if len(l.tail) > 0 {
		title += styleDim.Render("  ·  " + strings.Join(l.tail, "  ·  "))
	}
	return title
}

// capNote is the `3 of 20` cell, "" when the cap did not bind. The owner
// splices it into the tail where it wants it read.
//
// A capped build says how many of the matches are listed, so "your row
// is not here" can never read as "your row does not match" (issue #553
// decision 2). The picker's `▼ %d more` idiom is deliberately not
// reused: there it means the *window* overflowed, and one string may
// not mean two things.
func (l *chatInlineList) capNote() string {
	if l.matched > len(l.rows) {
		return fmt.Sprintf("%d of %d", len(l.rows), l.matched)
	}
	return ""
}

// chatDraftToken is the whitespace-delimited token under the composer's
// cursor, and where it sits in the draft.
type chatDraftToken struct {
	// text is the whole token, runes to the *right* of the cursor included.
	text string
	// line is its 0-indexed hard row, start its first rune's index in that
	// row.
	line, start int
	// first is true when nothing but whitespace precedes it on its row.
	first bool
	// spaceAfter is true when a whitespace rune follows it on its row.
	spaceAfter bool
}

// end is one past the token's last rune, in its row.
func (t chatDraftToken) end() int { return t.start + len([]rune(t.text)) }

// chatDraftTokenAt reads the token under ta's cursor.
//
// The text is the textarea's own Word(), which returns the whole word the
// cursor is in — runes to the right of it included — and "" when the cursor
// sits at the start of a row or on the rune just after a space
// (charm.land/bubbles/v2@v2.2.1 textarea/textarea.go:824). That second half
// is what gives the inline list its "a space follows the invocation" hide
// rule almost for free, and the first is the behaviour to spec rather than
// to work around: a cursor parked mid-token filters on the whole token.
//
// Word() reports no position, so the start is scanned here from the rune
// Word() itself starts at — the one to the *left* of the cursor.
func chatDraftTokenAt(ta *textarea.Model) (chatDraftToken, bool) {
	word := ta.Word()
	if word == "" {
		return chatDraftToken{}, false
	}
	row, col := ta.Line(), ta.Column()
	// Value() joins the textarea's hard rows with "\n" and Line() indexes
	// those same rows, so this is the row the cursor is on — soft wrapping
	// does not enter into it.
	rows := strings.Split(ta.Value(), "\n")
	if row < 0 || row >= len(rows) {
		return chatDraftToken{}, false
	}
	runes := []rune(rows[row])
	at := col - 1
	if at < 0 || at >= len(runes) {
		return chatDraftToken{}, false
	}
	tok := chatDraftToken{text: word, line: row}
	tok.start = at
	for tok.start > 0 && !unicode.IsSpace(runes[tok.start-1]) {
		tok.start--
	}
	tok.first = strings.TrimSpace(string(runes[:tok.start])) == ""
	if end := tok.end(); end < len(runes) {
		tok.spaceAfter = unicode.IsSpace(runes[end])
	}
	return tok, true
}

// chatSkillRowLine draws one row. The selection is a `› ` marker as well as a
// colour, so it survives NO_COLOR and 16 colours (§15 Colour).
//
// Below roughly 40 columns the description and the note are dropped before
// the insert text is truncated: what a row *is* is what a reader picks by.
func chatSkillRowLine(r chatInlineRow, selected bool, width int) string {
	inner := max(width-3, 8)
	mark, style := "  ", styleDim
	if selected {
		mark, style = styleFocus.Render("› "), styleTitle
	}
	if r.disabled {
		style = styleBad
	}
	name := ansi.Truncate(r.display, inner, "…")
	line := " " + mark + style.Render(name)
	used := cols(name)
	if r.hint != "" && used+1+cols(r.hint) <= inner {
		line += styleDim.Render(" " + r.hint)
		used += 1 + cols(r.hint)
	}
	// Below roughly 40 columns the description and the note go before the
	// invocation is truncated: what a row *is* is what a reader picks by.
	rest := strings.Join(nonEmptyStrings(r.description, r.note), "  ·  ")
	if room := inner - used - 2; rest != "" && room >= 8 && width >= 40 {
		line += "  " + styleDim.Render(ansi.Truncate(rest, room, "…"))
	}
	return line
}

// nonEmptyStrings drops the fields the CLI said nothing about, so two empties
// never render as a separator with nothing on either side.
func nonEmptyStrings(in ...string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
