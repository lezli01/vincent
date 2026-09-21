package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace's skill list (§15 view 9, task 124.13, issue #509): the
// skills the chat's agent CLI would load in the chat's directory, drawn
// between the note line and the composer, opened with `tab` (or `f2`) and
// closed with `esc`.
//
// The whole adapter syntax comes off the wire (task 124 decision 9): the
// sigil, where it goes and each row's exact invocation are the daemon's
// adapter's words, and nothing here builds one. `/` is never a case label —
// it is the `filter` operation's default key (internal/keymap/keymap.go).
//
// The list owns its own filter buffer and never touches the draft until a
// row is accepted (task 124 decision 70). The alternative — opening by
// inserting the sigil into the message — has a hole with
// `invoke_position: leading`: on a draft of `fix the bug` it makes the first
// token `/fix`, so browsing is filtered rather than whole, accepting eats
// the word, and an `enter` with no highlight sends a message beginning with
// a `/` nobody typed.

// chatSkillsDescLines is how many wrapped lines the highlighted row's
// description gets under the list. They are *reserved* whenever the list is
// open rather than added when a row is highlighted (decision 75), so arrowing
// through the rows does not change the body's budget and scroll the
// conversation under the reader.
const chatSkillsDescLines = 3

// chatSkillMode is which of the list's two openers put it on screen
// (task 124 decision 94). The rows, the ranking, the hostile-row guard, the
// window arithmetic and the renderer are shared; what differs is who owns
// the keyboard and where the filter comes from.
type chatSkillMode uint8

const (
	// skillModeBrowse is `tab`'s list: it owns the keyboard and types into
	// its own filter buffer (decision 70).
	skillModeBrowse chatSkillMode = iota
	// skillModeInline is the draft's list: the composer keeps the keyboard
	// and the filter is the draft's own sigil token, minus the sigil.
	skillModeInline
)

// chatSkillList is the list's whole state. The zero value is a closed list
// that has asked for nothing.
type chatSkillList struct {
	// open is whether the list is on screen.
	open bool
	// mode is which opener it is on screen for.
	mode chatSkillMode
	// loading is a request in flight with nothing to draw yet.
	loading bool
	// data is the last answer for chatID; nil until one arrives. It is kept
	// across opens and dropped on a `chat.*` event, which is the daemon's
	// own signal that a turn boundary may have changed the listing
	// (decision 73: there is no refresh key here, and ctrl+r keeps its one
	// meaning).
	data *apiclient.ChatSkills
	// err is a failed fetch, shown on the note line.
	err string

	// filter is the list's own buffer, drawn on the title line and never in
	// the composer.
	filter string
	// cursor indexes rows; -1 is "nothing highlighted", which is what keeps
	// `enter` meaning "send the message as typed" until the human moves into
	// the list.
	cursor int
	rows   []chatSkillRow
	// cap is the most rows build() keeps, out of however many matched.
	// Zero is no cap, which is what the skills list uses: a catalog is
	// dozens of rows and a cap there would be theatre (issue #553
	// decision 1). It is a field rather than a package constant because
	// the lists that share this renderer want different numbers — the
	// zero value stays a closed list that has asked for nothing.
	cap int
	// matched is how many rows matched the filter *before* cap truncated
	// them. It is the half of `3 of 20` that tells a reader their row
	// may exist and simply not be listed (decision 2), and it is free:
	// rankChatSkills already returns every matching index.
	matched int
	// rowLine draws one row, chatSkillRowLine unless a test replaced it.
	// It is a seam because "render styles only the visible rows" is a
	// property about *how many* calls happen, and counting them is
	// deterministic where a wall clock is not (decision 3).
	rowLine func(r chatSkillRow, selected bool, width int) string

	// suppressed is the draft token an inline `esc` closed the list on. It
	// stays shut until the token changes, which is also what gives `↑`/`↓`
	// back to editing the draft (decision 90).
	suppressed string
	// probeFailed latches the inline opener off for this chat (decision 91):
	// the human never asked for that probe, so a fetch that failed — or one
	// that answered a `list_verdict` other than `supported` — writes nothing
	// and is not re-fired on the next keystroke. forget() clears it when a
	// `chat.*` event drops the cached answer, and `tab` clears it because
	// then the human *did* ask.
	probeFailed bool
	// inlineNote is the inline opener's dim line under the composer: the
	// unmatched-name hint (decision 92) or what an accepted skill takes
	// (issue #510 item 3). Never a refusal — vincent sends the message as
	// typed (task 025 decision 5).
	inlineNote string
	// noteFor is the invocation inlineNote is about when it is an
	// after-accept note, "" when it is the transient hint. The note lives
	// until that invocation leaves the draft.
	noteFor string
}

// chatSkillRow is one drawn row: the flattened, sanitized text and whether it
// can be accepted.
type chatSkillRow struct {
	// invocation is the exact text inserted into the draft — the adapter's
	// own, byte for byte, never rebuilt here.
	invocation string
	// display is invocation flattened and sanitized for drawing. They differ
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
// Whitespace is allowed on purpose (decision 71). Banning it would disable
// codex's linked form for a duplicated name,
// `[$review](/path with spaces/.agents/skills/review/SKILL.md)`, which is
// exactly what task 124 decision 30 requires — and on macOS a chat
// worktree's path always has a space in it, so the ban would have fired on
// the common case rather than on a hostile one.
func chatSkillHostile(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
	}) >= 0
}

// reset clears the filter and the highlight, which is the state `tab` opens
// the list in: every row, nothing selected.
func (l *chatSkillList) reset() {
	l.filter = ""
	l.cursor = -1
}

// forget drops the cached answer. The next open asks again, and the inline
// opener's latch goes with it (decision 91): the answer this chat was told
// not to ask about again is the one that has just been thrown away.
func (l *chatSkillList) forget() {
	l.data = nil
	l.err = ""
	l.loading = false
	l.probeFailed = false
}

// canList reports an answer that has rows to draw — "supported" is the only
// verdict that makes an empty list mean "none" (task 124.9).
func (l *chatSkillList) canList() bool {
	return l.data != nil && l.data.ListVerdict == "supported"
}

// canInvoke reports that a picked row may be written into the draft.
// `unknown` behaves as `unsupported` here (decision 72): decision 58 gives an
// unregistered adapter `unknown` on both verdicts, and a client must not
// insert an invocation nobody said would work.
func (l *chatSkillList) canInvoke() bool {
	return l.data != nil && l.data.InvokeVerdict == "supported"
}

// build re-ranks the rows against the filter and keeps the highlight inside
// them.
func (l *chatSkillList) build() {
	l.rows = l.rows[:0]
	l.matched = 0
	if !l.canList() {
		l.cursor = -1
		return
	}
	reason := ""
	if !l.canInvoke() {
		reason = chatSkillsNoInvokeReason(l.data)
	}
	ranked := rankChatSkills(l.data.Skills, l.filter)
	l.matched = len(ranked)
	// The cap is applied *after* the ranking, so what survives is the best
	// matches rather than an arbitrary prefix of the catalog (issue #553).
	if l.cap > 0 && len(ranked) > l.cap {
		ranked = ranked[:l.cap]
	}
	for _, i := range ranked {
		s := l.data.Skills[i]
		row := chatSkillRow{
			invocation:  s.Invocation,
			display:     chatSkillFlatten(s.Invocation),
			hint:        chatSkillFlatten(s.ArgumentHint),
			description: chatSkillFlatten(s.Description),
			note:        chatSkillNote(s),
			disabled:    reason != "",
			reason:      reason,
		}
		switch {
		case s.Invocation == "":
			row.disabled, row.reason = true, "this skill has no invocation"
		case chatSkillHostile(s.Invocation):
			row.disabled, row.reason = true,
				"this skill's invocation carries a control character — vincent will not type it for you"
		}
		if row.display == "" {
			row.display = chatSkillFlatten(s.Name)
		}
		l.rows = append(l.rows, row)
	}
	if l.cursor >= len(l.rows) {
		l.cursor = len(l.rows) - 1
	}
}

// chatSkillNote is the dim tail: the scope and the plugin, each only where
// the CLI said one. A blank cell, never a dash (task 124 decisions 8 and 60).
func chatSkillNote(s apiclient.ChatSkill) string {
	parts := make([]string, 0, 2)
	if s.Scope != "" {
		parts = append(parts, chatSkillFlatten(s.Scope))
	}
	if s.Plugin != "" {
		parts = append(parts, chatSkillFlatten(s.Plugin))
	}
	return strings.Join(parts, " · ")
}

// chatSkillsNoInvokeReason says why a listed skill cannot be picked into the
// draft.
func chatSkillsNoInvokeReason(d *apiclient.ChatSkills) string {
	if d.UnavailableReason != "" {
		return d.UnavailableReason
	}
	return d.Agent + " does not say how a message invokes a skill — type it yourself"
}

// rankChatSkills orders the indices of skills that match q, Claude Code's
// own order (issue #509's prior art): a prefix of the name first, then a
// prefix of the part after a plugin namespace's `:` or of an alias, then a
// substring of the description. Within a tier the CLI's order is kept, which
// is what task 124 decision 17 requires of a list whose names may repeat.
//
// An empty q keeps every row in the CLI's order.
func rankChatSkills(skills []apiclient.ChatSkill, q string) []int {
	if len(skills) == 0 {
		return nil
	}
	if q = strings.ToLower(strings.TrimSpace(q)); q == "" {
		out := make([]int, len(skills))
		for i := range skills {
			out[i] = i
		}
		return out
	}
	tiers := make([][]int, 3)
	for i, s := range skills {
		switch tier := chatSkillTier(s, q); tier {
		case 0, 1, 2:
			tiers[tier] = append(tiers[tier], i)
		}
	}
	out := make([]int, 0, len(skills))
	for _, tier := range tiers {
		out = append(out, tier...)
	}
	return out
}

// chatSkillTier is one skill's match tier for q, or -1 for no match.
func chatSkillTier(s apiclient.ChatSkill, q string) int {
	name := strings.ToLower(s.Name)
	if strings.HasPrefix(name, q) {
		return 0
	}
	// A plugin skill matches on its bare name too, so `deploy` finds
	// `myplugin:deploy-app` — and so does any alias the CLI reported.
	if _, bare, ok := strings.Cut(name, ":"); ok && strings.HasPrefix(bare, q) {
		return 1
	}
	for _, a := range s.Aliases {
		if strings.HasPrefix(strings.ToLower(a), q) {
			return 1
		}
	}
	if strings.Contains(strings.ToLower(s.Description), q) {
		return 2
	}
	return -1
}

// exact reports a row whose invocation is tok byte for byte — the test
// behind the inline opener's "the invocation is typed out and a space
// follows it" hide rule.
func (l *chatSkillList) exact(tok string) bool {
	for _, r := range l.rows {
		if r.invocation == tok {
			return true
		}
	}
	return false
}

// move walks the highlight. From "nothing highlighted", `↓` lands on the
// first row and `↑` on the last.
func (l *chatSkillList) move(delta int) {
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
func (l *chatSkillList) pick() (chatSkillRow, bool) {
	if len(l.rows) == 0 {
		return chatSkillRow{}, false
	}
	return l.rows[max(l.cursor, 0)], true
}

// typeText extends the filter with the text one key produced. The press's
// own Text is what is asked, not its spelling: that is what a terminal says
// the key typed, and it is empty for every ctrl, alt and named key, so the
// list's buffer can never take a control by accident.
func (l *chatSkillList) typeText(text string) bool {
	if text == "" || chatSkillHostile(text) {
		return false
	}
	l.filter += text
	l.cursor = -1
	l.build()
	return true
}

// backspace shortens the filter, and reports the press that found it already
// empty: that closes the list, which is the way out the human walked in by.
func (l *chatSkillList) backspace() (closed bool) {
	if l.filter == "" {
		return true
	}
	runes := []rune(l.filter)
	l.filter = string(runes[:len(runes)-1])
	l.cursor = -1
	l.build()
	return false
}

// height is how many lines the list occupies, which render must spend out of
// the body's budget before it draws anything (#299). It does not depend on
// the highlight: the description's lines are reserved, not added.
func (l *chatSkillList) height(paneHeight int) int {
	if !l.open {
		return 0
	}
	return 1 + l.window(paneHeight) + chatSkillsDescLines
}

// window is how many row lines are drawn: the picker's fixed window, what the
// pane has room for, and at most a third of the pane — below the §7.4
// popup's half, because this list is an aid to typing and that popup is the
// turn itself.
func (l *chatSkillList) window(paneHeight int) int {
	rows := max(len(l.rows), 1)
	return max(min(rows, pickerWindow, paneHeight/3-1-chatSkillsDescLines), 1)
}

// render draws the list: a title line carrying the filter and the probe's
// age, the windowed rows, and the highlighted row's description wrapped into
// the fixed reserve. One slice element per rendered line (#299).
func (l *chatSkillList) render(width, paneHeight int, now time.Time) []string {
	if !l.open {
		return nil
	}
	win := l.window(paneHeight)
	out := make([]string, 0, 1+win+chatSkillsDescLines)
	out = append(out, " "+l.titleLine(now))

	switch {
	case l.loading:
		out = append(out, styleDim.Render(fmt.Sprintf("   asking %s which skills it has…", l.agentName())))
	case len(l.rows) == 0 && l.filter != "":
		out = append(out, styleDim.Render("   nothing matches — backspace clears the filter, esc closes the list"))
	case len(l.rows) == 0:
		out = append(out, styleDim.Render(fmt.Sprintf("   %s reported no skills for this chat", l.agentName())))
	default:
		out = append(out, l.visibleRows(width, win)...)
	}
	// The reserve, whether or not a row is highlighted.
	body := make([]string, chatSkillsDescLines)
	if r, ok := l.pick(); ok && l.cursor >= 0 {
		text := r.description
		if r.disabled && r.reason != "" {
			text = strings.TrimSpace(r.reason + " · " + text)
		}
		wrapped := wrapPlain(text, max(width-5, 8))
		for i := range min(len(wrapped), chatSkillsDescLines) {
			body[i] = styleDim.Render("    " + wrapped[i])
		}
	}
	out = append(out, body...)
	// Pad to the promised height: a window narrower than the rows still owes
	// render exactly what height() said it would draw.
	for len(out) < 1+win+chatSkillsDescLines {
		out = append(out, "")
	}
	return out[:1+win+chatSkillsDescLines]
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
// unreachable here — `chatSkillList.window` never returns less than 1. The
// scroll behaviour a reader sees is `windowStart`'s, and is unchanged.
func (l *chatSkillList) visibleRows(width, win int) []string {
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

// titleLine names the list, the filter being typed and how old the answer is.
func (l *chatSkillList) titleLine(now time.Time) string {
	title := styleTitle.Render("skills")
	tail := make([]string, 0, 4)
	// Only browse draws its filter: the inline list's filter is the token
	// the human can see in the draft, and printing it twice would read as
	// two buffers rather than one.
	if l.mode == skillModeBrowse && l.filter != "" {
		tail = append(tail, l.filter)
	}
	// A capped build says how many of the matches are listed, so "your row
	// is not here" can never read as "your row does not match" (issue #553
	// decision 2). The picker's `▼ %d more` idiom is deliberately not
	// reused: there it means the *window* overflowed, and one string may
	// not mean two things.
	if l.matched > len(l.rows) {
		tail = append(tail, fmt.Sprintf("%d of %d", len(l.rows), l.matched))
	}
	if l.data != nil && l.data.ProbedAt != nil {
		tail = append(tail, "probed "+formatElapsed(max(now.Sub(*l.data.ProbedAt), 0).Truncate(time.Second))+" ago")
	}
	if len(l.rows) > 0 {
		if l.mode == skillModeInline {
			tail = append(tail, "tab complete · ↑/↓ pick · esc back to the draft")
		} else {
			tail = append(tail, "tab insert · ↑/↓ pick · esc close")
		}
	}
	if len(tail) > 0 {
		title += styleDim.Render("  ·  " + strings.Join(tail, "  ·  "))
	}
	return title
}

// agentName is whose list this is, "the agent" before an answer arrived.
func (l *chatSkillList) agentName() string {
	if l.data != nil && l.data.Agent != "" {
		return l.data.Agent
	}
	return "the agent"
}

// refreshNote expires the after-accept note. It lives until the invocation
// it describes leaves the draft, or until the inline list opens again on a
// token of its own — either way what it is about is gone. The transient
// hint carries no noteFor and is recomputed from scratch on every sync.
func (l *chatSkillList) refreshNote(draft string, opened bool) {
	if l.noteFor == "" {
		return
	}
	if opened || !strings.Contains(draft, l.noteFor) {
		l.inlineNote, l.noteFor = "", ""
	}
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

// inlineFilter reports the filter an inline list would rank against for tok,
// and whether the draft asks for one at all. The sigil and its position are
// the wire's; nothing here knows which adapter it is talking about.
func (l *chatSkillList) inlineFilter(tok chatDraftToken) (string, bool) {
	// InvokeSigil is "" whenever the invoke verdict is not `supported`
	// (internal/api/chatskills.go), so this gates inline off for an adapter
	// that cannot invoke — tested for itself rather than left to that
	// coupling.
	if l.data == nil || !l.canList() || l.data.InvokeSigil == "" {
		return "", false
	}
	rest, ok := strings.CutPrefix(tok.text, l.data.InvokeSigil)
	if !ok {
		return "", false
	}
	switch l.data.InvokePosition {
	case "leading":
		// claude expands `/name` only at the very start of the message, and
		// the composer trims leading whitespace on send, so the position is
		// "the first token on row 0" rather than "column 0". A bare sigil
		// opens every row here, which is Claude Code's own behaviour and
		// almost always what it means (decision 93).
		if tok.line != 0 || !tok.first {
			return "", false
		}
	case "anywhere":
		// A bare `$` mid-prose is a shell variable far more often than the
		// start of an invocation, and an empty filter matches every row, so
		// the hide-on-no-match rule could not quiet it (decision 93). One
		// character after the sigil opens the list.
		if rest == "" {
			return "", false
		}
	default:
		return "", false
	}
	return rest, true
}

// chatFileMentionSigil is the rune a human writes a file with, not a skill.
// It is the one sigil chatSkillSigilShaped refuses; see there for why.
const chatFileMentionSigil = '@'

// chatSkillSigilShaped reports a token that could be an invocation under
// *some* adapter's sigil: one non-alphanumeric rune that is not
// chatFileMentionSigil, and then a name.
//
// It is the only thing the *first* inline fetch can be spent on
// (decision 91, narrowed by issue #552). The real sigil comes off the wire
// and is hard-coded nowhere, so before an answer is in hand there is nothing
// to test a token against but its shape; every keystroke after that is
// filtered by the answer's own sigil. A token this accepts that no adapter
// honours costs one silent probe, which chatSkillList.probeFailed latches off
// so a keystroke cannot re-fire it — and a lone sigil is deliberately not
// enough, because one punctuation rune says nothing about whose sigil it is.
//
// `@` is excluded because it is the one shape that can never pay off: no
// shipped adapter reports it (§9.1 — claude and cursor `/`, codex `$`), while
// `@name` is exactly what a human types to mean a file. Spending the chat's
// one probe there buys an agent CLI spawn and, when it fails, inline skills
// off for the rest of the chat. Only the bare short form reaches here anyway:
// `@README.md` and `@src/main.go` already fail the name loop below.
//
// The exclusion is unconditional because at probe time there is no wire sigil
// to consult. An adapter that reported `invoke_sigil: "@"` would simply not
// self-start its inline list from a typed `@` — it needs `tab`, or an answer
// already cached. Once an answer *is* in hand the wire's sigil wins unchanged:
// filtering runs through chatSkillList.inlineFilter, which never comes here.
func chatSkillSigilShaped(tok string) bool {
	runes := []rune(tok)
	if len(runes) < 2 || unicode.IsLetter(runes[0]) || unicode.IsDigit(runes[0]) || runes[0] == '_' {
		return false
	}
	if runes[0] == chatFileMentionSigil {
		return false
	}
	for _, r := range runes[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != ':' {
			return false
		}
	}
	return true
}

// chatSkillNameShaped reports the text after a sigil that is a skill name
// rather than a path, which is the whole of decision 92: `/tmp/notes.md` and
// `/Users/x` are silent, `/tdds` is hinted.
//
// Both separators are tested on every platform, not `\` on Windows alone: a
// draft is prose a human may write about any machine, and a rule that
// differed per host would make the same message say two things — and a test
// of it pass on one CI leg and fail on another.
func chatSkillNameShaped(rest string) bool {
	return rest != "" && !strings.ContainsAny(rest, `/\`)
}

// chatSkillAcceptNote is what an accepted skill takes, for the note line:
// the invocation, its argument hint and its description (issue #510 item 3).
func chatSkillAcceptNote(r chatSkillRow) string {
	head := strings.TrimSpace(r.display + " " + r.hint)
	if r.description == "" {
		return head
	}
	return head + " — " + r.description
}

// chatSkillRowLine draws one row. The selection is a `› ` marker as well as a
// colour, so it survives NO_COLOR and 16 colours (§15 Colour).
//
// Below roughly 40 columns the description and the note are dropped before
// the invocation is truncated: what a row *is* is what a reader picks by.
func chatSkillRowLine(r chatSkillRow, selected bool, width int) string {
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
